//go:build integration
// +build integration

package publisher

import (
	"context"
	"fmt"
	"observer_service/internal/models"
	"os"
	"testing"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

// TestRabbitMQPublisher_PublishIntegration проверяет реальную публикацию с confirms.
func TestRabbitMQPublisher_PublishIntegration(t *testing.T) {
	rabbitURL := os.Getenv("RABBITMQ_URL")
	if rabbitURL == "" {
		t.Skip("RABBITMQ_URL not set, skipping integration test")
	}

	exchangeName := fmt.Sprintf("test_exchange_%d", time.Now().Unix())
	queueName := fmt.Sprintf("test_queue_%d", time.Now().Unix())

	// Создаём publisher
	pub, err := NewRabbitMQPublisher(rabbitURL, exchangeName, 2, 3, 100, 5000, 2000)
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}
	defer pub.Close()

	// Создаём временную queue и binding для потребления сообщения
	conn, err := amqp091.Dial(rabbitURL)
	if err != nil {
		t.Fatalf("Failed to connect for consumer: %v", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("Failed to create channel: %v", err)
	}
	defer ch.Close()

	// Объявляем exchange (fanout)
	err = ch.ExchangeDeclare(exchangeName, "fanout", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("Failed to declare exchange: %v", err)
	}

	// Создаём временную queue
	q, err := ch.QueueDeclare(queueName, false, true, false, false, nil)
	if err != nil {
		t.Fatalf("Failed to declare queue: %v", err)
	}

	// Биндим queue к exchange
	err = ch.QueueBind(q.Name, "", exchangeName, false, nil)
	if err != nil {
		t.Fatalf("Failed to bind queue: %v", err)
	}

	// Публикуем block message
	msg := models.BlockMessage{
		UserEmail:   "test@example.com",
		BlockReason: "integration test",
		IPs:         []string{"192.168.1.1", "192.168.1.2"},
	}

	err = pub.PublishBlockMessage(msg)
	if err != nil {
		t.Fatalf("PublishBlockMessage failed: %v", err)
	}

	// Читаем сообщение из queue (с таймаутом)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msgs, err := ch.Consume(q.Name, "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("Failed to consume: %v", err)
	}

	select {
	case delivery := <-msgs:
		t.Logf("Received message: %d bytes, persistent=%d", len(delivery.Body), delivery.DeliveryMode)
		if delivery.DeliveryMode != amqp091.Persistent {
			t.Error("Expected persistent delivery mode")
		}
		if len(delivery.Body) == 0 {
			t.Error("Empty message body")
		}
		if delivery.MessageId == "" {
			t.Error("Expected MessageId in published message")
		}
	case <-ctx.Done():
		t.Fatal("Timeout waiting for message")
	}
}

// TestRabbitMQPublisher_MandatoryIntegration проверяет mandatory publish без binding.
func TestRabbitMQPublisher_MandatoryIntegration(t *testing.T) {
	rabbitURL := os.Getenv("RABBITMQ_URL")
	if rabbitURL == "" {
		t.Skip("RABBITMQ_URL not set, skipping integration test")
	}

	// Exchange без bindings → mandatory publish должен retry
	exchangeName := fmt.Sprintf("test_no_binding_%d", time.Now().Unix())

	pub, err := NewRabbitMQPublisher(rabbitURL, exchangeName, 1, 2, 100, 1000, 1000)
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}
	defer pub.Close()

	msg := models.BlockMessage{
		UserEmail:   "test@example.com",
		BlockReason: "mandatory test",
		IPs:         []string{"10.0.0.1"},
	}

	// Публикация должна failed после retries (т.к. нет bindings)
	start := time.Now()
	err = pub.PublishBlockMessage(msg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Expected publish to fail (no binding), but it succeeded")
	}

	// Проверяем что были retries (elapsed должен быть >500ms из-за backoff)
	if elapsed < 100*time.Millisecond {
		t.Errorf("Expected retries with backoff, but elapsed time is too short: %v", elapsed)
	}

	t.Logf("Publish correctly failed after %v with error: %v", elapsed, err)
}
