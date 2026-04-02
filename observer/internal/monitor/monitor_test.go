package monitor

import (
	"context"
	"testing"
	"time"

	"observer_service/internal/database"
)

// mockRepo implements database.Repository for testing.
type mockRepo struct {
	users []database.MonitorUserStats
}

func (m *mockRepo) InsertConnection(_ context.Context, _ *database.UserConnection) error { return nil }
func (m *mockRepo) InsertConnections(_ context.Context, _ []database.UserConnection) error {
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
	return m.users, nil
}
func (m *mockRepo) GetLatestScoreEvent(_ context.Context, _ string) (*database.UserScoreEvent, error) {
	return nil, nil
}
func (m *mockRepo) GetLatestScoreEventByObserveOnly(_ context.Context, _ string, _ bool) (*database.UserScoreEvent, error) {
	return nil, nil
}
func (m *mockRepo) Close() error { return nil }

func TestGetActiveUserEmails_Postgres(t *testing.T) {
	repo := &mockRepo{
		users: []database.MonitorUserStats{
			{UserID: "user1@test.com", UniqueASNs: 5, UniqueIPs: 10, UniqueCountries: 2},
			{UserID: "user2@test.com", UniqueASNs: 3, UniqueIPs: 5, UniqueCountries: 1},
			{UserID: "user3@test.com", UniqueASNs: 1, UniqueIPs: 2, UniqueCountries: 1},
		},
	}

	mon := &PoolMonitor{repo: repo}
	emails, err := mon.getActiveUserEmails(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(emails) != 3 {
		t.Errorf("expected 3 emails, got %d", len(emails))
	}
	if emails[0] != "user1@test.com" {
		t.Errorf("expected user1@test.com first, got %s", emails[0])
	}
}

func TestGetActiveUserEmails_NoRepo(t *testing.T) {
	mon := &PoolMonitor{repo: nil}
	_, err := mon.getActiveUserEmails(context.Background())
	if err == nil {
		t.Error("expected error when repo is nil")
	}
}

func TestGetActiveUserEmails_EmptyResult(t *testing.T) {
	repo := &mockRepo{users: []database.MonitorUserStats{}}
	mon := &PoolMonitor{repo: repo}
	emails, err := mon.getActiveUserEmails(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(emails) != 0 {
		t.Errorf("expected 0 emails, got %d", len(emails))
	}
}
