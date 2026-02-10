package geoip

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockRoundTripper delegates HTTP requests to a handler without real network I/O.
type mockRoundTripper struct {
	handler http.HandlerFunc
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rr := httptest.NewRecorder()
	m.handler(rr, req)
	return rr.Result(), nil
}

// slowRoundTripper simulates network latency; honours context cancellation.
type slowRoundTripper struct {
	delay time.Duration
}

func (s *slowRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case <-time.After(s.delay):
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"status":"success","countryCode":"US","city":"Slow"}`)),
			Header:     make(http.Header),
		}, nil
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

// newTestService builds a GeoIPService wired to rt, with no Redis and no ASN lookup.
func newTestService(rt http.RoundTripper, timeout time.Duration, rateIntervalMs int) *GeoIPService {
	if rateIntervalMs <= 0 {
		rateIntervalMs = 10 // fast ticker for tests
	}
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	return &GeoIPService{
		redisClient: nil,
		cacheTTL:    time.Minute,
		httpClient:  &http.Client{Transport: rt},
		timeout:     timeout,
		rateLimiter: time.NewTicker(time.Duration(rateIntervalMs) * time.Millisecond),
	}
}

// --- fetchFromIPAPI ---

func TestFetchFromIPAPI_Success(t *testing.T) {
	want := IPAPIResponse{
		Status: "success", CountryCode: "US", City: "New York",
		RegionName: "New York", Lat: 40.7128, Lon: -74.006,
		ISP: "Google LLC", AS: "AS15169 Google LLC",
	}
	rt := &mockRoundTripper{handler: func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(want)
	}}
	svc := newTestService(rt, 0, 0)
	defer svc.Close()

	got := svc.fetchFromIPAPI(context.Background(), "8.8.8.8")
	if got == nil {
		t.Fatal("fetchFromIPAPI returned nil on valid response")
	}
	if got.CountryCode != "US" {
		t.Errorf("CountryCode = %q, want %q", got.CountryCode, "US")
	}
	if got.City != "New York" {
		t.Errorf("City = %q, want %q", got.City, "New York")
	}
	if got.Lat != 40.7128 {
		t.Errorf("Lat = %f, want 40.7128", got.Lat)
	}
	if got.Lon != -74.006 {
		t.Errorf("Lon = %f, want -74.006", got.Lon)
	}
}

func TestFetchFromIPAPI_ContextCancelBeforeTick(t *testing.T) {
	rt := &mockRoundTripper{handler: func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("HTTP request must not be issued when context is already cancelled")
	}}
	// Ticker interval so large it will never fire during the test.
	svc := &GeoIPService{
		httpClient:  &http.Client{Transport: rt},
		timeout:     2 * time.Second,
		rateLimiter: time.NewTicker(10 * time.Hour),
	}
	defer svc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before entering fetchFromIPAPI

	if got := svc.fetchFromIPAPI(ctx, "1.2.3.4"); got != nil {
		t.Error("expected nil when context cancelled before rate-limit tick")
	}
}

func TestFetchFromIPAPI_PerRequestTimeout(t *testing.T) {
	// Transport takes 5 s; per-request timeout is 50 ms — should bail out fast.
	svc := newTestService(&slowRoundTripper{delay: 5 * time.Second}, 50*time.Millisecond, 0)
	defer svc.Close()

	start := time.Now()
	got := svc.fetchFromIPAPI(context.Background(), "8.8.8.8")
	elapsed := time.Since(start)

	if got != nil {
		t.Error("expected nil when per-request timeout fires")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("timeout did not fire promptly; elapsed %v", elapsed)
	}
}

func TestFetchFromIPAPI_NonOKStatus(t *testing.T) {
	rt := &mockRoundTripper{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}}
	svc := newTestService(rt, 0, 0)
	defer svc.Close()

	if got := svc.fetchFromIPAPI(context.Background(), "8.8.8.8"); got != nil {
		t.Error("expected nil on HTTP 429")
	}
}

func TestFetchFromIPAPI_InvalidJSON(t *testing.T) {
	rt := &mockRoundTripper{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not-json"))
	}}
	svc := newTestService(rt, 0, 0)
	defer svc.Close()

	if got := svc.fetchFromIPAPI(context.Background(), "8.8.8.8"); got != nil {
		t.Error("expected nil on malformed JSON body")
	}
}

func TestFetchFromIPAPI_APIFailStatus(t *testing.T) {
	rt := &mockRoundTripper{handler: func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(IPAPIResponse{Status: "fail", Message: "reserved range"})
	}}
	svc := newTestService(rt, 0, 0)
	defer svc.Close()

	if got := svc.fetchFromIPAPI(context.Background(), "127.0.0.1"); got != nil {
		t.Error("expected nil when ip-api returns status=fail")
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
