package remnawave

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client взаимодействует с Remnawave API для enforce операций.
type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
	redis      *redis.Client
	cacheTTL   time.Duration
}

// NewClient создаёт новый Remnawave client с HTTP timeout и Redis кэшированием.
func NewClient(baseURL, apiToken string, timeoutSeconds, cacheTTLHours int, redisClient *redis.Client) *Client {
	return &Client{
		baseURL:  baseURL,
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutSeconds) * time.Second,
		},
		redis:    redisClient,
		cacheTTL: time.Duration(cacheTTLHours) * time.Hour,
	}
}

// ResolveUUIDByInternalID резолвит UUID пользователя по internal numeric ID.
// Сначала проверяет Redis cache, если промах — запрашивает Remnawave API.
func (c *Client) ResolveUUIDByInternalID(ctx context.Context, internalID int64) (string, error) {
	// Проверяем кэш
	cacheKey := fmt.Sprintf("rw:uid2uuid:%d", internalID)
	cached, err := c.redis.Get(ctx, cacheKey).Result()
	if err == nil && cached != "" {
		return cached, nil
	}

	// Cache miss: запрашиваем API
	// TODO: Если у Remnawave API нет endpoint "get user by numeric ID", то нужен fallback:
	// - либо GET /users?id=<internalID> (если поддерживается фильтрация)
	// - либо итерация по пагинированному списку (НЕ рекомендуется из-за нагрузки)
	//
	// Временная заглушка для демонстрации структуры:
	// Предполагаем endpoint GET /api/users/<internalID> возвращает {"uuid": "...", "id": ...}
	url := fmt.Sprintf("%s/api/users/%d", c.baseURL, internalID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("user with internal ID %d not found", internalID)
	}
	if resp.StatusCode >= 500 {
		return "", fmt.Errorf("remnawave server error: %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var result struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if result.UUID == "" {
		return "", fmt.Errorf("empty UUID in response")
	}

	// Кэшируем результат
	_ = c.redis.Set(ctx, cacheKey, result.UUID, c.cacheTTL).Err()

	return result.UUID, nil
}

// DisableUser отключает пользователя по UUID через Remnawave API.
func (c *Client) DisableUser(ctx context.Context, uuid string) error {
	// Предполагаем endpoint POST /api/users/<uuid>/disable
	url := fmt.Sprintf("%s/api/users/%s/disable", c.baseURL, uuid)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("remnawave server error: %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("disable failed: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// EnableUser включает пользователя по UUID через Remnawave API.
func (c *Client) EnableUser(ctx context.Context, uuid string) error {
	// Предполагаем endpoint POST /api/users/<uuid>/enable
	url := fmt.Sprintf("%s/api/users/%s/enable", c.baseURL, uuid)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("remnawave server error: %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("enable failed: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// Ping проверяет доступность Remnawave API (для health check).
func (c *Client) Ping(ctx context.Context) error {
	if c.baseURL == "" || c.apiToken == "" {
		// Если Remnawave не настроен, считаем что всё ок (noop режим)
		return nil
	}

	// Предполагаем endpoint GET /api/health или GET /api/ping
	url := fmt.Sprintf("%s/api/health", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping failed: status %d", resp.StatusCode)
	}

	return nil
}
