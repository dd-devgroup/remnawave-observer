//go:build !linux

package firewall

import (
	"blocker-worker/internal/logger"
	"context"
)

// NetlinkBackend — заглушка для не-Linux платформ.
// Всегда использует fallback на ExecBackend.
type NetlinkBackend struct {
	logger       *logger.Logger
	fallbackExec *ExecBackend
}

// NewNetlinkBackend создаёт NetlinkBackend с fallback на exec (не-Linux платформы).
func NewNetlinkBackend(l *logger.Logger, fallbackExec *ExecBackend) (*NetlinkBackend, error) {
	if l != nil {
		l.Warning("Netlink backend недоступен на не-Linux платформе. Используется fallback на exec.")
	}
	return &NetlinkBackend{
		logger:       l,
		fallbackExec: fallbackExec,
	}, nil
}

// Add всегда использует fallback на exec.
func (n *NetlinkBackend) Add(ctx context.Context, ipOrPrefix, timeout string) error {
	return n.fallbackExec.Add(ctx, ipOrPrefix, timeout)
}

// Name возвращает имя backend'а.
func (n *NetlinkBackend) Name() string {
	return "netlink(not-supported:fallback-exec)"
}

// Close — заглушка.
func (n *NetlinkBackend) Close() error {
	return nil
}
