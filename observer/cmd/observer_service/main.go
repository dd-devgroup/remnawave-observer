package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"observer_service/internal/api"
	"observer_service/internal/config"
	"observer_service/internal/database"
	"observer_service/internal/metrics"
	"observer_service/internal/monitor"
	"observer_service/internal/processor"
	"observer_service/internal/services/alerter"
	"observer_service/internal/services/asn"
	"observer_service/internal/services/enforcement"
	"observer_service/internal/services/geodata"
	"observer_service/internal/services/geoip"
	"observer_service/internal/services/remnawave"
	"observer_service/internal/services/scoring"
	"observer_service/internal/services/storage"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg := config.New()

	// Context for background process shutdown signaling
	ctx, cancel := context.WithCancel(context.Background())
	// WaitGroup for waiting on all background goroutines
	var wg sync.WaitGroup

	redisStore, err := storage.NewRedisStore(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("Critical error: failed to connect to Redis: %v", err)
	}
	defer redisStore.Close()
	redisStore.SetScanMaxKeys(cfg.ScanMaxKeys)
	redisStore.SetScanCount(cfg.ScanCount)
	redisStore.SetScanTimeBudget(time.Duration(cfg.ScanTimeBudgetSeconds) * time.Second)

	// Initialize PostgreSQL
	if cfg.PostgresDSN == "" {
		log.Fatalf("Critical error: POSTGRES_DSN is required but not set")
	}
	db, err := database.NewPostgresDB(cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("Critical error: failed to connect to PostgreSQL: %v", err)
	}
	if err := database.AutoMigrate(db); err != nil {
		log.Fatalf("Critical error: database migration failed: %v", err)
	}
	repo := database.NewGormRepository(db)
	defer repo.Close()
	log.Println("PostgreSQL initialized and migrated")

	// MIG-9: RabbitMQ publisher removed, using Remnawave enforcement
	var enforcer enforcement.Enforcer
	if cfg.RemnawaveBaseURL != "" && cfg.RemnawaveAPIToken != "" {
		remnawaveClient := remnawave.NewClient(
			cfg.RemnawaveBaseURL,
			cfg.RemnawaveAPIToken,
			cfg.RemnawaveTimeoutSeconds,
			cfg.UserIDUUIDCacheTTLHours,
			redisStore.GetClient(),
		)
		enforcer = enforcement.NewRemnawaveEnforcer(remnawaveClient, redisStore)
		log.Printf("✅ Remnawave Enforcer initialized (URL: %s)", cfg.RemnawaveBaseURL)
	} else {
		enforcer = enforcement.NewNoopEnforcer()
		log.Printf("⚠️  Remnawave not configured, using noop enforcer")
	}

	webhookAlerter := alerter.NewWebhookAlerter(cfg.AlertWebhookURL)

	// Initialize ASN lookup service
	asnLookup, err := asn.NewASNLookup(cfg.IPtoASNDownloadURL, cfg.IPtoASNUpdateInterval)
	if err != nil {
		log.Fatalf("Critical error: failed to load ASN database: %v", err)
	}
	defer asnLookup.Close()
	log.Printf("ASN lookup initialized (records: %d)", asnLookup.Count())

	// Initialize GeoData loader (optional)
	var geoDataLoader *geodata.GeoDataLoader
	var geoService *geoip.GeoIPService
	var geoAnalyzer *geoip.GeoAnalyzer
	var asnClassifier *asn.ASNClassifier
	var scorer *scoring.Scorer

	if cfg.GeoIPEnabled || cfg.ScoringEnabled {
		// Initialize unknown providers logging
		geodata.InitUnknownProvidersLog(cfg.GeoDataDataDir, cfg.UnknownProvidersLogEnabled)

		// Load provider and agglomeration configs
		geoDataLoader, err = geodata.NewGeoDataLoader(cfg.GeoDataConfigDir, cfg.GeoDataDataDir)
		if err != nil {
			log.Fatalf("Critical error: failed to load geodata: %v", err)
		}
		log.Printf("✅ GeoData loaded (agglomerations: %d)", len(geoDataLoader.GetAgglomerations()))

		// Initialize MMDB reader (optional — files may not exist)
		var mmdbReader *geoip.MMDBReader
		mmdbReader, err = geoip.NewMMDBReader(cfg.GeoLiteASNPath, cfg.GeoLiteCityPath)
		if err != nil {
			log.Printf("Warning: MMDB files not available, GeoIP enrichment will be limited: %v", err)
			mmdbReader = nil
		}

		// Initialize GeoIP service
		geoService = geoip.NewGeoIPService(asnLookup, mmdbReader, redisStore.GetClient(), cfg.GeoIPCacheTTL)
		geoAnalyzer = geoip.NewGeoAnalyzer(geoService, geoDataLoader)
		log.Printf("GeoIP service initialized (cache TTL: %v, MMDB: %v)", cfg.GeoIPCacheTTL, mmdbReader != nil)

		// Initialize ASN classifier
		asnClassifier = asn.NewASNClassifier(geoDataLoader)
		log.Printf("✅ ASN classifier initialized")

		// Initialize scoring system
		if cfg.ScoringEnabled {
			thresholds := scoring.ScoreThresholds{
				MonitorThreshold:       25,
				WarnThreshold:          cfg.ScoreThresholdWarn,
				SoftChallengeThreshold: 60,
				TempDisableThreshold:   75,
				HardDisableThreshold:   cfg.ScoreThresholdBlock,
			}
			scorer = scoring.NewScorer(thresholds)
			log.Printf("✅ Scoring system initialized (warn: %.1f, hard_disable: %.1f)",
				cfg.ScoreThresholdWarn, cfg.ScoreThresholdBlock)
		}
	}

	logProcessor := processor.NewLogProcessor(
		redisStore,
		enforcer,
		webhookAlerter,
		cfg,
		asnLookup,
		repo,
		geoService,
		geoAnalyzer,
		asnClassifier,
		scorer,
	)

	// GeoIP service cleanup
	if geoService != nil {
		defer geoService.Close()
	}

	poolMonitor := monitor.NewPoolMonitor(redisStore, repo, cfg, geoService)
	apiServer := api.NewServer(cfg.Port, logProcessor, redisStore, cfg)

	// Initialize CAIDA AS2Org (optional, synchronous initial load)
	var as2orgLoader *geodata.AS2OrgLoader
	if cfg.CAIDAEnabled && cfg.GeoIPEnabled {
		as2orgLoader = geodata.NewAS2OrgLoader(cfg.GeoDataDataDir, cfg.CAIDADownloadURL, time.Duration(cfg.CAIDARefreshHours)*time.Hour)
		if err := as2orgLoader.InitialLoad(); err != nil {
			log.Printf("Warning: CAIDA initial load failed: %v (running without CAIDA)", err)
			as2orgLoader = nil
		} else {
			log.Printf("✅ CAIDA AS2Org loaded (%d records, refresh every %dh)", as2orgLoader.Count(), cfg.CAIDARefreshHours)
		}
	}

	// Initialize Auto-Learner (optional)
	var autoLearner *geodata.AutoLearner
	if cfg.AutoLearningEnabled && geoDataLoader != nil {
		autoLearner = geodata.NewAutoLearner(
			geoDataLoader,
			cfg.GeoDataConfigDir,
			cfg.GeoDataDataDir,
			cfg.AutoLearningInterval,
			cfg.AutoLearningMinCount,
			cfg.AutoLearningMinConfidence,
			cfg.AutoLearningMaxAddsPerRun,
			cfg.AutoLearningOutputFile,
			as2orgLoader,
		)
		autoLearner.SetRepo(repo)
		autoLearner.SetMinDistinctUsers(cfg.AutoLearnMinDistinctUsers)
		autoLearner.SetAutoApproveThreshold(cfg.AutoLearnAutoApproveThreshold)
		log.Printf("✅ Auto-Learner initialized (Postgres-backed, min_distinct_users: %d, auto_approve: %.2f)",
			cfg.AutoLearnMinDistinctUsers, cfg.AutoLearnAutoApproveThreshold)
	}

	// MIG-7: Initialize Re-enable Scheduler
	var reenableScheduler *enforcement.Scheduler
	if cfg.RemnawaveBaseURL != "" && cfg.RemnawaveAPIToken != "" {
		// Create scheduler with the same Remnawave client
		remnawaveClient := remnawave.NewClient(
			cfg.RemnawaveBaseURL,
			cfg.RemnawaveAPIToken,
			cfg.RemnawaveTimeoutSeconds,
			cfg.UserIDUUIDCacheTTLHours,
			redisStore.GetClient(),
		)
		reenableScheduler = enforcement.NewScheduler(
			remnawaveClient,
			redisStore,
			time.Duration(cfg.ReenableTickSeconds)*time.Second,
			cfg.ReenableBatchSize,
		)
		log.Printf("✅ Re-enable Scheduler initialized (tick: %ds, batch: %d)",
			cfg.ReenableTickSeconds, cfg.ReenableBatchSize)
	}

	// Tell WaitGroup how many goroutines we'll launch
	goroutineCount := 5 // poolMonitor + workerPool + sideEffectPool + metricsDumper + batchWriter
	if autoLearner != nil {
		goroutineCount++
	}
	if as2orgLoader != nil {
		goroutineCount++ // RunRefresh
	}
	if reenableScheduler != nil {
		goroutineCount++ // Re-enable scheduler
	}

	wg.Add(goroutineCount)
	go poolMonitor.Run(ctx, &wg)
	go logProcessor.StartWorkerPool(ctx, &wg)
	go logProcessor.StartSideEffectWorkerPool(ctx, &wg)
	go logProcessor.StartBatchWriter(ctx, &wg)
	go metrics.StartDumper(ctx, &wg, 60*time.Second)

	// Start Auto-Learner if enabled
	if autoLearner != nil {
		go autoLearner.Run(ctx, &wg)
	}

	// Background CAIDA refresh
	if as2orgLoader != nil {
		go as2orgLoader.RunRefresh(ctx, &wg)
	}

	// Start Re-enable Scheduler if configured
	if reenableScheduler != nil {
		go reenableScheduler.Run(ctx, &wg)
	}

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: apiServer.GetRouter(), // Get router from our api.Server
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
	}

	go func() {
		log.Printf("Observer Service server started on port %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server startup error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutdown signal received, stopping service...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	} else {
		log.Println("HTTP server stopped successfully")
	}

	cancel()

	log.Println("Waiting for background processes to finish...")
	wg.Wait()

	// Save unknown providers log before exit
	if cfg.UnknownProvidersLogEnabled {
		log.Println("Saving unknown providers log...")
		geodata.FlushUnknownProvidersLog()
	}

	log.Println("All background processes stopped. Service shutdown complete.")
}
