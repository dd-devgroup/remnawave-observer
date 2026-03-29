package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type testStorage struct{}

func (t *testStorage) CheckAndAddASN(_ context.Context, _, _ string, _ int, _, _ time.Duration) (*models.CheckResult, error) {
	return nil, nil
}
func (t *testStorage) AddIPToASNMapping(_ context.Context, _, _, _ string, _ time.Duration) error {
	return nil
}
func (t *testStorage) SetASNOrgName(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}
func (t *testStorage) GetIPsForUserASN(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (t *testStorage) GetASNOrgName(_ context.Context, _ string) (string, error) { return "", nil }
func (t *testStorage) GetUserActiveASNs(_ context.Context, _ string) (map[string]*models.ASNInfo, error) {
	return nil, nil
}
func (t *testStorage) HasAlertCooldown(_ context.Context, _ string) (bool, error) { return false, nil }
func (t *testStorage) AcquireAlertPermit(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}
func (t *testStorage) ClearUserASNData(_ context.Context, _ string) (int, error) { return 0, nil }
func (t *testStorage) Ping(_ context.Context) error                              { return nil }
func (t *testStorage) Close() error                                              { return nil }

func setupRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	server := NewServer("9000", &testStorage{}, &config.Config{})
	return server.GetRouter()
}

func TestHandleLegacyLogIngestRemoved(t *testing.T) {
	router := setupRouter()

	req := httptest.NewRequest(http.MethodPost, "/log-entry", strings.NewReader(`[{"user_email":"1","source_ip":"1.2.3.4"}]`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "legacy_ingest_removed") {
		t.Fatalf("expected legacy_ingest_removed code, got body: %s", w.Body.String())
	}
}

func TestHealthCheckOK(t *testing.T) {
	router := setupRouter()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("expected status ok, got body: %s", w.Body.String())
	}
}
