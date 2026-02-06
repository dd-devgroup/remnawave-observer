package publisher

import (
	"fmt"
	"testing"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

// --- Тесты для новой архитектуры confirms (Commit O) ---

func TestRegisterWaiter_AckReceived(t *testing.T) {
	chWrap := &channelWithReturns{
		confirmWaiters: make(map[uint64]*confirmWaiter),
		stopRouter:     make(chan struct{}),
	}

	waiter := chWrap.registerWaiter(1, 1*time.Second)

	// Simulate confirmRouter sending ack
	go func() {
		time.Sleep(10 * time.Millisecond)
		waiter.timer.Stop()
		waiter.result <- true
		close(waiter.result)
	}()

	ack, ok := <-waiter.result
	if !ok {
		t.Fatal("result channel closed unexpectedly")
	}
	if !ack {
		t.Fatal("expected ack=true")
	}
}

func TestRegisterWaiter_Timeout(t *testing.T) {
	chWrap := &channelWithReturns{
		confirmWaiters: make(map[uint64]*confirmWaiter),
		stopRouter:     make(chan struct{}),
	}

	waiter := chWrap.registerWaiter(2, 50*time.Millisecond)

	// Не отправляем confirm — таймер должен сработать
	ack, ok := <-waiter.result
	if !ok {
		t.Fatal("result channel should not be closed on timeout")
	}
	if ack {
		t.Fatal("expected ack=false on timeout")
	}

	// Убедимся что waiter удалён из map
	chWrap.waitersMutex.Lock()
	if _, exists := chWrap.confirmWaiters[2]; exists {
		t.Fatal("waiter should be removed from map on timeout")
	}
	chWrap.waitersMutex.Unlock()
}

func TestConfirmRouter_AckDelivery(t *testing.T) {
	confirmsChan := make(chan amqp091.Confirmation, 10)
	chWrap := &channelWithReturns{
		confirmsChan:   confirmsChan,
		confirmWaiters: make(map[uint64]*confirmWaiter),
		stopRouter:     make(chan struct{}),
	}

	go chWrap.confirmRouter()

	// Регистрируем waiter
	waiter := chWrap.registerWaiter(5, 1*time.Second)

	// Отправляем confirm в канал
	confirmsChan <- amqp091.Confirmation{DeliveryTag: 5, Ack: true}

	// Ждём результат
	ack, ok := <-waiter.result
	if !ok {
		t.Fatal("result channel closed unexpectedly")
	}
	if !ack {
		t.Fatal("expected ack=true")
	}

	// Проверяем что waiter удалён из map
	chWrap.waitersMutex.Lock()
	if _, exists := chWrap.confirmWaiters[5]; exists {
		t.Fatal("waiter should be removed from map after delivery")
	}
	chWrap.waitersMutex.Unlock()

	// Останавливаем router
	close(chWrap.stopRouter)
	time.Sleep(10 * time.Millisecond) // Даём время на graceful shutdown
}

func TestConfirmRouter_StopRouter(t *testing.T) {
	confirmsChan := make(chan amqp091.Confirmation, 10)
	chWrap := &channelWithReturns{
		confirmsChan:   confirmsChan,
		confirmWaiters: make(map[uint64]*confirmWaiter),
		stopRouter:     make(chan struct{}),
	}

	// Регистрируем несколько waiters
	w1 := chWrap.registerWaiter(10, 5*time.Second)
	w2 := chWrap.registerWaiter(11, 5*time.Second)

	go chWrap.confirmRouter()

	// Останавливаем router
	close(chWrap.stopRouter)

	// Все waiters должны получить закрытый канал
	_, ok1 := <-w1.result
	_, ok2 := <-w2.result
	if ok1 || ok2 {
		t.Fatal("waiter result channels should be closed when router stops")
	}

	// Map должна быть очищена
	chWrap.waitersMutex.Lock()
	if chWrap.confirmWaiters != nil && len(chWrap.confirmWaiters) > 0 {
		t.Fatalf("confirmWaiters should be cleared, got %d entries", len(chWrap.confirmWaiters))
	}
	chWrap.waitersMutex.Unlock()
}

// --- isChannelError tests (Commit B) ---

func TestIsChannelError_AMQPClosedError(t *testing.T) {
	if !isChannelError(amqp091.ErrClosed) {
		t.Error("amqp091.ErrClosed must be detected as channel error")
	}
}

func TestIsChannelError_WrappedAMQPError(t *testing.T) {
	base := &amqp091.Error{Code: 504, Reason: "channel/connection is not open"}
	err := fmt.Errorf("publish failed: %w", base)
	if !isChannelError(err) {
		t.Error("wrapped *amqp091.Error must be detected as channel error")
	}
}

func TestIsChannelError_GenericError(t *testing.T) {
	if isChannelError(fmt.Errorf("network timeout")) {
		t.Error("generic error must NOT be channel error")
	}
}

func TestIsChannelError_Nil(t *testing.T) {
	if isChannelError(nil) {
		t.Error("nil must NOT be channel error")
	}
}

// --- generateMessageID tests (Commit N) ---

func TestGenerateMessageID(t *testing.T) {
	id1 := generateMessageID()
	id2 := generateMessageID()
	if id1 == id2 {
		t.Fatal("generateMessageID must return unique IDs")
	}
	if len(id1) != 32 { // 16 bytes hex = 32 chars
		t.Fatalf("expected 32 char hex string, got %d chars", len(id1))
	}
}

func TestChannelWithReturns_Creation(t *testing.T) {
	// Smoke test: убедимся что структура корректна
	returns := make(chan amqp091.Return, 1)
	chWrap := &channelWithReturns{
		ch:      nil, // mock, не важно для этого теста
		returns: returns,
	}
	if chWrap.returns == nil {
		t.Fatal("returns channel must be initialized")
	}
}
