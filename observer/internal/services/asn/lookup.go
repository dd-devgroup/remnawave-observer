package asn

import (
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/oschwald/maxminddb-golang"
)

// ASNRecord структура записи в базе MaxMind GeoLite2 ASN
type ASNRecord struct {
	AutonomousSystemNumber       uint   `maxminddb:"autonomous_system_number"`
	AutonomousSystemOrganization string `maxminddb:"autonomous_system_organization"`
}

// ASNLookup сервис для определения ASN по IP-адресу
type ASNLookup struct {
	db     *maxminddb.Reader
	mu     sync.RWMutex
	dbPath string
}

// NewASNLookup создает новый экземпляр сервиса ASN lookup
func NewASNLookup(dbPath string) (*ASNLookup, error) {
	db, err := maxminddb.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть ASN базу %s: %w", dbPath, err)
	}

	log.Printf("✅ ASN база успешно загружена: %s", dbPath)
	return &ASNLookup{db: db, dbPath: dbPath}, nil
}

// Lookup возвращает ASN и имя организации для IP-адреса
func (a *ASNLookup) Lookup(ipStr string) (uint, string, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return 0, "", fmt.Errorf("невалидный IP: %s", ipStr)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	var record ASNRecord
	err := a.db.Lookup(ip, &record)
	if err != nil {
		return 0, "", fmt.Errorf("ошибка lookup для %s: %w", ipStr, err)
	}

	if record.AutonomousSystemNumber == 0 {
		return 0, "", fmt.Errorf("ASN не найден для %s", ipStr)
	}

	return record.AutonomousSystemNumber, record.AutonomousSystemOrganization, nil
}

// LookupString возвращает строковый идентификатор ASN (например "AS12389")
func (a *ASNLookup) LookupString(ipStr string) (string, error) {
	asn, _, err := a.Lookup(ipStr)
	if err != nil {
		return "", err
	}
	if asn == 0 {
		return "", fmt.Errorf("ASN не найден для %s", ipStr)
	}
	return fmt.Sprintf("AS%d", asn), nil
}

// LookupWithOrg возвращает строковый идентификатор ASN с названием организации
func (a *ASNLookup) LookupWithOrg(ipStr string) (string, string, error) {
	asn, org, err := a.Lookup(ipStr)
	if err != nil {
		return "", "", err
	}
	if asn == 0 {
		return "", "", fmt.Errorf("ASN не найден для %s", ipStr)
	}
	return fmt.Sprintf("AS%d", asn), org, nil
}

// Reload перезагружает базу данных (для hot-reload)
func (a *ASNLookup) Reload() error {
	newDB, err := maxminddb.Open(a.dbPath)
	if err != nil {
		return fmt.Errorf("ошибка перезагрузки ASN базы: %w", err)
	}

	a.mu.Lock()
	oldDB := a.db
	a.db = newDB
	a.mu.Unlock()

	if oldDB != nil {
		oldDB.Close()
	}

	log.Println("ASN база успешно перезагружена")
	return nil
}

// Close закрывает базу данных
func (a *ASNLookup) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.db != nil {
		return a.db.Close()
	}
	return nil
}
