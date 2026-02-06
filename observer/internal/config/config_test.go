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
