package publisher

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"observer_service/internal/models"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

// EventPublisher определяет интерфейс для публикации событий.
type EventPublisher interface {
	PublishBlockMessage(ips []string, duration string) error
	Close() error
	Ping() error
}

// RabbitMQPublisher реализует EventPublisher для RabbitMQ.
type RabbitMQPublisher struct {
	conn         *amqp091.Connection
	channelPool  chan *amqp091.Channel
	exchangeName string
	url          string
	poolSize     int
	maxRetries   int
	retryDelay   time.Duration
}

// NewRabbitMQPublisher создает и настраивает нового издателя RabbitMQ.
func NewRabbitMQPublisher(url, exchangeName string) (*RabbitMQPublisher, error) {
	poolSize := 5 // Константа: пул из 5 каналов

	p := &RabbitMQPublisher{
		url:          url,
		exchangeName: exchangeName,
		poolSize:     poolSize,
		channelPool:  make(chan *amqp091.Channel, poolSize),
		maxRetries:   5,
		retryDelay:   2 * time.Second,
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

	// Создаём пул каналов
	for i := 0; i < p.poolSize; i++ {
		ch, err := conn.Channel()
		if err != nil {
			conn.Close()
			// Закрыть уже созданные каналы
			close(p.channelPool)
			for oldCh := range p.channelPool {
				oldCh.Close()
			}
			return fmt.Errorf("ошибка создания канала %d: %w", i, err)
		}

		err = ch.ExchangeDeclare(
			p.exchangeName,
			"fanout",
			true,
			false,
			false,
			false,
			nil,
		)
		if err != nil {
			ch.Close()
			conn.Close()
			// Закрыть уже созданные каналы
			close(p.channelPool)
			for oldCh := range p.channelPool {
				oldCh.Close()
			}
			return fmt.Errorf("ошибка создания exchange: %w", err)
		}

		p.channelPool <- ch
	}

	p.conn = conn
	log.Printf("Успешное (пере)подключение к RabbitMQ. Создан пул из %d каналов.", p.poolSize)
	return nil
}

func (p *RabbitMQPublisher) connectWithRetry() error {
	var err error
	for i := 0; i < p.maxRetries; i++ {
		err = p.connect()
		if err == nil {
			return nil
		}
		log.Printf("Не удалось подключиться к RabbitMQ (попытка %d/%d): %v. Повтор через %v...", i+1, p.maxRetries, err, p.retryDelay)
		time.Sleep(p.retryDelay)
	}
	return fmt.Errorf("не удалось подключиться к RabbitMQ после %d попыток: %w", p.maxRetries, err)
}

// getChannel получает канал из пула
func (p *RabbitMQPublisher) getChannel() *amqp091.Channel {
	return <-p.channelPool
}

// returnChannel возвращает канал обратно в пул
func (p *RabbitMQPublisher) returnChannel(ch *amqp091.Channel) {
	p.channelPool <- ch
}

// PublishBlockMessage публикует сообщение о блокировке с логикой переподключения.
func (p *RabbitMQPublisher) PublishBlockMessage(ips []string, duration string) error {
	blockMsg := models.BlockMessage{
		IPs:      ips,
		Duration: duration,
	}
	body, err := json.Marshal(blockMsg)
	if err != nil {
		return fmt.Errorf("ошибка сериализации сообщения о блокировке: %w", err)
	}

	for i := 0; i < p.maxRetries; i++ {
		if p.conn == nil || p.conn.IsClosed() {
			log.Println("Соединение с RabbitMQ потеряно. Попытка переподключения...")
			if err := p.connectWithRetry(); err != nil {
				log.Printf("Не удалось восстановить соединение с RabbitMQ: %v", err)
				time.Sleep(p.retryDelay)
				continue
			}
		}

		// Берём канал из пула (без mutex!)
		ch := p.getChannel()

		err = ch.Publish(
			p.exchangeName,
			"",
			false,
			false,
			amqp091.Publishing{
				ContentType:  "application/json",
				Body:         body,
				DeliveryMode: amqp091.Transient, // Изменено на Transient!
			},
		)

		// Возвращаем канал в пул
		p.returnChannel(ch)

		if err == nil {
			return nil // Успех
		}

		log.Printf("Ошибка публикации сообщения в RabbitMQ (попытка %d/%d): %v. Повтор...", i+1, p.maxRetries, err)
		time.Sleep(p.retryDelay)
	}

	return fmt.Errorf("критическая ошибка: не удалось опубликовать сообщение в RabbitMQ после %d попыток", p.maxRetries)
}

// Ping проверяет текущее состояние соединения с RabbitMQ без попытки переподключения.
func (p *RabbitMQPublisher) Ping() error {
	if p.conn == nil || p.conn.IsClosed() {
		return errors.New("rabbitmq connection is not active")
	}

	// Проверяем что хотя бы один канал доступен
	select {
	case ch := <-p.channelPool:
		p.channelPool <- ch // Возвращаем обратно
		return nil
	default:
		return errors.New("rabbitmq channel pool is empty")
	}
}

// Close закрывает соединение с RabbitMQ.
func (p *RabbitMQPublisher) Close() error {
	// Закрываем все каналы в пуле
	close(p.channelPool)
	for ch := range p.channelPool {
		ch.Close()
	}

	if p.conn != nil && !p.conn.IsClosed() {
		return p.conn.Close()
	}
	return nil
}