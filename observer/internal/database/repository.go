package database

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Repository defines the interface for database operations.
type Repository interface {
	InsertConnection(ctx context.Context, conn *UserConnection) error
	InsertConnections(ctx context.Context, conns []UserConnection) error
	InsertScoreEvent(ctx context.Context, event *UserScoreEvent) error
	InsertAction(ctx context.Context, action *AntiAbuseAction) error
	GetUserConnectionsLast30d(ctx context.Context, userID string) ([]UserConnection, error)
	GetDailyProfile(ctx context.Context, userID string, dayDate string) (*UserDailyProfile, error)
	UpsertDailyProfile(ctx context.Context, profile *UserDailyProfile) error
	GetEnrichment(ctx context.Context, ip string) (*IPEnrichmentCache, error)
	UpsertEnrichment(ctx context.Context, cache *IPEnrichmentCache) error
	CleanExpiredEnrichments(ctx context.Context) (int64, error)
	GetASNOrgStats(ctx context.Context, minDistinctUsers int) ([]ASNOrgStats, error)
	InsertCandidate(ctx context.Context, candidate *LearningCandidate) error
	GetPendingCandidates(ctx context.Context) ([]LearningCandidate, error)
	UpdateCandidateStatus(ctx context.Context, id uint, status string) error
	GetActiveUsersForMonitor(ctx context.Context, since time.Time) ([]MonitorUserStats, error)
	GetLatestScoreEvent(ctx context.Context, userID string) (*UserScoreEvent, error)
	Close() error
}

// GormRepository implements Repository using GORM.
type GormRepository struct {
	db *gorm.DB
}

// NewGormRepository creates a new GormRepository.
func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

func (r *GormRepository) InsertConnection(ctx context.Context, conn *UserConnection) error {
	return r.db.WithContext(ctx).Create(conn).Error
}

func (r *GormRepository) InsertConnections(ctx context.Context, conns []UserConnection) error {
	if len(conns) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).CreateInBatches(conns, 100).Error
}

func (r *GormRepository) InsertScoreEvent(ctx context.Context, event *UserScoreEvent) error {
	return r.db.WithContext(ctx).Create(event).Error
}

func (r *GormRepository) InsertAction(ctx context.Context, action *AntiAbuseAction) error {
	return r.db.WithContext(ctx).Create(action).Error
}

func (r *GormRepository) GetUserConnectionsLast30d(ctx context.Context, userID string) ([]UserConnection, error) {
	var conns []UserConnection
	cutoff := time.Now().AddDate(0, 0, -30)
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND created_at >= ?", userID, cutoff).
		Order("created_at DESC").
		Find(&conns).Error
	if err != nil {
		return nil, fmt.Errorf("get user connections last 30d: %w", err)
	}
	return conns, nil
}

func (r *GormRepository) GetDailyProfile(ctx context.Context, userID string, dayDate string) (*UserDailyProfile, error) {
	var profile UserDailyProfile
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND day_date = ?", userID, dayDate).
		First(&profile).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get daily profile: %w", err)
	}
	return &profile, nil
}

func (r *GormRepository) UpsertDailyProfile(ctx context.Context, profile *UserDailyProfile) error {
	return r.db.WithContext(ctx).Save(profile).Error
}

func (r *GormRepository) GetEnrichment(ctx context.Context, ip string) (*IPEnrichmentCache, error) {
	var cache IPEnrichmentCache
	err := r.db.WithContext(ctx).Where("ip = ?", ip).First(&cache).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get enrichment: %w", err)
	}
	// Check expiry
	if !cache.ExpiresAt.IsZero() && cache.ExpiresAt.Before(time.Now()) {
		return nil, nil // expired
	}
	return &cache, nil
}

func (r *GormRepository) UpsertEnrichment(ctx context.Context, cache *IPEnrichmentCache) error {
	return r.db.WithContext(ctx).Save(cache).Error
}

func (r *GormRepository) CleanExpiredEnrichments(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("expires_at < ?", time.Now()).
		Delete(&IPEnrichmentCache{})
	return result.RowsAffected, result.Error
}

func (r *GormRepository) GetASNOrgStats(ctx context.Context, minDistinctUsers int) ([]ASNOrgStats, error) {
	var stats []ASNOrgStats
	err := r.db.WithContext(ctx).
		Model(&UserConnection{}).
		Select("asn, org_name, COUNT(DISTINCT user_id) as distinct_users, COUNT(*) as total_conns").
		Where("asn != '' AND org_name != ''").
		Group("asn, org_name").
		Having("COUNT(DISTINCT user_id) >= ?", minDistinctUsers).
		Find(&stats).Error
	if err != nil {
		return nil, fmt.Errorf("get ASN/org stats: %w", err)
	}
	return stats, nil
}

func (r *GormRepository) InsertCandidate(ctx context.Context, candidate *LearningCandidate) error {
	return r.db.WithContext(ctx).Create(candidate).Error
}

func (r *GormRepository) GetPendingCandidates(ctx context.Context) ([]LearningCandidate, error) {
	var candidates []LearningCandidate
	err := r.db.WithContext(ctx).
		Where("status = ?", "pending").
		Order("confidence DESC").
		Find(&candidates).Error
	if err != nil {
		return nil, fmt.Errorf("get pending candidates: %w", err)
	}
	return candidates, nil
}

func (r *GormRepository) UpdateCandidateStatus(ctx context.Context, id uint, status string) error {
	return r.db.WithContext(ctx).
		Model(&LearningCandidate{}).
		Where("id = ?", id).
		Update("status", status).Error
}

func (r *GormRepository) GetActiveUsersForMonitor(ctx context.Context, since time.Time) ([]MonitorUserStats, error) {
	var stats []MonitorUserStats
	err := r.db.WithContext(ctx).
		Model(&UserConnection{}).
		Select("user_id, COUNT(DISTINCT asn) as unique_asns, COUNT(DISTINCT source_ip) as unique_ips, COUNT(DISTINCT country) as unique_countries").
		Where("created_at >= ?", since).
		Group("user_id").
		Order("unique_asns DESC").
		Find(&stats).Error
	if err != nil {
		return nil, fmt.Errorf("get active users for monitor: %w", err)
	}
	return stats, nil
}

func (r *GormRepository) GetLatestScoreEvent(ctx context.Context, userID string) (*UserScoreEvent, error) {
	var event UserScoreEvent
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		First(&event).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get latest score event: %w", err)
	}
	return &event, nil
}

func (r *GormRepository) Close() error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
