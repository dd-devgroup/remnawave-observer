package enforcement

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"observer_service/internal/services/remnawave"
	"observer_service/internal/services/storage"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupEnforcerTest создаёт test enforcer + mock HTTP server + in-memory Redis.
func setupEnforcerTest(t *testing.T, handler http.HandlerFunc) (Enforcer, *redis.Client, *mockRedisStore) {
	// Mock HTTP server для Remnawave API
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// In-memory Redis
	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:63791"})

	// Remnawave client
	client := remnawave.NewClient(server.URL, "test-token", 5, 24, redisClient)

	// Mock storage (заглушка для тестов без реального Redis)
	store := &mockRedisStore{
		uuidCache:      make(map[int64]string),
		disableRecords: make(map[int64]*storage.DisableRecord),
	}

	enforcer := &RemnawaveEnforcer{
		client:  client,
		storage: store,
	}

	return enforcer, redisClient, store
}

// mockRedisStore — in-memory заглушка storage для тестов
type mockRedisStore struct {
	uuidCache      map[int64]string
	disableRecords map[int64]*storage.DisableRecord
}

func (m *mockRedisStore) GetUserUUIDCache(ctx context.Context, internalID int64) (string, bool) {
	uuid, ok := m.uuidCache[internalID]
	return uuid, ok
}

func (m *mockRedisStore) SetUserUUIDCache(ctx context.Context, internalID int64, uuid string, ttl time.Duration) error {
	m.uuidCache[internalID] = uuid
	return nil
}

func (m *mockRedisStore) ScheduleDisable(ctx context.Context, internalID int64, uuid string, untilUnix int64, reason string, score int) error {
	m.disableRecords[internalID] = &storage.DisableRecord{
		InternalID: internalID,
		UUID:       uuid,
		UntilUnix:  untilUnix,
		Reason:     reason,
		Score:      score,
	}
	return nil
}

func (m *mockRedisStore) GetDisableRecord(ctx context.Context, internalID int64) (*storage.DisableRecord, bool) {
	rec, ok := m.disableRecords[internalID]
	return rec, ok
}

func (m *mockRedisStore) ClearDisableRecord(ctx context.Context, internalID int64) error {
	delete(m.disableRecords, internalID)
	return nil
}

// Тесты

func TestRemnawaveEnforcer_DisableTempByInternalID_Success(t *testing.T) {
	resolveCallCount := 0
	disableCallCount := 0

	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/users/12345" {
			resolveCallCount++
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"uuid": "user-uuid-12345"})
			return
		}
		if r.URL.Path == "/api/users/user-uuid-12345/disable" {
			disableCallCount++
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}

	enforcer, _, store := setupEnforcerTest(t, handler)
	ctx := context.Background()

	err := enforcer.DisableTempByInternalID(ctx, 12345, 10*time.Minute, "test reason", 85)
	require.NoError(t, err)

	// Проверяем что UUID был закэширован
	uuid, ok := store.GetUserUUIDCache(ctx, 12345)
	assert.True(t, ok)
	assert.Equal(t, "user-uuid-12345", uuid)

	// Проверяем что disable был вызван
	assert.Equal(t, 1, disableCallCount)

	// Проверяем что запись о disable создана
	rec, ok := store.GetDisableRecord(ctx, 12345)
	assert.True(t, ok)
	assert.Equal(t, "user-uuid-12345", rec.UUID)
	assert.Equal(t, "test reason", rec.Reason)
	assert.Equal(t, 85, rec.Score)
}

func TestRemnawaveEnforcer_DisableTempByInternalID_CacheHit(t *testing.T) {
	resolveCallCount := 0
	disableCallCount := 0

	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/users/12345" {
			resolveCallCount++
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"uuid": "user-uuid-12345"})
			return
		}
		if r.URL.Path == "/api/users/user-uuid-cached/disable" {
			disableCallCount++
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}

	enforcer, _, store := setupEnforcerTest(t, handler)
	ctx := context.Background()

	// Pre-populate cache
	store.SetUserUUIDCache(ctx, 12345, "user-uuid-cached", 24*time.Hour)

	err := enforcer.DisableTempByInternalID(ctx, 12345, 10*time.Minute, "test", 80)
	require.NoError(t, err)

	// Resolve API не должен был вызываться (cache hit)
	assert.Equal(t, 0, resolveCallCount)
	assert.Equal(t, 1, disableCallCount)
}

func TestRemnawaveEnforcer_DisableTempByInternalID_Idempotent(t *testing.T) {
	disableCallCount := 0

	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/users/12345" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"uuid": "user-uuid-12345"})
			return
		}
		if r.URL.Path == "/api/users/user-uuid-12345/disable" {
			disableCallCount++
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}

	enforcer, _, store := setupEnforcerTest(t, handler)
	ctx := context.Background()

	// Первый disable на 10 минут
	err := enforcer.DisableTempByInternalID(ctx, 12345, 10*time.Minute, "first", 80)
	require.NoError(t, err)
	assert.Equal(t, 1, disableCallCount)

	rec1, _ := store.GetDisableRecord(ctx, 12345)

	// Второй disable на 5 минут (меньше): должен пропуститься (идемпотентность)
	err = enforcer.DisableTempByInternalID(ctx, 12345, 5*time.Minute, "second", 90)
	require.NoError(t, err)

	// Disable API не должен был вызваться второй раз
	assert.Equal(t, 1, disableCallCount, "idempotent: should not call disable again")

	// Запись не должна была измениться
	rec2, _ := store.GetDisableRecord(ctx, 12345)
	assert.Equal(t, rec1.UntilUnix, rec2.UntilUnix)
	assert.Equal(t, "first", rec2.Reason) // старая запись сохранена
}

func TestRemnawaveEnforcer_Ping_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}

	enforcer, _, _ := setupEnforcerTest(t, handler)
	ctx := context.Background()

	err := enforcer.Ping(ctx)
	require.NoError(t, err)
}
