package processor

import (
	"strings"
	"testing"
	"time"

	"observer_service/internal/config"
	"observer_service/internal/models"
	"observer_service/internal/services/enforcement"
)

func queueTestConfig(buffer int) *config.Config {
	return &config.Config{
		MaxASNsPerUser:              4,
		UserASNTTL:                  time.Hour,
		AlertCooldown:               time.Minute,
		LogChannelBufferSize:        buffer,
		SideEffectChannelBufferSize: 1,
		WorkerPoolSize:              1,
		SideEffectWorkerPoolSize:    1,
		ExcludedUsers:               make(map[string]bool),
		ExcludedASNs:                make(map[string]bool),
	}
}

func TestEnqueueEntries_ClosedChannelReturnsError(t *testing.T) {
	cfg := queueTestConfig(1)
	p := NewLogProcessor(&MockStorage{}, enforcement.NewNoopEnforcer(), &MockAlerter{}, cfg, nil, nil, nil, nil, nil, nil)

	close(p.logChannel)

	err := p.EnqueueEntries([]models.LogEntry{{UserEmail: "1", SourceIP: "1.1.1.1"}})
	if err == nil {
		t.Fatal("expected error when enqueueing into closed channel, got nil")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Fatalf("expected closed-channel error, got: %v", err)
	}
}

func TestEnqueueEntries_FullChannelReturnsError(t *testing.T) {
	cfg := queueTestConfig(1)
	p := NewLogProcessor(&MockStorage{}, enforcement.NewNoopEnforcer(), &MockAlerter{}, cfg, nil, nil, nil, nil, nil, nil)

	first := p.EnqueueEntries([]models.LogEntry{{UserEmail: "1", SourceIP: "1.1.1.1"}})
	if first != nil {
		t.Fatalf("first enqueue failed unexpectedly: %v", first)
	}

	err := p.EnqueueEntries([]models.LogEntry{{UserEmail: "1", SourceIP: "1.1.1.2"}})
	if err == nil {
		t.Fatal("expected error when enqueueing into full channel, got nil")
	}
	if !strings.Contains(err.Error(), "full") {
		t.Fatalf("expected full-channel error, got: %v", err)
	}
}

func TestTargetLogWorkerCount_GrowsOnHighQueueLoad(t *testing.T) {
	cfg := queueTestConfig(8)
	cfg.WorkerPoolSize = 1
	p := NewLogProcessor(&MockStorage{}, enforcement.NewNoopEnforcer(), &MockAlerter{}, cfg, nil, nil, nil, nil, nil, nil)

	for i := 0; i < 7; i++ {
		p.logChannel <- []models.LogEntry{{UserEmail: "1", SourceIP: "1.1.1.1"}}
	}

	target := p.targetLogWorkerCount()
	if target <= p.logWorkerBase {
		t.Fatalf("expected target workers > base (%d), got %d", p.logWorkerBase, target)
	}
}

func TestCalculateEnqueueBackpressureWait_IncreasesWithQueuePressure(t *testing.T) {
	cfg := queueTestConfig(10)
	p := NewLogProcessor(&MockStorage{}, enforcement.NewNoopEnforcer(), &MockAlerter{}, cfg, nil, nil, nil, nil, nil, nil)

	low := p.calculateEnqueueBackpressureWait()
	if low < 20*time.Millisecond {
		t.Fatalf("expected at least 20ms on empty queue, got %v", low)
	}

	for i := 0; i < 9; i++ {
		p.logChannel <- []models.LogEntry{{UserEmail: "1", SourceIP: "1.1.1.1"}}
	}

	high := p.calculateEnqueueBackpressureWait()
	if high <= low {
		t.Fatalf("expected higher wait on high pressure (low=%v, high=%v)", low, high)
	}
}
