package processor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// slowExecutor — mock executor, который симулирует долгое выполнение.
type slowExecutor struct {
	delay time.Duration
}

func (s *slowExecutor) RunNftCommand(ctx context.Context, args ...string) error {
	select {
	case <-time.After(s.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestMessageProcessor_NftTimeout(t *testing.T) {
	// Executor с задержкой 2 секунды
	exec := &slowExecutor{delay: 2 * time.Second}
	pool := NewWorkerPool(2, 10, 5*time.Second)
	pool.Start()
	defer pool.Shutdown()

	// Timeout для nft операции — 100ms
	nftTimeout := 100 * time.Millisecond

	// Эта функция должна завершиться быстро (timeout), не дожидаясь 2 секунд
	start := time.Now()

	// Для теста создаем специальную задачу
	var wg sync.WaitGroup
	wg.Add(1)
	task := func(taskCtx context.Context) {
		defer wg.Done()

		nftCtx, cancel := context.WithTimeout(taskCtx, nftTimeout)
		defer cancel()

		err := exec.RunNftCommand(nftCtx, "add", "element")
		if err == nil {
			t.Error("Expected timeout error, got nil")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Expected DeadlineExceeded error, got: %v", err)
		}
	}

	pool.Submit(task)
	wg.Wait()

	elapsed := time.Since(start)

	// Проверяем, что операция завершилась по timeout (~100ms), а не по истечении delay (2s)
	if elapsed > 500*time.Millisecond {
		t.Errorf("Operation took too long: %v (expected ~100ms timeout)", elapsed)
	}
	if elapsed < 90*time.Millisecond {
		t.Errorf("Operation completed too quickly: %v", elapsed)
	}
}

func TestMessageProcessor_NftContextCancellation(t *testing.T) {
	// Executor с задержкой 5 секунд
	exec := &slowExecutor{delay: 5 * time.Second}
	pool := NewWorkerPool(1, 10, 1*time.Second)
	pool.Start()

	nftTimeout := 10 * time.Second // Большой timeout

	var cancelled bool
	var wg sync.WaitGroup
	wg.Add(1)

	task := func(taskCtx context.Context) {
		defer wg.Done()

		nftCtx, cancel := context.WithTimeout(taskCtx, nftTimeout)
		defer cancel()

		err := exec.RunNftCommand(nftCtx, "add", "element")
		if errors.Is(err, context.Canceled) {
			cancelled = true
		}
	}

	pool.Submit(task)

	// Даем задаче начаться
	time.Sleep(50 * time.Millisecond)

	// Останавливаем pool (отменяет контекст)
	pool.Shutdown()

	wg.Wait()

	// Контекст должен был быть отменен
	if !cancelled {
		t.Error("Expected context to be cancelled during shutdown")
	}
}
