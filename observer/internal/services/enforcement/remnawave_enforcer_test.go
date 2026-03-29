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

const enforcerTestUUID = "123e4567-e89b-12d3-a456-426614174000"

func setupEnforcerTest(t *testing.T, handler http.HandlerFunc) (Enforcer, *redis.Client, *mockRedisStore) {
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:63791"})
	client := remnawave.NewClient(server.URL, "test-token", 5, 24, redisClient)

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

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func mockUserResponse(uuid string) map[string]any {
	timestamp := "2026-02-16T17:57:06.730Z"
	return map[string]any{
		"response": map[string]any{
			"uuid":                   uuid,
			"id":                     1,
			"shortUuid":              "short-uuid",
			"username":               "test-user",
			"status":                 "ACTIVE",
			"trafficLimitBytes":      0,
			"trafficLimitStrategy":   "NO_RESET",
			"expireAt":               timestamp,
			"telegramId":             nil,
			"email":                  nil,
			"description":            nil,
			"tag":                    nil,
			"hwidDeviceLimit":        nil,
			"externalSquadUuid":      nil,
			"trojanPassword":         "tp",
			"vlessUuid":              uuid,
			"ssPassword":             "sp",
			"lastTriggeredThreshold": 0,
			"subRevokedAt":           nil,
			"subLastUserAgent":       nil,
			"subLastOpenedAt":        nil,
			"lastTrafficResetAt":     nil,
			"createdAt":              timestamp,
			"updatedAt":              timestamp,
			"subscriptionUrl":        "https://example/sub",
			"activeInternalSquads": []map[string]any{
				{"uuid": uuid, "name": "default"},
			},
			"userTraffic": map[string]any{
				"usedTrafficBytes":         1,
				"lifetimeUsedTrafficBytes": 1,
				"onlineAt":                 nil,
				"firstConnectedAt":         nil,
				"lastConnectedNodeUuid":    nil,
			},
		},
	}
}

// mockRedisStore is an in-memory test storage.
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

func TestRemnawaveEnforcer_DisableTempByInternalID_Success(t *testing.T) {
	resolveCallCount := 0
	disableCallCount := 0

	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/users/by-id/12345" {
			resolveCallCount++
			writeJSON(w, http.StatusOK, mockUserResponse(enforcerTestUUID))
			return
		}
		if r.URL.Path == "/api/users/"+enforcerTestUUID+"/actions/disable" {
			disableCallCount++
			writeJSON(w, http.StatusOK, mockUserResponse(enforcerTestUUID))
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "not found", "statusCode": 404})
	}

	enforcer, _, store := setupEnforcerTest(t, handler)
	err := enforcer.DisableTempByInternalID(context.Background(), 12345, 10*time.Minute, "test reason", 85)
	require.NoError(t, err)

	uuid, ok := store.GetUserUUIDCache(context.Background(), 12345)
	assert.True(t, ok)
	assert.Equal(t, enforcerTestUUID, uuid)
	assert.Equal(t, 1, resolveCallCount)
	assert.Equal(t, 1, disableCallCount)

	rec, ok := store.GetDisableRecord(context.Background(), 12345)
	assert.True(t, ok)
	assert.Equal(t, enforcerTestUUID, rec.UUID)
	assert.Equal(t, "test reason", rec.Reason)
	assert.Equal(t, 85, rec.Score)
}

func TestRemnawaveEnforcer_DisableTempByInternalID_CacheHit(t *testing.T) {
	resolveCallCount := 0
	disableCallCount := 0
	cachedUUID := "223e4567-e89b-12d3-a456-426614174000"

	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/users/by-id/12345" {
			resolveCallCount++
			writeJSON(w, http.StatusOK, mockUserResponse(enforcerTestUUID))
			return
		}
		if r.URL.Path == "/api/users/"+cachedUUID+"/actions/disable" {
			disableCallCount++
			writeJSON(w, http.StatusOK, mockUserResponse(cachedUUID))
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "not found", "statusCode": 404})
	}

	enforcer, _, store := setupEnforcerTest(t, handler)
	_ = store.SetUserUUIDCache(context.Background(), 12345, cachedUUID, 24*time.Hour)

	err := enforcer.DisableTempByInternalID(context.Background(), 12345, 10*time.Minute, "test", 80)
	require.NoError(t, err)

	assert.Equal(t, 0, resolveCallCount)
	assert.Equal(t, 1, disableCallCount)
}

func TestRemnawaveEnforcer_DisableTempByInternalID_Idempotent(t *testing.T) {
	disableCallCount := 0

	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/users/by-id/12345" {
			writeJSON(w, http.StatusOK, mockUserResponse(enforcerTestUUID))
			return
		}
		if r.URL.Path == "/api/users/"+enforcerTestUUID+"/actions/disable" {
			disableCallCount++
			writeJSON(w, http.StatusOK, mockUserResponse(enforcerTestUUID))
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "not found", "statusCode": 404})
	}

	enforcer, _, store := setupEnforcerTest(t, handler)

	err := enforcer.DisableTempByInternalID(context.Background(), 12345, 10*time.Minute, "first", 80)
	require.NoError(t, err)
	assert.Equal(t, 1, disableCallCount)

	rec1, _ := store.GetDisableRecord(context.Background(), 12345)

	err = enforcer.DisableTempByInternalID(context.Background(), 12345, 5*time.Minute, "second", 90)
	require.NoError(t, err)
	assert.Equal(t, 1, disableCallCount, "idempotent: should not call disable again")

	rec2, _ := store.GetDisableRecord(context.Background(), 12345)
	assert.Equal(t, rec1.UntilUnix, rec2.UntilUnix)
	assert.Equal(t, "first", rec2.Reason)
}

func TestRemnawaveEnforcer_Ping_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/system/health" {
			writeJSON(w, http.StatusOK, map[string]any{
				"response": map[string]any{
					"pm2Stats": []map[string]any{{
						"name":   "api",
						"memory": "10MB",
						"cpu":    "1%",
					}},
				},
			})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "not found", "statusCode": 404})
	}

	enforcer, _, _ := setupEnforcerTest(t, handler)
	err := enforcer.Ping(context.Background())
	require.NoError(t, err)
}
