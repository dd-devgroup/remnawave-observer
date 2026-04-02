package processor

import (
	"context"
	"observer_service/internal/database"
	"sync"
	"testing"
	"time"
)

// mockRepo captures InsertConnections calls.
type mockRepo struct {
	mu      sync.Mutex
	batches [][]database.UserConnection
}

func (m *mockRepo) InsertConnection(_ context.Context, _ *database.UserConnection) error { return nil }
func (m *mockRepo) InsertConnections(_ context.Context, conns []database.UserConnection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]database.UserConnection, len(conns))
	copy(cp, conns)
	m.batches = append(m.batches, cp)
	return nil
}
func (m *mockRepo) InsertScoreEvent(_ context.Context, _ *database.UserScoreEvent) error   { return nil }
func (m *mockRepo) InsertAction(_ context.Context, _ *database.AntiAbuseAction) error      { return nil }
func (m *mockRepo) GetUserConnectionsLast30d(_ context.Context, _ string) ([]database.UserConnection, error) {
	return nil, nil
}
func (m *mockRepo) GetDailyProfile(_ context.Context, _, _ string) (*database.UserDailyProfile, error) {
	return nil, nil
}
func (m *mockRepo) UpsertDailyProfile(_ context.Context, _ *database.UserDailyProfile) error { return nil }
func (m *mockRepo) GetEnrichment(_ context.Context, _ string) (*database.IPEnrichmentCache, error) {
	return nil, nil
}
func (m *mockRepo) UpsertEnrichment(_ context.Context, _ *database.IPEnrichmentCache) error {
	return nil
}
func (m *mockRepo) CleanExpiredEnrichments(_ context.Context) (int64, error) { return 0, nil }
func (m *mockRepo) GetASNOrgStats(_ context.Context, _ int) ([]database.ASNOrgStats, error) {
	return nil, nil
}
func (m *mockRepo) InsertCandidate(_ context.Context, _ *database.LearningCandidate) error {
	return nil
}
func (m *mockRepo) GetPendingCandidates(_ context.Context) ([]database.LearningCandidate, error) {
	return nil, nil
}
func (m *mockRepo) UpdateCandidateStatus(_ context.Context, _ uint, _ string) error { return nil }
func (m *mockRepo) GetActiveUsersForMonitor(_ context.Context, _ time.Time) ([]database.MonitorUserStats, error) {
	return nil, nil
}
func (m *mockRepo) GetLatestScoreEvent(_ context.Context, _ string) (*database.UserScoreEvent, error) {
	return nil, nil
}
func (m *mockRepo) GetLatestScoreEventByObserveOnly(_ context.Context, _ string, _ bool) (*database.UserScoreEvent, error) {
	return nil, nil
}
func (m *mockRepo) Close() error { return nil }

func (m *mockRepo) totalRecords() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, b := range m.batches {
		total += len(b)
	}
	return total
}

func (m *mockRepo) batchCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.batches)
}

func TestBatchWriter_FlushOnBatchSize(t *testing.T) {
	repo := &mockRepo{}
	bw := NewBatchWriter(repo)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go bw.Run(ctx, &wg)

	// Write exactly batchSize records to trigger a flush
	for i := 0; i < batchSize; i++ {
		bw.Write(database.UserConnection{UserID: "user1", SourceIP: "10.0.0.1", ASN: "AS1"})
	}

	// Wait for flush
	time.Sleep(100 * time.Millisecond)

	if repo.totalRecords() != batchSize {
		t.Errorf("expected %d records flushed, got %d", batchSize, repo.totalRecords())
	}

	cancel()
	wg.Wait()
}

func TestBatchWriter_FlushOnTimer(t *testing.T) {
	repo := &mockRepo{}
	bw := NewBatchWriter(repo)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go bw.Run(ctx, &wg)

	// Write fewer than batchSize
	bw.Write(database.UserConnection{UserID: "user1", SourceIP: "10.0.0.1", ASN: "AS1"})
	bw.Write(database.UserConnection{UserID: "user2", SourceIP: "10.0.0.2", ASN: "AS2"})

	// Wait longer than flushInterval (5s)
	time.Sleep(flushInterval + 500*time.Millisecond)

	if repo.totalRecords() != 2 {
		t.Errorf("expected 2 records flushed on timer, got %d", repo.totalRecords())
	}

	cancel()
	wg.Wait()
}

func TestBatchWriter_ContextCancel_DrainsPending(t *testing.T) {
	repo := &mockRepo{}
	bw := NewBatchWriter(repo)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go bw.Run(ctx, &wg)

	// Write a few records
	bw.Write(database.UserConnection{UserID: "user1", SourceIP: "10.0.0.1", ASN: "AS1"})
	bw.Write(database.UserConnection{UserID: "user2", SourceIP: "10.0.0.2", ASN: "AS2"})

	// Small delay to let records enter the channel
	time.Sleep(10 * time.Millisecond)

	// Cancel — should drain and flush
	cancel()
	wg.Wait()

	if repo.totalRecords() < 2 {
		t.Errorf("expected at least 2 records after drain, got %d", repo.totalRecords())
	}
}

func TestBatchWriter_NilRepo(t *testing.T) {
	// NewBatchWriter with nil repo — should not panic
	bw := NewBatchWriter(nil)
	if bw == nil {
		t.Fatal("expected non-nil BatchWriter")
	}
}
