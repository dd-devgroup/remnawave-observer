package remnawave

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestClient создаёт test client + mock HTTP server + in-memory Redis.
func setupTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *redis.Client) {
	// In-memory Redis для тестов
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:63790", // несуществующий адрес для in-memory mock
	})

	// Mock HTTP server
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "test-token", 5, 24, redisClient)
	return client, redisClient
}

func TestResolveUUIDByInternalID_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/users/12345", r.URL.Path)
		assert.Equal(t, "test-token", r.Header.Get("X-Api-Key"))

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"uuid": "user-uuid-12345"})
	}

	client, _ := setupTestClient(t, handler)
	ctx := context.Background()

	uuid, err := client.ResolveUUIDByInternalID(ctx, 12345)
	require.NoError(t, err)
	assert.Equal(t, "user-uuid-12345", uuid)
}

func TestResolveUUIDByInternalID_NotFound(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}

	client, _ := setupTestClient(t, handler)
	ctx := context.Background()

	_, err := client.ResolveUUIDByInternalID(ctx, 99999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveUUIDByInternalID_ServerError_Retry(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}

	client, _ := setupTestClient(t, handler)
	ctx := context.Background()

	_, err := client.ResolveUUIDByInternalID(ctx, 12345)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server error")
}

func TestResolveUUIDByInternalID_Timeout(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Спим дольше чем timeout клиента
		time.Sleep(10 * time.Second)
	}

	client, _ := setupTestClient(t, handler)
	ctx := context.Background()

	_, err := client.ResolveUUIDByInternalID(ctx, 12345)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http request")
}

func TestDisableUser_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/users/uuid-123/disable", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "test-token", r.Header.Get("X-Api-Key"))

		w.WriteHeader(http.StatusOK)
	}

	client, _ := setupTestClient(t, handler)
	ctx := context.Background()

	err := client.DisableUser(ctx, "uuid-123")
	require.NoError(t, err)
}

func TestDisableUser_ServerError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}

	client, _ := setupTestClient(t, handler)
	ctx := context.Background()

	err := client.DisableUser(ctx, "uuid-123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server error")
}

func TestEnableUser_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/users/uuid-456/enable", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		w.WriteHeader(http.StatusOK)
	}

	client, _ := setupTestClient(t, handler)
	ctx := context.Background()

	err := client.EnableUser(ctx, "uuid-456")
	require.NoError(t, err)
}

func TestPing_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/health", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}

	client, _ := setupTestClient(t, handler)
	ctx := context.Background()

	err := client.Ping(ctx)
	require.NoError(t, err)
}

func TestPing_NotConfigured(t *testing.T) {
	// Если baseURL пустой, Ping должен возвращать nil (noop режим)
	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:63790"})
	client := NewClient("", "", 5, 24, redisClient)
	ctx := context.Background()

	err := client.Ping(ctx)
	require.NoError(t, err, "ping should succeed when not configured (noop mode)")
}
