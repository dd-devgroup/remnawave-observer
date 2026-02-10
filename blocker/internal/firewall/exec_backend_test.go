package firewall

import (
	"blocker-worker/internal/services/command"
	"testing"
)

func TestExecBackend_Name(t *testing.T) {
	backend := NewExecBackend(nil, command.NewExecutor(nil))

	if backend.Name() != "exec" {
		t.Errorf("Expected Name() = 'exec', got '%s'", backend.Name())
	}
}

// Остальные тесты (Add с моками) будут добавлены в N4 после рефакторинга Executor для тестируемости.
