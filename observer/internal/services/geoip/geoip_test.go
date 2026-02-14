package geoip

import (
	"context"
	"net/http"
	"testing"
)

// --- Lookup with nil MMDB (iptoasn only) ---

func TestGeoIPService_Close_NilMMDB(t *testing.T) {
	svc := &GeoIPService{mmdbReader: nil}
	// Should not panic
	svc.Close()
}

// --- MMDBReader construction ---

func TestNewMMDBReader_EmptyPaths(t *testing.T) {
	// Empty paths should return an error (validation added to prevent empty reader)
	reader, err := NewMMDBReader("", "")
	if err == nil {
		if reader != nil {
			reader.Close()
		}
		t.Fatal("expected error for empty paths, got nil")
	}
	if reader != nil {
		t.Error("expected nil reader when error is returned")
	}
}

func TestNewMMDBReader_InvalidASNPath(t *testing.T) {
	_, err := NewMMDBReader("/nonexistent/GeoLite2-ASN.mmdb", "")
	if err == nil {
		t.Fatal("expected error for nonexistent ASN MMDB path")
	}
}

func TestNewMMDBReader_InvalidCityPath(t *testing.T) {
	_, err := NewMMDBReader("", "/nonexistent/GeoLite2-City.mmdb")
	if err == nil {
		t.Fatal("expected error for nonexistent City MMDB path")
	}
}

func TestMMDBReader_LookupASN_NoDB(t *testing.T) {
	reader := &MMDBReader{}
	_, _, err := reader.LookupASN("8.8.8.8")
	if err == nil {
		t.Fatal("expected error when ASN DB not loaded")
	}
}

func TestMMDBReader_LookupCity_NoDB(t *testing.T) {
	reader := &MMDBReader{}
	_, _, _, _, _, err := reader.LookupCity("8.8.8.8")
	if err == nil {
		t.Fatal("expected error when City DB not loaded")
	}
}

func TestMMDBReader_LookupASN_InvalidIP(t *testing.T) {
	reader := &MMDBReader{}
	_, _, err := reader.LookupASN("not-an-ip")
	if err == nil {
		t.Fatal("expected error for invalid IP")
	}
}

// --- ASN double-check logic ---

func TestLookup_ASNAgreement(t *testing.T) {
	loc := &GeoLocation{
		IP:           "8.8.8.8",
		ASN:          "AS15169",
		ASNAgreement: true,
		Confidence:   0.95,
		Source:       "mmdb",
	}

	if !loc.ASNAgreement {
		t.Error("expected ASNAgreement to be true")
	}
	if loc.Confidence != 0.95 {
		t.Errorf("expected Confidence 0.95, got %f", loc.Confidence)
	}
	if loc.Source != "mmdb" {
		t.Errorf("expected Source 'mmdb', got %q", loc.Source)
	}
}

func TestLookup_SourceConfidence_Fields(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		confidence float64
	}{
		{"iptoasn_only", "iptoasn", 0.5},
		{"mmdb_enriched", "mmdb", 0.8},
		{"cache_hit", "cache", 0.0},
		{"agreement", "mmdb", 0.95},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc := &GeoLocation{Source: tc.source, Confidence: tc.confidence}
			if loc.Source != tc.source {
				t.Errorf("Source = %q, want %q", loc.Source, tc.source)
			}
			if loc.Confidence != tc.confidence {
				t.Errorf("Confidence = %f, want %f", loc.Confidence, tc.confidence)
			}
		})
	}
}

// --- NormalizeCity ---

func TestNormalizeCity(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"plain", "New York", "new york"},
		{"prefix г.", "г. Москва", "москва"},
		{"prefix город", "город Москва", "москва"},
		{"whitespace", "  г. Москва  ", "москва"},
		{"empty", "", ""},
		{"no prefix", "Paris", "paris"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeCity(tt.input); got != tt.want {
				t.Errorf("NormalizeCity(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- NewGeoIPService construction ---

func TestNewGeoIPService_NilMMDB(t *testing.T) {
	svc := NewGeoIPService(nil, nil, nil, 0)
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	defer svc.Close()
}

// --- Lookup requires asnLookup ---

func TestLookup_WithoutASNLookup_ContinuesGracefully(t *testing.T) {
	// Test that Lookup handles nil asnLookup gracefully instead of panicking
	svc := NewGeoIPService(nil, nil, nil, 0)
	defer svc.Close()

	loc, err := svc.Lookup(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("unexpected error with nil asnLookup: %v", err)
	}
	if loc == nil {
		t.Fatal("expected non-nil location even with nil asnLookup")
	}
	// ASN should be empty when asnLookup is nil
	if loc.ASN != "" {
		t.Errorf("expected empty ASN with nil asnLookup, got %q", loc.ASN)
	}
}

type fallbackProviderStub struct {
	name  string
	err   error
	calls int
}

func (s *fallbackProviderStub) Name() string {
	if s.name == "" {
		return "stub"
	}
	return s.name
}

func (s *fallbackProviderStub) Lookup(ctx context.Context, ip string) (*FallbackLocation, error) {
	s.calls++
	return nil, s.err
}

func TestLookup_DisablesFallbackAfterUnauthorized(t *testing.T) {
	svc := NewGeoIPService(nil, nil, nil, 0)
	defer svc.Close()

	fallback := &fallbackProviderStub{
		name: "2ip",
		err: &httpStatusError{
			Provider:   "2ip",
			StatusCode: http.StatusUnauthorized,
			Body:       `{"error":"Unauthorized"}`,
		},
	}
	svc.SetFallbackProvider(fallback)

	if _, err := svc.Lookup(context.Background(), "8.8.8.8"); err != nil {
		t.Fatalf("unexpected lookup error on first call: %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("expected 1 fallback call after first lookup, got %d", fallback.calls)
	}

	if _, err := svc.Lookup(context.Background(), "1.1.1.1"); err != nil {
		t.Fatalf("unexpected lookup error on second call: %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("expected fallback to be disabled after 401, calls=%d", fallback.calls)
	}
}

func TestMergeFallbackLocation_FillsMissingFields(t *testing.T) {
	location := &GeoLocation{
		Source:      "mmdb",
		CountryCode: "RU",
	}
	fallback := &FallbackLocation{
		City:           "Samara",
		Latitude:       53.1959,
		Longitude:      50.1008,
		HasCoordinates: true,
		Source:         "2ip",
		Confidence:     0.85,
	}

	mergeFallbackLocation(location, fallback)

	if location.City != "Samara" {
		t.Fatalf("expected city Samara, got %q", location.City)
	}
	if location.Latitude == 0 || location.Longitude == 0 {
		t.Fatalf("expected coordinates from fallback, got %.4f, %.4f", location.Latitude, location.Longitude)
	}
	if location.Source != "mmdb+2ip" {
		t.Fatalf("expected merged source mmdb+2ip, got %q", location.Source)
	}
}

func TestNeedsFallbackGeo(t *testing.T) {
	if !needsFallbackGeo(&GeoLocation{City: "", Latitude: 10, Longitude: 10}) {
		t.Fatal("expected fallback when city is empty")
	}
	if !needsFallbackGeo(&GeoLocation{City: "Moscow", Latitude: 0, Longitude: 0}) {
		t.Fatal("expected fallback when coordinates are missing")
	}
	if needsFallbackGeo(&GeoLocation{City: "Moscow", Latitude: 55.7, Longitude: 37.6}) {
		t.Fatal("did not expect fallback for complete geo")
	}
}
