package geodata

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultCAIDADownloadURL = "https://data.caida.org/datasets/as-organizations/latest.as-org2info.txt.gz"
	caidaLocalFilename      = "as-org2info.txt.gz"
	caidaHTTPTimeout        = 120 * time.Second
)

// ASInfo маппинг одной строки ASN из CAIDA AS-Organizations.
type ASInfo struct {
	OrgID   string // идентификатор организации (second-level key)
	AutName string // description-поле (имя AS в реестре)
}

// OrgInfo маппинг одной строки организации из CAIDA.
type OrgInfo struct {
	OrgName string // полное имя организации
	Country string // код страны ISO 3166-1 alpha-2
}

// AS2OrgLoader загружает и кэшит CAIDA AS-Organizations dataset.
//
// Файл as-org2info.txt.gz содержит два раздела, разделённых строками «# format:»:
//
//	Org раздел: org_id|changed|org_name|country|source          (5 полей)
//	AS  раздел: aut|changed|aut_name|org_id|opaque_id|source    (6 полей)
//
// Порядок разделов не фиксирован; строки до первого «# format:» игнорируются.
// Все строки начинающиеся с «#» пропускаются (кроме смены режима).
type AS2OrgLoader struct {
	dataDir         string
	downloadURL     string
	refreshInterval time.Duration

	mu          sync.RWMutex
	asnMap      map[int]ASInfo
	orgMap      map[string]OrgInfo
	lastUpdated time.Time
	entryCount  int
}

// NewAS2OrgLoader создаёт загрузчик CAIDA AS2Org.
func NewAS2OrgLoader(dataDir, downloadURL string, refreshInterval time.Duration) *AS2OrgLoader {
	if downloadURL == "" {
		downloadURL = defaultCAIDADownloadURL
	}
	return &AS2OrgLoader{
		dataDir:         dataDir,
		downloadURL:     downloadURL,
		refreshInterval: refreshInterval,
		asnMap:          make(map[int]ASInfo),
		orgMap:          make(map[string]OrgInfo),
	}
}

// InitialLoad загружает данные CAIDA синхронно: из локального кэша или по HTTP.
// Не фатален — при невозможности загрузки возвращает ошибку, но сервис стартует без CAIDA.
func (l *AS2OrgLoader) InitialLoad() error {
	filePath := filepath.Join(l.dataDir, caidaLocalFilename)
	if info, err := os.Stat(filePath); err == nil && time.Since(info.ModTime()) < l.refreshInterval {
		if err := l.loadFromFile(); err == nil {
			return nil
		}
	}
	if err := l.download(context.Background()); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	return l.loadFromFile()
}

// RunRefresh запускает фоновое периодическое обновление. Блокирует горутину до отмены ctx.
func (l *AS2OrgLoader) RunRefresh(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(l.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[AS2Org] Остановка фонового обновления")
			return
		case <-ticker.C:
			log.Println("[AS2Org] Плановое обновление...")
			if err := l.download(ctx); err != nil {
				log.Printf("[AS2Org] Ошибка скачивания: %v", err)
				continue
			}
			if err := l.loadFromFile(); err != nil {
				log.Printf("[AS2Org] Ошибка загрузки файла после скачивания: %v", err)
			}
		}
	}
}

// LookupOrgByASN возвращает информацию об организации по номеру ASN.
// Если запись организации в orgMap отсутствует, orgName = AutName из AS-строки.
func (l *AS2OrgLoader) LookupOrgByASN(asn int) (orgName, country, orgID string, ok bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	entry, exists := l.asnMap[asn]
	if !exists {
		return "", "", "", false
	}

	org, exists := l.orgMap[entry.OrgID]
	if !exists {
		return entry.AutName, "", entry.OrgID, true
	}

	return org.OrgName, org.Country, entry.OrgID, true
}

// Count возвращает количество загруженных записей ASN.
func (l *AS2OrgLoader) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.entryCount
}

// download скачивает файл CAIDA и атомарно записывает в dataDir.
func (l *AS2OrgLoader) download(ctx context.Context) error {
	if err := os.MkdirAll(l.dataDir, 0755); err != nil {
		return fmt.Errorf("mkdirAll dataDir: %w", err)
	}

	client := &http.Client{Timeout: caidaHTTPTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.downloadURL, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	tmpPath := filepath.Join(l.dataDir, caidaLocalFilename+".tmp")
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	f.Close()

	finalPath := filepath.Join(l.dataDir, caidaLocalFilename)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename tmp→final: %w", err)
	}

	log.Printf("[AS2Org] Файл успешно скачан (%s)", finalPath)
	return nil
}

// loadFromFile парсит локальный gzip-файл CAIDA и обновляет maps in-place (copy-on-write под lock).
func (l *AS2OrgLoader) loadFromFile() error {
	filePath := filepath.Join(l.dataDir, caidaLocalFilename)

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	asnMap := make(map[int]ASInfo)
	orgMap := make(map[string]OrgInfo)

	// Режимы парсинга: определяются строками «# format:»
	const (
		modeNone = iota
		modeOrg  // org_id|changed|org_name|country|source
		modeAS   // aut|changed|aut_name|org_id|opaque_id|source
	)
	mode := modeNone

	scanner := bufio.NewScanner(gz)
	// CAIDA файл может содержать очень длинные строки — увеличиваем буфер
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Комментарии: пропускаем, но отслеживаем смену раздела
		if line[0] == '#' {
			if strings.HasPrefix(line, "# format:org_id|") {
				mode = modeOrg
			} else if strings.HasPrefix(line, "# format:aut|") {
				mode = modeAS
			}
			continue
		}

		fields := strings.Split(line, "|")

		switch mode {
		case modeOrg:
			// org_id|changed|org_name|country|source — ровно 5 полей
			if len(fields) != 5 {
				continue
			}
			orgMap[fields[0]] = OrgInfo{
				OrgName: fields[2],
				Country: fields[3],
			}
		case modeAS:
			// aut|changed|aut_name|org_id|opaque_id|source — ровно 6 полей
			if len(fields) != 6 {
				continue
			}
			asnNum, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			asnMap[asnNum] = ASInfo{
				OrgID:   fields[3],
				AutName: fields[2],
			}
		// modeNone: строки до первого «# format:» молча игнорируются
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	l.mu.Lock()
	l.asnMap = asnMap
	l.orgMap = orgMap
	l.entryCount = len(asnMap)
	l.lastUpdated = time.Now()
	l.mu.Unlock()

	log.Printf("[AS2Org] Загружено: %d ASN, %d организаций", len(asnMap), len(orgMap))
	return nil
}
