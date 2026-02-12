package enrichment

import (
	"context"
	"testing"
	"time"

	"observer_service/internal/database"
)

// mockRepo implements database.Repository for testing.
type mockRepo struct {
	enrichment *database.IPEnrichmentCache
	upserted   *database.IPEnrichmentCache
	cleaned    int64
}

func (m *mockRepo) InsertConnection(_ context.Context, _ *database.UserConnection) error { return nil }
func (m *mockRepo) InsertConnections(_ context.Context, _ []database.UserConnection) error {
	return nil
}
func (m *mockRepo) InsertScoreEvent(_ context.Context, _ *database.UserScoreEvent) error { return nil }
func (m *mockRepo) InsertAction(_ context.Context, _ *database.AntiAbuseAction) error    { return nil }
func (m *mockRepo) GetUserConnectionsLast30d(_ context.Context, _ string) ([]database.UserConnection, error) {
	return nil, nil
}
func (m *mockRepo) GetDailyProfile(_ context.Context, _, _ string) (*database.UserDailyProfile, error) {
	return nil, nil
}
func (m *mockRepo) UpsertDailyProfile(_ context.Context, _ *database.UserDailyProfile) error {
	return nil
}
func (m *mockRepo) GetEnrichment(_ context.Context, _ string) (*database.IPEnrichmentCache, error) {
	return m.enrichment, nil
}
func (m *mockRepo) UpsertEnrichment(_ context.Context, cache *database.IPEnrichmentCache) error {
	m.upserted = cache
	return nil
}
func (m *mockRepo) CleanExpiredEnrichments(_ context.Context) (int64, error) {
	return m.cleaned, nil
}
func (m *mockRepo) Close() error { return nil }

// --- Tests ---

func TestEnricher_PostgresCacheHit(t *testing.T) {
	repo := &mockRepo{
		enrichment: &database.IPEnrichmentCache{
			IP:         "1.2.3.4",
			ASN:        "AS13335",
			Org:        "Cloudflare",
			Country:    "US",
			City:       "San Francisco",
			Region:     "California",
			Lat:        37.77,
			Lon:        -122.42,
			Source:     "mmdb",
			Confidence: 0.95,
			ExpiresAt:  time.Now().Add(time.Hour),
		},
	}

	enricher := NewEnricher(repo, nil, 24*time.Hour)
	result, err := enricher.Enrich(context.Background(), "1.2.3.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Source != "postgres" {
		t.Errorf("expected source 'postgres', got %q", result.Source)
	}
	if result.ASN != "AS13335" {
		t.Errorf("expected ASN 'AS13335', got %q", result.ASN)
	}
	if result.Org != "Cloudflare" {
		t.Errorf("expected Org 'Cloudflare', got %q", result.Org)
	}
	if result.Country != "US" {
		t.Errorf("expected Country 'US', got %q", result.Country)
	}
	if result.Confidence != 0.95 {
		t.Errorf("expected Confidence 0.95, got %f", result.Confidence)
	}
}

func TestEnricher_PostgresMiss_NilGeoService(t *testing.T) {
	repo := &mockRepo{enrichment: nil} // Postgres miss

	enricher := NewEnricher(repo, nil, 24*time.Hour)
	result, err := enricher.Enrich(context.Background(), "1.2.3.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Source != "none" {
		t.Errorf("expected source 'none' when geoService is nil, got %q", result.Source)
	}
	if result.Confidence != 0 {
		t.Errorf("expected Confidence 0, got %f", result.Confidence)
	}
}

func TestEnricher_NilRepo_NilGeoService(t *testing.T) {
	enricher := NewEnricher(nil, nil, 24*time.Hour)
	result, err := enricher.Enrich(context.Background(), "1.2.3.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Source != "none" {
		t.Errorf("expected source 'none', got %q", result.Source)
	}
}

func TestEnricher_CleanExpired(t *testing.T) {
	repo := &mockRepo{cleaned: 42}
	enricher := NewEnricher(repo, nil, 24*time.Hour)

	count, err := enricher.CleanExpired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 42 {
		t.Errorf("expected 42 cleaned, got %d", count)
	}
}

func TestEnricher_CleanExpired_NilRepo(t *testing.T) {
	enricher := NewEnricher(nil, nil, 24*time.Hour)

	count, err := enricher.CleanExpired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 cleaned with nil repo, got %d", count)
	}
}

func TestNewEnricher_Construction(t *testing.T) {
	repo := &mockRepo{}
	enricher := NewEnricher(repo, nil, 24*time.Hour)
	if enricher == nil {
		t.Fatal("expected non-nil enricher")
	}
	if enricher.cacheTTL != 24*time.Hour {
		t.Errorf("expected cacheTTL 24h, got %v", enricher.cacheTTL)
	}
}
