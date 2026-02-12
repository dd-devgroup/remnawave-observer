package database

import (
	"fmt"

	"gorm.io/gorm"
)

// AutoMigrate runs schema migration for all database models.
func AutoMigrate(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&UserConnection{},
		&UserDailyProfile{},
		&UserProviderFingerprint{},
		&AntiAbuseAction{},
		&UserScoreEvent{},
		&IPEnrichmentCache{},
		&LearningCandidate{},
	); err != nil {
		return fmt.Errorf("auto-migration failed: %w", err)
	}
	return nil
}
