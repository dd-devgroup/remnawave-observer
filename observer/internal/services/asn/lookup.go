package asn

import (
	"context"
	"fmt"
	"log"
	"time"
)

// ASNLookup сервис для определения ASN по IP-адресу
// Использует базу данных iptoasn.com с автоматическим обновлением
type ASNLookup struct {
	db      *IPtoASNDatabase
	updater *ASNUpdater
}

// NewASNLookup создает новый экземпляр сервиса ASN lookup
// downloadURL - URL для скачивания базы (пустая строка = default)
// updateInterval - интервал обновления (0 = default 1 час)
func NewASNLookup(downloadURL string, updateInterval time.Duration) (*ASNLookup, error) {
	db := NewIPtoASNDatabase()
	updater := NewASNUpdater(db, downloadURL, updateInterval)

	// Запускаем загрузку базы и фоновое обновление
	ctx := context.Background()
	if err := updater.Start(ctx); err != nil {
		return nil, fmt.Errorf("не удалось инициализировать ASN lookup: %w", err)
	}

	log.Printf("ASN lookup service initialized (source: iptoasn.com)")
	return &ASNLookup{
		db:      db,
		updater: updater,
	}, nil
}

// Lookup returns ASN and organization name for IP address
func (a *ASNLookup) Lookup(ipStr string) (uint, string, error) {
	asn, org, err := a.db.Lookup(ipStr)
	if err != nil {
		return 0, "", err
	}
	return uint(asn), org, nil
}

// LookupString возвращает строковый идентификатор ASN (например "AS12389")
func (a *ASNLookup) LookupString(ipStr string) (string, error) {
	return a.db.LookupString(ipStr)
}

// LookupWithOrg возвращает строковый идентификатор ASN с названием организации
func (a *ASNLookup) LookupWithOrg(ipStr string) (string, string, error) {
	return a.db.LookupWithOrg(ipStr)
}

// LookupFull возвращает полную информацию: ASN (string), CountryCode, Organization
func (a *ASNLookup) LookupFull(ipStr string) (string, string, string, error) {
	asn, countryCode, org, err := a.db.LookupFull(ipStr)
	if err != nil {
		return "", "", "", err
	}
	return fmt.Sprintf("AS%d", asn), countryCode, org, nil
}

// Reload перезагружает базу данных (принудительное обновление)
func (a *ASNLookup) Reload() error {
	return a.updater.ForceReload()
}

// Close stops background update
func (a *ASNLookup) Close() error {
	a.updater.Stop()
	log.Println("ASN lookup service stopped")
	return nil
}

// Count возвращает количество записей в базе данных
func (a *ASNLookup) Count() int {
	return a.db.Count()
}

// IsLoaded проверяет загружена ли база данных
func (a *ASNLookup) IsLoaded() bool {
	return a.db.IsLoaded()
}
