package processor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerPool_BasicExecution(t *testing.T) {
	pool := NewWorkerPool(4, 10, 5*time.Second)
	pool.Start()
	defer pool.Shutdown()

	var counter atomic.Int32
	var wg sync.WaitGroup

	// Отправляем 10 задач
	for i := 0; i < 10; i++ {
		wg.Add(1)
		pool.Submit(func(ctx context.Context) {
			counter.Add(1)
			wg.Done()
		})
	}

	wg.Wait()

	if counter.Load() != 10 {
		t.Errorf("Expected 10 tasks executed, got %d", counter.Load())
	}
}

func TestWorkerPool_Concurrency(t *testing.T) {
	workers := 4
	pool := NewWorkerPool(workers, 100, 5*time.Second)
	pool.Start()
	defer pool.Shutdown()

	var activeWorkers atomic.Int32
	var maxConcurrent atomic.Int32
	var wg sync.WaitGroup

	// Отправляем 20 задач, каждая "работает" 10ms
	for i := 0; i < 20; i++ {
		wg.Add(1)
		pool.Submit(func(ctx context.Context) {
			defer wg.Done()

			active := activeWorkers.Add(1)
			// Обновляем максимум конкурентных воркеров
			for {
				max := maxConcurrent.Load()
				if active <= max || maxConcurrent.CompareAndSwap(max, active) {
					break
				}
			}

			time.Sleep(10 * time.Millisecond)
			activeWorkers.Add(-1)
		})
	}

	wg.Wait()

	// Максимум конкурентных воркеров не должен превышать размер пула
	if maxConcurrent.Load() > int32(workers) {
		t.Errorf("Expected max concurrent workers <= %d, got %d", workers, maxConcurrent.Load())
	}

	// Должно быть хотя бы 2+ воркеров работало одновременно
	if maxConcurrent.Load() < 2 {
		t.Errorf("Expected some concurrency, got max %d", maxConcurrent.Load())
	}
}

func TestWorkerPool_QueueFull(t *testing.T) {
	queueSize := 5
	pool := NewWorkerPool(1, queueSize, 5*time.Second)
	pool.Start()
	defer pool.Shutdown()

	// Заполняем очередь задачами, которые блокируются
	blocker := make(chan struct{})
	for i := 0; i < queueSize+1; i++ { // +1 — одна задача выполняется воркером
		pool.Submit(func(ctx context.Context) {
			<-blocker
		})
	}

	// Очередь должна быть заполнена
	if pool.QueueLen() != queueSize {
		t.Errorf("Expected queue full (%d), got %d", queueSize, pool.QueueLen())
	}

	// TrySubmit должен вернуть false (очередь заполнена)
	submitted := pool.TrySubmit(func(ctx context.Context) {})
	if submitted {
		t.Error("Expected TrySubmit to fail when queue is full")
	}

	// Разблокируем задачи
	close(blocker)
	time.Sleep(50 * time.Millisecond)
}

func TestWorkerPool_GracefulShutdown(t *testing.T) {
	pool := NewWorkerPool(2, 10, 500*time.Millisecond)
	pool.Start()

	var completed atomic.Int32

	// Отправляем 4 задачи (2 воркера * 2 волны)
	for i := 0; i < 4; i++ {
		pool.Submit(func(ctx context.Context) {
			time.Sleep(50 * time.Millisecond)
			completed.Add(1)
		})
	}

	// Ждем, чтобы задачи начали выполняться
	time.Sleep(20 * time.Millisecond)

	// Shutdown — должен дождаться завершения задач
	start := time.Now()
	pool.Shutdown()
	elapsed := time.Since(start)

	// Все задачи должны быть выполнены (Shutdown ждет их завершения)
	if completed.Load() != 4 {
		t.Errorf("Expected 4 tasks completed, got %d", completed.Load())
	}

	// Shutdown должен был дождаться хотя бы 50ms (время выполнения задач)
	if elapsed < 50*time.Millisecond {
		t.Errorf("Shutdown returned too quickly: %v", elapsed)
	}

	// Но не дольше 200ms (50ms * 2 волны + overhead)
	if elapsed > 200*time.Millisecond {
		t.Errorf("Shutdown took too long: %v", elapsed)
	}
}

func TestWorkerPool_ShutdownTimeout(t *testing.T) {
	pool := NewWorkerPool(1, 10, 100*time.Millisecond)
	pool.Start()

	// Используем канал чтобы убедиться, что задача началась
	started := make(chan struct{})
	pool.Submit(func(ctx context.Context) {
		close(started)
		// Игнорируем ctx, спим долго
		time.Sleep(5 * time.Second)
	})

	// Ждем, пока задача начнет выполняться
	<-started

	// Shutdown — должен прерваться по timeout (100ms + 100ms grace)
	start := time.Now()
	pool.Shutdown()
	elapsed := time.Since(start)

	// Shutdown должен вернуться примерно через drainTimeout + grace (200-300ms max)
	if elapsed > 400*time.Millisecond {
		t.Errorf("Shutdown took too long: %v (expected ~200-300ms)", elapsed)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("Shutdown returned too quickly: %v", elapsed)
	}
}

func TestWorkerPool_SubmitAfterShutdown(t *testing.T) {
	pool := NewWorkerPool(2, 10, 1*time.Second)
	pool.Start()
	pool.Shutdown()

	// Submit после shutdown должен вернуть false
	submitted := pool.Submit(func(ctx context.Context) {})
	if submitted {
		t.Error("Expected Submit to fail after shutdown")
	}
}

func TestWorkerPool_ContextCancellation(t *testing.T) {
	pool := NewWorkerPool(2, 10, 50*time.Millisecond)
	pool.Start()

	var cancelled atomic.Bool
	var wg sync.WaitGroup

	wg.Add(1)
	pool.Submit(func(ctx context.Context) {
		defer wg.Done()
		select {
		case <-time.After(5 * time.Second):
			// Задача не должна завершиться по timeout
		case <-ctx.Done():
			// Контекст отменен — это ожидаемое поведение при shutdown
			cancelled.Store(true)
		}
	})

	// Даем задаче начаться
	time.Sleep(20 * time.Millisecond)

	// Вызываем shutdown — контекст должен быть отменен сразу
	pool.Shutdown()

	wg.Wait()

	// Контекст должен был быть отменен
	if !cancelled.Load() {
		t.Error("Expected task context to be cancelled during shutdown")
	}
}
