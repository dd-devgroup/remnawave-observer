package enforcement

import (
	"context"
	"fmt"
	"log"
	"observer_service/internal/metrics"
	"observer_service/internal/services/remnawave"
	"observer_service/internal/services/storage"
	"time"
)

// DisableScheduler определяет интерфейс для Redis scheduling операций.
type DisableScheduler interface {
	GetUserUUIDCache(ctx context.Context, internalID int64) (string, bool)
	SetUserUUIDCache(ctx context.Context, internalID int64, uuid string, ttl time.Duration) error
	ScheduleDisable(ctx context.Context, internalID int64, uuid string, untilUnix int64, reason string, score int) error
	GetDisableRecord(ctx context.Context, internalID int64) (*storage.DisableRecord, bool)
	ClearDisableRecord(ctx context.Context, internalID int64) error
}

// RemnawaveEnforcer реализует Enforcer через Remnawave API + Redis scheduling.
type RemnawaveEnforcer struct {
	client  *remnawave.Client
	storage DisableScheduler
}

// NewRemnawaveEnforcer создаёт новый enforcer с Remnawave client и Redis storage.
func NewRemnawaveEnforcer(client *remnawave.Client, storage DisableScheduler) Enforcer {
	return &RemnawaveEnforcer{
		client:  client,
		storage: storage,
	}
}

// DisableTempByInternalID отключает пользователя на заданное время.
// 1. Резолвит UUID по internal ID (cache hit → ok, miss → API call)
// 2. Проверяет идемпотентность: если уже есть активный disable с >= until, skip
// 3. Вызывает Remnawave DisableUser(uuid)
// 4. Планирует auto-enable через Redis scheduler
func (e *RemnawaveEnforcer) DisableTempByInternalID(ctx context.Context, internalID int64, duration time.Duration, reason string, score int) error {
	// Шаг 1: Резолв UUID (с кэшированием)
	uuid, err := e.resolveUUIDWithCache(ctx, internalID)
	if err != nil {
		metrics.RemnawaveDisableFail.Add(1)
		return fmt.Errorf("resolve uuid for id=%d: %w", internalID, err)
	}

	untilUnix := time.Now().Add(duration).Unix()

	// Шаг 2: Идемпотентность (проверяем существующий disable)
	if rec, ok := e.storage.GetDisableRecord(ctx, internalID); ok {
		if rec.UntilUnix >= untilUnix {
			// Уже отключён на >= текущий срок, пропускаем
			log.Printf("[Enforcer] User %d (uuid=%s) already disabled until %d (>= %d), skipping",
				internalID, uuid, rec.UntilUnix, untilUnix)
			return nil
		}
		// Если нужно продлить: обновим запись ниже после Disable call
	}

	// Шаг 3: Вызываем Remnawave DisableUser
	if err := e.client.DisableUser(ctx, uuid); err != nil {
		metrics.RemnawaveDisableFail.Add(1)
		return fmt.Errorf("remnawave disable user %s: %w", uuid, err)
	}

	metrics.RemnawaveDisableOk.Add(1)
	log.Printf("[Enforcer] Disabled user %d (uuid=%s) for %v, reason: %s, score: %d",
		internalID, uuid, duration, reason, score)

	// Шаг 4: Планируем auto-enable
	if err := e.storage.ScheduleDisable(ctx, internalID, uuid, untilUnix, reason, score); err != nil {
		// Не критично: пользователь уже отключён, но auto-enable может не сработать
		log.Printf("[Enforcer] Warning: failed to schedule auto-enable for %d: %v", internalID, err)
	}

	return nil
}

// Ping проверяет доступность Remnawave API.
func (e *RemnawaveEnforcer) Ping(ctx context.Context) error {
	return e.client.Ping(ctx)
}

// resolveUUIDWithCache пытается получить UUID из Redis cache, при промахе запрашивает API.
func (e *RemnawaveEnforcer) resolveUUIDWithCache(ctx context.Context, internalID int64) (string, error) {
	// Cache hit?
	if uuid, ok := e.storage.GetUserUUIDCache(ctx, internalID); ok {
		metrics.UUIDCacheHit.Add(1)
		return uuid, nil
	}

	metrics.UUIDCacheMiss.Add(1)

	// Cache miss: запрашиваем API
	uuid, err := e.client.ResolveUUIDByInternalID(ctx, internalID)
	if err != nil {
		return "", err
	}

	// Сохраняем в кэш (TTL настроен в client при создании)
	// Используем 24h как fallback (реальный TTL из config будет передан при создании client)
	_ = e.storage.SetUserUUIDCache(ctx, internalID, uuid, 24*time.Hour)

	return uuid, nil
}
