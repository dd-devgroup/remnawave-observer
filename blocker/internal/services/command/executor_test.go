package command

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockRunner — мок для тестирования без реального выполнения команд.
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

func TestExecutor_RunNftCommand_Success(t *testing.T) {
	mock := &mockRunner{}
	exec := &Executor{
		logger: nil, // В тестах можем не логировать
		runner: mock,
	}

	err := exec.RunNftCommand(context.Background(), "add", "element", "inet", "firewall", "user_blacklist", "{ 192.168.1.1 timeout 5m }")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if mock.lastCommand != "nft" {
		t.Errorf("Expected command 'nft', got: %s", mock.lastCommand)
	}

	expectedArgs := []string{"add", "element", "inet", "firewall", "user_blacklist", "{ 192.168.1.1 timeout 5m }"}
	if len(mock.lastArgs) != len(expectedArgs) {
		t.Fatalf("Expected %d args, got %d", len(expectedArgs), len(mock.lastArgs))
	}

	for i, arg := range expectedArgs {
		if mock.lastArgs[i] != arg {
			t.Errorf("Arg[%d]: expected %s, got %s", i, arg, mock.lastArgs[i])
		}
	}
}

func TestExecutor_RunNftCommand_Error(t *testing.T) {
	mock := &mockRunner{
		err: errors.New("command failed"),
	}
	exec := &Executor{
		logger: nil,
		runner: mock,
	}

	err := exec.RunNftCommand(context.Background(), "add", "element")
	if err == nil {
		t.Error("Expected error, got nil")
	}
}

// TestExecutor_NoInjection проверяет, что потенциальные injection строки НЕ выполняются как shell команды.
func TestExecutor_NoInjection(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		checkFn  func([]string) bool
		expected string
	}{
		{
			name: "semicolon injection attempt",
			args: []string{"add", "element", "inet", "firewall", "user_blacklist", "{ 1.2.3.4; rm -rf / }"},
			checkFn: func(args []string) bool {
				// Проверяем, что аргумент с ; передается как есть (не интерпретируется shell)
				return len(args) == 6 && strings.Contains(args[5], "; rm -rf /")
			},
			expected: "arg should contain literal semicolon",
		},
		{
			name: "pipe injection attempt",
			args: []string{"add", "element", "inet", "firewall", "user_blacklist", "{ 1.2.3.4 | cat /etc/passwd }"},
			checkFn: func(args []string) bool {
				return len(args) == 6 && strings.Contains(args[5], "| cat")
			},
			expected: "arg should contain literal pipe",
		},
		{
			name: "ampersand injection attempt",
			args: []string{"add", "element", "inet", "firewall", "user_blacklist", "{ 1.2.3.4 && echo pwned }"},
			checkFn: func(args []string) bool {
				return len(args) == 6 && strings.Contains(args[5], "&& echo")
			},
			expected: "arg should contain literal ampersand",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockRunner{}
			exec := &Executor{
				logger: nil,
				runner: mock,
			}

			// Выполняем команду (mock не выполнит реально, просто запишет аргументы)
			_ = exec.RunNftCommand(context.Background(), tt.args...)

			// Проверяем, что аргументы переданы как есть (не интерпретируются shell)
			if !tt.checkFn(mock.lastArgs) {
				t.Errorf("%s: args = %v", tt.expected, mock.lastArgs)
			}
		})
	}
}

func TestBuildSetExpression(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		duration string
		expected string
	}{
		{"ipv4 with timeout", "192.168.1.1", "5m", "{ 192.168.1.1 timeout 5m }"},
		{"ipv6 with timeout", "2001:db8::1", "10s", "{ 2001:db8::1 timeout 10s }"},
		{"cidr with timeout", "10.0.0.0/8", "1h", "{ 10.0.0.0/8 timeout 1h }"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildSetExpression(tt.ip, tt.duration)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}
