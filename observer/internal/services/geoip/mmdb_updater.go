package geoip

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	mmdbHTTPTimeout = 120 * time.Second
	userAgent       = "remnawave-observer/1.0"
)

// GeoLiteUpdater handles automatic downloading and hot-reloading of GeoLite2 MMDB files.
type GeoLiteUpdater struct {
	dataDir         string
	downloadURLs    map[string]string // "asn" -> URL, "city" -> URL
	refreshInterval time.Duration
	geoService      *GeoIPService

	mu sync.Mutex // protects download operations (not MMDB reader — that's in GeoIPService)
}

// NewGeoLiteUpdater creates a new GeoLite2 MMDB updater.
// geoService can be nil if hot-reload is not needed (e.g., in updater-only service).
func NewGeoLiteUpdater(dataDir, asnURL, cityURL string, refreshInterval time.Duration, geoService *GeoIPService) *GeoLiteUpdater {
	downloadURLs := make(map[string]string)
	if asnURL != "" {
		downloadURLs["asn"] = asnURL
	}
	if cityURL != "" {
		downloadURLs["city"] = cityURL
	}

	return &GeoLiteUpdater{
		dataDir:         dataDir,
		downloadURLs:    downloadURLs,
		refreshInterval: refreshInterval,
		geoService:      geoService,
	}
}

// InitialLoad downloads MMDB files if they are missing or stale, then triggers hot-reload.
// Non-fatal: returns error but service can continue with existing files.
func (u *GeoLiteUpdater) InitialLoad() error {
	if len(u.downloadURLs) == 0 {
		return fmt.Errorf("no download URLs configured")
	}

	downloadNeeded := false
	for fileType := range u.downloadURLs {
		filePath := u.getFilePath(fileType)
		info, err := os.Stat(filePath)
		if err != nil {
			// File missing — download
			downloadNeeded = true
			break
		}
		// File exists, check freshness
		if time.Since(info.ModTime()) >= u.refreshInterval {
			downloadNeeded = true
			break
		}
	}

	if downloadNeeded {
		log.Println("[GeoLiteUpdater] Initial download required...")
		if err := u.downloadAll(context.Background()); err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
	} else {
		log.Println("[GeoLiteUpdater] Existing files are fresh, skipping download")
	}

	// Trigger hot-reload if GeoIPService is wired
	if u.geoService != nil {
		if err := u.triggerHotReload(); err != nil {
			return fmt.Errorf("hot-reload failed: %w", err)
		}
	}

	return nil
}

// RunRefresh starts background periodic refresh. Blocks goroutine until ctx is canceled.
func (u *GeoLiteUpdater) RunRefresh(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(u.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[GeoLiteUpdater] Background refresh stopped")
			return
		case <-ticker.C:
			log.Println("[GeoLiteUpdater] Scheduled refresh...")
			if err := u.downloadAll(ctx); err != nil {
				log.Printf("[GeoLiteUpdater] Download error: %v", err)
				continue
			}
			if u.geoService != nil {
				if err := u.triggerHotReload(); err != nil {
					log.Printf("[GeoLiteUpdater] Hot-reload error: %v", err)
				}
			}
		}
	}
}

// downloadAll downloads all configured MMDB files.
func (u *GeoLiteUpdater) downloadAll(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	for fileType, url := range u.downloadURLs {
		if err := u.download(ctx, fileType, url); err != nil {
			return fmt.Errorf("download %s: %w", fileType, err)
		}
	}
	return nil
}

// download fetches a single MMDB file and writes it atomically.
func (u *GeoLiteUpdater) download(ctx context.Context, fileType, url string) error {
	if err := os.MkdirAll(u.dataDir, 0755); err != nil {
		return fmt.Errorf("mkdirAll dataDir: %w", err)
	}

	client := &http.Client{Timeout: mmdbHTTPTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	filePath := u.getFilePath(fileType)
	tmpPath := filePath + ".tmp"

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

	// Atomic rename
	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename tmp→final: %w", err)
	}

	log.Printf("[GeoLiteUpdater] File successfully downloaded (%s)", filePath)
	return nil
}

// triggerHotReload creates a new MMDBReader and swaps it in GeoIPService.
func (u *GeoLiteUpdater) triggerHotReload() error {
	asnPath := u.getFilePath("asn")
	cityPath := u.getFilePath("city")

	// Try to open MMDB reader with current file paths
	// Don't check file existence beforehand to avoid TOCTOU race
	// NewMMDBReader will return error if both paths are empty or if files can't be opened
	newReader, err := NewMMDBReader(asnPath, cityPath)
	if err != nil {
		return fmt.Errorf("MMDB files not ready for hot-reload: %w", err)
	}

	// Swap the reader atomically
	if err := u.geoService.UpdateMMDBReader(newReader); err != nil {
		newReader.Close()
		return fmt.Errorf("failed to update MMDB reader: %w", err)
	}

	// Log which databases were loaded
	var loadedDBs []string
	if newReader.asnDB != nil {
		loadedDBs = append(loadedDBs, "ASN")
	}
	if newReader.cityDB != nil {
		loadedDBs = append(loadedDBs, "City")
	}
	log.Printf("[GeoLiteUpdater] MMDB hot-reload successful (loaded: %v)", loadedDBs)
	return nil
}

// getFilePath returns the file path for a given MMDB type.
func (u *GeoLiteUpdater) getFilePath(fileType string) string {
	filename := fmt.Sprintf("GeoLite2-%s.mmdb", fileType)
	// Capitalize first letter: asn -> ASN, city -> City
	if fileType == "asn" {
		filename = "GeoLite2-ASN.mmdb"
	} else if fileType == "city" {
		filename = "GeoLite2-City.mmdb"
	}
	return filepath.Join(u.dataDir, filename)
}
