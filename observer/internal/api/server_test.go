package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// --- test doubles ---

type testEnqueuer struct {
	err       error
	enqueued  int
}

func (t *testEnqueuer) EnqueueEntries(entries []models.LogEntry) error {
	if t.err != nil {
		return t.err
	}
	t.enqueued += len(entries)
	return nil
}

type testStorage struct{}

func (t *testStorage) CheckAndAddIP(_ context.Context, _, _ string, _ int, _, _ time.Duration) (*models.CheckResult, error) {
	return nil, nil
}
func (t *testStorage) ClearUserIPs(_ context.Context, _ string) (int, error)                        { return 0, nil }
func (t *testStorage) GetUserActiveIPs(_ context.Context, _ string) (map[string]int, error)         { return nil, nil }
func (t *testStorage) GetAllUserEmails(_ context.Context) ([]string, error)                         { return nil, nil }
func (t *testStorage) HasAlertCooldown(_ context.Context, _ string) (bool, error)                   { return false, nil }
func (t *testStorage) Ping(_ context.Context) error                                                 { return nil }
func (t *testStorage) Close() error                                                                 { return nil }
func (t *testStorage) CheckAndAddSubnet(_ context.Context, _, _ string, _ int, _, _ time.Duration) (*models.CheckResult, error) {
	return nil, nil
}
func (t *testStorage) ClearUserSubnets(_ context.Context, _ string) (int, error)                    { return 0, nil }
func (t *testStorage) GetUserActiveSubnets(_ context.Context, _ string) (map[string]int, error)     { return nil, nil }
func (t *testStorage) GetUserActiveASNs(_ context.Context, _ string) (map[string]*models.ASNInfo, error) {
	return nil, nil
}

type testPublisher struct{}

func (t *testPublisher) PublishBlockMessage(_ models.BlockMessage) error { return nil }
func (t *testPublisher) Close() error                                   { return nil }
func (t *testPublisher) Ping() error                                    { return nil }

// --- helpers ---

func setupRouter(cfg *config.Config, eq EntryEnqueuer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	s := &Server{
		router:    r,
		enqueuer:  eq,
		storage:   &testStorage{},
		publisher: &testPublisher{},
		cfg:       cfg,
	}
	s.setupRoutes()
	return r
}

func defaultCfg() *config.Config {
	return &config.Config{
		MaxRequestBytes:         2 * 1024 * 1024,
		MaxLogEntriesPerRequest: 1000,
	}
}

// --- body-size limit ---

func TestHandleProcessLogEntries_BodyTooLarge(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxRequestBytes = 64
	router := setupRouter(cfg, &testEnqueuer{})

	// Build a valid JSON array that exceeds 64 bytes
	entries := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		entries = append(entries, fmt.Sprintf(`{"user_email":"u%d@test.com","source_ip":"10.0.0.%d"}`, i, i+1))
	}
	body := "[" + strings.Join(entries, ",") + "]"

	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d; body: %s", w.Code, w.Body.String())
	}
}

// --- entry-count limit ---

func TestHandleProcessLogEntries_TooManyEntries(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxLogEntriesPerRequest = 2
	router := setupRouter(cfg, &testEnqueuer{})

	body := `[{"user_email":"a@b.com","source_ip":"1.2.3.4"},{"user_email":"a@b.com","source_ip":"1.2.3.5"},{"user_email":"a@b.com","source_ip":"1.2.3.6"}]`

	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "too many entries") {
		t.Errorf("expected 'too many entries' in body; got: %s", w.Body.String())
	}
}

// --- IP validation ---

func TestHandleProcessLogEntries_InvalidSourceIP(t *testing.T) {
	cfg := defaultCfg()
	router := setupRouter(cfg, &testEnqueuer{})

	cases := []struct {
		name   string
		ip     string
		expect int
	}{
		{"injection_semicolon", "1.2.3.4; rm -rf /", http.StatusBadRequest},
		{"injection_subshell", "$(rm -rf /)", http.StatusBadRequest},
		{"invalid_octet", "1.2.3.999", http.StatusBadRequest},
		{"whitespace_only", "   ", http.StatusBadRequest},
		{"long_garbage", strings.Repeat("x", 200), http.StatusBadRequest},
		{"cidr_notation", "10.0.0.0/24", http.StatusBadRequest},
		{"not_ip", "not-an-ip", http.StatusBadRequest},
		{"backtick", "`id`", http.StatusBadRequest},
		{"valid_ipv4", "192.168.1.1", http.StatusAccepted},
		{"valid_ipv6", "2001:db8::1", http.StatusAccepted},
		{"valid_loopback_v6", "::1", http.StatusAccepted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`[{"user_email":"test@test.com","source_ip":"%s"}]`, tc.ip)
			req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.expect {
				t.Errorf("IP=%q: expected %d, got %d; body: %s", tc.ip, tc.expect, w.Code, w.Body.String())
			}
		})
	}
}

// --- empty array ---

func TestHandleProcessLogEntries_EmptyArray(t *testing.T) {
	cfg := defaultCfg()
	router := setupRouter(cfg, &testEnqueuer{})

	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(`[]`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty array, got %d", w.Code)
	}
}

// --- extra fields from Vector are silently ignored ---

func TestHandleProcessLogEntries_VectorExtraFields(t *testing.T) {
	cfg := defaultCfg()
	eq := &testEnqueuer{}
	router := setupRouter(cfg, eq)

	// Vector adds "path" (and potentially other metadata) to every entry.
	body := `[{"user_email":"a@b.com","source_ip":"1.2.3.4","path":"/var/log/app.log","timestamp":"2026-02-05T00:00:00Z"}]`
	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202 with extra Vector fields, got %d; body: %s", w.Code, w.Body.String())
	}
	if eq.enqueued != 1 {
		t.Errorf("expected 1 enqueued entry, got %d", eq.enqueued)
	}
}

// --- malformed JSON ---

func TestHandleProcessLogEntries_MalformedJSON(t *testing.T) {
	cfg := defaultCfg()
	router := setupRouter(cfg, &testEnqueuer{})

	body := `[{"user_email":"a@b.com","source_ip":}]`
	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed JSON, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_json") {
		t.Errorf("expected code invalid_json; body: %s", w.Body.String())
	}
}

// --- missing user_email ---

func TestHandleProcessLogEntries_MissingUserEmail(t *testing.T) {
	cfg := defaultCfg()
	router := setupRouter(cfg, &testEnqueuer{})

	body := `[{"source_ip":"1.2.3.4"}]`
	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing email, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_json") {
		t.Errorf("expected code invalid_json; body: %s", w.Body.String())
	}
}

// --- error code on invalid IP ---

func TestHandleProcessLogEntries_InvalidIPCode(t *testing.T) {
	cfg := defaultCfg()
	router := setupRouter(cfg, &testEnqueuer{})

	body := `[{"user_email":"a@b.com","source_ip":"not-an-ip"}]`
	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_ip") {
		t.Errorf("expected code invalid_ip; body: %s", w.Body.String())
	}
}

// --- error code on too many entries ---

func TestHandleProcessLogEntries_TooManyEntriesCode(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxLogEntriesPerRequest = 1
	router := setupRouter(cfg, &testEnqueuer{})

	body := `[{"user_email":"a@b.com","source_ip":"1.2.3.4"},{"user_email":"b@b.com","source_ip":"1.2.3.5"}]`
	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "too_many_entries") {
		t.Errorf("expected code too_many_entries; body: %s", w.Body.String())
	}
}

// --- STRICT_JSON_DECODE tests (Commit P) ---

func TestHandleProcessLogEntries_StrictMode_False_AcceptsUnknownFields(t *testing.T) {
	cfg := defaultCfg()
	cfg.StrictJSONDecode = false // default
	eq := &testEnqueuer{}
	router := setupRouter(cfg, eq)

	// Body с дополнительным полем "extra_field"
	body := `[{"user_email":"a@b.com","source_ip":"1.2.3.4","extra_field":"should be ignored"}]`
	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("strict=false should accept unknown fields; got %d; body: %s", w.Code, w.Body.String())
	}
	if eq.enqueued != 1 {
		t.Errorf("expected 1 enqueued entry, got %d", eq.enqueued)
	}
}

func TestHandleProcessLogEntries_StrictMode_True_RejectsUnknownFields(t *testing.T) {
	cfg := defaultCfg()
	cfg.StrictJSONDecode = true
	router := setupRouter(cfg, &testEnqueuer{})

	// Body с дополнительным полем "unknown_field"
	body := `[{"user_email":"a@b.com","source_ip":"1.2.3.4","unknown_field":"reject me"}]`
	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("strict=true should reject unknown fields; got %d; body: %s", w.Code, w.Body.String())
	}
	// Код ошибки должен быть "unknown_field" или "invalid_json" (DisallowUnknownFields даёт общую ошибку)
	if !strings.Contains(w.Body.String(), "unknown") && !strings.Contains(w.Body.String(), "invalid") {
		t.Errorf("expected error code with 'unknown' or 'invalid'; body: %s", w.Body.String())
	}
}

// --- happy path ---

func TestHandleProcessLogEntries_Success(t *testing.T) {
	cfg := defaultCfg()
	eq := &testEnqueuer{}
	router := setupRouter(cfg, eq)

	body := `[{"user_email":"user@example.com","source_ip":"10.0.0.1"},{"user_email":"user@example.com","source_ip":"10.0.0.2"}]`
	req := httptest.NewRequest("POST", "/log-entry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d; body: %s", w.Code, w.Body.String())
	}
	if eq.enqueued != 2 {
		t.Errorf("expected 2 enqueued entries, got %d", eq.enqueued)
	}
}
