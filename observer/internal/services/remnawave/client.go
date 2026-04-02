package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"observer_service/internal/metrics"

	"github.com/redis/go-redis/v9"
)

// Client is a tolerant raw HTTP client for Remnawave API versions where the
// generated SDK lags behind panel response changes.
type Client struct {
	baseURL  string
	apiToken string

	httpClient *http.Client
	redis      *redis.Client
	cacheTTL   time.Duration

	gateName  string
	gateValue string
}

// NodeInfo is the minimal node shape used by panel ingest.
type NodeInfo struct {
	UUID        string
	Name        string
	IsDisabled  bool
	IsConnected bool
}

// InternalSquadInfo is the minimal internal squad shape used for exclusions.
type InternalSquadInfo struct {
	UUID string
	Name string
}

// UserInfo is the minimal user shape used by enforcement and squad exclusion.
type UserInfo struct {
	UUID                 string
	InternalID           int64
	ActiveInternalSquads []InternalSquadInfo
}

// HwidDevice is the normalized shape of a user HWID device from Remnawave API.
type HwidDevice struct {
	HWID        string
	UserUUID    string
	Platform    string
	OSVersion   string
	DeviceModel string
	UserAgent   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SubscriptionRequestRecord is a normalized subscription request history item.
type SubscriptionRequestRecord struct {
	ID        int64
	UserUUID  string
	RequestAt time.Time
	RequestIP string
	UserAgent string
}

// UserEvidence combines user-device and subscription request history evidence.
type UserEvidence struct {
	UserUUID             string
	InternalID           int64
	HwidDevices          []HwidDevice
	SubscriptionRequests []SubscriptionRequestRecord
}

type UserIPSnapshotIP struct {
	IP       string
	LastSeen time.Time
}

type UserIPSnapshotNode struct {
	NodeUUID    string
	NodeName    string
	CountryCode string
	IPs         []UserIPSnapshotIP
}

type UserIPSnapshotResult struct {
	Status     string
	Message    string
	UserUUID   string
	UserID     string
	Nodes      []UserIPSnapshotNode
	IsComplete bool
	IsFailed   bool
}

// IPObservation is a normalized user/IP observation from panel ingest.
type IPObservation struct {
	NodeUUID       string
	UserIdentifier string
	SourceIP       string
	LastSeen       time.Time
}

// FetchUsersIPsResult is a normalized poll result for a single node job.
type FetchUsersIPsResult struct {
	Status       string
	Message      string
	Observations []IPObservation
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if msg == "" {
		msg = "request failed"
	}
	return fmt.Sprintf("status %d: %s", e.Status, msg)
}

func normalizeAPIToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) >= 7 && strings.EqualFold(token[:7], "Bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	return token
}

func parseNameValue(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	name := strings.TrimSpace(parts[0])
	val := strings.TrimSpace(parts[1])
	if name == "" || val == "" {
		return "", "", false
	}
	return name, val, true
}

// NewClient creates a raw Remnawave client.
func NewClient(baseURL, apiToken string, timeoutSeconds, cacheTTLHours int, redisClient *redis.Client) *Client {
	return NewClientWithHeader(baseURL, apiToken, timeoutSeconds, cacheTTLHours, redisClient, "")
}

// NewClientWithHeader creates a client with optional reverse-proxy gate bypass.
func NewClientWithHeader(baseURL, apiToken string, timeoutSeconds, cacheTTLHours int, redisClient *redis.Client, gateHeader string) *Client {
	apiToken = normalizeAPIToken(apiToken)
	gateName, gateValue, _ := parseNameValue(gateHeader)
	if timeoutSeconds < 1 {
		timeoutSeconds = 5
	}
	if cacheTTLHours < 1 {
		cacheTTLHours = 24
	}
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
		redis:      redisClient,
		cacheTTL:   time.Duration(cacheTTLHours) * time.Hour,
		gateName:   gateName,
		gateValue:  gateValue,
	}
}

// Ping checks panel availability. No body shape is required; any 2xx is enough.
func (c *Client) Ping(ctx context.Context) error {
	if c.baseURL == "" || c.apiToken == "" {
		return nil
	}
	_, _, err := c.doJSON(ctx, http.MethodGet, "/api/system/health", nil)
	return err
}

// ResolveUUIDByInternalID resolves a user UUID by internal numeric ID.
func (c *Client) ResolveUUIDByInternalID(ctx context.Context, internalID int64) (string, error) {
	user, err := c.GetUserByInternalID(ctx, internalID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(user.UUID) == "" {
		return "", fmt.Errorf("empty UUID in response")
	}
	return user.UUID, nil
}

// GetUserByInternalID loads a user and caches the minimal shape needed by the service.
func (c *Client) GetUserByInternalID(ctx context.Context, internalID int64) (*UserInfo, error) {
	if internalID <= 0 {
		return nil, fmt.Errorf("invalid internal ID %d", internalID)
	}
	if cached, ok := c.getCachedUserInfo(ctx, internalID); ok {
		metrics.UserResolveCacheHit.Add(1)
		return cached, nil
	}
	metrics.UserResolveCacheMiss.Add(1)

	_, data, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/users/by-id/%d", internalID), nil)
	if err != nil {
		return nil, err
	}

	user, err := parseUserInfo(data)
	if err != nil {
		return nil, err
	}
	if user.InternalID == 0 {
		user.InternalID = internalID
	}
	c.setCachedUserInfo(ctx, user)
	return user, nil
}

// ResolveUser attempts to resolve a user by arbitrary identifier using the new 2.7.0 route.
func (c *Client) ResolveUser(ctx context.Context, identifier string) (*UserInfo, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, fmt.Errorf("empty identifier")
	}
	if numericID, err := strconv.ParseInt(identifier, 10, 64); err == nil {
		_, data, err := c.doJSON(ctx, http.MethodPost, "/api/users/resolve", map[string]any{"id": numericID})
		if err != nil {
			return nil, err
		}
		user, err := parseResolvedUserInfo(data)
		if err != nil {
			return nil, err
		}
		c.setCachedUserInfo(ctx, user)
		return user, nil
	}

	candidates := make([]map[string]any, 0, 3)
	if looksLikeUUID(identifier) {
		candidates = append(candidates, map[string]any{"uuid": identifier})
	}
	candidates = append(candidates,
		map[string]any{"username": identifier},
		map[string]any{"shortUuid": identifier},
	)

	var lastErr error
	for _, payload := range candidates {
		_, data, err := c.doJSON(ctx, http.MethodPost, "/api/users/resolve", payload)
		if err != nil {
			if apiErr, ok := err.(*apiError); ok && apiErr.Status >= 400 && apiErr.Status < 500 {
				lastErr = err
				continue
			}
			return nil, err
		}

		user, parseErr := parseResolvedUserInfo(data)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		if user.InternalID > 0 {
			c.setCachedUserInfo(ctx, user)
		}
		return user, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("resolve user %q: no compatible request payload", identifier)
}

// DisableUser disables a user UUID.
func (c *Client) DisableUser(ctx context.Context, uuid string) error {
	_, _, err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/users/%s/actions/disable", url.PathEscape(strings.TrimSpace(uuid))), nil)
	return err
}

// EnableUser enables a user UUID.
func (c *Client) EnableUser(ctx context.Context, uuid string) error {
	_, _, err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/users/%s/actions/enable", url.PathEscape(strings.TrimSpace(uuid))), nil)
	return err
}

// ListNodes returns the minimal node list needed by the poller.
func (c *Client) ListNodes(ctx context.Context) ([]NodeInfo, error) {
	_, data, err := c.doJSON(ctx, http.MethodGet, "/api/nodes", nil)
	if err != nil {
		return nil, err
	}
	root, err := decodeJSONAny(data)
	if err != nil {
		return nil, err
	}
	payload := extractResponsePayload(root)
	items, ok := payload.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected nodes response shape")
	}

	nodes := make([]NodeInfo, 0, len(items))
	for _, item := range items {
		nodeMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		uuid := stringify(mapLookup(nodeMap, "uuid"))
		if strings.TrimSpace(uuid) == "" {
			continue
		}
		nodes = append(nodes, NodeInfo{
			UUID:        uuid,
			Name:        stringify(mapLookup(nodeMap, "name")),
			IsDisabled:  boolValue(mapLookup(nodeMap, "isDisabled")),
			IsConnected: boolValue(mapLookup(nodeMap, "isConnected")),
		})
	}

	return nodes, nil
}

// ListInternalSquads returns all internal squads in minimal form.
func (c *Client) ListInternalSquads(ctx context.Context) ([]InternalSquadInfo, error) {
	_, data, err := c.doJSON(ctx, http.MethodGet, "/api/internal-squads", nil)
	if err != nil {
		return nil, err
	}
	root, err := decodeJSONAny(data)
	if err != nil {
		return nil, err
	}
	payload := extractResponsePayload(root)
	responseMap, ok := payload.(map[string]any)
	if ok {
		payload = mapLookup(responseMap, "internalSquads")
	}
	items, ok := payload.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected internal squads response shape")
	}

	squads := make([]InternalSquadInfo, 0, len(items))
	for _, item := range items {
		squadMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		uuid := stringify(mapLookup(squadMap, "uuid"))
		if strings.TrimSpace(uuid) == "" {
			continue
		}
		squads = append(squads, InternalSquadInfo{
			UUID: uuid,
			Name: stringify(mapLookup(squadMap, "name")),
		})
	}

	return squads, nil
}

// SubmitFetchUsersIPs starts a per-node IP fetch job and returns its job ID.
func (c *Client) SubmitFetchUsersIPs(ctx context.Context, nodeUUID string) (string, error) {
	_, data, err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/ip-control/fetch-users-ips/%s", url.PathEscape(strings.TrimSpace(nodeUUID))), nil)
	if err != nil {
		return "", err
	}
	root, err := decodeJSONAny(data)
	if err != nil {
		return "", err
	}
	jobID := extractStringByKeysRecursive(root, "jobId", "jobID", "id", "uuid")
	if strings.TrimSpace(jobID) == "" {
		return "", fmt.Errorf("fetch-users-ips submit: empty job ID")
	}
	return jobID, nil
}

// GetFetchUsersIPsResult gets a normalized fetch result for a job.
func (c *Client) GetFetchUsersIPsResult(ctx context.Context, jobID, nodeUUID string) (*FetchUsersIPsResult, error) {
	_, data, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/ip-control/fetch-users-ips/result/%s", url.PathEscape(strings.TrimSpace(jobID))), nil)
	if err != nil {
		return nil, err
	}
	root, err := decodeJSONAny(data)
	if err != nil {
		return nil, err
	}
	payload := extractResponsePayload(root)
	result := &FetchUsersIPsResult{
		Status:  strings.ToLower(strings.TrimSpace(extractStringByKeysRecursive(payload, "status", "state", "jobStatus"))),
		Message: strings.TrimSpace(extractStringByKeysRecursive(payload, "message", "reason", "error")),
	}

	if payloadMap, ok := payload.(map[string]any); ok {
		resultPayload := mapLookup(payloadMap, "result")
		if resultMap, ok := resultPayload.(map[string]any); ok {
			if successRaw := mapLookup(resultMap, "success"); successRaw != nil && !boolValue(successRaw) {
				result.Status = "failed"
			}
		}
		switch {
		case boolValue(mapLookup(payloadMap, "isFailed")):
			result.Status = "failed"
		case boolValue(mapLookup(payloadMap, "isCompleted")):
			result.Status = "completed"
		case result.Status == "":
			result.Status = "pending"
		}
		if resultNodeUUID := strings.TrimSpace(extractStringByKeysRecursive(resultPayload, "nodeUuid")); resultNodeUUID != "" {
			nodeUUID = resultNodeUUID
		}
		if officialObservations := collectFetchUsersIPsResultObservations(resultPayload, nodeUUID); len(officialObservations) > 0 {
			result.Observations = officialObservations
		}
	}
	if len(result.Observations) == 0 {
		result.Observations = collectObservations(root, nodeUUID)
	}
	if result.Status == "" {
		if len(result.Observations) > 0 {
			result.Status = "completed"
		} else {
			result.Status = "pending"
		}
	}
	return result, nil
}

func (c *Client) SubmitFetchUserIPs(ctx context.Context, userUUID string) (string, error) {
	_, data, err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/ip-control/fetch-ips/%s", url.PathEscape(strings.TrimSpace(userUUID))), nil)
	if err != nil {
		return "", err
	}
	root, err := decodeJSONAny(data)
	if err != nil {
		return "", err
	}
	jobID := extractStringByKeysRecursive(root, "jobId", "jobID", "id", "uuid")
	if strings.TrimSpace(jobID) == "" {
		return "", fmt.Errorf("fetch-ips submit: empty job ID")
	}
	return jobID, nil
}

func (c *Client) GetFetchUserIPsResult(ctx context.Context, jobID string) (*UserIPSnapshotResult, error) {
	_, data, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/ip-control/fetch-ips/result/%s", url.PathEscape(strings.TrimSpace(jobID))), nil)
	if err != nil {
		return nil, err
	}
	root, err := decodeJSONAny(data)
	if err != nil {
		return nil, err
	}
	payload := extractResponsePayload(root)
	result := &UserIPSnapshotResult{
		Status: strings.ToLower(strings.TrimSpace(extractStringByKeysRecursive(payload, "status", "state", "jobStatus"))),
		Message: strings.TrimSpace(extractStringByKeysRecursive(payload, "message", "reason", "error")),
	}
	if payloadMap, ok := payload.(map[string]any); ok {
		result.IsComplete = boolValue(mapLookup(payloadMap, "isCompleted"))
		result.IsFailed = boolValue(mapLookup(payloadMap, "isFailed"))
		switch {
		case result.IsFailed:
			result.Status = "failed"
		case result.IsComplete:
			result.Status = "completed"
		case result.Status == "":
			result.Status = "pending"
		}
		if parsed := parseUserIPSnapshotResult(mapLookup(payloadMap, "result")); parsed != nil {
			result.UserUUID = parsed.UserUUID
			result.UserID = parsed.UserID
			result.Nodes = parsed.Nodes
			if !parsed.Success && result.Status == "" {
				result.Status = "failed"
			}
		}
	}
	if result.Status == "" {
		if len(result.Nodes) > 0 {
			result.Status = "completed"
		} else {
			result.Status = "pending"
		}
	}
	return result, nil
}

// ExecuteTemporaryIPBlock applies a temporary IP block on selected nodes.
func (c *Client) ExecuteTemporaryIPBlock(ctx context.Context, nodeUUIDs []string, ips []string, duration time.Duration) error {
	nodeUUIDs = dedupeStrings(nodeUUIDs)
	ips = dedupeStrings(ips)
	if len(nodeUUIDs) == 0 || len(ips) == 0 {
		return nil
	}

	seconds := int64(duration / time.Second)
	if seconds < 1 {
		seconds = 1
	}

	payload := map[string]any{
		"command": map[string]any{
			"command": "blockIps",
			"ips":     buildExecutorBlockItems(ips, seconds),
		},
		"targetNodes": map[string]any{
			"target":    "specificNodes",
			"nodeUuids": nodeUUIDs,
		},
	}
	_, _, err := c.doJSON(ctx, http.MethodPost, "/api/node-plugins/executor", payload)
	return err
}

// GetUserHwidDevices fetches HWID devices for a user UUID.
func (c *Client) GetUserHwidDevices(ctx context.Context, userUUID string) ([]HwidDevice, error) {
	_, data, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/hwid/devices/%s", url.PathEscape(strings.TrimSpace(userUUID))), nil)
	if err != nil {
		return nil, err
	}
	return parseUserHwidDevices(data)
}

// GetUserSubscriptionRequestHistory fetches recent subscription request history for a user UUID.
func (c *Client) GetUserSubscriptionRequestHistory(ctx context.Context, userUUID string) ([]SubscriptionRequestRecord, error) {
	_, data, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/users/%s/subscription-request-history", url.PathEscape(strings.TrimSpace(userUUID))), nil)
	if err != nil {
		return nil, err
	}
	return parseSubscriptionRequestHistory(data)
}

// GetUserEvidenceByInternalID fetches user evidence used to reduce anti-sharing false positives.
func (c *Client) GetUserEvidenceByInternalID(ctx context.Context, internalID int64) (*UserEvidence, error) {
	user, err := c.GetUserByInternalID(ctx, internalID)
	if err != nil {
		return nil, err
	}

	devices, err := c.GetUserHwidDevices(ctx, user.UUID)
	if err != nil {
		return nil, err
	}
	requests, err := c.GetUserSubscriptionRequestHistory(ctx, user.UUID)
	if err != nil {
		return nil, err
	}

	return &UserEvidence{
		UserUUID:             user.UUID,
		InternalID:           user.InternalID,
		HwidDevices:          devices,
		SubscriptionRequests: requests,
	}, nil
}

func (c *Client) GetUserIPSnapshotByInternalID(ctx context.Context, internalID int64, timeout, pollInterval time.Duration) (*UserIPSnapshotResult, error) {
	user, err := c.GetUserByInternalID(ctx, internalID)
	if err != nil {
		return nil, err
	}
	jobID, err := c.SubmitFetchUserIPs(ctx, user.UUID)
	if err != nil {
		return nil, err
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		result, err := c.GetFetchUserIPsResult(pollCtx, jobID)
		if err != nil {
			return nil, err
		}
		if result != nil && (result.IsComplete || result.Status == "completed" || result.IsFailed || result.Status == "failed") {
			return result, nil
		}
		select {
		case <-pollCtx.Done():
			return nil, pollCtx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (c *Client) getCachedUserInfo(ctx context.Context, internalID int64) (*UserInfo, bool) {
	if c.redis == nil {
		return nil, false
	}
	key := fmt.Sprintf("rw:userinfo:%d", internalID)
	data, err := c.redis.Get(ctx, key).Bytes()
	if err != nil || len(data) == 0 {
		return nil, false
	}
	var user UserInfo
	if err := json.Unmarshal(data, &user); err != nil {
		return nil, false
	}
	return &user, true
}

func (c *Client) setCachedUserInfo(ctx context.Context, user *UserInfo) {
	if c.redis == nil || user == nil || user.InternalID <= 0 {
		return
	}
	data, err := json.Marshal(user)
	if err != nil {
		return
	}
	key := fmt.Sprintf("rw:userinfo:%d", user.InternalID)
	_ = c.redis.Set(ctx, key, data, c.cacheTTL).Err()
	if strings.TrimSpace(user.UUID) != "" {
		_ = c.redis.Set(ctx, fmt.Sprintf("rw:uid2uuid:%d", user.InternalID), user.UUID, c.cacheTTL).Err()
	}
}

func (c *Client) doJSON(ctx context.Context, method, apiPath string, body any) (int, []byte, error) {
	if strings.TrimSpace(c.baseURL) == "" || strings.TrimSpace(c.apiToken) == "" {
		return 0, nil, fmt.Errorf("remnawave client is not configured")
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	reqURL, err := url.Parse(c.baseURL)
	if err != nil {
		return 0, nil, fmt.Errorf("parse base URL: %w", err)
	}
	reqURL.Path = strings.TrimRight(reqURL.Path, "/") + apiPath
	if c.gateName != "" && c.gateValue != "" {
		query := reqURL.Query()
		query.Set(c.gateName, c.gateValue)
		reqURL.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.gateName != "" && c.gateValue != "" {
		req.AddCookie(&http.Cookie{Name: c.gateName, Value: c.gateValue})
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := extractErrorMessage(data)
		if resp.StatusCode == http.StatusNotFound && message == "" {
			message = "not found"
		}
		return resp.StatusCode, data, &apiError{Status: resp.StatusCode, Message: message}
	}

	return resp.StatusCode, data, nil
}

func parseUserInfo(data []byte) (*UserInfo, error) {
	root, err := decodeJSONAny(data)
	if err != nil {
		return nil, err
	}
	userMap, ok := extractResponsePayload(root).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected user response shape")
	}

	user := &UserInfo{
		UUID:       strings.TrimSpace(extractStringByKeysRecursive(userMap, "uuid")),
		InternalID: intValue(mapLookup(userMap, "id")),
	}

	if squadsRaw := mapLookup(userMap, "activeInternalSquads"); squadsRaw != nil {
		if squads, ok := squadsRaw.([]any); ok {
			user.ActiveInternalSquads = make([]InternalSquadInfo, 0, len(squads))
			for _, squadItem := range squads {
				switch typed := squadItem.(type) {
				case map[string]any:
					uuid := strings.TrimSpace(stringify(mapLookup(typed, "uuid")))
					if uuid == "" {
						continue
					}
					user.ActiveInternalSquads = append(user.ActiveInternalSquads, InternalSquadInfo{
						UUID: uuid,
						Name: stringify(mapLookup(typed, "name")),
					})
				default:
					uuid := strings.TrimSpace(stringify(typed))
					if uuid != "" {
						user.ActiveInternalSquads = append(user.ActiveInternalSquads, InternalSquadInfo{UUID: uuid})
					}
				}
			}
		}
	}

	if user.UUID == "" {
		return nil, fmt.Errorf("empty UUID in user response")
	}

	return user, nil
}

func parseResolvedUserInfo(data []byte) (*UserInfo, error) {
	root, err := decodeJSONAny(data)
	if err != nil {
		return nil, err
	}
	userMap, ok := extractResponsePayload(root).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected resolve user response shape")
	}

	user := &UserInfo{
		UUID:       strings.TrimSpace(extractStringByKeysRecursive(userMap, "uuid")),
		InternalID: intValue(mapLookup(userMap, "id")),
	}
	if user.UUID == "" {
		return nil, fmt.Errorf("empty UUID in resolve user response")
	}
	return user, nil
}

func parseUserHwidDevices(data []byte) ([]HwidDevice, error) {
	root, err := decodeJSONAny(data)
	if err != nil {
		return nil, err
	}
	payload, ok := extractResponsePayload(root).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected HWID devices response shape")
	}
	items, ok := mapLookup(payload, "devices").([]any)
	if !ok {
		return []HwidDevice{}, nil
	}

	devices := make([]HwidDevice, 0, len(items))
	for _, item := range items {
		deviceMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hwid := strings.TrimSpace(stringify(mapLookup(deviceMap, "hwid")))
		if hwid == "" {
			continue
		}
		devices = append(devices, HwidDevice{
			HWID:        hwid,
			UserUUID:    strings.TrimSpace(stringify(mapLookup(deviceMap, "userUuid"))),
			Platform:    strings.TrimSpace(stringify(mapLookup(deviceMap, "platform"))),
			OSVersion:   strings.TrimSpace(stringify(mapLookup(deviceMap, "osVersion"))),
			DeviceModel: strings.TrimSpace(stringify(mapLookup(deviceMap, "deviceModel"))),
			UserAgent:   strings.TrimSpace(stringify(mapLookup(deviceMap, "userAgent"))),
			CreatedAt:   timeValue(mapLookup(deviceMap, "createdAt")),
			UpdatedAt:   timeValue(mapLookup(deviceMap, "updatedAt")),
		})
	}
	return devices, nil
}

func parseSubscriptionRequestHistory(data []byte) ([]SubscriptionRequestRecord, error) {
	root, err := decodeJSONAny(data)
	if err != nil {
		return nil, err
	}
	payload, ok := extractResponsePayload(root).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected subscription request history response shape")
	}
	items, ok := mapLookup(payload, "records").([]any)
	if !ok {
		return []SubscriptionRequestRecord{}, nil
	}

	records := make([]SubscriptionRequestRecord, 0, len(items))
	for _, item := range items {
		recordMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		records = append(records, SubscriptionRequestRecord{
			ID:        intValue(mapLookup(recordMap, "id")),
			UserUUID:  strings.TrimSpace(stringify(mapLookup(recordMap, "userUuid"))),
			RequestAt: timeValue(mapLookup(recordMap, "requestAt")),
			RequestIP: strings.TrimSpace(stringify(mapLookup(recordMap, "requestIp"))),
			UserAgent: strings.TrimSpace(stringify(mapLookup(recordMap, "userAgent"))),
		})
	}
	return records, nil
}

type parsedUserIPSnapshot struct {
	Success  bool
	UserUUID string
	UserID   string
	Nodes    []UserIPSnapshotNode
}

func parseUserIPSnapshotResult(value any) *parsedUserIPSnapshot {
	resultMap, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	parsed := &parsedUserIPSnapshot{
		Success:  boolValue(mapLookup(resultMap, "success")),
		UserUUID: strings.TrimSpace(stringify(mapLookup(resultMap, "userUuid"))),
		UserID:   strings.TrimSpace(stringify(mapLookup(resultMap, "userId"))),
	}
	nodesRaw, ok := mapLookup(resultMap, "nodes").([]any)
	if !ok {
		return parsed
	}
	parsed.Nodes = make([]UserIPSnapshotNode, 0, len(nodesRaw))
	for _, nodeItem := range nodesRaw {
		nodeMap, ok := nodeItem.(map[string]any)
		if !ok {
			continue
		}
		node := UserIPSnapshotNode{
			NodeUUID:    strings.TrimSpace(stringify(mapLookup(nodeMap, "nodeUuid"))),
			NodeName:    strings.TrimSpace(stringify(mapLookup(nodeMap, "nodeName"))),
			CountryCode: strings.TrimSpace(stringify(mapLookup(nodeMap, "countryCode"))),
		}
		ipsRaw, _ := mapLookup(nodeMap, "ips").([]any)
		node.IPs = make([]UserIPSnapshotIP, 0, len(ipsRaw))
		for _, ipItem := range ipsRaw {
			ipMap, ok := ipItem.(map[string]any)
			if !ok {
				continue
			}
			ip := strings.TrimSpace(stringify(mapLookup(ipMap, "ip")))
			if ip == "" {
				continue
			}
			node.IPs = append(node.IPs, UserIPSnapshotIP{
				IP:       ip,
				LastSeen: timeValue(mapLookup(ipMap, "lastSeen")),
			})
		}
		parsed.Nodes = append(parsed.Nodes, node)
	}
	return parsed
}

func collectFetchUsersIPsResultObservations(result any, nodeUUID string) []IPObservation {
	resultMap, ok := result.(map[string]any)
	if !ok {
		return nil
	}
	users, ok := mapLookup(resultMap, "users").([]any)
	if !ok {
		return nil
	}

	seen := make(map[string]struct{}, len(users))
	observations := make([]IPObservation, 0)
	for _, item := range users {
		userMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		identifier := strings.TrimSpace(stringify(mapLookup(userMap, "userId")))
		if identifier == "" {
			continue
		}
		appendObservations(&observations, seen, identifier, mapLookup(userMap, "ips"), nodeUUID)
	}
	return observations
}

func buildExecutorBlockItems(ips []string, timeoutSeconds int64) []map[string]any {
	items := make([]map[string]any, 0, len(ips))
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		items = append(items, map[string]any{
			"ip":      ip,
			"timeout": timeoutSeconds,
		})
	}
	return items
}

func collectObservations(root any, nodeUUID string) []IPObservation {
	seen := make(map[string]struct{})
	var out []IPObservation

	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			if ipsRaw := mapLookup(typed, "ips"); ipsRaw != nil {
				identifier := extractUserIdentifier(typed)
				if userRaw := mapLookup(typed, "user"); identifier == "" {
					if userMap, ok := userRaw.(map[string]any); ok {
						identifier = extractUserIdentifier(userMap)
					}
				}
				appendObservations(&out, seen, identifier, ipsRaw, nodeUUID)
			} else if ipRaw := mapLookup(typed, "ip"); ipRaw != nil {
				identifier := extractUserIdentifier(typed)
				if userRaw := mapLookup(typed, "user"); identifier == "" {
					if userMap, ok := userRaw.(map[string]any); ok {
						identifier = extractUserIdentifier(userMap)
					}
				}
				appendObservations(&out, seen, identifier, []any{typed}, nodeUUID)
			}

			for _, child := range typed {
				walk(child)
			}
		}
	}

	walk(root)
	return out
}

func appendObservations(dst *[]IPObservation, seen map[string]struct{}, identifier string, rawIPs any, nodeUUID string) {
	items, ok := rawIPs.([]any)
	if !ok {
		return
	}
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return
	}

	for _, item := range items {
		var ip string
		var lastSeen time.Time
		switch typed := item.(type) {
		case string:
			ip = typed
		case map[string]any:
			ip = stringify(mapLookup(typed, "ip"))
			if ip == "" {
				ip = stringify(mapLookup(typed, "address"))
			}
			lastSeen = timeValue(mapLookup(typed, "lastSeen"))
			if lastSeen.IsZero() {
				lastSeen = timeValue(mapLookup(typed, "seenAt"))
			}
			if lastSeen.IsZero() {
				lastSeen = timeValue(mapLookup(typed, "timestamp"))
			}
			if lastSeen.IsZero() {
				lastSeen = timeValue(mapLookup(typed, "updatedAt"))
			}
		}
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		key := fmt.Sprintf("%s|%s|%s|%d", nodeUUID, identifier, ip, lastSeen.Unix())
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		*dst = append(*dst, IPObservation{
			NodeUUID:       nodeUUID,
			UserIdentifier: identifier,
			SourceIP:       ip,
			LastSeen:       lastSeen,
		})
	}
}

func extractUserIdentifier(value map[string]any) string {
	for _, key := range []string{"userId", "userID", "internalId", "internalID", "id", "uuid", "email", "username", "tag"} {
		if found := strings.TrimSpace(stringify(mapLookup(value, key))); found != "" {
			return found
		}
	}
	return ""
}

func decodeJSONAny(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	return root, nil
}

func extractResponsePayload(root any) any {
	if rootMap, ok := root.(map[string]any); ok {
		if response := mapLookup(rootMap, "response"); response != nil {
			return response
		}
	}
	return root
}

func extractErrorMessage(data []byte) string {
	root, err := decodeJSONAny(data)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(extractStringByKeysRecursive(root, "message", "error", "reason"))
}

func extractStringByKeysRecursive(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if found := strings.TrimSpace(stringify(mapLookup(typed, key))); found != "" {
				return found
			}
		}
		for _, child := range typed {
			if found := extractStringByKeysRecursive(child, keys...); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := extractStringByKeysRecursive(child, keys...); found != "" {
				return found
			}
		}
	}
	return ""
}

func mapLookup(value map[string]any, key string) any {
	for existingKey, existingValue := range value {
		if strings.EqualFold(existingKey, key) {
			return existingValue
		}
	}
	return nil
}

func stringify(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func intValue(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case json.Number:
		if v, err := typed.Int64(); err == nil {
			return v
		}
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	case string:
		v, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return v
	}
	return 0
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		v, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return v
	case json.Number:
		v, _ := typed.Int64()
		return v != 0
	case float64:
		return typed != 0
	default:
		return false
	}
}

func timeValue(value any) time.Time {
	switch typed := value.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, strings.TrimSpace(typed)); err == nil {
				return parsed
			}
		}
	case json.Number:
		if v, err := typed.Int64(); err == nil && v > 0 {
			return time.Unix(v, 0).UTC()
		}
	case float64:
		if typed > 0 {
			return time.Unix(int64(typed), 0).UTC()
		}
	}
	return time.Time{}
}

func dedupeStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func looksLikeUUID(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if len(value) != 36 {
		return false
	}
	for idx, ch := range value {
		switch idx {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
				return false
			}
		}
	}
	return true
}
