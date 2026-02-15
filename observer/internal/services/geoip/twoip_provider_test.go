package geoip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTwoIPProviderLookup_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/9.9.9.9" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("token"); got != "" {
			t.Fatalf("token must not be sent in query, got %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-token" {
			t.Fatalf("unexpected X-API-Key header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"ip":"9.9.9.9",
			"city":"Zurich",
			"lat":"47.37688660",
			"lon":"8.54169400",
			"country":"Switzerland",
			"code":"CH",
			"timezone":"Europe/Zurich",
			"asn":{"id":"19281","name":"QUAD9-AS-1","hosting":false}
		}`))
	}))
	defer server.Close()

	provider := NewTwoIPProvider(server.URL, "test-token", 2*time.Second)
	loc, err := provider.Lookup(context.Background(), "9.9.9.9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loc == nil {
		t.Fatal("expected non-nil fallback location")
	}
	if loc.CountryCode != "CH" {
		t.Errorf("CountryCode = %q, want CH", loc.CountryCode)
	}
	if loc.City != "Zurich" {
		t.Errorf("City = %q, want Zurich", loc.City)
	}
	if !loc.HasCoordinates {
		t.Error("expected HasCoordinates=true")
	}
	if loc.ASN != "AS19281" {
		t.Errorf("ASN = %q, want AS19281", loc.ASN)
	}
	if loc.Organization != "QUAD9-AS-1" {
		t.Errorf("Organization = %q, want QUAD9-AS-1", loc.Organization)
	}
	if loc.Source != "2ip" {
		t.Errorf("Source = %q, want 2ip", loc.Source)
	}
}

func TestTwoIPProviderLookup_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Unauthorized","message":"token is invalid"}`))
	}))
	defer server.Close()

	provider := NewTwoIPProvider(server.URL, "bad-token", 2*time.Second)
	_, err := provider.Lookup(context.Background(), "9.9.9.9")
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if !isHTTPStatusCode(err, http.StatusUnauthorized) {
		t.Fatalf("expected unauthorized status error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "token is invalid") {
		t.Fatalf("expected response body in error, got: %v", err)
	}
}

func TestParseFlexibleFloat(t *testing.T) {
	v, ok := parseFlexibleFloat([]byte(`"47.1"`))
	if !ok || v != 47.1 {
		t.Fatalf("string float parse failed: v=%v ok=%v", v, ok)
	}

	v, ok = parseFlexibleFloat([]byte(`47.2`))
	if !ok || v != 47.2 {
		t.Fatalf("numeric float parse failed: v=%v ok=%v", v, ok)
	}

	_, ok = parseFlexibleFloat([]byte(`""`))
	if ok {
		t.Fatal("empty string must not be treated as valid float")
	}
}

func TestRedactSecret(t *testing.T) {
	got := redactSecret(`{"message":"Token <abc123> invalid"}`, "abc123")
	if !strings.Contains(got, "***") {
		t.Fatalf("expected secret to be masked, got: %s", got)
	}
	if strings.Contains(got, "abc123") {
		t.Fatalf("expected secret not to appear, got: %s", got)
	}
}
