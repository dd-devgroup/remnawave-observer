package processor

import (
	"context"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"observer_service/internal/services/enforcement"
	"testing"
	"time"
)

// Mock implementations for testing

type MockStorage struct {
	checkAndAddASNCalls int
}

func (m *MockStorage) CheckAndAddASN(_ context.Context, _, _ string, _ int, _, _ time.Duration) (*models.CheckResult, error) {
	m.checkAndAddASNCalls++
	return &models.CheckResult{StatusCode: 0, CurrentCount: int64(m.checkAndAddASNCalls), IsNew: true}, nil
}

func (m *MockStorage) AddIPToASNMapping(_ context.Context, _, _, _ string, _ time.Duration) error {
	return nil
}
func (m *MockStorage) SetASNOrgName(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}
func (m *MockStorage) GetIPsForUserASN(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (m *MockStorage) GetASNOrgName(_ context.Context, _ string) (string, error) { return "", nil }
func (m *MockStorage) GetUserActiveASNs(_ context.Context, _ string) (map[string]*models.ASNInfo, error) {
	return make(map[string]*models.ASNInfo), nil
}
func (m *MockStorage) HasAlertCooldown(_ context.Context, _ string) (bool, error) { return false, nil }
func (m *MockStorage) AcquireAlertPermit(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}
func (m *MockStorage) ClearUserASNData(_ context.Context, _ string) (int, error) { return 0, nil }
func (m *MockStorage) Ping(_ context.Context) error                              { return nil }
func (m *MockStorage) Close() error                                              { return nil }

type MockAlerter struct {
	alertsSent int
}

func (m *MockAlerter) SendAlert(_ context.Context, _ models.AlertPayload) error {
	m.alertsSent++
	return nil
}

func TestLogProcessor_ASNMode_Initialization(t *testing.T) {
	cfg := &config.Config{
		MaxASNsPerUser:              4,
		UserASNTTL:                  time.Hour,
		AlertCooldown:               time.Minute,
		LogChannelBufferSize:        10,
		SideEffectChannelBufferSize: 10,
		WorkerPoolSize:              2,
		SideEffectWorkerPoolSize:    2,
		ExcludedUsers:               make(map[string]bool),
		ExcludedASNs:                make(map[string]bool),
	}

	stor := &MockStorage{}
	alrt := &MockAlerter{}

	processor := NewLogProcessor(stor, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil, nil)

	if processor.cfg.MaxASNsPerUser != 4 {
		t.Errorf("Expected MaxASNsPerUser to be 4, got %d", processor.cfg.MaxASNsPerUser)
	}
}

func TestLogProcessor_ASNMode_ProcessEntry_UNKNOWN_Fallback(t *testing.T) {
	ctx := context.Background()

	cfg := &config.Config{
		MaxASNsPerUser:              4,
		UserASNTTL:                  time.Hour,
		AlertCooldown:               time.Minute,
		LogChannelBufferSize:        10,
		SideEffectChannelBufferSize: 10,
		WorkerPoolSize:              2,
		SideEffectWorkerPoolSize:    2,
		ExcludedUsers:               make(map[string]bool),
		ExcludedASNs:                make(map[string]bool),
	}

	stor := &MockStorage{}
	alrt := &MockAlerter{}

	// Without ASN lookup — UNKNOWN fallback will be used
	processor := NewLogProcessor(stor, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil, nil)

	entry := models.LogEntry{
		UserEmail: "test@example.com",
		SourceIP:  "8.8.8.8",
	}

	processor.processSingleEntry(ctx, entry)

	// CheckAndAddASN should be called once with UNKNOWN identifier
	if stor.checkAndAddASNCalls != 1 {
		t.Errorf("Expected CheckAndAddASN to be called once, got %d", stor.checkAndAddASNCalls)
	}
}

func TestLogProcessor_ASNMode_ExcludedASN(t *testing.T) {
	ctx := context.Background()

	cfg := &config.Config{
		MaxASNsPerUser:              4,
		UserASNTTL:                  time.Hour,
		AlertCooldown:               time.Minute,
		LogChannelBufferSize:        10,
		SideEffectChannelBufferSize: 10,
		WorkerPoolSize:              2,
		SideEffectWorkerPoolSize:    2,
		ExcludedUsers:               make(map[string]bool),
		ExcludedASNs: map[string]bool{
			"AS15169": true, // Google
		},
	}

	stor := &MockStorage{}
	alrt := &MockAlerter{}

	// Note: Without real ASN lookup, UNKNOWN fallback is used (not excluded)
	processor := NewLogProcessor(stor, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil, nil)

	entry := models.LogEntry{
		UserEmail: "test@example.com",
		SourceIP:  "8.8.8.8", // Google DNS
	}

	processor.processSingleEntry(ctx, entry)

	// Without real ASN lookup, falls back to UNKNOWN — not in exclusion list
	if stor.checkAndAddASNCalls != 1 {
		t.Errorf("Expected CheckAndAddASN to be called once (UNKNOWN not excluded), got %d", stor.checkAndAddASNCalls)
	}
}

func TestLogProcessor_UNKNOWN_Fallback_MultipleIPs(t *testing.T) {
	ctx := context.Background()

	cfg := &config.Config{
		MaxASNsPerUser:              4,
		UserASNTTL:                  time.Hour,
		AlertCooldown:               time.Minute,
		LogChannelBufferSize:        10,
		SideEffectChannelBufferSize: 10,
		WorkerPoolSize:              2,
		SideEffectWorkerPoolSize:    2,
		ExcludedUsers:               make(map[string]bool),
		ExcludedASNs:                make(map[string]bool),
	}

	stor := &MockStorage{}
	alrt := &MockAlerter{}

	processor := NewLogProcessor(stor, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil, nil)

	// All IPs without ASN lookup should use UNKNOWN — same identifier each time
	testIPs := []string{
		"176.59.40.10",
		"176.59.172.25",
		"176.59.164.100",
	}

	for _, ip := range testIPs {
		entry := models.LogEntry{
			UserEmail: "test@example.com",
			SourceIP:  ip,
		}
		processor.processSingleEntry(ctx, entry)
	}

	// All 3 IPs should call CheckAndAddASN with UNKNOWN identifier
	if stor.checkAndAddASNCalls != 3 {
		t.Errorf("Expected CheckAndAddASN to be called 3 times, got %d", stor.checkAndAddASNCalls)
	}
}
