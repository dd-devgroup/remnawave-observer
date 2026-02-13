package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"observer_service/internal/config"
	"observer_service/internal/services/asn"
	"observer_service/internal/services/geodata"
	"observer_service/internal/services/geoip"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("========================================")
	log.Println("  Observer Data Updater Service")
	log.Println("========================================")

	cfg := config.New()

	// Context for shutdown signaling
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// ============================================
	// 1. ASN Updater (iptoasn.com)
	// ============================================
	asnDB := asn.NewIPtoASNDatabase()
	asnUpdater := asn.NewASNUpdater(asnDB, cfg.IPtoASNDownloadURL, cfg.IPtoASNUpdateInterval, cfg.GeoDataDataDir)
	if err := asnUpdater.Start(ctx); err != nil {
		log.Fatalf("[Updater] Critical error: failed to load initial ASN database: %v", err)
	}
	log.Printf("[Updater] ✅ ASN updater initialized (interval: %v, records: %d)", cfg.IPtoASNUpdateInterval, asnDB.Count())

	// ============================================
	// 2. CAIDA AS2Org Updater
	// ============================================
	var as2orgLoader *geodata.AS2OrgLoader
	if cfg.CAIDAEnabled {
		as2orgLoader = geodata.NewAS2OrgLoader(
			cfg.GeoDataDataDir,
			cfg.CAIDADownloadURL,
			time.Duration(cfg.CAIDARefreshHours)*time.Hour,
		)
		if err := as2orgLoader.InitialLoad(); err != nil {
			log.Printf("[Updater] Warning: CAIDA initial load failed: %v (will retry on next interval)", err)
		} else {
			log.Printf("[Updater] ✅ CAIDA AS2Org updater initialized (interval: %dh)", cfg.CAIDARefreshHours)
		}
	}

	// ============================================
	// 3. GeoLite MMDB Updater
	// ============================================
	var geoLiteUpdater *geoip.GeoLiteUpdater
	if cfg.GeoLiteASNDownloadURL != "" || cfg.GeoLiteCityDownloadURL != "" {
		// Pass nil for geoService since updater-only service doesn't need hot-reload callback
		geoLiteUpdater = geoip.NewGeoLiteUpdater(
			cfg.GeoDataDataDir,
			cfg.GeoLiteASNDownloadURL,
			cfg.GeoLiteCityDownloadURL,
			cfg.GeoLiteUpdateInterval,
			nil, // no hot-reload callback in updater service
		)
		if err := geoLiteUpdater.InitialLoad(); err != nil {
			log.Printf("[Updater] Warning: GeoLite initial download failed: %v (will retry on next interval)", err)
		} else {
			log.Printf("[Updater] ✅ GeoLite updater initialized (interval: %v)", cfg.GeoLiteUpdateInterval)
		}
	}

	// ============================================
	// Start Background Refresh Goroutines
	// ============================================
	// Note: ASN updater already started background goroutine in Start()
	goroutineCount := 0
	if as2orgLoader != nil {
		goroutineCount++
	}
	if geoLiteUpdater != nil {
		goroutineCount++
	}

	wg.Add(goroutineCount)

	// CAIDA background refresh
	if as2orgLoader != nil {
		go as2orgLoader.RunRefresh(ctx, &wg)
	}

	// GeoLite background refresh
	if geoLiteUpdater != nil {
		go geoLiteUpdater.RunRefresh(ctx, &wg)
	}

	log.Printf("[Updater] ✅ All updaters running (ASN: always, CAIDA: %v, GeoLite: %v)", as2orgLoader != nil, geoLiteUpdater != nil)
	log.Println("[Updater] Observer Data Updater is now running. Press Ctrl+C to stop.")

	// ============================================
	// Wait for Shutdown Signal
	// ============================================
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[Updater] Shutdown signal received, stopping updaters...")
	cancel()
	asnUpdater.Stop() // Stop ASN updater's internal goroutine
	wg.Wait()         // Wait for CAIDA and GeoLite updaters
	log.Println("[Updater] All updaters stopped. Goodbye!")
}
