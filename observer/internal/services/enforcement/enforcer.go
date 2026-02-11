package enforcement

import (
	"context"
	"time"
)

// Enforcer определяет интерфейс для enforcement действий (disable/enable пользователей).
type Enforcer interface {
	// DisableTempByInternalID отключает пользователя на заданное время по internal numeric ID.
	// internalID: внутренний числовой идентификатор пользователя (из LogEntry.UserEmail)
	// duration: длительность отключения
	// reason: причина отключения (для логов/метрик)
	// score: скор нарушения (для логов/метрик)
	DisableTempByInternalID(ctx context.Context, internalID int64, duration time.Duration, reason string, score int) error

	// Ping проверяет доступность enforcement backend (для health check).
	Ping(ctx context.Context) error
}

// noopEnforcer — временная заглушка для компиляции (будет заменена RemnawaveEnforcer).
type noopEnforcer struct{}

// NewNoopEnforcer создаёт заглушку enforcer (для тестов/миграции).
func NewNoopEnforcer() Enforcer {
	return &noopEnforcer{}
}

func (e *noopEnforcer) DisableTempByInternalID(ctx context.Context, internalID int64, duration time.Duration, reason string, score int) error {
	// noop: успешно "отключили" (ничего не делаем)
	return nil
}

func (e *noopEnforcer) Ping(ctx context.Context) error {
	// noop: всегда успешен
	return nil
}
