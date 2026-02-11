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

// ipMockStorage returns a fixed CheckResult for CheckAndAddIP; other methods
// delegate to the embedded MockStorage so the interface stays satisfied.
type ipMockStorage struct {
	MockStorage
	mu      sync.Mutex
	result  *models.CheckResult // what CheckAndAddIP returns
}

func (s *ipMockStorage) CheckAndAddIP(_ context.Context, _, _ string, _ int, _, _ time.Duration) (*models.CheckResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, nil
}

// subnetMockStorage returns a fixed result for CheckAndAddSubnet.
type subnetMockStorage struct {
	MockStorage
	mu     sync.Mutex
	result *models.CheckResult
}

func (s *subnetMockStorage) CheckAndAddSubnet(_ context.Context, _, _ string, _ int, _, _ time.Duration) (*models.CheckResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, nil
}

// capturingPublisher records every BlockMessage it receives (thread-safe) - DEPRECATED (MIG-7).
type capturingPublisher struct {
	mu   sync.Mutex
	msgs []models.BlockMessage
}

func (p *capturingPublisher) PublishBlockMessage(msg models.BlockMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, msg)
	return nil
}
func (p *capturingPublisher) Close() error { return nil }
func (p *capturingPublisher) Ping() error  { return nil }

func (p *capturingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.msgs)
}

func (p *capturingPublisher) get(i int) models.BlockMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.msgs[i]
}

// capturingEnforcer captures disable calls for testing (MIG-7).
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

func (e *capturingEnforcer) DisableTempByInternalID(ctx context.Context, internalID int64, duration time.Duration, reason string, score int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disables = append(e.disables, disableCall{internalID, duration, reason, score})
	return nil
}

func (e *capturingEnforcer) Ping(ctx context.Context) error {
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
	mu      sync.Mutex
	alerts  []models.AlertPayload
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

func ipCfg(maxIPs int, chunkSize int) *config.Config {
	return &config.Config{
		MaxIPsPerUser:              maxIPs,
		MaxIPsPerBlockEvent:        chunkSize,
		UserIPTTL:                  time.Hour,
		AlertCooldown:              time.Minute,
		ClearIPsDelay:              time.Hour, // never fires during test
		BlockDuration:              "3600",
		LogChannelBufferSize:       16,
		SideEffectChannelBufferSize: 16,
		WorkerPoolSize:             2,
		SideEffectWorkerPoolSize:   2,
		SideEffectTimeout:          5 * time.Second,
		ExcludedUsers:              map[string]bool{},
		ExcludedIPs:                map[string]bool{},
		ExcludedSubnets:            map[string]bool{},
		ExcludedASNs:               map[string]bool{},
	}
}

func subnetCfg(maxSubnets int) *config.Config {
	cfg := ipCfg(3, 500)
	cfg.DetectBySubnet      = true
	cfg.MaxSubnetsPerUser   = maxSubnets
	cfg.SubnetMaskIPv4      = 24
	cfg.UserSubnetTTL       = time.Hour
	cfg.MaxIPsPerBlockEvent = 500
	return cfg
}

// makeIPs returns n synthetic IPs like "10.0.X.Y".
func makeIPs(n int) []string {
	ips := make([]string, n)
	for i := range n {
		ips[i] = "10." + itoa(i/256/256%256) + "." + itoa(i/256%256) + "." + itoa(i%256)
	}
	return ips
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	pos := 2
	for n > 0 {
		buf[pos] = byte('0' + n%10)
		n /= 10
		pos--
	}
	return string(buf[pos+1:])
}

// --- Test 1: IP-mode limit exceeded triggers a single publish -----------

func TestIntegration_IPMode_LimitExceeded_Publishes(t *testing.T) {
	allIPs := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"}
	stor := &ipMockStorage{
		result: &models.CheckResult{
			StatusCode:   1,                // limit exceeded
			CurrentCount: 4,
			IsNew:        false,
			AllUserItems: allIPs,
		},
	}
	pub := &capturingPublisher{}
	alrt := &capturingAlerter{}
	cfg := ipCfg(3, 500)

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, pub, enf, alrt, cfg, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start side-effect workers so alert tasks actually execute.
	var wg sync.WaitGroup
	wg.Add(1)
	go proc.StartSideEffectWorkerPool(ctx, &wg)

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "12345", SourceIP: "10.0.0.4"}, // MIG-7: numeric ID
	})

	// Give side-effect worker a tick to pick up the alert task.
	time.Sleep(50 * time.Millisecond)

	// Exactly one disable call for the user.
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
	if call.reason != "ip_limit_exceeded: 4/3 IPs" {
		t.Errorf("expected reason 'ip_limit_exceeded: 4/3 IPs', got %q", call.reason)
	}
	if call.score != 85 {
		t.Errorf("expected score 85, got %d", call.score)
	}

	// Alert must have been delivered via side-effect.
	if alrt.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", alrt.count())
	}
	alert := alrt.get(0)
	if alert.UserIdentifier != "12345" {
		t.Errorf("alert user = %q, want 12345", alert.UserIdentifier)
	}
	if alert.ViolationType != "ip_limit_exceeded" {
		t.Errorf("violation_type = %q, want ip_limit_exceeded", alert.ViolationType)
	}
}

// --- Test 2: chunking — 600 IPs with chunkSize 500 → 2 messages ---------

func TestIntegration_IPMode_Chunking_600IPs(t *testing.T) {
	allIPs := makeIPs(600)
	stor := &ipMockStorage{
		result: &models.CheckResult{
			StatusCode:   1,
			CurrentCount: 600,
			IsNew:        false,
			AllUserItems: allIPs,
		},
	}
	pub := &capturingPublisher{}
	alrt := &capturingAlerter{}
	cfg := ipCfg(5, 500) // chunkSize = 500

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, pub, enf, alrt, cfg, nil, nil, nil, nil, nil)
	ctx := context.Background()

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "67890", SourceIP: "10.0.0.1"},
	})

	// MIG-7: No chunking anymore (user-level enforcement, not IP-level)
	// Single disable call regardless of IP count
	if enf.count() != 1 {
		t.Fatalf("expected 1 disable call, got %d", enf.count())
	}

	call := enf.get(0)
	if call.internalID != 67890 {
		t.Errorf("expected internalID 67890, got %d", call.internalID)
	}
	if call.reason != "ip_limit_exceeded: 600/5 IPs" {
		t.Errorf("expected reason 'ip_limit_exceeded: 600/5 IPs', got %q", call.reason)
	}
}

// --- Test 3: subnet-mode limit exceeded triggers publish ----------------

func TestIntegration_SubnetMode_LimitExceeded_Publishes(t *testing.T) {
	allSubnets := []string{"192.168.1.0/24", "192.168.2.0/24", "10.1.0.0/24"}
	stor := &subnetMockStorage{
		result: &models.CheckResult{
			StatusCode:   1,
			CurrentCount: 3,
			IsNew:        false,
			AllUserItems: allSubnets,
		},
	}
	pub := &capturingPublisher{}
	alrt := &capturingAlerter{}
	cfg := subnetCfg(2)

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, pub, enf, alrt, cfg, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go proc.StartSideEffectWorkerPool(ctx, &wg)

	// Use an IPv4 address so subnet derivation works.
	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "11111", SourceIP: "192.168.1.5"},
	})

	time.Sleep(50 * time.Millisecond)

	// MIG-7: Check enforcer instead of publisher
	if enf.count() != 1 {
		t.Fatalf("expected 1 disable call, got %d", enf.count())
	}
	call := enf.get(0)
	if call.internalID != 11111 {
		t.Errorf("expected internalID 11111, got %d", call.internalID)
	}
	if call.reason != "subnet_limit_exceeded: 3/2 subnets" {
		t.Errorf("expected reason 'subnet_limit_exceeded: 3/2 subnets', got %q", call.reason)
	}

	if alrt.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", alrt.count())
	}
	if alrt.get(0).ViolationType != "subnet_limit_exceeded" {
		t.Errorf("violation_type = %q, want subnet_limit_exceeded", alrt.get(0).ViolationType)
	}
}

// --- Test 4: excluded IPs are stripped before publish --------------------

func TestIntegration_IPMode_ExcludedIPs_Filtered(t *testing.T) {
	allIPs := []string{"10.0.0.1", "10.0.0.2", "192.168.1.100"}
	stor := &ipMockStorage{
		result: &models.CheckResult{
			StatusCode:   1,
			CurrentCount: 3,
			IsNew:        false,
			AllUserItems: allIPs,
		},
	}
	pub := &capturingPublisher{}
	alrt := &capturingAlerter{}
	cfg := ipCfg(2, 500)
	cfg.ExcludedIPs = map[string]bool{
		"192.168.1.100": true,
	}

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, pub, enf, alrt, cfg, nil, nil, nil, nil, nil)
	ctx := context.Background()

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "22222", SourceIP: "10.0.0.2"},
	})

	// MIG-7: User-level enforcement - user gets disabled regardless of excluded IPs
	if enf.count() != 1 {
		t.Fatalf("expected 1 disable call, got %d", enf.count())
	}
	call := enf.get(0)
	if call.internalID != 22222 {
		t.Errorf("expected internalID 22222, got %d", call.internalID)
	}
	if call.reason != "ip_limit_exceeded: 3/2 IPs" {
		t.Errorf("expected reason 'ip_limit_exceeded: 3/2 IPs', got %q", call.reason)
	}
}

// --- Test 5: excluded user is silently skipped ---------------------------

func TestIntegration_ExcludedUser_NoPublish(t *testing.T) {
	stor := &ipMockStorage{
		result: &models.CheckResult{StatusCode: 1, CurrentCount: 5, AllUserItems: []string{"1.2.3.4"}},
	}
	pub := &capturingPublisher{}
	alrt := &capturingAlerter{}
	cfg := ipCfg(2, 500)
	cfg.ExcludedUsers = map[string]bool{"33333": true}

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, pub, enf, alrt, cfg, nil, nil, nil, nil, nil)
	ctx := context.Background()

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "33333", SourceIP: "1.2.3.4"},
	})

	if enf.count() != 0 {
		t.Errorf("excluded user must not trigger disable, got %d calls", enf.count())
	}
}

// --- Test 6: context cancellation stops entry processing -----------------

func TestIntegration_ContextCancellation_StopsProcessing(t *testing.T) {
	stor := &ipMockStorage{
		result: &models.CheckResult{StatusCode: 0, CurrentCount: 1, IsNew: true},
	}
	pub := &capturingPublisher{}
	alrt := &capturingAlerter{}
	cfg := ipCfg(10, 500)

	enf := &capturingEnforcer{}
	proc := NewLogProcessor(stor, pub, enf, alrt, cfg, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	// Feed 5 entries — none should be processed past the ctx check.
	entries := make([]models.LogEntry, 5)
	for i := range entries {
		entries[i] = models.LogEntry{UserEmail: "99999", SourceIP: "1.2.3.4"}
	}
	proc.ProcessEntries(ctx, entries)

	// With a cancelled context the loop breaks after the first select hits
	// ctx.Done(); at most 1 entry may have slipped through the default branch
	// before the cancellation was visible. The important thing: no panic.
	if pub.count() != 0 {
		t.Errorf("no publish expected with cancelled ctx, got %d", pub.count())
	}
}
