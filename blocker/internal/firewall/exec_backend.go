package firewall

import (
	"blocker-worker/internal/logger"
	"blocker-worker/internal/services/command"
	"context"
	"fmt"
)

// ExecBackend — реализация FirewallBackend через exec nft команды.
// Использует существующий command.Executor для выполнения команд.
type ExecBackend struct {
	logger   *logger.Logger
	executor *command.Executor
}

// NewExecBackend создаёт новый ExecBackend.
func NewExecBackend(l *logger.Logger, exec *command.Executor) *ExecBackend {
	return &ExecBackend{
		logger:   l,
		executor: exec,
	}
}

// Add добавляет IP/CIDR в nftables set через exec команду.
func (e *ExecBackend) Add(ctx context.Context, ipOrPrefix, timeout string) error {
	// Формируем set expression (безопасная функция из command package)
	setExpr := command.BuildSetExpression(ipOrPrefix, timeout)

	// Выполняем nft add element команду
	err := e.executor.RunNftCommand(ctx, "add", "element", "inet", "firewall", "user_blacklist", setExpr)
	if err != nil {
		return fmt.Errorf("exec backend failed: %w", err)
	}

	return nil
}

// Name возвращает имя backend'а.
func (e *ExecBackend) Name() string {
	return "exec"
}
