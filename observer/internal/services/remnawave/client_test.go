package remnawave

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testUUID = "123e4567-e89b-12d3-a456-426614174000"

func setupTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return NewClient(server.URL, "test-token", 1, 24, nil)
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

func TestResolveUUIDByInternalID_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/users/by-id/12345", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "test-token", r.Header.Get("X-Api-Key"))
		writeJSON(w, http.StatusOK, mockUserResponse(testUUID))
	}

	client := setupTestClient(t, handler)
	uuid, err := client.ResolveUUIDByInternalID(context.Background(), 12345)
	require.NoError(t, err)
	assert.Equal(t, testUUID, uuid)
}

func TestResolveUUIDByInternalID_NotFound(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"message":    "user not found",
			"statusCode": 404,
		})
	}

	client := setupTestClient(t, handler)
	_, err := client.ResolveUUIDByInternalID(context.Background(), 99999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveUUIDByInternalID_ServerError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "boom"})
	}

	client := setupTestClient(t, handler)
	_, err := client.ResolveUUIDByInternalID(context.Background(), 12345)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server error")
}

func TestResolveUUIDByInternalID_Timeout(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}

	client := setupTestClient(t, handler)
	_, err := client.ResolveUUIDByInternalID(context.Background(), 12345)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http request")
}

func TestDisableUser_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/users/"+testUUID+"/actions/disable", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "test-token", r.Header.Get("X-Api-Key"))
		writeJSON(w, http.StatusOK, mockUserResponse(testUUID))
	}

	client := setupTestClient(t, handler)
	err := client.DisableUser(context.Background(), testUUID)
	require.NoError(t, err)
}

func TestDisableUser_ServerError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "boom"})
	}

	client := setupTestClient(t, handler)
	err := client.DisableUser(context.Background(), testUUID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server error")
}

func TestEnableUser_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/users/"+testUUID+"/actions/enable", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		writeJSON(w, http.StatusOK, mockUserResponse(testUUID))
	}

	client := setupTestClient(t, handler)
	err := client.EnableUser(context.Background(), testUUID)
	require.NoError(t, err)
}

func TestPing_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/system/health", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "test-token", r.Header.Get("X-Api-Key"))
		writeJSON(w, http.StatusOK, map[string]any{
			"response": map[string]any{
				"pm2Stats": []map[string]any{{
					"name":   "api",
					"memory": "10MB",
					"cpu":    "1%",
				}},
			},
		})
	}

	client := setupTestClient(t, handler)
	err := client.Ping(context.Background())
	require.NoError(t, err)
}

func TestPing_NotConfigured(t *testing.T) {
	client := NewClient("", "", 1, 24, nil)
	err := client.Ping(context.Background())
	require.NoError(t, err)
}
