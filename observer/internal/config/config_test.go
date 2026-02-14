package config

import (
	"os"
	"testing"
	"time"
)

// --- REMNAWAVE CONFIG tests (MIG-3) ---

func TestRemnawaveConfigDefaults(t *testing.T) {
	os.Clearenv()
	cfg := New()

	if cfg.RemnawaveBaseURL != "" {
		t.Errorf("expected RemnawaveBaseURL default empty, got %q", cfg.RemnawaveBaseURL)
	}
	if cfg.RemnawaveAPIToken != "" {
		t.Errorf("expected RemnawaveAPIToken default empty, got %q", cfg.RemnawaveAPIToken)
	}
	if cfg.RemnawaveTimeoutSeconds != 5 {
		t.Errorf("expected RemnawaveTimeoutSeconds = 5, got %d", cfg.RemnawaveTimeoutSeconds)
	}
	if cfg.UserIDUUIDCacheTTLHours != 24 {
		t.Errorf("expected UserIDUUIDCacheTTLHours = 24, got %d", cfg.UserIDUUIDCacheTTLHours)
	}
	if cfg.ReenableTickSeconds != 10 {
		t.Errorf("expected ReenableTickSeconds = 10, got %d", cfg.ReenableTickSeconds)
	}
	if cfg.ReenableBatchSize != 100 {
		t.Errorf("expected ReenableBatchSize = 100, got %d", cfg.ReenableBatchSize)
	}
}

func TestRemnawaveConfigCustomValues(t *testing.T) {
	os.Clearenv()
	os.Setenv("REMNAWAVE_BASE_URL", "https://panel.example.com")
	os.Setenv("REMNAWAVE_API_TOKEN", "secret-token")
	os.Setenv("REMNAWAVE_TIMEOUT_SECONDS", "10")
	os.Setenv("USERID_UUID_CACHE_TTL_HOURS", "48")
	os.Setenv("REENABLE_TICK_SECONDS", "30")
	os.Setenv("REENABLE_BATCH_SIZE", "200")

	cfg := New()

	if cfg.RemnawaveBaseURL != "https://panel.example.com" {
		t.Errorf("expected RemnawaveBaseURL = https://panel.example.com, got %q", cfg.RemnawaveBaseURL)
	}
	if cfg.RemnawaveAPIToken != "secret-token" {
		t.Errorf("expected RemnawaveAPIToken = secret-token, got %q", cfg.RemnawaveAPIToken)
	}
	if cfg.RemnawaveTimeoutSeconds != 10 {
		t.Errorf("expected RemnawaveTimeoutSeconds = 10, got %d", cfg.RemnawaveTimeoutSeconds)
	}
	if cfg.UserIDUUIDCacheTTLHours != 48 {
		t.Errorf("expected UserIDUUIDCacheTTLHours = 48, got %d", cfg.UserIDUUIDCacheTTLHours)
	}
	if cfg.ReenableTickSeconds != 30 {
		t.Errorf("expected ReenableTickSeconds = 30, got %d", cfg.ReenableTickSeconds)
	}
	if cfg.ReenableBatchSize != 200 {
		t.Errorf("expected ReenableBatchSize = 200, got %d", cfg.ReenableBatchSize)
	}
}

func TestRemnawaveConfigValidation(t *testing.T) {
	os.Clearenv()
	// Отрицательные значения должны сброситься на дефолты
	os.Setenv("REMNAWAVE_TIMEOUT_SECONDS", "-5")
	os.Setenv("USERID_UUID_CACHE_TTL_HOURS", "0")
	os.Setenv("REENABLE_TICK_SECONDS", "-10")
	os.Setenv("REENABLE_BATCH_SIZE", "0")

	cfg := New()

	if cfg.RemnawaveTimeoutSeconds != 5 {
		t.Errorf("expected RemnawaveTimeoutSeconds clamp to 5, got %d", cfg.RemnawaveTimeoutSeconds)
	}
	if cfg.UserIDUUIDCacheTTLHours != 24 {
		t.Errorf("expected UserIDUUIDCacheTTLHours clamp to 24, got %d", cfg.UserIDUUIDCacheTTLHours)
	}
	if cfg.ReenableTickSeconds != 10 {
		t.Errorf("expected ReenableTickSeconds clamp to 10, got %d", cfg.ReenableTickSeconds)
	}
	if cfg.ReenableBatchSize != 100 {
		t.Errorf("expected ReenableBatchSize clamp to 100, got %d", cfg.ReenableBatchSize)
	}
}

func TestGeoFallbackConfigDefaults(t *testing.T) {
	os.Clearenv()
	cfg := New()

	if cfg.GeoFallbackEnabled {
		t.Errorf("expected GeoFallbackEnabled default false, got true")
	}
	if cfg.GeoFallbackTimeout != 3*time.Second {
		t.Errorf("expected GeoFallbackTimeout default 3s, got %v", cfg.GeoFallbackTimeout)
	}
	if cfg.TwoIPToken != "" {
		t.Errorf("expected TwoIPToken default empty, got %q", cfg.TwoIPToken)
	}
	if cfg.TwoIPBaseURL != "https://api.2ip.io" {
		t.Errorf("expected TwoIPBaseURL default https://api.2ip.io, got %q", cfg.TwoIPBaseURL)
	}
}

func TestGeoFallbackConfigCustomValues(t *testing.T) {
	os.Clearenv()
	os.Setenv("GEO_FALLBACK_ENABLED", "true")
	os.Setenv("GEO_FALLBACK_TIMEOUT_SECONDS", "7")
	os.Setenv("TWOIP_TOKEN", "test-token")
	os.Setenv("TWOIP_BASE_URL", "https://api.test-2ip.local")

	cfg := New()

	if !cfg.GeoFallbackEnabled {
		t.Errorf("expected GeoFallbackEnabled=true")
	}
	if cfg.GeoFallbackTimeout != 7*time.Second {
		t.Errorf("expected GeoFallbackTimeout=7s, got %v", cfg.GeoFallbackTimeout)
	}
	if cfg.TwoIPToken != "test-token" {
		t.Errorf("expected TwoIPToken=test-token, got %q", cfg.TwoIPToken)
	}
	if cfg.TwoIPBaseURL != "https://api.test-2ip.local" {
		t.Errorf("expected TwoIPBaseURL custom value, got %q", cfg.TwoIPBaseURL)
	}
}
