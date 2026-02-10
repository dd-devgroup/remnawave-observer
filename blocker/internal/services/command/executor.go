package command

import (
	"blocker-worker/internal/logger"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CommandRunner — интерфейс для выполнения внешних команд (для мокинга в тестах).
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// realRunner — реальная реализация выполнения команд через exec.CommandContext.
type realRunner struct{}

func (r *realRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// Executor отвечает за выполнение внешних команд.
type Executor struct {
	logger *logger.Logger
	runner CommandRunner
}

// NewExecutor создает новый исполнитель команд.
func NewExecutor(l *logger.Logger) *Executor {
	return &Executor{
		logger: l,
		runner: &realRunner{},
	}
}

// SetRunner заменяет runner (для тестов с моками).
func (e *Executor) SetRunner(r CommandRunner) {
	e.runner = r
}

// BuildSetExpression формирует nftables set expression безопасно.
// Возвращает строку вида: "{ <ip> timeout <duration> }"
// Примечание: ip и duration уже должны быть провалидированы перед вызовом.
func BuildSetExpression(ip, duration string) string {
	// Не используем fmt.Sprintf с untrusted input — только с уже провалидированными значениями
	return fmt.Sprintf("{ %s timeout %s }", ip, duration)
}

// RunNftCommand безопасно выполняет команду nftables через exec.CommandContext.
// Все аргументы передаются как отдельные параметры (не через shell).
func (e *Executor) RunNftCommand(ctx context.Context, args ...string) error {
	output, err := e.runner.Run(ctx, "nft", args...)

	// Формируем строку команды только для логирования (не для выполнения)
	fullCommandStr := "nft " + strings.Join(args, " ")
	if err != nil {
		if e.logger != nil {
			e.logger.Error(fmt.Sprintf("Ошибка выполнения команды '%s': %s", fullCommandStr, string(output)))
		}
		return err
	}

	if e.logger != nil {
		e.logger.Info(fmt.Sprintf("Команда '%s' выполнена успешно.", fullCommandStr))
	}
	return nil
}