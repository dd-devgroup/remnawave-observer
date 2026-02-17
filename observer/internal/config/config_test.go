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
	if cfg.RemnawaveHeader != "" {
		t.Errorf("expected RemnawaveHeader default empty, got %q", cfg.RemnawaveHeader)
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
	os.Setenv("REMNAWAVE_HEADER", "gate_key=gate_value")
	os.Setenv("USERID_UUID_CACHE_TTL_HOURS", "48")

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
	if cfg.RemnawaveHeader != "gate_key=gate_value" {
		t.Errorf("expected RemnawaveHeader = gate_key=gate_value, got %q", cfg.RemnawaveHeader)
	}
	if cfg.UserIDUUIDCacheTTLHours != 48 {
		t.Errorf("expected UserIDUUIDCacheTTLHours = 48, got %d", cfg.UserIDUUIDCacheTTLHours)
	}
	if cfg.ReenableTickSeconds != 10 {
		t.Errorf("expected ReenableTickSeconds internal default 10, got %d", cfg.ReenableTickSeconds)
	}
	if cfg.ReenableBatchSize != 100 {
		t.Errorf("expected ReenableBatchSize internal default 100, got %d", cfg.ReenableBatchSize)
	}
}

func TestRemnawaveConfigValidation(t *testing.T) {
	os.Clearenv()
	os.Setenv("REMNAWAVE_TIMEOUT_SECONDS", "-5")
	os.Setenv("USERID_UUID_CACHE_TTL_HOURS", "0")

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

func TestRemnawaveConfigHeaderShortcut(t *testing.T) {
	os.Clearenv()
	os.Setenv("REMNAWAVE_BASE_URL", "https://panel.example.com")
	os.Setenv("REMNAWAVE_API_TOKEN", "secret-token")
	os.Setenv("REMNAWAVE_HEADER", "gate_key=gate_value")

	cfg := New()

	if cfg.RemnawaveBaseURL != "https://panel.example.com" {
		t.Errorf("expected RemnawaveBaseURL = https://panel.example.com, got %q", cfg.RemnawaveBaseURL)
	}
	if cfg.RemnawaveHeader != "gate_key=gate_value" {
		t.Errorf("expected RemnawaveHeader from REMNAWAVE_HEADER, got %q", cfg.RemnawaveHeader)
	}
}

func TestRemnawaveHeaderInvalidFormat(t *testing.T) {
	os.Clearenv()
	os.Setenv("REMNAWAVE_HEADER", "bad-format")

	cfg := New()

	if cfg.RemnawaveHeader != "" {
		t.Errorf("expected invalid REMNAWAVE_HEADER to be disabled, got %q", cfg.RemnawaveHeader)
	}
}

func TestInternalDefaults_NotReadFromEnv(t *testing.T) {
	os.Clearenv()
	os.Setenv("WORKER_POOL_SIZE", "99")
	os.Setenv("LOG_CHANNEL_BUFFER_SIZE", "999")
	os.Setenv("SIDE_EFFECT_WORKER_POOL_SIZE", "77")
	os.Setenv("SIDE_EFFECT_CHANNEL_BUFFER_SIZE", "777")
	os.Setenv("SIDE_EFFECT_TIMEOUT_SECONDS", "123")
	os.Setenv("MONITORING_INTERVAL", "1")
	os.Setenv("REENABLE_TICK_SECONDS", "1")
	os.Setenv("REENABLE_BATCH_SIZE", "1")
	os.Setenv("MAX_REQUEST_BYTES", "123")
	os.Setenv("MAX_LOG_ENTRIES_PER_REQUEST", "4")

	cfg := New()

	if cfg.WorkerPoolSize == 99 {
		t.Fatalf("WORKER_POOL_SIZE should be ignored, got %d", cfg.WorkerPoolSize)
	}
	if cfg.LogChannelBufferSize == 999 {
		t.Fatalf("LOG_CHANNEL_BUFFER_SIZE should be ignored, got %d", cfg.LogChannelBufferSize)
	}
	if cfg.SideEffectWorkerPoolSize == 77 {
		t.Fatalf("SIDE_EFFECT_WORKER_POOL_SIZE should be ignored, got %d", cfg.SideEffectWorkerPoolSize)
	}
	if cfg.SideEffectChannelBufferSize == 777 {
		t.Fatalf("SIDE_EFFECT_CHANNEL_BUFFER_SIZE should be ignored, got %d", cfg.SideEffectChannelBufferSize)
	}
	if cfg.SideEffectTimeout == 123*time.Second {
		t.Fatalf("SIDE_EFFECT_TIMEOUT_SECONDS should be ignored, got %v", cfg.SideEffectTimeout)
	}
	if cfg.MonitoringInterval == 1*time.Second {
		t.Fatalf("MONITORING_INTERVAL should be ignored, got %v", cfg.MonitoringInterval)
	}
	if cfg.ReenableTickSeconds != 10 {
		t.Fatalf("REENABLE_TICK_SECONDS should be ignored, got %d", cfg.ReenableTickSeconds)
	}
	if cfg.ReenableBatchSize != 100 {
		t.Fatalf("REENABLE_BATCH_SIZE should be ignored, got %d", cfg.ReenableBatchSize)
	}
	if cfg.MaxRequestBytes == 123 {
		t.Fatalf("MAX_REQUEST_BYTES should be ignored, got %d", cfg.MaxRequestBytes)
	}
	if cfg.MaxLogEntriesPerRequest == 4 {
		t.Fatalf("MAX_LOG_ENTRIES_PER_REQUEST should be ignored, got %d", cfg.MaxLogEntriesPerRequest)
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
