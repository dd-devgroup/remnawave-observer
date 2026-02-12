package processor

import (
	"context"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"sync"
	"testing"
	"time"
)

// --- controllable mocks ------------------------------------------------

// asnMockStorage returns a fixed CheckResult for CheckAndAddASN.
type asnMockStorage struct {
	MockStorage
	mu     sync.Mutex
	result *models.CheckResult
}

func (s *asnMockStorage) CheckAndAddASN(_ context.Context, _, _ string, _ int, _, _ time.Duration) (*models.CheckResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, nil
}

// capturingEnforcer captures disable calls for testing.
type capturingEnforcer struct {
	mu       sync.Mutex
	disables []disableCall
}

type disableCall struct {
	internalID int64
	duration   time.Duration
	reason     string
	score      int
}

func (e *capturingEnforcer) DisableTempByInternalID(_ context.Context, internalID int64, duration time.Duration, reason string, score int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disables = append(e.disables, disableCall{internalID, duration, reason, score})
	return nil
}

func (e *capturingEnforcer) Ping(_ context.Context) error {
	return nil
}

func (e *capturingEnforcer) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.disables)
}

func (e *capturingEnforcer) get(i int) disableCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.disables[i]
}

// capturingAlerter records every AlertPayload.
type capturingAlerter struct {
	mu     sync.Mutex
	alerts []models.AlertPayload
}

func (a *capturingAlerter) SendAlert(_ context.Context, payload models.AlertPayload) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, payload)
	return nil
}

func (a *capturingAlerter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.alerts)
}

func (a *capturingAlerter) get(i int) models.AlertPayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.alerts[i]
}

// --- helpers ------------------------------------------------------------

func asnCfg(maxASNs int) *config.Config {
	return &config.Config{
		MaxASNsPerUser:              maxASNs,
		UserASNTTL:                  time.Hour,
		AlertCooldown:               time.Minute,
		BlockDuration:               "3600",
		LogChannelBufferSize:        16,
		SideEffectChannelBufferSize: 16,
		WorkerPoolSize:              2,
		SideEffectWorkerPoolSize:    2,
		SideEffectTimeout:           5 * time.Second,
		ExcludedUsers:               map[string]bool{},
		ExcludedIPs:                 map[string]bool{},
		ExcludedASNs:                map[string]bool{},
	}
}

// --- Test 1: ASN limit exceeded triggers enforcement --------------------

func TestIntegration_ASNMode_LimitExceeded_DisablesUser(t *testing.T) {
	allASNs := []string{"AS13335", "AS15169", "AS32934", "AS16509"}
	stor := &asnMockStorage{
		result: &models.CheckResult{
			StatusCode:   1, // limit exceeded
			CurrentCount: 4,
			IsNew:        false,
			AllUserItems: allASNs,
		},
	}
	alrt := &capturingAlerter{}
	cfg := asnCfg(3)

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, enf, alrt, cfg, nil, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go proc.StartSideEffectWorkerPool(ctx, &wg)

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "12345", SourceIP: "10.0.0.4"},
	})

	time.Sleep(50 * time.Millisecond)

	if enf.count() != 1 {
		t.Fatalf("expected 1 disable call, got %d", enf.count())
	}
	call := enf.get(0)
	if call.internalID != 12345 {
		t.Errorf("expected internalID 12345, got %d", call.internalID)
	}
	// BlockDuration "3600" is invalid (no unit), so disableUser falls back to 5m
	if call.duration != 5*time.Minute {
		t.Errorf("expected duration 5m0s, got %v", call.duration)
	}
	if call.reason != "asn_limit_exceeded: 4/3 ASNs" {
		t.Errorf("expected reason 'asn_limit_exceeded: 4/3 ASNs', got %q", call.reason)
	}

	if alrt.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", alrt.count())
	}
	alert := alrt.get(0)
	if alert.UserIdentifier != "12345" {
		t.Errorf("alert user = %q, want 12345", alert.UserIdentifier)
	}
	if alert.ViolationType != "asn_limit_exceeded" {
		t.Errorf("violation_type = %q, want asn_limit_exceeded", alert.ViolationType)
	}
}

// --- Test 2: excluded IPs are filtered from block IP collection ---------

func TestIntegration_ASNMode_ExcludedIPs_FilteredFromBlock(t *testing.T) {
	// Excluded IPs are still processed for ASN tracking but filtered
	// from the IP list sent in block events (filterExcludedIPs).
	// Entry-level processing is not skipped for excluded IPs.
	stor := &asnMockStorage{
		result: &models.CheckResult{
			StatusCode:   1,
			CurrentCount: 3,
			IsNew:        false,
			AllUserItems: []string{"AS13335", "AS15169", "UNKNOWN"},
		},
	}
	alrt := &capturingAlerter{}
	cfg := asnCfg(2)
	cfg.ExcludedIPs = map[string]bool{
		"192.168.1.100": true,
	}

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, enf, alrt, cfg, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "22222", SourceIP: "192.168.1.100"},
	})

	// User-level enforcement still triggers (excluded IPs only filter block IP lists)
	if enf.count() != 1 {
		t.Errorf("expected 1 disable call (excluded IPs don't prevent enforcement), got %d", enf.count())
	}
}

// --- Test 3: excluded user is silently skipped --------------------------

func TestIntegration_ExcludedUser_NoEnforcement(t *testing.T) {
	stor := &asnMockStorage{
		result: &models.CheckResult{StatusCode: 1, CurrentCount: 5, AllUserItems: []string{"AS13335"}},
	}
	alrt := &capturingAlerter{}
	cfg := asnCfg(2)
	cfg.ExcludedUsers = map[string]bool{"33333": true}

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, enf, alrt, cfg, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "33333", SourceIP: "1.2.3.4"},
	})

	if enf.count() != 0 {
		t.Errorf("excluded user must not trigger disable, got %d calls", enf.count())
	}
}

// --- Test 4: context cancellation stops entry processing ----------------

func TestIntegration_ContextCancellation_StopsProcessing(t *testing.T) {
	stor := &asnMockStorage{
		result: &models.CheckResult{StatusCode: 0, CurrentCount: 1, IsNew: true},
	}
	alrt := &capturingAlerter{}
	cfg := asnCfg(10)

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, enf, alrt, cfg, nil, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	entries := make([]models.LogEntry, 5)
	for i := range entries {
		entries[i] = models.LogEntry{UserEmail: "99999", SourceIP: "1.2.3.4"}
	}
	proc.ProcessEntries(ctx, entries)

	if enf.count() != 0 {
		t.Errorf("no enforcement expected with cancelled ctx, got %d", enf.count())
	}
}
