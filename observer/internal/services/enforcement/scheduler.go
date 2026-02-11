package enforcement

import (
	"context"
	"log"
	"math/rand/v2"
	"observer_service/internal/metrics"
	"observer_service/internal/services/storage"
	"sync"
	"time"
)

// RemnawaveClient определяет интерфейс для Remnawave operations.
type RemnawaveClient interface {
	EnableUser(ctx context.Context, uuid string) error
}

// Scheduler периодически проверяет просроченные disable и включает пользователей обратно.
type Scheduler struct {
	client         RemnawaveClient
	storage        DisableSchedulerWithPop // расширенный интерфейс с PopDueDisables
	tickInterval   time.Duration
	batchSize      int
	maxBackoffSecs int // максимальный backoff при ошибках (default: 300s = 5 min)
}

// DisableSchedulerWithPop расширяет DisableScheduler методом PopDueDisables.
type DisableSchedulerWithPop interface {
	DisableScheduler
	PopDueDisables(ctx context.Context, nowUnix int64, limit int) ([]int64, error)
}

// NewScheduler создаёт новый re-enable scheduler.
func NewScheduler(client RemnawaveClient, storage DisableSchedulerWithPop, tickInterval time.Duration, batchSize int) *Scheduler {
	return &Scheduler{
		client:         client,
		storage:        storage,
		tickInterval:   tickInterval,
		batchSize:      batchSize,
		maxBackoffSecs: 300, // 5 минут max backoff
	}
}

// Run запускает scheduler loop (блокирует до ctx.Done).
func (s *Scheduler) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	log.Printf("[Scheduler] Started re-enable scheduler (tick: %v, batch: %d)", s.tickInterval, s.batchSize)

	for {
		select {
		case <-ticker.C:
			s.processDueDisables(ctx)
		case <-ctx.Done():
			log.Println("[Scheduler] Stopping re-enable scheduler")
			return
		}
	}
}

// processDueDisables обрабатывает одну итерацию scheduler.
func (s *Scheduler) processDueDisables(ctx context.Context) {
	nowUnix := time.Now().Unix()

	// Атомарно извлекаем просроченные ID из ZSET
	ids, err := s.storage.PopDueDisables(ctx, nowUnix, s.batchSize)
	if err != nil {
		log.Printf("[Scheduler] Error popping due disables: %v", err)
		return
	}

	if len(ids) == 0 {
		return // нечего делать
	}

	log.Printf("[Scheduler] Processing %d due disables", len(ids))

	for _, id := range ids {
		s.processOneEnable(ctx, id, nowUnix)
	}
}

// processOneEnable пытается включить одного пользователя.
func (s *Scheduler) processOneEnable(ctx context.Context, internalID int64, nowUnix int64) {
	// Получаем запись о disable
	rec, ok := s.storage.GetDisableRecord(ctx, internalID)
	if !ok {
		// Запись уже удалена или не существует (race condition), пропускаем
		log.Printf("[Scheduler] No disable record found for ID %d, skipping", internalID)
		return
	}

	// Проверяем что время действительно наступило (на случай clock skew)
	if rec.UntilUnix > nowUnix {
		// Слишком рано, вернём обратно в ZSET
		log.Printf("[Scheduler] ID %d not yet due (until=%d, now=%d), rescheduling", internalID, rec.UntilUnix, nowUnix)
		_ = s.storage.ScheduleDisable(ctx, internalID, rec.UUID, rec.UntilUnix, rec.Reason, rec.Score)
		return
	}

	// Вызываем Remnawave EnableUser
	enableCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := s.client.EnableUser(enableCtx, rec.UUID); err != nil {
		metrics.RemnawaveEnableFail.Add(1)
		log.Printf("[Scheduler] Failed to enable user %d (uuid=%s): %v", internalID, rec.UUID, err)

		// Exponential backoff с jitter: повторим через backoff время
		backoffSecs := s.calculateBackoff(rec)
		retryAt := time.Now().Add(time.Duration(backoffSecs) * time.Second).Unix()
		log.Printf("[Scheduler] Rescheduling ID %d for retry in %ds", internalID, backoffSecs)

		_ = s.storage.ScheduleDisable(ctx, internalID, rec.UUID, retryAt, rec.Reason, rec.Score)
		return
	}

	metrics.RemnawaveEnableOk.Add(1)
	log.Printf("[Scheduler] Successfully enabled user %d (uuid=%s)", internalID, rec.UUID)

	// Удаляем запись о disable
	if err := s.storage.ClearDisableRecord(ctx, internalID); err != nil {
		log.Printf("[Scheduler] Warning: failed to clear disable record for %d: %v", internalID, err)
	}
}

// calculateBackoff вычисляет exponential backoff с jitter и cap.
func (s *Scheduler) calculateBackoff(rec *storage.DisableRecord) int {
	// Простой backoff: 30s → 60s → 120s → 240s → 300s (cap)
	// Можно расширить: хранить retry_count в DisableRecord
	// Пока используем простой fixed backoff с jitter
	baseSecs := 30
	jitter := rand.IntN(20) // 0..19s jitter
	backoff := baseSecs + jitter

	if backoff > s.maxBackoffSecs {
		backoff = s.maxBackoffSecs
	}

	return backoff
}
