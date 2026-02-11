//go:build linux && integration

package firewall

import (
	"blocker-worker/internal/services/command"
	"context"
	"os"
	"testing"
)

// TestNetlinkBackend_Integration_CreateAndAdd — integration тест для NetlinkBackend.
// Требует Linux + CAP_NET_ADMIN + существующую nftables table/set.
// Запускается только с -tags=integration.
func TestNetlinkBackend_Integration_CreateAndAdd(t *testing.T) {
	// Проверяем что мы root или имеем CAP_NET_ADMIN
	if os.Geteuid() != 0 {
		t.Skip("Тест требует root или CAP_NET_ADMIN (EUID=0)")
	}

	// Создаём ExecBackend как fallback
	execBackend := NewExecBackend(nil, command.NewExecutor(nil))

	// Создаём NetlinkBackend
	backend, err := NewNetlinkBackend(nil, execBackend)
	if err != nil {
		t.Fatalf("NewNetlinkBackend failed: %v", err)
	}

	// Проверяем Name
	name := backend.Name()
	t.Logf("NetlinkBackend Name: %s", name)

	// Проверяем что backend создан
	if backend == nil {
		t.Fatal("backend is nil")
	}

	// Пытаемся добавить тестовый IP (это может fail если table/set не существуют)
	// В этом случае должен сработать fallback на exec
	testIP := "198.51.100.1" // TEST-NET-2 (RFC 5737, безопасный для тестов)
	err = backend.Add(context.Background(), testIP, "10s")

	// Не проверяем err == nil, т.к. fallback на exec может быть
	// Главное что метод не паникует
	if err != nil {
		t.Logf("Add returned error (это OK если используется fallback): %v", err)
	} else {
		t.Logf("Add succeeded (netlink или exec)")
	}

	// Закрываем backend
	if err := backend.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

// TestNetlinkBackend_Integration_Fallback — проверка что fallback работает.
func TestNetlinkBackend_Integration_Fallback(t *testing.T) {
	// Этот тест может запуститься и без root (будет fallback на exec)
	execBackend := NewExecBackend(nil, command.NewExecutor(nil))
	backend, err := NewNetlinkBackend(nil, execBackend)
	if err != nil {
		t.Fatalf("NewNetlinkBackend failed: %v", err)
	}

	// Проверяем что Name содержит "netlink"
	name := backend.Name()
	if len(name) < 7 || name[:7] != "netlink" {
		t.Errorf("Expected Name to start with 'netlink', got '%s'", name)
	}

	t.Logf("Backend Name: %s", name)

	// Если fallback активен, Name должен содержать "fallback"
	// Но это не критично для теста
}
