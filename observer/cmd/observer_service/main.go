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
	"observer_service/internal/services/observations"
	"observer_service/internal/services/panelingest"
	"observer_service/internal/services/remnawave"
	"observer_service/internal/services/scoring"
	"observer_service/internal/services/storage"
	"observer_service/internal/services/userpolicy"
	"observer_service/internal/updater"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg := config.New()

	// Context for background process shutdown signaling
	ctx, cancel := context.WithCancel(context.Background())
	// WaitGroup for waiting on all background goroutines
	var wg sync.WaitGroup

	log.Println("[Startup] Stage 1/5: starting updater bootstrap")
	updaterManager := updater.NewManager(cfg, nil)
	if err := updaterManager.Start(ctx); err != nil {
		log.Fatalf("Critical error: updater bootstrap failed: %v", err)
	}
	log.Println("[Startup] Stage 1/5 complete: updater bootstrap finished")

	log.Println("[Startup] Stage 2/5: connecting Redis and PostgreSQL")
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

	cleanupCtx, cleanupCancel := context.WithTimeout(ctx, time.Duration(cfg.ScanTimeBudgetSeconds)*time.Second)
	removedRows, cleanupErr := database.CleanupGarbageIPs(cleanupCtx, db)
	cleanupCancel()
	if cleanupErr != nil {
		log.Printf("Warning: failed to cleanup garbage IPs in PostgreSQL: %v", cleanupErr)
	} else if removedRows > 0 {
		log.Printf("Startup cleanup: removed %d garbage IP rows from PostgreSQL", removedRows)
	}

	cleanupCtx, cleanupCancel = context.WithTimeout(ctx, time.Duration(cfg.ScanTimeBudgetSeconds)*time.Second)
	removedRedisIPs, scannedRedisKeys, cleanupErr := redisStore.CleanupGarbageIPs(cleanupCtx)
	cleanupCancel()
	if cleanupErr != nil {
		log.Printf("Warning: failed to cleanup garbage IPs in Redis: %v", cleanupErr)
	} else if removedRedisIPs > 0 {
		log.Printf("Startup cleanup: removed %d garbage IP entries from Redis hot-window (%d keys scanned)", removedRedisIPs, scannedRedisKeys)
	}

	log.Println("[Startup] Stage 3/5: initializing observer core")
	if cfg.RemnawaveBaseURL == "" || cfg.RemnawaveAPIToken == "" {
		log.Fatalf("Critical error: REMNAWAVE_BASE_URL and REMNAWAVE_API_TOKEN are required in panel-only mode")
	}
	remnawaveClient := remnawave.NewClientWithHeader(
		cfg.RemnawaveBaseURL,
		cfg.RemnawaveAPIToken,
		cfg.RemnawaveTimeoutSeconds,
		cfg.UserIDUUIDCacheTTLHours,
		redisStore.GetClient(),
		cfg.RemnawaveHeader,
	)
	pingCtx, pingCancel := context.WithTimeout(ctx, time.Duration(cfg.RemnawaveTimeoutSeconds)*time.Second)
	if err := remnawaveClient.Ping(pingCtx); err != nil {
		pingCancel()
		log.Fatalf("Critical error: failed to connect to Remnawave API (%s): %v", cfg.RemnawaveBaseURL, err)
	}
	pingCancel()
	enforcer := enforcement.NewRemnawaveEnforcer(remnawaveClient, redisStore)
	log.Printf("✅ Remnawave Enforcer initialized (URL: %s)", cfg.RemnawaveBaseURL)
	squadExcluder := userpolicy.NewExcluder(remnawaveClient, cfg.ExcludedInternalSquads)
	if squadExcluder != nil {
		validateCtx, validateCancel := context.WithTimeout(ctx, time.Duration(cfg.RemnawaveTimeoutSeconds)*time.Second)
		missingSquads, err := squadExcluder.ValidateConfiguredSquads(validateCtx)
		validateCancel()
		if err != nil {
			log.Printf("Warning: failed to validate excluded internal squads: %v", err)
		} else if len(missingSquads) > 0 {
			log.Printf("Warning: configured excluded internal squads are missing in panel: %v", missingSquads)
		}
	}

	webhookAlerter := alerter.NewWebhookAlerter(cfg.AlertWebhookURL)
	observationStore := observations.NewStore(redisStore.GetClient())

	// Initialize ASN lookup service
	asnLookup, err := asn.NewASNLookupReadOnly(cfg.GeoDataDataDir)
	if err != nil {
		log.Fatalf("Critical error: failed to load ASN database: %v", err)
	}
	defer asnLookup.Close()
	log.Printf("[Observer] ASN lookup loaded from file (records: %d)", asnLookup.Count())

	log.Println("[Startup] Stage 4/5: initializing policy and scoring services")
	// Initialize GeoData loader (optional)
	var geoDataLoader *geodata.GeoDataLoader
	var geoService *geoip.GeoIPService
	var geoAnalyzer *geoip.GeoAnalyzer
	var asnClassifier *asn.ASNClassifier
	var scorer *scoring.Scorer
	var fallbackWorkerEnabled bool

	// Initialize unknown providers logging
	geodata.InitUnknownProvidersLog(cfg.GeoDataDataDir, cfg.UnknownProvidersLogEnabled)

	// Load provider and agglomeration configs
	geoDataLoader, err = geodata.NewGeoDataLoader(cfg.GeoDataConfigDir, cfg.GeoDataDataDir)
	if err != nil {
		log.Fatalf("Critical error: failed to load geodata: %v", err)
	}
	log.Printf("✅ GeoData loaded (agglomerations: %d)", len(geoDataLoader.GetAgglomerations()))

	if cfg.GeoIPEnabled {
		// Initialize MMDB reader (optional — files may not exist)
		var mmdbReader *geoip.MMDBReader
		mmdbReader, err = geoip.NewMMDBReader(cfg.GeoLiteASNPath, cfg.GeoLiteCityPath)
		if err != nil {
			log.Printf("Warning: MMDB files not available, GeoIP enrichment will be limited: %v", err)
			mmdbReader = nil
		}

		// Initialize GeoIP service
		geoService = geoip.NewGeoIPService(asnLookup, mmdbReader, redisStore.GetClient(), cfg.GeoIPCacheTTL)
		if cfg.GeoFallbackEnabled {
			if cfg.TwoIPToken == "" {
				log.Printf("[Observer] Geo fallback requested but TWOIP_TOKEN is empty; fallback disabled")
			} else {
				fallbackProvider := geoip.NewTwoIPProvider(cfg.TwoIPBaseURL, cfg.TwoIPToken, cfg.GeoFallbackTimeout)
				geoService.SetFallbackProvider(fallbackProvider)
				fallbackWorkerEnabled = true
				log.Printf("[Observer] Geo fallback enabled via %s", fallbackProvider.Name())
			}
		}
		geoAnalyzer = geoip.NewGeoAnalyzer(geoService, geoDataLoader)
		log.Printf("[Observer] GeoIP service initialized (cache TTL: %v)", cfg.GeoIPCacheTTL)
		updaterManager.SetGeoService(geoService)
	} else {
		log.Printf("[Observer] GeoIP enrichment disabled; scoring will run without geographic evidence")
	}

	// Initialize ASN classifier
	asnClassifier = asn.NewASNClassifier(geoDataLoader)
	log.Printf("✅ ASN classifier initialized")

	// Initialize scoring system
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
	logProcessor.SetUserExcluder(squadExcluder)
	logProcessor.SetNodeObservationRecorder(observationStore)
	if remnawaveClient != nil {
		logProcessor.SetUserEvidenceProvider(remnawaveClient)
	}
	if cfg.NodeExecutorBlockEnabled && remnawaveClient != nil {
		logProcessor.SetUserIPMitigator(enforcement.NewLocalIPBlocker(remnawaveClient, observationStore))
	}

	// GeoIP service cleanup
	if geoService != nil {
		defer geoService.Close()
	}

	poolMonitor := monitor.NewPoolMonitor(redisStore, repo, cfg, geoService)
	apiServer := api.NewServer(cfg.Port, redisStore, cfg)
	panelIngestService := panelingest.NewService(remnawaveClient, logProcessor, squadExcluder, observationStore, cfg)

	// Initialize CAIDA AS2Org (read-only mode - files managed by integrated updater)
	var as2orgLoader *geodata.AS2OrgLoader
	if cfg.CAIDAEnabled && cfg.GeoIPEnabled {
		as2orgLoader = geodata.NewAS2OrgLoader(cfg.GeoDataDataDir, "", 0)
		// Load from local file (downloads managed by updater manager)
		if err := as2orgLoader.LoadFromLocalFile(); err != nil {
			log.Printf("[Observer] Warning: CAIDA file not found: %v (running without CAIDA)", err)
			as2orgLoader = nil
		} else {
			log.Printf("[Observer] CAIDA AS2Org loaded from file (%d records)", as2orgLoader.Count())
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
	reenableScheduler := enforcement.NewScheduler(
		remnawaveClient,
		redisStore,
		time.Duration(cfg.ReenableTickSeconds)*time.Second,
		cfg.ReenableBatchSize,
	)
	log.Printf("✅ Re-enable Scheduler initialized (tick: %ds, batch: %d)",
		cfg.ReenableTickSeconds, cfg.ReenableBatchSize)

	// Tell WaitGroup how many goroutines we'll launch
	goroutineCount := 8 // poolMonitor + workerPool + sideEffectPool + metricsDumper + batchWriter + updaterManager + panelIngest + reenableScheduler
	if autoLearner != nil {
		goroutineCount++
	}
	if fallbackWorkerEnabled {
		goroutineCount++
	}

	wg.Add(goroutineCount)
	log.Println("[Startup] Stage 5/5: launching background workers")
	go poolMonitor.Run(ctx, &wg)
	go logProcessor.StartWorkerPool(ctx, &wg)
	go logProcessor.StartSideEffectWorkerPool(ctx, &wg)
	go logProcessor.StartBatchWriter(ctx, &wg)
	go metrics.StartDumper(ctx, &wg, 60*time.Second)
	if fallbackWorkerEnabled && geoService != nil {
		go geoService.StartFallbackWorker(ctx, &wg)
	}
	go panelIngestService.Run(ctx, &wg)

	// Start Auto-Learner if enabled
	if autoLearner != nil {
		go autoLearner.Run(ctx, &wg)
	}

	// Start Data Updater Manager (ASN, CAIDA, GeoLite background updates)
	go updaterManager.Run(ctx, &wg)

	// Start Re-enable Scheduler if configured
	go reenableScheduler.Run(ctx, &wg)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           apiServer.GetRouter(), // Get router from our api.Server
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
