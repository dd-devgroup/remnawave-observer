package firewall

import (
	"blocker-worker/internal/services/command"
	"testing"
)

func TestNetlinkBackend_Name(t *testing.T) {
	// На не-Linux платформах должен быть fallback
	fallbackExec := NewExecBackend(nil, command.NewExecutor(nil))
	backend, err := NewNetlinkBackend(nil, fallbackExec)
	if err != nil {
		t.Fatalf("NewNetlinkBackend failed: %v", err)
	}

	name := backend.Name()
	// На Linux: "netlink" или "netlink(fallback:exec)" если не удалось инициализировать
	// На не-Linux: "netlink(not-supported:fallback-exec)"
	if name == "" {
		t.Errorf("Name() returned empty string")
	}

	// Проверяем что Name содержит "netlink"
	if len(name) < 7 || name[:7] != "netlink" {
		t.Errorf("Expected Name() to start with 'netlink', got '%s'", name)
	}
}

func TestNetlinkBackend_Close(t *testing.T) {
	fallbackExec := NewExecBackend(nil, command.NewExecutor(nil))
	backend, err := NewNetlinkBackend(nil, fallbackExec)
	if err != nil {
		t.Fatalf("NewNetlinkBackend failed: %v", err)
	}

	// Close не должен возвращать ошибку
	if err := backend.Close(); err != nil {
		t.Errorf("Close() returned error: %v", err)
	}
}

// Полноценные тесты Add будут в N4 (с моками) и N5 (integration на Linux).
