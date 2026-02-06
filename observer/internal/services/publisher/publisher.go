package publisher

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	mathrand "math/rand/v2"
	"observer_service/internal/metrics"
	"observer_service/internal/models"
	"sync"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

// EventPublisher определяет интерфейс для публикации событий.
type EventPublisher interface {
	PublishBlockMessage(msg models.BlockMessage) error
	Close() error
	Ping() error
}

// confirmWaiter — ожидатель подтверждения для одной публикации.
type confirmWaiter struct {
	result chan bool // true=ack, false=nack
	timer  *time.Timer
}

// channelWithReturns оборачивает AMQP канал с returns, confirms reader и картой waiters.
type channelWithReturns struct {
	ch           *amqp091.Channel
	returns      chan amqp091.Return
	confirmsChan chan amqp091.Confirmation
	// confirmWaiters хранит ожидателей подтверждений по sequence number
	confirmWaiters map[uint64]*confirmWaiter
	// waitersMutex защищает confirmWaiters map
	waitersMutex   sync.Mutex
	// stopRouter закрывает reader goroutine
	stopRouter     chan struct{}
}

// RabbitMQPublisher реализует EventPublisher для RabbitMQ с confirm mode и
// экспоненциальным backoff с full jitter на повторах.
type RabbitMQPublisher struct {
	conn          *amqp091.Connection
	channelPool   chan *channelWithReturns
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
		channelPool:      make(chan *channelWithReturns, poolSize),
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

// createChannelWithReturns создаёт и настраивает канал с return + confirm listeners.
func (p *RabbitMQPublisher) createChannelWithReturns(conn *amqp091.Connection) (*channelWithReturns, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}

	if err := ch.ExchangeDeclare(p.exchangeName, "fanout", true, false, false, false, nil); err != nil {
		ch.Close()
		return nil, err
	}

	if err := ch.Confirm(false); err != nil {
		ch.Close()
		return nil, err
	}

	// Подписываемся на returns (mandatory publish)
	returns := make(chan amqp091.Return, 1)
	ch.NotifyReturn(returns)

	// Подписываемся на confirms (buffered для предотвращения deadlock)
	confirmsChan := make(chan amqp091.Confirmation, 100)
	ch.NotifyPublish(confirmsChan)

	chWrap := &channelWithReturns{
		ch:             ch,
		returns:        returns,
		confirmsChan:   confirmsChan,
		confirmWaiters: make(map[uint64]*confirmWaiter),
		stopRouter:     make(chan struct{}),
	}

	// Запускаем confirmRouter goroutine для раздачи подтверждений
	go chWrap.confirmRouter()

	return chWrap, nil
}

// confirmRouter читает подтверждения и раздаёт их ожидателям.
func (chWrap *channelWithReturns) confirmRouter() {
	for {
		select {
		case <-chWrap.stopRouter:
			// Закрытие канала: завершить всех ожидателей с ошибкой
			chWrap.waitersMutex.Lock()
			for _, w := range chWrap.confirmWaiters {
				w.timer.Stop()
				close(w.result) // закрытие без значения = признак ошибки
			}
			chWrap.confirmWaiters = nil
			chWrap.waitersMutex.Unlock()
			return
		case conf, ok := <-chWrap.confirmsChan:
			if !ok {
				// Канал confirmsChan закрыт (соединение оборвано)
				chWrap.waitersMutex.Lock()
				for _, w := range chWrap.confirmWaiters {
					w.timer.Stop()
					close(w.result)
				}
				chWrap.confirmWaiters = nil
				chWrap.waitersMutex.Unlock()
				return
			}

			// Раздаём подтверждение соответствующему waiter'у
			chWrap.waitersMutex.Lock()
			w, exists := chWrap.confirmWaiters[conf.DeliveryTag]
			if exists {
				delete(chWrap.confirmWaiters, conf.DeliveryTag)
				w.timer.Stop()
				w.result <- conf.Ack
				close(w.result)
			}
			chWrap.waitersMutex.Unlock()
		}
	}
}

// closeRouter останавливает confirmRouter goroutine.
func (chWrap *channelWithReturns) closeRouter() {
	close(chWrap.stopRouter)
}

func (p *RabbitMQPublisher) connect() error {
	conn, err := amqp091.Dial(p.url)
	if err != nil {
		return fmt.Errorf("ошибка подключения к RabbitMQ: %w", err)
	}

	pool := make(chan *channelWithReturns, p.poolSize)
	for i := 0; i < p.poolSize; i++ {
		chWrap, err := p.createChannelWithReturns(conn)
		if err != nil {
			conn.Close()
			close(pool)
			for old := range pool {
				old.ch.Close()
			}
			return fmt.Errorf("ошибка создания канала %d: %w", i, err)
		}
		pool <- chWrap
	}

	p.channelPool = pool
	p.conn = conn
	log.Printf("Успешное (пере)подключение к RabbitMQ. Создан пул из %d каналов с confirm mode + returns.", p.poolSize)
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
	return time.Duration(mathrand.IntN(exp)) * time.Millisecond
}

// generateMessageID создаёт уникальный идентификатор сообщения для tracking returns.
func generateMessageID() string {
	buf := make([]byte, 16)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

// registerWaiter регистрирует waiter для ожидания confirm по seqNo с таймаутом.
func (chWrap *channelWithReturns) registerWaiter(seqNo uint64, timeout time.Duration) *confirmWaiter {
	w := &confirmWaiter{
		result: make(chan bool, 1),
	}

	chWrap.waitersMutex.Lock()
	chWrap.confirmWaiters[seqNo] = w
	chWrap.waitersMutex.Unlock()

	// Таймер удаляет waiter если confirm не пришёл вовремя
	w.timer = time.AfterFunc(timeout, func() {
		chWrap.waitersMutex.Lock()
		delete(chWrap.confirmWaiters, seqNo)
		chWrap.waitersMutex.Unlock()
		// Отправляем признак таймаута (не закрываем канал, а шлём false)
		select {
		case w.result <- false:
		default:
		}
	})

	return w
}

// PublishBlockMessage публикует сообщение о блокировке с подтверждением от брокера,
// mandatory publish (no-route detection) и экспоненциальным backoff с jitter на каждом повторе.
// Использует seqNo-based confirms без горутины-на-публикацию.
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

		chWrap := <-p.channelPool
		messageID := generateMessageID()

		// Получаем seqNo ПЕРЕД публикацией (в confirm mode это sequence number следующего сообщения)
		seqNo := chWrap.ch.GetNextPublishSeqNo()

		// Регистрируем waiter для ожидания confirm с таймаутом
		waiter := chWrap.registerWaiter(seqNo, time.Duration(p.confirmTimeoutMs)*time.Millisecond)

		// Публикуем с mandatory=true для отслеживания unroutable сообщений
		err := chWrap.ch.Publish(
			p.exchangeName,
			"",
			true, // mandatory — брокер вернёт сообщение если нет binding
			false,
			amqp091.Publishing{
				ContentType:  "application/json",
				Body:         body,
				DeliveryMode: amqp091.Persistent,
				MessageId:    messageID,
			},
		)
		if err != nil {
			// Отменяем waiter при ошибке публикации
			waiter.timer.Stop()
			chWrap.waitersMutex.Lock()
			delete(chWrap.confirmWaiters, seqNo)
			chWrap.waitersMutex.Unlock()

			if isChannelError(err) {
				chWrap.closeRouter()
				chWrap.ch.Close()
				log.Printf("Канал в пуле закрыт, удалён из пула (попытка %d/%d). Попытка замещения...", attempt+1, p.maxRetries)
				if !p.replaceChannel() {
					log.Println("Замещение не удалось, инициируя переподключение...")
					if reconnErr := p.connectWithRetry(); reconnErr != nil {
						log.Printf("Не удалось переподключиться: %v", reconnErr)
					}
				}
			} else {
				p.channelPool <- chWrap
			}
			log.Printf("Ошибка публикации сообщения в RabbitMQ (попытка %d/%d): %v", attempt+1, p.maxRetries, err)
			metrics.RabbitPublishRetry.Add(1)
			time.Sleep(p.backoffDelay(attempt))
			continue
		}

		// Проверка возврата (unroutable message) с коротким таймаутом
		select {
		case ret := <-chWrap.returns:
			waiter.timer.Stop()
			chWrap.waitersMutex.Lock()
			delete(chWrap.confirmWaiters, seqNo)
			chWrap.waitersMutex.Unlock()
			p.channelPool <- chWrap
			log.Printf("Сообщение возвращено брокером (unroutable): MessageId=%s ReplyCode=%d ReplyText=%s (попытка %d/%d)",
				ret.MessageId, ret.ReplyCode, ret.ReplyText, attempt+1, p.maxRetries)
			metrics.RabbitPublishRetry.Add(1)
			time.Sleep(p.backoffDelay(attempt))
			continue
		case <-time.After(200 * time.Millisecond):
			// Нет return — сообщение успешно роутится, переходим к ожиданию confirm
		}

		// Ожидаем confirm через waiter.result
		ack, ok := <-waiter.result
		if !ok {
			// Канал закрыт = соединение оборвано (confirmRouter закрыт)
			p.channelPool <- chWrap
			log.Printf("Соединение с RabbitMQ потеряно во время ожидания подтверждения (попытка %d/%d)", attempt+1, p.maxRetries)
			metrics.RabbitPublishRetry.Add(1)
			time.Sleep(p.backoffDelay(attempt))
			continue
		}
		if !ack {
			// Nack или таймаут (таймер AfterFunc отправил false)
			p.channelPool <- chWrap
			log.Printf("Брокер отклонил сообщение или таймаут (%dms) (попытка %d/%d)", p.confirmTimeoutMs, attempt+1, p.maxRetries)
			metrics.RabbitPublishRetry.Add(1)
			time.Sleep(p.backoffDelay(attempt))
			continue
		}

		// Успех: ack=true, сообщение подтверждено
		p.channelPool <- chWrap
		metrics.RabbitPublishSuccess.Add(1)
		return nil
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
	chWrap, err := p.createChannelWithReturns(p.conn)
	if err != nil {
		log.Printf("Не удалось создать замещающий канал: %v", err)
		return false
	}
	p.channelPool <- chWrap
	log.Println("Замещающий канал успешно добавлен в пул.")
	return true
}

// Close закрывает соединение с RabbitMQ.
func (p *RabbitMQPublisher) Close() error {
	close(p.channelPool)
	for chWrap := range p.channelPool {
		chWrap.closeRouter()
		chWrap.ch.Close()
	}

	if p.conn != nil && !p.conn.IsClosed() {
		return p.conn.Close()
	}
	return nil
}
