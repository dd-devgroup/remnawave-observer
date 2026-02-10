package firewall

import (
	"blocker-worker/internal/services/command"
	"context"
	"errors"
	"testing"
)

// mockRunner — мок для тестирования без реального выполнения nft команд.
type mockRunner struct {
	lastCommand string
	lastArgs    []string
	err         error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.lastCommand = name
	m.lastArgs = args
	return nil, m.err
}

func TestExecBackend_Name(t *testing.T) {
	backend := NewExecBackend(nil, command.NewExecutor(nil))

	if backend.Name() != "exec" {
		t.Errorf("Expected Name() = 'exec', got '%s'", backend.Name())
	}
}

func TestExecBackend_Add_Success(t *testing.T) {
	mock := &mockRunner{}
	executor := command.NewExecutor(nil)
	executor.SetRunner(mock)

	backend := NewExecBackend(nil, executor)

	err := backend.Add(context.Background(), "192.168.1.1", "5m")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Проверяем что nft команда была вызвана с правильными аргументами
	if mock.lastCommand != "nft" {
		t.Errorf("Expected command 'nft', got '%s'", mock.lastCommand)
	}

	expectedArgs := []string{"add", "element", "inet", "firewall", "user_blacklist", "{ 192.168.1.1 timeout 5m }"}
	if len(mock.lastArgs) != len(expectedArgs) {
		t.Fatalf("Expected %d args, got %d: %v", len(expectedArgs), len(mock.lastArgs), mock.lastArgs)
	}

	for i, arg := range expectedArgs {
		if mock.lastArgs[i] != arg {
			t.Errorf("Arg[%d]: expected '%s', got '%s'", i, arg, mock.lastArgs[i])
		}
	}
}

func TestExecBackend_Add_Error(t *testing.T) {
	mock := &mockRunner{
		err: errors.New("nft command failed"),
	}
	executor := command.NewExecutor(nil)
	executor.SetRunner(mock)

	backend := NewExecBackend(nil, executor)

	err := backend.Add(context.Background(), "10.0.0.1", "1h")
	if err == nil {
		t.Error("Expected error, got nil")
	}
}

func TestExecBackend_Add_IPv6(t *testing.T) {
	mock := &mockRunner{}
	executor := command.NewExecutor(nil)
	executor.SetRunner(mock)

	backend := NewExecBackend(nil, executor)

	err := backend.Add(context.Background(), "2001:db8::1", "30s")
	if err != nil {
		t.Fatalf("Add IPv6 failed: %v", err)
	}

	expectedSetExpr := "{ 2001:db8::1 timeout 30s }"
	if mock.lastArgs[5] != expectedSetExpr {
		t.Errorf("Expected set expression '%s', got '%s'", expectedSetExpr, mock.lastArgs[5])
	}
}

func TestExecBackend_Add_CIDR(t *testing.T) {
	mock := &mockRunner{}
	executor := command.NewExecutor(nil)
	executor.SetRunner(mock)

	backend := NewExecBackend(nil, executor)

	err := backend.Add(context.Background(), "10.0.0.0/8", "2h")
	if err != nil {
		t.Fatalf("Add CIDR failed: %v", err)
	}

	expectedSetExpr := "{ 10.0.0.0/8 timeout 2h }"
	if mock.lastArgs[5] != expectedSetExpr {
		t.Errorf("Expected set expression '%s', got '%s'", expectedSetExpr, mock.lastArgs[5])
	}
}
