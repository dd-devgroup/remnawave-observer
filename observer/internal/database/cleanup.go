package database

import (
	"context"

	"gorm.io/gorm"
)

// CleanupGarbageIPs removes persisted unspecified source IP rows.
func CleanupGarbageIPs(ctx context.Context, db *gorm.DB) (int64, error) {
	result := db.WithContext(ctx).
		Where("source_ip IN ?", []string{"0.0.0.0", "::"}).
		Delete(&UserConnection{})
	return result.RowsAffected, result.Error
}
