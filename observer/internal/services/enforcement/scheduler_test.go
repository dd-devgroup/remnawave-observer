package enforcement

import (
	"context"
	"observer_service/internal/services/remnawave"
	"observer_service/internal/services/storage"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSchedulerStore расширяет mockRedisStore методом PopDueDisables.
type mockSchedulerStore struct {
	mockRedisStore
	mu              sync.Mutex
	zset            map[int64]int64 // id -> untilUnix
	popCalls        int
	scheduledIDs    []int64
}

func (m *mockSchedulerStore) PopDueDisables(ctx context.Context, nowUnix int64, limit int) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.popCalls++

	var due []int64
	for id, until := range m.zset {
		if until <= nowUnix {
			due = append(due, id)
			if len(due) >= limit {
				break
			}
		}
	}

	// Удаляем из zset (атомарный pop)
	for _, id := range due {
		delete(m.zset, id)
	}

	return due, nil
}

func (m *mockSchedulerStore) ScheduleDisable(ctx context.Context, internalID int64, uuid string, untilUnix int64, reason string, score int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Вызываем базовый метод для disableRecords
	m.mockRedisStore.disableRecords[internalID] = &storage.DisableRecord{
		InternalID: internalID,
		UUID:       uuid,
		UntilUnix:  untilUnix,
		Reason:     reason,
		Score:      score,
	}

	// Добавляем в ZSET
	if m.zset == nil {
		m.zset = make(map[int64]int64)
	}
	m.zset[internalID] = untilUnix
	m.scheduledIDs = append(m.scheduledIDs, internalID)

	return nil
}

// capturingClient захватывает вызовы EnableUser для проверки в тестах.
type capturingClient struct {
	*remnawave.Client
	enabledUUIDs []string
	mu           sync.Mutex
}

func (c *capturingClient) EnableUser(ctx context.Context, uuid string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabledUUIDs = append(c.enabledUUIDs, uuid)
	return nil
}

func TestScheduler_ProcessDueDisables_Success(t *testing.T) {
	store := &mockSchedulerStore{
		mockRedisStore: mockRedisStore{
			uuidCache:      make(map[int64]string),
			disableRecords: make(map[int64]*storage.DisableRecord),
		},
		zset: make(map[int64]int64),
	}

	// Подготовка: добавляем 2 просроченных disable
	nowUnix := time.Now().Unix()
	store.ScheduleDisable(context.Background(), 100, "uuid-100", nowUnix-10, "test1", 80)
	store.ScheduleDisable(context.Background(), 200, "uuid-200", nowUnix-5, "test2", 90)

	// Capturing client
	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:63792"})
	baseClient := remnawave.NewClient("http://fake", "token", 5, 24, redisClient)
	capClient := &capturingClient{Client: baseClient}

	scheduler := NewScheduler(capClient, store, 1*time.Second, 100)

	// Обрабатываем одну итерацию
	scheduler.processDueDisables(context.Background())

	// Проверяем что PopDueDisables был вызван
	assert.Equal(t, 1, store.popCalls)

	// Проверяем что EnableUser был вызван для обоих пользователей
	assert.Equal(t, 2, len(capClient.enabledUUIDs))
	assert.Contains(t, capClient.enabledUUIDs, "uuid-100")
	assert.Contains(t, capClient.enabledUUIDs, "uuid-200")

	// Проверяем что записи о disable удалены
	_, ok1 := store.GetDisableRecord(context.Background(), 100)
	_, ok2 := store.GetDisableRecord(context.Background(), 200)
	assert.False(t, ok1, "record 100 should be cleared")
	assert.False(t, ok2, "record 200 should be cleared")
}

func TestScheduler_ProcessDueDisables_NotYetDue(t *testing.T) {
	store := &mockSchedulerStore{
		mockRedisStore: mockRedisStore{
			uuidCache:      make(map[int64]string),
			disableRecords: make(map[int64]*storage.DisableRecord),
		},
		zset: make(map[int64]int64),
	}

	// Добавляем disable который ещё не наступил (в будущем)
	nowUnix := time.Now().Unix()
	futureUnix := nowUnix + 3600 // через час
	store.ScheduleDisable(context.Background(), 300, "uuid-300", futureUnix, "future", 75)

	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:63793"})
	baseClient := remnawave.NewClient("http://fake", "token", 5, 24, redisClient)
	capClient := &capturingClient{Client: baseClient}

	scheduler := NewScheduler(capClient, store, 1*time.Second, 100)

	scheduler.processDueDisables(context.Background())

	// EnableUser НЕ должен был вызваться
	assert.Equal(t, 0, len(capClient.enabledUUIDs))

	// Запись должна остаться
	rec, ok := store.GetDisableRecord(context.Background(), 300)
	require.True(t, ok)
	assert.Equal(t, futureUnix, rec.UntilUnix)
}

func TestScheduler_ProcessDueDisables_NoRecords(t *testing.T) {
	store := &mockSchedulerStore{
		mockRedisStore: mockRedisStore{
			uuidCache:      make(map[int64]string),
			disableRecords: make(map[int64]*storage.DisableRecord),
		},
		zset: make(map[int64]int64),
	}

	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:63794"})
	baseClient := remnawave.NewClient("http://fake", "token", 5, 24, redisClient)
	capClient := &capturingClient{Client: baseClient}

	scheduler := NewScheduler(capClient, store, 1*time.Second, 100)

	scheduler.processDueDisables(context.Background())

	// PopDueDisables был вызван, но вернул пустой список
	assert.Equal(t, 1, store.popCalls)

	// EnableUser не должен был вызваться
	assert.Equal(t, 0, len(capClient.enabledUUIDs))
}

func TestScheduler_Run_ContextCancellation(t *testing.T) {
	store := &mockSchedulerStore{
		mockRedisStore: mockRedisStore{
			uuidCache:      make(map[int64]string),
			disableRecords: make(map[int64]*storage.DisableRecord),
		},
		zset: make(map[int64]int64),
	}

	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:63795"})
	baseClient := remnawave.NewClient("http://fake", "token", 5, 24, redisClient)

	scheduler := NewScheduler(baseClient, store, 100*time.Millisecond, 10)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Add(1)
	go scheduler.Run(ctx, &wg)

	// Даём scheduler поработать немного
	time.Sleep(50 * time.Millisecond)

	// Отменяем контекст
	cancel()

	// Ждём завершения (с таймаутом чтобы не зависнуть)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK: scheduler завершился
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after context cancellation")
	}
}
