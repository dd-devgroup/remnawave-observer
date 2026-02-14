package updater

import (
	"context"
	"log"
	"sync"
	"time"

	"observer_service/internal/config"
	"observer_service/internal/services/asn"
	"observer_service/internal/services/geodata"
	"observer_service/internal/services/geoip"
)

// Manager coordinates all data updaters (ASN, CAIDA, GeoLite).
// It runs in the background and manages file downloads and updates.
type Manager struct {
	cfg            *config.Config
	asnUpdater     *asn.ASNUpdater
	as2orgLoader   *geodata.AS2OrgLoader
	geoLiteUpdater *geoip.GeoLiteUpdater
	geoService     *geoip.GeoIPService // for hot-reload callback
	enabled        bool
}

// NewManager creates a new updater manager.
// Pass nil for geoService if hot-reload is not needed.
func NewManager(cfg *config.Config, geoService *geoip.GeoIPService) *Manager {
	return &Manager{
		cfg:        cfg,
		geoService: geoService,
		enabled:    cfg.UpdaterEnabled,
	}
}

// Start initializes all updaters and performs initial data load.
// Returns error if critical initialization fails.
func (m *Manager) Start(ctx context.Context) error {
	if !m.enabled {
		log.Println("[Updater] Data updater disabled via UPDATER_ENABLED=false")
		return nil
	}

	log.Println("========================================")
	log.Println("  Data Updater Manager Starting")
	log.Println("========================================")

	// ============================================
	// 1. ASN Updater (iptoasn.com)
	// ============================================
	asnDB := asn.NewIPtoASNDatabase()
	m.asnUpdater = asn.NewASNUpdater(
		asnDB,
		m.cfg.IPtoASNDownloadURL,
		m.cfg.IPtoASNUpdateInterval,
		m.cfg.GeoDataDataDir,
	)
	if err := m.asnUpdater.Start(ctx); err != nil {
		return err
	}
	log.Printf("[Updater] ✅ ASN updater initialized (interval: %v, records: %d)",
		m.cfg.IPtoASNUpdateInterval, asnDB.Count())

	// ============================================
	// 2. CAIDA AS2Org Updater
	// ============================================
	if m.cfg.CAIDAEnabled {
		m.as2orgLoader = geodata.NewAS2OrgLoader(
			m.cfg.GeoDataDataDir,
			m.cfg.CAIDADownloadURL,
			time.Duration(m.cfg.CAIDARefreshHours)*time.Hour,
		)
		if err := m.as2orgLoader.InitialLoad(); err != nil {
			log.Printf("[Updater] Warning: CAIDA initial load failed: %v (will retry on next interval)", err)
		} else {
			log.Printf("[Updater] ✅ CAIDA AS2Org updater initialized (interval: %dh)", m.cfg.CAIDARefreshHours)
		}
	}

	// ============================================
	// 3. GeoLite MMDB Updater
	// ============================================
	if m.cfg.GeoLiteASNDownloadURL != "" || m.cfg.GeoLiteCityDownloadURL != "" {
		m.geoLiteUpdater = geoip.NewGeoLiteUpdater(
			m.cfg.GeoDataDataDir,
			m.cfg.GeoLiteASNDownloadURL,
			m.cfg.GeoLiteCityDownloadURL,
			m.cfg.GeoLiteUpdateInterval,
			m.geoService, // pass geoService for hot-reload callback
		)
		if err := m.geoLiteUpdater.InitialLoad(); err != nil {
			log.Printf("[Updater] Warning: GeoLite initial download failed: %v (will retry on next interval)", err)
		} else {
			log.Printf("[Updater] ✅ GeoLite updater initialized (interval: %v)", m.cfg.GeoLiteUpdateInterval)
		}
	}

	log.Println("[Updater] ✅ All updaters initialized successfully")
	return nil
}

// Run starts background refresh goroutines for all updaters.
// Blocks until context is cancelled.
func (m *Manager) Run(ctx context.Context, wg *sync.WaitGroup) {
	if !m.enabled {
		return
	}

	defer wg.Done()

	var localWg sync.WaitGroup
	goroutineCount := 0

	// CAIDA background refresh
	if m.as2orgLoader != nil {
		goroutineCount++
		localWg.Add(1)
		go m.as2orgLoader.RunRefresh(ctx, &localWg)
	}

	// GeoLite background refresh
	if m.geoLiteUpdater != nil {
		goroutineCount++
		localWg.Add(1)
		go m.geoLiteUpdater.RunRefresh(ctx, &localWg)
	}

	log.Printf("[Updater] Background refresh goroutines started: %d (ASN runs in separate goroutine)", goroutineCount)

	// Wait for context cancellation
	<-ctx.Done()
	log.Println("[Updater] Shutdown signal received, stopping updaters...")

	// Stop ASN updater's internal goroutine
	if m.asnUpdater != nil {
		m.asnUpdater.Stop()
	}

	// Wait for CAIDA and GeoLite updaters to finish
	localWg.Wait()
	log.Println("[Updater] All updaters stopped successfully")
}
