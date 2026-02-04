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

// Notifier определяет интерфейс для отправки уведомлений.
type Notifier interface {
	SendAlert(ctx context.Context, payload models.AlertPayload) error
}

// WebhookAlerter реализует Notifier для отправки вебхуков.
type WebhookAlerter struct {
	client *http.Client
	url    string
}

// NewWebhookAlerter создает новый экземпляр WebhookAlerter.
func NewWebhookAlerter(url string) *WebhookAlerter {
	return &WebhookAlerter{
		client: &http.Client{},
		url:    url,
	}
}

// SendAlert отправляет уведомление на заданный URL. Таймаут контролируется ctx.
func (a *WebhookAlerter) SendAlert(ctx context.Context, payload models.AlertPayload) error {
	if a.url == "" {
		log.Println("ALERT_WEBHOOK_URL не задан, вебхук не отправляется")
		return nil
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("ошибка сериализации payload: %w", err)
	}

	log.Printf("Попытка отправить вебхук на URL: %s", a.url)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("ошибка создания запроса вебхука: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("сетевая ошибка при отправке вебхука: %w", err)
	}
	defer resp.Body.Close()

	var respBody bytes.Buffer
	_, _ = io.Copy(&respBody, io.LimitReader(resp.Body, maxWebhookResponseBytes))

	log.Printf("Вебхук-уведомление для %s отправлен. Статус ответа: %d. Тело ответа (макс. %d байт): %s",
		payload.UserIdentifier, resp.StatusCode, maxWebhookResponseBytes, respBody.String())

	if resp.StatusCode >= 400 {
		return fmt.Errorf("сервер вебхука ответил ошибкой: %s", resp.Status)
	}

	return nil
}
