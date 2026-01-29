package asn

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ASNEntry представляет одну запись из базы iptoasn.com
type ASNEntry struct {
	RangeStart  uint32
	RangeEnd    uint32
	ASN         uint32
	CountryCode string
	Description string
}

// IPtoASNDatabase хранит базу данных ASN в памяти
type IPtoASNDatabase struct {
	entries []ASNEntry
	mu      sync.RWMutex
}

// NewIPtoASNDatabase создает новый экземпляр базы данных
func NewIPtoASNDatabase() *IPtoASNDatabase {
	return &IPtoASNDatabase{
		entries: make([]ASNEntry, 0),
	}
}

// ipToUint32 конвертирует IPv4 адрес в uint32
func ipToUint32(ipStr string) (uint32, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return 0, fmt.Errorf("невалидный IP адрес: %s", ipStr)
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return 0, fmt.Errorf("не IPv4 адрес: %s", ipStr)
	}

	return binary.BigEndian.Uint32(ip4), nil
}

// parseIPToUint32 парсит IP адрес из строки базы данных
func parseIPToUint32(ipStr string) (uint32, error) {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return 0, fmt.Errorf("невалидный IP: %s", ipStr)
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return 0, fmt.Errorf("не IPv4: %s", ipStr)
	}

	return binary.BigEndian.Uint32(ip4), nil
}

// Lookup ищет ASN для указанного IP адреса используя binary search
func (db *IPtoASNDatabase) Lookup(ipStr string) (uint32, string, error) {
	ipNum, err := ipToUint32(ipStr)
	if err != nil {
		return 0, "", err
	}

	db.mu.RLock()
	defer db.mu.RUnlock()

	if len(db.entries) == 0 {
		return 0, "", fmt.Errorf("база данных пуста")
	}

	// Binary search: ищем первую запись где RangeEnd >= ipNum
	idx := sort.Search(len(db.entries), func(i int) bool {
		return db.entries[i].RangeEnd >= ipNum
	})

	// Проверяем что IP попадает в найденный диапазон
	if idx < len(db.entries) && db.entries[idx].RangeStart <= ipNum {
		entry := db.entries[idx]
		if entry.ASN == 0 {
			return 0, "", fmt.Errorf("IP %s не маршрутизируется (Not routed)", ipStr)
		}
		return entry.ASN, entry.Description, nil
	}

	return 0, "", fmt.Errorf("ASN не найден для %s", ipStr)
}

// LookupString возвращает ASN в формате "AS12345"
func (db *IPtoASNDatabase) LookupString(ipStr string) (string, error) {
	asn, _, err := db.Lookup(ipStr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("AS%d", asn), nil
}

// LookupWithOrg возвращает ASN в формате "AS12345" и название организации
func (db *IPtoASNDatabase) LookupWithOrg(ipStr string) (string, string, error) {
	asn, org, err := db.Lookup(ipStr)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("AS%d", asn), org, nil
}

// LoadFromReader загружает базу данных из io.Reader (TSV формат)
func (db *IPtoASNDatabase) LoadFromReader(reader io.Reader) error {
	var entries []ASNEntry

	scanner := bufio.NewScanner(reader)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Пропускаем пустые строки
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Парсим TSV: range_start \t range_end \t AS_number \t country_code \t AS_description
		parts := strings.Split(line, "\t")
		if len(parts) < 5 {
			continue // Пропускаем некорректные строки
		}

		rangeStart, err := parseIPToUint32(parts[0])
		if err != nil {
			continue // Пропускаем строки с невалидными IP
		}

		rangeEnd, err := parseIPToUint32(parts[1])
		if err != nil {
			continue
		}

		asn, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 32)
		if err != nil {
			continue
		}

		entry := ASNEntry{
			RangeStart:  rangeStart,
			RangeEnd:    rangeEnd,
			ASN:         uint32(asn),
			CountryCode: strings.TrimSpace(parts[3]),
			Description: strings.TrimSpace(parts[4]),
		}

		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("ошибка чтения базы данных: %w", err)
	}

	if len(entries) == 0 {
		return fmt.Errorf("база данных пуста или некорректна")
	}

	// Сортируем по RangeStart для binary search
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RangeStart < entries[j].RangeStart
	})

	// Atomic swap
	db.mu.Lock()
	db.entries = entries
	db.mu.Unlock()

	return nil
}

// Count возвращает количество записей в базе
func (db *IPtoASNDatabase) Count() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.entries)
}

// IsLoaded проверяет загружена ли база данных
func (db *IPtoASNDatabase) IsLoaded() bool {
	return db.Count() > 0
}
