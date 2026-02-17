package remnawave

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	remapi "github.com/Jolymmiles/remnawave-api-go/v2/api"
	"github.com/redis/go-redis/v9"
)

// Client wraps Remnawave community SDK and local UUID cache.
type Client struct {
	baseURL  string
	apiToken string

	sdk     *remapi.ClientExt
	initErr error

	redis    *redis.Client
	cacheTTL time.Duration
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

// NewClient creates a Remnawave client with timeout and Redis cache settings.
func NewClient(baseURL, apiToken string, timeoutSeconds, cacheTTLHours int, redisClient *redis.Client) *Client {
	return NewClientWithHeader(baseURL, apiToken, timeoutSeconds, cacheTTLHours, redisClient, "")
}

// NewClientWithHeader creates a Remnawave client with optional gate bypass header in KEY=VALUE format.
// If provided, the pair is added both as query parameter and cookie on each request.
func NewClientWithHeader(baseURL, apiToken string, timeoutSeconds, cacheTTLHours int, redisClient *redis.Client, gateHeader string) *Client {
	apiToken = normalizeAPIToken(apiToken)
	gateName, gateValue, hasGateHeader := parseNameValue(gateHeader)

	client := &Client{
		baseURL:  baseURL,
		apiToken: apiToken,
		redis:    redisClient,
		cacheTTL: time.Duration(cacheTTLHours) * time.Hour,
	}

	if baseURL == "" || apiToken == "" {
		return client
	}

	httpClient := &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second}

	baseSDK, err := remapi.NewClient(
		baseURL,
		remapi.StaticToken{Token: apiToken},
		remapi.WithClient(httpClient),
		remapi.WithRequestEditor(func(_ context.Context, req *http.Request) error {
			if hasGateHeader {
				query := req.URL.Query()
				query.Set(gateName, gateValue)
				req.URL.RawQuery = query.Encode()
				req.AddCookie(&http.Cookie{Name: gateName, Value: gateValue})
			}

			return nil
		}),
	)
	if err != nil {
		client.initErr = fmt.Errorf("init remnawave sdk: %w", err)
		return client
	}

	client.sdk = remapi.NewClientExt(baseSDK)
	return client
}

func (c *Client) ensureSDK() error {
	if c.baseURL == "" || c.apiToken == "" {
		return nil
	}
	if c.initErr != nil {
		return c.initErr
	}
	if c.sdk == nil {
		return fmt.Errorf("remnawave sdk is not initialized")
	}
	return nil
}

func internalServerErrorMessage(resp *remapi.InternalServerError) string {
	if resp == nil {
		return "internal server error"
	}
	if msg, ok := resp.Message.Get(); ok {
		msg = strings.TrimSpace(msg)
		if msg != "" {
			return msg
		}
	}
	return "internal server error"
}

// ResolveUUIDByInternalID resolves a user UUID by internal numeric ID.
func (c *Client) ResolveUUIDByInternalID(ctx context.Context, internalID int64) (string, error) {
	cacheKey := fmt.Sprintf("rw:uid2uuid:%d", internalID)
	if c.redis != nil {
		cached, err := c.redis.Get(ctx, cacheKey).Result()
		if err == nil && cached != "" {
			return cached, nil
		}
	}

	if err := c.ensureSDK(); err != nil {
		return "", err
	}

	res, err := c.sdk.Users().GetUserById(ctx, strconv.FormatInt(internalID, 10))
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}

	var uuid string
	switch v := res.(type) {
	case *remapi.UserResponse:
		uuid = strings.TrimSpace(v.Response.UUID.String())
		if uuid == "" || uuid == "00000000-0000-0000-0000-000000000000" {
			return "", fmt.Errorf("empty UUID in response")
		}
	case *remapi.NotFoundError:
		return "", fmt.Errorf("user with internal ID %d not found", internalID)
	case *remapi.InternalServerError:
		return "", fmt.Errorf("remnawave server error: %s", internalServerErrorMessage(v))
	default:
		return "", fmt.Errorf("unexpected response type for get user by id: %T", res)
	}

	if c.redis != nil {
		_ = c.redis.Set(ctx, cacheKey, uuid, c.cacheTTL).Err()
	}

	return uuid, nil
}

// DisableUser disables a user by UUID via Remnawave API.
func (c *Client) DisableUser(ctx context.Context, uuid string) error {
	if err := c.ensureSDK(); err != nil {
		return err
	}

	res, err := c.sdk.Users().DisableUser(ctx, uuid)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}

	switch v := res.(type) {
	case *remapi.UserResponse:
		return nil
	case *remapi.NotFoundError:
		if strings.TrimSpace(v.Message) != "" {
			return fmt.Errorf("disable failed: %s", strings.TrimSpace(v.Message))
		}
		return fmt.Errorf("disable failed: user not found")
	case *remapi.BadRequestError:
		return fmt.Errorf("disable failed: bad request")
	case *remapi.InternalServerError:
		return fmt.Errorf("remnawave server error: %s", internalServerErrorMessage(v))
	default:
		return fmt.Errorf("disable failed: unexpected response type %T", res)
	}
}

// EnableUser enables a user by UUID via Remnawave API.
func (c *Client) EnableUser(ctx context.Context, uuid string) error {
	if err := c.ensureSDK(); err != nil {
		return err
	}

	res, err := c.sdk.Users().EnableUser(ctx, uuid)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}

	switch v := res.(type) {
	case *remapi.UserResponse:
		return nil
	case *remapi.NotFoundError:
		if strings.TrimSpace(v.Message) != "" {
			return fmt.Errorf("enable failed: %s", strings.TrimSpace(v.Message))
		}
		return fmt.Errorf("enable failed: user not found")
	case *remapi.BadRequestError:
		return fmt.Errorf("enable failed: bad request")
	case *remapi.InternalServerError:
		return fmt.Errorf("remnawave server error: %s", internalServerErrorMessage(v))
	default:
		return fmt.Errorf("enable failed: unexpected response type %T", res)
	}
}

// Ping checks Remnawave API availability for health checks.
func (c *Client) Ping(ctx context.Context) error {
	if c.baseURL == "" || c.apiToken == "" {
		// Noop mode when Remnawave is not configured.
		return nil
	}

	if err := c.ensureSDK(); err != nil {
		return err
	}

	res, err := c.sdk.System().GetRemnawaveHealth(ctx)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}

	switch v := res.(type) {
	case *remapi.GetRemnawaveHealthResponse:
		return nil
	case *remapi.BadRequestError:
		return fmt.Errorf("ping failed: bad request")
	case *remapi.InternalServerError:
		return fmt.Errorf("ping failed: server error: %s", internalServerErrorMessage(v))
	default:
		return fmt.Errorf("ping failed: unexpected response type %T", res)
	}
}
