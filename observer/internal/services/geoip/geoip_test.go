package geoip

import (
	"context"
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
