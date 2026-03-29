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
			"uuid":                 uuid,
			"id":                   1,
			"shortUuid":            "short-uuid",
			"username":             "test-user",
			"status":               "ACTIVE",
			"trafficLimitBytes":    0,
			"trafficLimitStrategy": "MONTH_ROLLING",
			"expireAt":             timestamp,
			"createdAt":            timestamp,
			"updatedAt":            timestamp,
			"subscriptionUrl":      "https://example/sub",
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
		writeJSON(w, http.StatusOK, mockUserResponse(testUUID))
	}

	client := setupTestClient(t, handler)
	uuid, err := client.ResolveUUIDByInternalID(context.Background(), 12345)
	require.NoError(t, err)
	assert.Equal(t, testUUID, uuid)
}

func TestRequestEditor_GateQueryAndCookie(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/system/health", r.URL.Path)
		assert.Equal(t, "gate_value", r.URL.Query().Get("gate_key"))
		assert.Equal(t, "gate_key=gate_value", r.Header.Get("Cookie"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		writeJSON(w, http.StatusOK, map[string]any{
			"response": map[string]any{
				"runtimeMetrics": []map[string]any{{
					"name":   "api",
					"memory": "10MB",
					"cpu":    "1%",
				}},
			},
		})
	}

	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)

	client := NewClientWithHeader(server.URL, "test-token", 1, 24, nil, "gate_key=gate_value")

	err := client.Ping(context.Background())
	require.NoError(t, err)
}

func TestAuthToken_BearerPrefixInEnvIsNormalized(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/system/health", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		writeJSON(w, http.StatusOK, map[string]any{
			"response": map[string]any{
				"runtimeMetrics": []map[string]any{{"name": "api", "memory": "10MB", "cpu": "1%"}},
			},
		})
	}

	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "Bearer test-token", 1, 24, nil)
	err := client.Ping(context.Background())
	require.NoError(t, err)
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
	assert.Contains(t, err.Error(), "status 500")
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
	assert.Contains(t, err.Error(), "status 500")
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
		writeJSON(w, http.StatusOK, map[string]any{
			"response": map[string]any{
				"runtimeMetrics": []map[string]any{{
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

func TestListNodes_Success_2_7_0Shape(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/nodes", r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{
			"response": []map[string]any{
				{
					"uuid":             "node-1",
					"name":             "de-1",
					"isConnected":      true,
					"isDisabled":       false,
					"xrayUptime":       12345,
					"usersOnline":      7,
					"versions":         map[string]any{"xray": "25.2.1", "node": "2.7.0"},
					"system":           map[string]any{"cpu": map[string]any{"load": 1}},
					"activePluginUuid": nil,
				},
			},
		})
	}

	client := setupTestClient(t, handler)
	nodes, err := client.ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "node-1", nodes[0].UUID)
	assert.Equal(t, "de-1", nodes[0].Name)
	assert.True(t, nodes[0].IsConnected)
	assert.False(t, nodes[0].IsDisabled)
}

func TestGetFetchUsersIPsResult_NormalizesObservations(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/ip-control/fetch-users-ips/result/job-1", r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{
			"response": map[string]any{
				"isCompleted": true,
				"isFailed":    false,
				"result": map[string]any{
					"success":  true,
					"nodeUuid": "node-1",
					"users": []map[string]any{
						{
							"userId": "12345",
							"ips": []map[string]any{
								{"ip": "1.2.3.4", "lastSeen": "2026-03-29T12:00:00Z"},
							},
						},
					},
				},
			},
		})
	}

	client := setupTestClient(t, handler)
	result, err := client.GetFetchUsersIPsResult(context.Background(), "job-1", "node-1")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Status)
	require.Len(t, result.Observations, 1)
	assert.Equal(t, "12345", result.Observations[0].UserIdentifier)
	assert.Equal(t, "1.2.3.4", result.Observations[0].SourceIP)
	assert.Equal(t, "node-1", result.Observations[0].NodeUUID)
}

func TestResolveUser_UsesOfficialRequestBody(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/users/resolve", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "alice", body["username"])
		_, hasShortUUID := body["shortUuid"]
		assert.False(t, hasShortUUID)

		writeJSON(w, http.StatusOK, map[string]any{
			"response": map[string]any{
				"uuid":      testUUID,
				"id":        12345,
				"shortUuid": "short-uuid",
				"username":  "alice",
			},
		})
	}

	client := setupTestClient(t, handler)
	user, err := client.ResolveUser(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, testUUID, user.UUID)
	assert.EqualValues(t, 12345, user.InternalID)
}

func TestExecuteTemporaryIPBlock_UsesOfficialExecutorPayload(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/node-plugins/executor", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		command, ok := body["command"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "blockIps", command["command"])

		ips, ok := command["ips"].([]any)
		require.True(t, ok)
		require.Len(t, ips, 2)
		firstIP, ok := ips[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "1.2.3.4", firstIP["ip"])
		assert.EqualValues(t, 300, firstIP["timeout"])

		targetNodes, ok := body["targetNodes"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "specificNodes", targetNodes["target"])
		nodeUUIDs, ok := targetNodes["nodeUuids"].([]any)
		require.True(t, ok)
		require.Len(t, nodeUUIDs, 2)

		writeJSON(w, http.StatusOK, map[string]any{
			"response": map[string]any{"eventSent": true},
		})
	}

	client := setupTestClient(t, handler)
	err := client.ExecuteTemporaryIPBlock(
		context.Background(),
		[]string{"node-1", "node-2"},
		[]string{"1.2.3.4", "5.6.7.8"},
		5*time.Minute,
	)
	require.NoError(t, err)
}

func TestGetUserEvidenceByInternalID_UsesOfficialEndpoints(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/users/by-id/12345":
			writeJSON(w, http.StatusOK, mockUserResponse(testUUID))
		case "/api/hwid/devices/" + testUUID:
			writeJSON(w, http.StatusOK, map[string]any{
				"response": map[string]any{
					"total": 1,
					"devices": []map[string]any{
						{
							"hwid":        "device-1",
							"userUuid":    testUUID,
							"platform":    "ios",
							"osVersion":   "18.1",
							"deviceModel": "iphone",
							"userAgent":   "Agent/1.0",
							"createdAt":   "2026-03-29T12:00:00Z",
							"updatedAt":   "2026-03-29T12:05:00Z",
						},
					},
				},
			})
		case "/api/users/" + testUUID + "/subscription-request-history":
			writeJSON(w, http.StatusOK, map[string]any{
				"response": map[string]any{
					"total": 1,
					"records": []map[string]any{
						{
							"id":        10,
							"userUuid":  testUUID,
							"requestAt": "2026-03-29T11:59:00Z",
							"requestIp": "1.2.3.4",
							"userAgent": "Agent/1.0",
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}

	client := setupTestClient(t, handler)
	evidence, err := client.GetUserEvidenceByInternalID(context.Background(), 12345)
	require.NoError(t, err)
	require.NotNil(t, evidence)
	assert.Equal(t, testUUID, evidence.UserUUID)
	require.Len(t, evidence.HwidDevices, 1)
	require.Len(t, evidence.SubscriptionRequests, 1)
	assert.Equal(t, "device-1", evidence.HwidDevices[0].HWID)
	assert.Equal(t, "1.2.3.4", evidence.SubscriptionRequests[0].RequestIP)
}
