package asn

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultDownloadURL - URL по умолчанию для скачивания базы iptoasn.com
	DefaultDownloadURL = "https://iptoasn.com/data/ip2asn-v4.tsv.gz"

	// DefaultUpdateInterval - интервал обновления по умолчанию
	DefaultUpdateInterval = 1 * time.Hour

	// httpTimeout - таймаут для HTTP запросов
	httpTimeout = 60 * time.Second
)

// ASNUpdater управляет скачиванием и обновлением базы данных ASN
type ASNUpdater struct {
	db          *IPtoASNDatabase
	downloadURL string
	interval    time.Duration
	httpClient  *http.Client
	stopCh      chan struct{}
	dataDir     string // Directory to save downloaded files
}

// NewASNUpdater создает новый экземпляр ASNUpdater
func NewASNUpdater(db *IPtoASNDatabase, downloadURL string, interval time.Duration, dataDir string) *ASNUpdater {
	if downloadURL == "" {
		downloadURL = DefaultDownloadURL
	}
	if interval == 0 {
		interval = DefaultUpdateInterval
	}

	return &ASNUpdater{
		db:          db,
		downloadURL: downloadURL,
		interval:    interval,
		httpClient: &http.Client{
			Timeout: httpTimeout,
		},
		stopCh:  make(chan struct{}),
		dataDir: dataDir,
	}
}

// Start запускает процесс обновления базы данных
// При первом запуске блокирующе загружает базу данных
// Then starts background update by interval
func (u *ASNUpdater) Start(ctx context.Context) error {
	// Load at startup (blocking)
	log.Printf("Loading ASN database from %s...", u.downloadURL)
	if err := u.downloadAndReload(); err != nil {
		return fmt.Errorf("failed to load ASN database: %w", err)
	}

	// Start background update
	go u.runBackgroundUpdates(ctx)

	return nil
}

// Stop stops background update
func (u *ASNUpdater) Stop() {
	close(u.stopCh)
}

// runBackgroundUpdates runs periodic database update
func (u *ASNUpdater) runBackgroundUpdates(ctx context.Context) {
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	log.Printf("ASN database background update started (interval: %v)", u.interval)

	for {
		select {
		case <-ticker.C:
			log.Println("Starting scheduled ASN database update...")
			if err := u.downloadAndReload(); err != nil {
				log.Printf("ASN database update error: %v", err)
			}
		case <-ctx.Done():
			log.Println("ASN database background update stopped (context cancelled)")
			return
		case <-u.stopCh:
			log.Println("ASN database background update stopped")
			return
		}
	}
}

// downloadAndReload скачивает, сохраняет в файл и загружает базу данных
func (u *ASNUpdater) downloadAndReload() error {
	startTime := time.Now()

	// Создаем HTTP запрос
	req, err := http.NewRequest(http.MethodGet, u.downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// Добавляем User-Agent
	req.Header.Set("User-Agent", "remnawave-observer/1.0")

	// Выполняем запрос
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// If dataDir is set, save to file (updater mode)
	if u.dataDir != "" {
		if err := u.saveToFile(resp.Body); err != nil {
			return fmt.Errorf("save to file: %w", err)
		}

		// Load from saved file
		filePath := filepath.Join(u.dataDir, "ip2asn-v4.tsv.gz")
		f, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("open saved file: %w", err)
		}
		defer f.Close()

		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()

		if err := u.db.LoadFromReader(gz); err != nil {
			return fmt.Errorf("load from file: %w", err)
		}
	} else {
		// In-memory mode (legacy, no file saving)
		var reader io.Reader = resp.Body

		// Проверяем Content-Type или расширение URL для определения gzip
		contentType := resp.Header.Get("Content-Type")
		isGzip := contentType == "application/gzip" ||
			contentType == "application/x-gzip" ||
			len(u.downloadURL) > 3 && u.downloadURL[len(u.downloadURL)-3:] == ".gz"

		if isGzip {
			gzipReader, err := gzip.NewReader(resp.Body)
			if err != nil {
				return fmt.Errorf("create gzip reader: %w", err)
			}
			defer gzipReader.Close()
			reader = gzipReader
		}

		// Загружаем в базу данных
		if err := u.db.LoadFromReader(reader); err != nil {
			return fmt.Errorf("load from reader: %w", err)
		}
	}

	duration := time.Since(startTime)
	log.Printf("ASN database successfully loaded: %d records in %v", u.db.Count(), duration)

	return nil
}

// saveToFile atomically saves downloaded content to disk
func (u *ASNUpdater) saveToFile(r io.Reader) error {
	targetPath := filepath.Join(u.dataDir, "ip2asn-v4.tsv.gz")
	tmpPath := targetPath + ".tmp"

	// Create temp file
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	defer os.Remove(tmpPath) // Cleanup on error

	// Copy downloaded data to temp file
	written, err := io.Copy(tmpFile, r)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("copy data: %w", err)
	}
	tmpFile.Close()

	// Atomic rename
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("atomic rename: %w", err)
	}

	log.Printf("[Updater] Saved ASN database to %s (%d bytes)", targetPath, written)
	return nil
}

// ForceReload forcibly reloads the database
func (u *ASNUpdater) ForceReload() error {
	log.Println("Forcing ASN database reload...")
	return u.downloadAndReload()
}

// GetDatabase возвращает текущую базу данных
func (u *ASNUpdater) GetDatabase() *IPtoASNDatabase {
	return u.db
}

// LoadFromLocalFile загружает ASN базу из локального файла (read-only, без скачивания)
// Используется в режиме когда observer-updater сервис скачивает файлы
func (u *ASNUpdater) LoadFromLocalFile(dataDir string) error {
	filePath := filepath.Join(dataDir, "ip2asn-v4.tsv.gz")

	log.Printf("[Observer] Loading ASN database from local file: %s", filePath)

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	if err := u.db.LoadFromReader(gz); err != nil {
		return fmt.Errorf("load from reader: %w", err)
	}

	log.Printf("[Observer] ASN database loaded from file: %d records", u.db.Count())
	return nil
}
