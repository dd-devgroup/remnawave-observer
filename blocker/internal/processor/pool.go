package processor

import (
	"context"
	"sync"
	"time"
)

// Task представляет задачу для выполнения в worker pool.
type Task func(ctx context.Context)

// WorkerPool управляет пулом воркеров для выполнения задач.
type WorkerPool struct {
	workers      int
	taskQueue    chan Task
	wg           sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	drainTimeout time.Duration
}

// NewWorkerPool создает новый worker pool.
func NewWorkerPool(workers int, queueSize int, drainTimeout time.Duration) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkerPool{
		workers:      workers,
		taskQueue:    make(chan Task, queueSize),
		ctx:          ctx,
		cancel:       cancel,
		drainTimeout: drainTimeout,
	}
}

// Start запускает воркеры в пуле.
func (p *WorkerPool) Start() {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
}

// worker — горутина, которая забирает задачи из очереди и выполняет их.
func (p *WorkerPool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case task, ok := <-p.taskQueue:
			if !ok {
				return
			}
			// Выполняем задачу с контекстом pool (для возможности отмены)
			task(p.ctx)
		}
	}
}

// Submit отправляет задачу в очередь. Блокирует, если очередь заполнена.
// Возвращает false, если pool остановлен или канал закрыт.
func (p *WorkerPool) Submit(task Task) bool {
	// Используем defer recover для защиты от panic при отправке в закрытый канал
	defer func() {
		recover() // Игнорируем panic "send on closed channel"
	}()

	select {
	case <-p.ctx.Done():
		return false
	case p.taskQueue <- task:
		return true
	}
}

// TrySubmit пытается отправить задачу без блокировки.
// Возвращает false, если очередь заполнена, pool остановлен или канал закрыт.
func (p *WorkerPool) TrySubmit(task Task) bool {
	// Используем defer recover для защиты от panic при отправке в закрытый канал
	defer func() {
		recover() // Игнорируем panic "send on closed channel"
	}()

	select {
	case <-p.ctx.Done():
		return false
	case p.taskQueue <- task:
		return true
	default:
		return false
	}
}

// Shutdown корректно останавливает worker pool.
// 1. Закрывает очередь (новые Submit вернут false, воркеры завершатся после обработки)
// 2. Ждет завершения текущих задач с timeout
// 3. Если timeout — отменяет контекст для force stop
func (p *WorkerPool) Shutdown() {
	// Закрываем очередь — новые Submit вернут false, воркеры завершатся после обработки существующих задач
	close(p.taskQueue)

	// Создаем канал для ожидания с timeout
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	// Ждем завершения с timeout
	select {
	case <-done:
		// Все задачи выполнены успешно
		p.cancel() // Отменяем контекст для очистки
	case <-time.After(p.drainTimeout):
		// Timeout — воркеры не успели завершиться
		// Отменяем контекст для force stop
		p.cancel()
		// Ждем еще немного (100ms) для окончательного завершения
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			// Force exit — воркеры зависли
		}
	}
}

// QueueLen возвращает текущую длину очереди (для мониторинга).
func (p *WorkerPool) QueueLen() int {
	return len(p.taskQueue)
}

// QueueCap возвращает емкость очереди.
func (p *WorkerPool) QueueCap() int {
	return cap(p.taskQueue)
}
