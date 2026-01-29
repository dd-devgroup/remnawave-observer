package asn

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
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
}

// NewASNUpdater создает новый экземпляр ASNUpdater
func NewASNUpdater(db *IPtoASNDatabase, downloadURL string, interval time.Duration) *ASNUpdater {
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
		stopCh: make(chan struct{}),
	}
}

// Start запускает процесс обновления базы данных
// При первом запуске блокирующе загружает базу данных
// Затем запускает фоновое обновление по интервалу
func (u *ASNUpdater) Start(ctx context.Context) error {
	// Загрузить при старте (блокирующе)
	log.Printf("Загрузка ASN базы данных с %s...", u.downloadURL)
	if err := u.downloadAndReload(); err != nil {
		return fmt.Errorf("не удалось загрузить ASN базу данных: %w", err)
	}

	// Запустить фоновое обновление
	go u.runBackgroundUpdates(ctx)

	return nil
}

// Stop останавливает фоновое обновление
func (u *ASNUpdater) Stop() {
	close(u.stopCh)
}

// runBackgroundUpdates запускает периодическое обновление базы
func (u *ASNUpdater) runBackgroundUpdates(ctx context.Context) {
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	log.Printf("Фоновое обновление ASN базы запущено (интервал: %v)", u.interval)

	for {
		select {
		case <-ticker.C:
			log.Println("Начало планового обновления ASN базы...")
			if err := u.downloadAndReload(); err != nil {
				log.Printf("Ошибка обновления ASN базы: %v", err)
			}
		case <-ctx.Done():
			log.Println("Фоновое обновление ASN базы остановлено (context cancelled)")
			return
		case <-u.stopCh:
			log.Println("Фоновое обновление ASN базы остановлено")
			return
		}
	}
}

// downloadAndReload скачивает и загружает базу данных
func (u *ASNUpdater) downloadAndReload() error {
	startTime := time.Now()

	// Создаем HTTP запрос
	req, err := http.NewRequest(http.MethodGet, u.downloadURL, nil)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса: %w", err)
	}

	// Добавляем User-Agent
	req.Header.Set("User-Agent", "remnawave-observer/1.0")

	// Выполняем запрос
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка скачивания: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("неожиданный статус код: %d", resp.StatusCode)
	}

	// Определяем нужна ли распаковка gzip
	var reader io.Reader = resp.Body

	// Проверяем Content-Type или расширение URL для определения gzip
	contentType := resp.Header.Get("Content-Type")
	isGzip := contentType == "application/gzip" ||
		contentType == "application/x-gzip" ||
		len(u.downloadURL) > 3 && u.downloadURL[len(u.downloadURL)-3:] == ".gz"

	if isGzip {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("ошибка создания gzip reader: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	}

	// Загружаем в базу данных
	if err := u.db.LoadFromReader(reader); err != nil {
		return fmt.Errorf("ошибка загрузки данных: %w", err)
	}

	duration := time.Since(startTime)
	log.Printf("ASN база данных успешно загружена: %d записей за %v", u.db.Count(), duration)

	return nil
}

// ForceReload принудительно перезагружает базу данных
func (u *ASNUpdater) ForceReload() error {
	log.Println("Принудительная перезагрузка ASN базы...")
	return u.downloadAndReload()
}

// GetDatabase возвращает текущую базу данных
func (u *ASNUpdater) GetDatabase() *IPtoASNDatabase {
	return u.db
}
