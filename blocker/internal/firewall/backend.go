package firewall

import (
	"context"
)

// FirewallBackend — интерфейс для различных backend'ов управления nftables.
// Позволяет абстрагироваться от конкретной реализации (exec vs netlink).
type FirewallBackend interface {
	// Add добавляет IP-адрес или CIDR в blacklist set с указанным timeout.
	// ipOrPrefix должен быть уже провалидирован (валидный IPv4/IPv6 или CIDR).
	// timeout должен быть в формате nftables: "5m", "1h", "30s", и т.д.
	Add(ctx context.Context, ipOrPrefix, timeout string) error

	// Name возвращает имя backend'а для логирования.
	Name() string
}
