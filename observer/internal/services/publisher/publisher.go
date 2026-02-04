package publisher

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"observer_service/internal/models"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

// EventPublisher определяет интерфейс для публикации событий.
type EventPublisher interface {
	PublishBlockMessage(msg models.BlockMessage) error
	Close() error
	Ping() error
}

// RabbitMQPublisher реализует EventPublisher для RabbitMQ с confirm mode и
// экспоненциальным backoff с full jitter на повторах.
type RabbitMQPublisher struct {
	conn          *amqp091.Connection
	channelPool   chan *amqp091.Channel
	exchangeName  string
	url           string
	poolSize      int
	maxRetries    int
	backoffBaseMs int
	backoffMaxMs  int
}

// NewRabbitMQPublisher создает и настраивает нового издателя RabbitMQ.
// poolSize — количество каналов в пуле.
// backoffBaseMs / backoffMaxMs — параметры экспоненциального backoff с full jitter.
func NewRabbitMQPublisher(url, exchangeName string, poolSize, maxRetries, backoffBaseMs, backoffMaxMs int) (*RabbitMQPublisher, error) {
	if poolSize <= 0 {
		poolSize = 5
	}
	if maxRetries <= 0 {
		maxRetries = 5
	}
	if backoffBaseMs <= 0 {
		backoffBaseMs = 500
	}
	if backoffMaxMs <= 0 {
		backoffMaxMs = 30000
	}

	p := &RabbitMQPublisher{
		url:           url,
		exchangeName:  exchangeName,
		poolSize:      poolSize,
		channelPool:   make(chan *amqp091.Channel, poolSize),
		maxRetries:    maxRetries,
		backoffBaseMs: backoffBaseMs,
		backoffMaxMs:  backoffMaxMs,
	}

	if err := p.connectWithRetry(); err != nil {
		return nil, fmt.Errorf("не удалось подключиться к RabbitMQ при инициализации: %w", err)
	}

	return p, nil
}

func (p *RabbitMQPublisher) connect() error {
	conn, err := amqp091.Dial(p.url)
	if err != nil {
		return fmt.Errorf("ошибка подключения к RabbitMQ: %w", err)
	}

	pool := make(chan *amqp091.Channel, p.poolSize)
	for i := 0; i < p.poolSize; i++ {
		ch, err := conn.Channel()
		if err != nil {
			conn.Close()
			close(pool)
			for old := range pool {
				old.Close()
			}
			return fmt.Errorf("ошибка создания канала %d: %w", i, err)
		}

		if err := ch.ExchangeDeclare(p.exchangeName, "fanout", true, false, false, false, nil); err != nil {
			ch.Close()
			conn.Close()
			close(pool)
			for old := range pool {
				old.Close()
			}
			return fmt.Errorf("ошибка объявления exchange: %w", err)
		}

		// Переводим канал в режим подтверждений от брокера
		if err := ch.Confirm(false); err != nil {
			ch.Close()
			conn.Close()
			close(pool)
			for old := range pool {
				old.Close()
			}
			return fmt.Errorf("ошибка включения confirm mode на канале %d: %w", i, err)
		}

		pool <- ch
	}

	p.channelPool = pool
	p.conn = conn
	log.Printf("Успешное (пере)подключение к RabbitMQ. Создан пул из %d каналов с confirm mode.", p.poolSize)
	return nil
}

func (p *RabbitMQPublisher) connectWithRetry() error {
	var err error
	for i := 0; i < p.maxRetries; i++ {
		err = p.connect()
		if err == nil {
			return nil
		}
		delay := p.backoffDelay(i)
		log.Printf("Не удалось подключиться к RabbitMQ (попытка %d/%d): %v. Повтор через %v...", i+1, p.maxRetries, err, delay)
		time.Sleep(delay)
	}
	return fmt.Errorf("не удалось подключиться к RabbitMQ после %d попыток: %w", p.maxRetries, err)
}

// backoffDelay — full jitter: sleep = rand(0, min(backoffMaxMs, backoffBaseMs * 2^attempt)).
func (p *RabbitMQPublisher) backoffDelay(attempt int) time.Duration {
	if attempt > 30 {
		attempt = 30 // cap shift to prevent int overflow
	}
	exp := p.backoffBaseMs << uint(attempt)
	if exp > p.backoffMaxMs || exp <= 0 {
		exp = p.backoffMaxMs
	}
	return time.Duration(rand.IntN(exp)) * time.Millisecond
}

// PublishBlockMessage публикует сообщение о блокировке с подтверждением от брокера
// и экспоненциальным backoff с jitter на каждом повторе.
func (p *RabbitMQPublisher) PublishBlockMessage(msg models.BlockMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ошибка сериализации сообщения о блокировке: %w", err)
	}

	for attempt := 0; attempt < p.maxRetries; attempt++ {
		if p.conn == nil || p.conn.IsClosed() {
			log.Println("Соединение с RabbitMQ потеряно. Попытка переподключения...")
			if err := p.connectWithRetry(); err != nil {
				log.Printf("Не удалось восстановить соединение с RabbitMQ: %v", err)
				time.Sleep(p.backoffDelay(attempt))
				continue
			}
		}

		ch := <-p.channelPool

		dc, err := ch.PublishWithDeferredConfirm(
			p.exchangeName,
			"",
			false,
			false,
			amqp091.Publishing{
				ContentType:  "application/json",
				Body:         body,
				DeliveryMode: amqp091.Persistent,
			},
		)
		if err != nil {
			p.channelPool <- ch
			log.Printf("Ошибка публикации сообщения в RabbitMQ (попытка %d/%d): %v", attempt+1, p.maxRetries, err)
			time.Sleep(p.backoffDelay(attempt))
			continue
		}

		// Wait блокируется до подтверждения от брокера; true = ack, false = nack
		if !dc.Wait() {
			p.channelPool <- ch
			log.Printf("Брокер отклонил сообщение (Nack) (попытка %d/%d)", attempt+1, p.maxRetries)
			time.Sleep(p.backoffDelay(attempt))
			continue
		}

		p.channelPool <- ch
		return nil // Сообщение подтверждено брокером
	}

	return fmt.Errorf("критическая ошибка: не удалось опубликовать сообщение в RabbitMQ после %d попыток", p.maxRetries)
}

// Ping проверяет текущее состояние соединения с RabbitMQ без попытки переподключения.
func (p *RabbitMQPublisher) Ping() error {
	if p.conn == nil || p.conn.IsClosed() {
		return errors.New("rabbitmq connection is not active")
	}

	select {
	case ch := <-p.channelPool:
		p.channelPool <- ch
		return nil
	default:
		return errors.New("rabbitmq channel pool is empty")
	}
}

// Close закрывает соединение с RabbitMQ.
func (p *RabbitMQPublisher) Close() error {
	close(p.channelPool)
	for ch := range p.channelPool {
		ch.Close()
	}

	if p.conn != nil && !p.conn.IsClosed() {
		return p.conn.Close()
	}
	return nil
}
