package database

import (
	"testing"
)

func TestNewPostgresDB_EmptyDSN(t *testing.T) {
	_, err := NewPostgresDB("")
	if err == nil {
		t.Fatal("expected error for empty DSN, got nil")
	}
	if err.Error() != "POSTGRES_DSN is required but empty" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewPostgresDB_InvalidDSN(t *testing.T) {
	_, err := NewPostgresDB("not-a-valid-dsn")
	if err == nil {
		t.Fatal("expected error for invalid DSN, got nil")
	}
}

func TestAutoMigrate_NilDB(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil DB, got none")
		}
	}()
	_ = AutoMigrate(nil)
}

func TestGormRepository_InsertConnections_Empty(t *testing.T) {
	// InsertConnections with empty slice should be a no-op (no DB needed)
	repo := &GormRepository{db: nil}
	err := repo.InsertConnections(nil, []UserConnection{})
	if err != nil {
		t.Errorf("expected no error for empty connections, got %v", err)
	}
}
