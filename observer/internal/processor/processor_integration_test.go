package processor

import (
	"context"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"observer_service/internal/services/scoring"
	"sync"
	"testing"
	"time"
)

// --- controllable mocks ------------------------------------------------

// asnMockStorage returns a fixed CheckResult for CheckAndAddASN.
type asnMockStorage struct {
	MockStorage
	mu                 sync.Mutex
	result             *models.CheckResult
	clearCh            chan struct{}
	acquireAlertPermit bool
}

func (s *asnMockStorage) CheckAndAddASN(_ context.Context, _, _ string, _ int, _, _ time.Duration) (*models.CheckResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, nil
}

func (s *asnMockStorage) ClearUserASNData(_ context.Context, _ string) (int, error) {
	s.mu.Lock()
	clearCh := s.clearCh
	s.mu.Unlock()

	if clearCh != nil {
		select {
		case clearCh <- struct{}{}:
		default:
		}
	}

	return 1, nil
}

func (s *asnMockStorage) AcquireAlertPermit(_ context.Context, _ string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquireAlertPermit, nil
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

// --- Test 1: legacy ASN limit statuses are ignored in scoring-only mode ---

func TestIntegration_ASNMode_LegacyLimitExceeded_IsIgnored(t *testing.T) {
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

	if enf.count() != 0 {
		t.Fatalf("expected 0 disable calls, got %d", enf.count())
	}

	if alrt.count() != 0 {
		t.Fatalf("expected 0 alerts, got %d", alrt.count())
	}
}

func TestIntegration_ASNMode_LegacyLimitExceeded_DoesNotScheduleCleanup(t *testing.T) {
	stor := &asnMockStorage{
		result: &models.CheckResult{
			StatusCode:   1,
			CurrentCount: 4,
			AllUserItems: []string{"AS13335", "AS15169", "AS32934", "AS16509"},
		},
		clearCh: make(chan struct{}, 1),
	}
	alrt := &capturingAlerter{}
	cfg := asnCfg(3)
	cfg.ClearIPsDelay = 10 * time.Millisecond

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, enf, alrt, cfg, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "12345", SourceIP: "10.0.0.4"},
	})

	select {
	case <-stor.clearCh:
		t.Fatal("did not expect ClearUserASNData call without blocking score-based enforcement")
	case <-time.After(200 * time.Millisecond):
		// No cleanup expected.
	}
}

// --- Test 2: legacy limit status does not bypass excluded-IP behavior ---

func TestIntegration_ASNMode_ExcludedIPs_NoDirectDisableOnLegacyLimit(t *testing.T) {
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

	if enf.count() != 0 {
		t.Errorf("expected 0 disable calls, got %d", enf.count())
	}
}

func TestIntegration_ScoringWarn_QueuesAlert(t *testing.T) {
	stor := &asnMockStorage{acquireAlertPermit: true}
	alrt := &capturingAlerter{}
	cfg := asnCfg(3)

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, enf, alrt, cfg, nil, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go proc.StartSideEffectWorkerPool(ctx, &wg)

	proc.maybeQueueScoringAlert(
		ctx,
		models.LogEntry{UserEmail: "12345", SourceIP: "10.0.0.4"},
		[]string{"AS13335"},
		map[string]*models.ASNInfo{"AS13335": {ASN: "AS13335", IPs: []string{"10.0.0.4"}, IPCount: 1}},
		nil,
		map[string]string{"AS13335": "hosting"},
		&scoring.ViolationScore{
			FinalScore: 55,
			Confidence: 0.9,
			Action:     scoring.ActionWarn,
			Features: []scoring.FeatureResult{
				{Name: "geo", Score: 70, Weight: 0.55, Confidence: 0.9, Details: "countries=2"},
			},
		},
		scoringTriggerContext{},
	)

	time.Sleep(50 * time.Millisecond)

	if alrt.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", alrt.count())
	}

	alert := alrt.get(0)
	if alert.ViolationType != "scoring_action" {
		t.Fatalf("expected scoring_action violation type, got %q", alert.ViolationType)
	}
	if alert.ScoreAction != string(scoring.ActionWarn) {
		t.Fatalf("expected score action warn, got %q", alert.ScoreAction)
	}
	if alert.Score == nil || *alert.Score != 55 {
		t.Fatalf("expected score 55, got %#v", alert.Score)
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
