//go:build linux

package firewall

import (
	"blocker-worker/internal/logger"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/nftables"
)

// NetlinkBackend — реализация FirewallBackend через netlink API.
// Использует github.com/google/nftables для прямого взаимодействия с kernel netfilter.
type NetlinkBackend struct {
	logger       *logger.Logger
	conn         *nftables.Conn
	table        *nftables.Table
	set          *nftables.Set
	fallbackExec *ExecBackend // Fallback на exec если netlink недоступен
}

// NewNetlinkBackend создаёт новый NetlinkBackend.
// fallbackExec используется если netlink не поддерживается или недоступен.
func NewNetlinkBackend(l *logger.Logger, fallbackExec *ExecBackend) (*NetlinkBackend, error) {
	conn, err := nftables.New()
	if err != nil {
		if l != nil {
			l.Warning(fmt.Sprintf("Не удалось создать netlink соединение: %v. Используется fallback на exec.", err))
		}
		// Возвращаем структуру с fallback, но без conn
		return &NetlinkBackend{
			logger:       l,
			conn:         nil,
			fallbackExec: fallbackExec,
		}, nil
	}

	backend := &NetlinkBackend{
		logger:       l,
		conn:         conn,
		fallbackExec: fallbackExec,
	}

	// Инициализируем table и set (или находим существующие)
	if err := backend.ensureTableAndSet(); err != nil {
		if l != nil {
			l.Warning(fmt.Sprintf("Не удалось инициализировать nftables table/set: %v. Используется fallback на exec.", err))
		}
		// Закрываем conn и переключаемся на fallback
		backend.conn = nil
		return backend, nil
	}

	return backend, nil
}

// ensureTableAndSet создаёт или находит существующую table и set.
func (n *NetlinkBackend) ensureTableAndSet() error {
	// 1. Найти или создать table "firewall" в inet family
	tables, err := n.conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return fmt.Errorf("failed to list tables: %w", err)
	}

	var foundTable *nftables.Table
	for _, t := range tables {
		if t.Name == "firewall" {
			foundTable = t
			break
		}
	}

	if foundTable == nil {
		// Table не найдена — создаём (но в продакшене она должна уже существовать)
		// Для безопасности логируем предупреждение
		if n.logger != nil {
			n.logger.Warning("Table 'inet firewall' не найдена. Netlink backend требует существующую table.")
		}
		// Не создаём table автоматически — это может быть опасно
		return fmt.Errorf("table 'inet firewall' не найдена, требуется ручная настройка")
	}

	n.table = foundTable

	// 2. Найти set "user_blacklist"
	sets, err := n.conn.GetSets(n.table)
	if err != nil {
		return fmt.Errorf("failed to list sets: %w", err)
	}

	var foundSet *nftables.Set
	for _, s := range sets {
		if s.Name == "user_blacklist" {
			foundSet = s
			break
		}
	}

	if foundSet == nil {
		// Set не найден — логируем предупреждение
		if n.logger != nil {
			n.logger.Warning("Set 'user_blacklist' не найден в table 'inet firewall'. Netlink backend требует существующий set.")
		}
		return fmt.Errorf("set 'user_blacklist' не найден, требуется ручная настройка")
	}

	n.set = foundSet
	return nil
}

// Add добавляет IP/CIDR в nftables set через netlink API с timeout.
func (n *NetlinkBackend) Add(ctx context.Context, ipOrPrefix, timeout string) error {
	// Если conn не инициализирован (fallback режим), используем exec
	if n.conn == nil {
		if n.logger != nil {
			n.logger.Info(fmt.Sprintf("Netlink недоступен для %s, используется exec fallback", ipOrPrefix))
		}
		return n.fallbackExec.Add(ctx, ipOrPrefix, timeout)
	}

	// Для CIDR используем fallback на exec (упрощение для MVP)
	// Причина: nftables set может требовать тип ipv4_prefix вместо ipv4_addr для CIDR
	if strings.Contains(ipOrPrefix, "/") {
		if n.logger != nil {
			n.logger.Info(fmt.Sprintf("CIDR %s требует exec fallback (MVP ограничение)", ipOrPrefix))
		}
		return n.fallbackExec.Add(ctx, ipOrPrefix, timeout)
	}

	// Парсим timeout в time.Duration (формат nftables: "5m", "1h", "30s")
	duration, err := parseNftTimeout(timeout)
	if err != nil {
		return fmt.Errorf("invalid timeout '%s': %w", timeout, err)
	}

	// Парсим IP (не CIDR)
	key, err := parseIP(ipOrPrefix)
	if err != nil {
		return fmt.Errorf("invalid IP '%s': %w", ipOrPrefix, err)
	}

	// Добавляем элемент в set с timeout
	elem := nftables.SetElement{
		Key:     key,
		Timeout: duration,
	}

	if err := n.conn.SetAddElements(n.set, []nftables.SetElement{elem}); err != nil {
		return fmt.Errorf("netlink SetAddElements failed: %w", err)
	}

	// Применяем изменения
	if err := n.conn.Flush(); err != nil {
		return fmt.Errorf("netlink Flush failed: %w", err)
	}

	return nil
}

// Name возвращает имя backend'а.
func (n *NetlinkBackend) Name() string {
	if n.conn == nil {
		return "netlink(fallback:exec)"
	}
	return "netlink"
}

// parseNftTimeout парсит nftables timeout формат ("5m", "1h", "30s", "2d") в time.Duration.
func parseNftTimeout(timeout string) (time.Duration, error) {
	if timeout == "" {
		return 0, fmt.Errorf("empty timeout")
	}

	// nftables формат: <number><unit>
	// units: s (seconds), m (minutes), h (hours), d (days)
	unit := timeout[len(timeout)-1]
	valueStr := timeout[:len(timeout)-1]

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout value: %w", err)
	}

	switch unit {
	case 's':
		return time.Duration(value) * time.Second, nil
	case 'm':
		return time.Duration(value) * time.Minute, nil
	case 'h':
		return time.Duration(value) * time.Hour, nil
	case 'd':
		return time.Duration(value) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown timeout unit '%c'", unit)
	}
}

// parseIP парсит IP адрес (не CIDR) в []byte для nftables key.
func parseIP(ipAddr string) ([]byte, error) {
	ip := net.ParseIP(ipAddr)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP address")
	}

	// Определяем IPv4 или IPv6
	if ip4 := ip.To4(); ip4 != nil {
		// IPv4: 4 байта
		return []byte(ip4), nil
	}

	// IPv6: 16 байт
	return []byte(ip.To16()), nil
}

// Close закрывает netlink соединение (вызывается при shutdown).
func (n *NetlinkBackend) Close() error {
	// nftables.Conn не требует явного закрытия (использует netlink сокет через OS)
	// Но можем обнулить ссылку для GC
	n.conn = nil
	return nil
}
