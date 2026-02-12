package alerter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"observer_service/internal/models"
)

const maxWebhookResponseBytes int64 = 8 * 1024

// Notifier defines the interface for sending notifications.
type Notifier interface {
	SendAlert(ctx context.Context, payload models.AlertPayload) error
}

// WebhookAlerter implements Notifier for sending webhooks.
type WebhookAlerter struct {
	client *http.Client
	url    string
}

// NewWebhookAlerter creates a new WebhookAlerter instance.
func NewWebhookAlerter(url string) *WebhookAlerter {
	return &WebhookAlerter{
		client: &http.Client{},
		url:    url,
	}
}

// SendAlert sends a notification to the specified URL. Timeout is controlled by ctx.
func (a *WebhookAlerter) SendAlert(ctx context.Context, payload models.AlertPayload) error {
	if a.url == "" {
		log.Println("ALERT_WEBHOOK_URL not set, webhook not sent")
		return nil
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("payload serialization error: %w", err)
	}

	log.Printf("Attempting to send webhook to URL: %s", a.url)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("webhook request creation error: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("network error sending webhook: %w", err)
	}
	defer resp.Body.Close()

	var respBody bytes.Buffer
	_, _ = io.Copy(&respBody, io.LimitReader(resp.Body, maxWebhookResponseBytes))

	log.Printf("Webhook notification for %s sent. Response status: %d. Response body (max %d bytes): %s",
		payload.UserIdentifier, resp.StatusCode, maxWebhookResponseBytes, respBody.String())

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook server responded with error: %s", resp.Status)
	}

	return nil
}
