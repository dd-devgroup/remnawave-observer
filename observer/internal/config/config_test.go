package config

import (
	"os"
	"testing"
)

func TestHTTPTimeoutDefaults(t *testing.T) {
	// Очистить env переменные чтобы получить дефолты
	os.Clearenv()

	cfg := New()

	// Проверка дефолтных значений HTTP таймаутов
	if cfg.HTTPReadHeaderTimeoutSeconds != 5 {
		t.Errorf("expected HTTPReadHeaderTimeoutSeconds = 5, got %d", cfg.HTTPReadHeaderTimeoutSeconds)
	}
	if cfg.HTTPReadTimeoutSeconds != 15 {
		t.Errorf("expected HTTPReadTimeoutSeconds = 15, got %d", cfg.HTTPReadTimeoutSeconds)
	}
	if cfg.HTTPWriteTimeoutSeconds != 15 {
		t.Errorf("expected HTTPWriteTimeoutSeconds = 15, got %d", cfg.HTTPWriteTimeoutSeconds)
	}
	if cfg.HTTPIdleTimeoutSeconds != 60 {
		t.Errorf("expected HTTPIdleTimeoutSeconds = 60, got %d", cfg.HTTPIdleTimeoutSeconds)
	}
	if cfg.HTTPMaxHeaderBytes != (1 << 20) {
		t.Errorf("expected HTTPMaxHeaderBytes = 1048576, got %d", cfg.HTTPMaxHeaderBytes)
	}
}

func TestHTTPTimeoutCustomValues(t *testing.T) {
	os.Clearenv()
	os.Setenv("HTTP_READ_HEADER_TIMEOUT_SECONDS", "10")
	os.Setenv("HTTP_READ_TIMEOUT_SECONDS", "30")
	os.Setenv("HTTP_WRITE_TIMEOUT_SECONDS", "30")
	os.Setenv("HTTP_IDLE_TIMEOUT_SECONDS", "120")
	os.Setenv("HTTP_MAX_HEADER_BYTES", "2097152")

	cfg := New()

	if cfg.HTTPReadHeaderTimeoutSeconds != 10 {
		t.Errorf("expected HTTPReadHeaderTimeoutSeconds = 10, got %d", cfg.HTTPReadHeaderTimeoutSeconds)
	}
	if cfg.HTTPReadTimeoutSeconds != 30 {
		t.Errorf("expected HTTPReadTimeoutSeconds = 30, got %d", cfg.HTTPReadTimeoutSeconds)
	}
	if cfg.HTTPWriteTimeoutSeconds != 30 {
		t.Errorf("expected HTTPWriteTimeoutSeconds = 30, got %d", cfg.HTTPWriteTimeoutSeconds)
	}
	if cfg.HTTPIdleTimeoutSeconds != 120 {
		t.Errorf("expected HTTPIdleTimeoutSeconds = 120, got %d", cfg.HTTPIdleTimeoutSeconds)
	}
	if cfg.HTTPMaxHeaderBytes != 2097152 {
		t.Errorf("expected HTTPMaxHeaderBytes = 2097152, got %d", cfg.HTTPMaxHeaderBytes)
	}
}

func TestHTTPTimeoutValidation(t *testing.T) {
	os.Clearenv()
	// Установить отрицательные/нулевые значения
	os.Setenv("HTTP_READ_HEADER_TIMEOUT_SECONDS", "-1")
	os.Setenv("HTTP_READ_TIMEOUT_SECONDS", "0")
	os.Setenv("HTTP_WRITE_TIMEOUT_SECONDS", "-5")
	os.Setenv("HTTP_IDLE_TIMEOUT_SECONDS", "0")
	os.Setenv("HTTP_MAX_HEADER_BYTES", "-100")

	cfg := New()

	// Валидация должна сбросить их на дефолты
	if cfg.HTTPReadHeaderTimeoutSeconds != 5 {
		t.Errorf("expected HTTPReadHeaderTimeoutSeconds clamp to 5, got %d", cfg.HTTPReadHeaderTimeoutSeconds)
	}
	if cfg.HTTPReadTimeoutSeconds != 15 {
		t.Errorf("expected HTTPReadTimeoutSeconds clamp to 15, got %d", cfg.HTTPReadTimeoutSeconds)
	}
	if cfg.HTTPWriteTimeoutSeconds != 15 {
		t.Errorf("expected HTTPWriteTimeoutSeconds clamp to 15, got %d", cfg.HTTPWriteTimeoutSeconds)
	}
	if cfg.HTTPIdleTimeoutSeconds != 60 {
		t.Errorf("expected HTTPIdleTimeoutSeconds clamp to 60, got %d", cfg.HTTPIdleTimeoutSeconds)
	}
	if cfg.HTTPMaxHeaderBytes != (1 << 20) {
		t.Errorf("expected HTTPMaxHeaderBytes clamp to 1048576, got %d", cfg.HTTPMaxHeaderBytes)
	}
}

// --- STRICT_JSON_DECODE tests (Commit P) ---

func TestStrictJSONDecodeDefault(t *testing.T) {
	os.Clearenv()
	cfg := New()
	if cfg.StrictJSONDecode {
		t.Errorf("expected StrictJSONDecode default false, got true")
	}
}

func TestStrictJSONDecodeTrue(t *testing.T) {
	os.Clearenv()
	os.Setenv("STRICT_JSON_DECODE", "true")
	cfg := New()
	if !cfg.StrictJSONDecode {
		t.Errorf("expected StrictJSONDecode = true, got false")
	}
}

func TestStrictJSONDecodeFalse(t *testing.T) {
	os.Clearenv()
	os.Setenv("STRICT_JSON_DECODE", "false")
	cfg := New()
	if cfg.StrictJSONDecode {
		t.Errorf("expected StrictJSONDecode = false, got true")
	}
}

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
