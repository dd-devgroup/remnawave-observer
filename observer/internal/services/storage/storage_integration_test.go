//go:build integration
// +build integration

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestRedisStore_SCANIntegration проверяет реальный Redis SCAN с time budget.
func TestRedisStore_SCANIntegration(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set, skipping integration test")
	}

	ctx := context.Background()
	store, err := NewRedisStore(ctx, redisURL)
	if err != nil {
		t.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer store.Close()

	// Создаём 100 ключей для теста
	testPrefix := fmt.Sprintf("test_scan_%d_", time.Now().Unix())
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("%suser_%d_ip_192.168.1.%d", testPrefix, i, i)
		err := store.client.Set(ctx, key, "1", time.Hour).Err()
		if err != nil {
			t.Fatalf("Failed to set key: %v", err)
		}
	}

	// Очистим эти ключи после теста
	defer func() {
		keys, _ := store.client.Keys(ctx, testPrefix+"*").Result()
		if len(keys) > 0 {
			store.client.Del(ctx, keys...)
		}
	}()

	// Устанавливаем маленький time budget чтобы получить partial result
	store.SetScanTimeBudget(100 * time.Millisecond)
	store.SetScanCount(10)
	store.SetScanMaxKeys(50)

	// Вызываем GetAllUserEmails (использует SCAN под капотом)
	emails, err := store.GetAllUserEmails(ctx)
	if err != nil {
		t.Fatalf("GetAllUserEmails failed: %v", err)
	}

	// Должны получить частичный результат (из-за budget/maxKeys)
	t.Logf("Scanned emails: %d", len(emails))
	if len(emails) > 50 {
		t.Errorf("Expected partial result (<= 50), got %d", len(emails))
	}
}

// TestRedisStore_PingIntegration проверяет базовое подключение к Redis.
func TestRedisStore_PingIntegration(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set, skipping integration test")
	}

	ctx := context.Background()
	store, err := NewRedisStore(ctx, redisURL)
	if err != nil {
		t.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer store.Close()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}
