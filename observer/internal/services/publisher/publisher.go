package publisher

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"observer_service/internal/metrics"
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
	backoffMaxMs     int
	confirmTimeoutMs int
}

// NewRabbitMQPublisher создает и настраивает нового издателя RabbitMQ.
// poolSize — количество каналов в пуле.
// backoffBaseMs / backoffMaxMs — параметры экспоненциального backoff с full jitter.
func NewRabbitMQPublisher(url, exchangeName string, poolSize, maxRetries, backoffBaseMs, backoffMaxMs, confirmTimeoutMs int) (*RabbitMQPublisher, error) {
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
	if confirmTimeoutMs <= 0 {
		confirmTimeoutMs = 3000
	}

	p := &RabbitMQPublisher{
		url:              url,
		exchangeName:     exchangeName,
		poolSize:         poolSize,
		channelPool:      make(chan *amqp091.Channel, poolSize),
		maxRetries:       maxRetries,
		backoffBaseMs:    backoffBaseMs,
		backoffMaxMs:     backoffMaxMs,
		confirmTimeoutMs: confirmTimeoutMs,
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

// deferredConfirm абстрагирует Wait() у *amqp091.DeferredConfirmation,
// позволяя подставлять фейки в unit-тестах.
type deferredConfirm interface {
	Wait() bool
}

// waitWithTimeout ожидает подтверждения от брокера с дедлайном.
// При таймауте фоновая горутина с dc.Wait() продолжит ждать до ответа
// брокера или закрытия соединения — утечка ограничена maxRetries × poolSize.
func waitWithTimeout(dc deferredConfirm, timeout time.Duration) (ack bool, timedOut bool) {
	done := make(chan bool, 1)
	go func() {
		done <- dc.Wait()
	}()
	select {
	case ack = <-done:
		return ack, false
	case <-time.After(timeout):
		return false, true
	}
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
			if isChannelError(err) {
				ch.Close()
				log.Printf("Канал в пуле закрыт, удалён из пула (попытка %d/%d). Попытка замещения...", attempt+1, p.maxRetries)
				if !p.replaceChannel() {
					log.Println("Замещение не удалось, инициируя переподключение...")
					if reconnErr := p.connectWithRetry(); reconnErr != nil {
						log.Printf("Не удалось переподключиться: %v", reconnErr)
					}
				}
			} else {
				p.channelPool <- ch
			}
			log.Printf("Ошибка публикации сообщения в RabbitMQ (попытка %d/%d): %v", attempt+1, p.maxRetries, err)
			metrics.RabbitPublishRetry.Add(1)
			time.Sleep(p.backoffDelay(attempt))
			continue
		}

		ack, timedOut := waitWithTimeout(dc, time.Duration(p.confirmTimeoutMs)*time.Millisecond)
		if timedOut {
			p.channelPool <- ch
			log.Printf("Таймаут подтверждения от брокера (%dms) (попытка %d/%d)", p.confirmTimeoutMs, attempt+1, p.maxRetries)
			metrics.RabbitPublishRetry.Add(1)
			time.Sleep(p.backoffDelay(attempt))
			continue
		}
		if !ack {
			p.channelPool <- ch
			log.Printf("Брокер отклонил сообщение (Nack) (попытка %d/%d)", attempt+1, p.maxRetries)
			metrics.RabbitPublishRetry.Add(1)
			time.Sleep(p.backoffDelay(attempt))
			continue
		}

		p.channelPool <- ch
		metrics.RabbitPublishSuccess.Add(1)
		return nil // Сообщение подтверждено брокером
	}

	metrics.RabbitPublishFail.Add(1)
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

// isChannelError возвращает true, если ошибка указывает на закрытый / невалидный канал AMQP.
func isChannelError(err error) bool {
	if err == nil {
		return false
	}
	var amqpErr *amqp091.Error
	return errors.As(err, &amqpErr)
}

// replaceChannel создаёт новый канал на текущем соединении и кладёт его в пул.
// Возвращает true при успехе; при отсутствии соединения — false.
func (p *RabbitMQPublisher) replaceChannel() bool {
	if p.conn == nil || p.conn.IsClosed() {
		return false
	}
	ch, err := p.conn.Channel()
	if err != nil {
		log.Printf("Не удалось создать замещающий канал: %v", err)
		return false
	}
	if err := ch.ExchangeDeclare(p.exchangeName, "fanout", true, false, false, false, nil); err != nil {
		ch.Close()
		log.Printf("Не удалось объявить exchange на замещающем канале: %v", err)
		return false
	}
	if err := ch.Confirm(false); err != nil {
		ch.Close()
		log.Printf("Не удалось включить confirm mode на замещающем канале: %v", err)
		return false
	}
	p.channelPool <- ch
	log.Println("Замещающий канал успешно добавлен в пул.")
	return true
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
