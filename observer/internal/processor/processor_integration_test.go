package processor

import (
	"context"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"observer_service/internal/services/enforcement"
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

// capturingPublisher records every BlockMessage it receives (thread-safe).
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

	proc := NewLogProcessor(stor, pub, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start side-effect workers so alert tasks actually execute.
	var wg sync.WaitGroup
	wg.Add(1)
	go proc.StartSideEffectWorkerPool(ctx, &wg)

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "alice@test.com", SourceIP: "10.0.0.4"},
	})

	// Give side-effect worker a tick to pick up the alert task.
	time.Sleep(50 * time.Millisecond)

	// Exactly one block message with all 4 IPs (no chunking needed).
	if pub.count() != 1 {
		t.Fatalf("expected 1 published message, got %d", pub.count())
	}
	msg := pub.get(0)
	if len(msg.IPs) != 4 {
		t.Errorf("expected 4 IPs in block message, got %d", len(msg.IPs))
	}
	if msg.Duration != "3600" {
		t.Errorf("expected duration 3600, got %q", msg.Duration)
	}
	// Single-chunk messages must NOT have chunking envelope.
	if msg.EventID != "" || msg.ChunkIndex != nil || msg.ChunkTotal != nil || msg.SchemaVersion != 0 {
		t.Errorf("single-chunk message must not have chunking fields; got %+v", msg)
	}

	// Alert must have been delivered via side-effect.
	if alrt.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", alrt.count())
	}
	alert := alrt.get(0)
	if alert.UserIdentifier != "alice@test.com" {
		t.Errorf("alert user = %q, want alice@test.com", alert.UserIdentifier)
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

	proc := NewLogProcessor(stor, pub, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil)
	ctx := context.Background()

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "bob@test.com", SourceIP: "10.0.0.1"},
	})

	if pub.count() != 2 {
		t.Fatalf("expected 2 chunks, got %d", pub.count())
	}

	// Chunk 0
	c0 := pub.get(0)
	if len(c0.IPs) != 500 {
		t.Errorf("chunk 0: expected 500 IPs, got %d", len(c0.IPs))
	}
	if c0.ChunkIndex == nil || *c0.ChunkIndex != 0 {
		t.Errorf("chunk 0: ChunkIndex should be 0, got %v", c0.ChunkIndex)
	}
	if c0.ChunkTotal == nil || *c0.ChunkTotal != 2 {
		t.Errorf("chunk 0: ChunkTotal should be 2, got %v", c0.ChunkTotal)
	}
	if c0.SchemaVersion != 2 {
		t.Errorf("chunk 0: SchemaVersion should be 2, got %d", c0.SchemaVersion)
	}
	if c0.EventID == "" {
		t.Error("chunk 0: EventID must not be empty")
	}

	// Chunk 1
	c1 := pub.get(1)
	if len(c1.IPs) != 100 {
		t.Errorf("chunk 1: expected 100 IPs, got %d", len(c1.IPs))
	}
	if c1.ChunkIndex == nil || *c1.ChunkIndex != 1 {
		t.Errorf("chunk 1: ChunkIndex should be 1, got %v", c1.ChunkIndex)
	}

	// Both chunks share the same EventID.
	if c0.EventID != c1.EventID {
		t.Errorf("EventID mismatch between chunks: %q vs %q", c0.EventID, c1.EventID)
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

	proc := NewLogProcessor(stor, pub, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go proc.StartSideEffectWorkerPool(ctx, &wg)

	// Use an IPv4 address so subnet derivation works.
	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "charlie@test.com", SourceIP: "192.168.1.5"},
	})

	time.Sleep(50 * time.Millisecond)

	if pub.count() != 1 {
		t.Fatalf("expected 1 published message, got %d", pub.count())
	}
	msg := pub.get(0)
	if len(msg.IPs) != 3 {
		t.Errorf("expected 3 subnets in block message, got %d: %v", len(msg.IPs), msg.IPs)
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

	proc := NewLogProcessor(stor, pub, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil)
	ctx := context.Background()

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "dave@test.com", SourceIP: "10.0.0.2"},
	})

	if pub.count() != 1 {
		t.Fatalf("expected 1 published message, got %d", pub.count())
	}
	msg := pub.get(0)
	// 192.168.1.100 should have been stripped.
	if len(msg.IPs) != 2 {
		t.Errorf("expected 2 IPs after exclusion, got %d: %v", len(msg.IPs), msg.IPs)
	}
	for _, ip := range msg.IPs {
		if ip == "192.168.1.100" {
			t.Error("excluded IP 192.168.1.100 must not appear in block message")
		}
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
	cfg.ExcludedUsers = map[string]bool{"skip@test.com": true}

	proc := NewLogProcessor(stor, pub, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil)
	ctx := context.Background()

	proc.ProcessEntries(ctx, []models.LogEntry{
		{UserEmail: "skip@test.com", SourceIP: "1.2.3.4"},
	})

	if pub.count() != 0 {
		t.Errorf("excluded user must not trigger publish, got %d messages", pub.count())
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

	proc := NewLogProcessor(stor, pub, enforcement.NewNoopEnforcer(), alrt, cfg, nil, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	// Feed 5 entries — none should be processed past the ctx check.
	entries := make([]models.LogEntry, 5)
	for i := range entries {
		entries[i] = models.LogEntry{UserEmail: "x@test.com", SourceIP: "1.2.3.4"}
	}
	proc.ProcessEntries(ctx, entries)

	// With a cancelled context the loop breaks after the first select hits
	// ctx.Done(); at most 1 entry may have slipped through the default branch
	// before the cancellation was visible. The important thing: no panic.
	if pub.count() != 0 {
		t.Errorf("no publish expected with cancelled ctx, got %d", pub.count())
	}
}
