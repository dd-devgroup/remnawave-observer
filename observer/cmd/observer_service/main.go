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
	"observer_service/internal/monitor"
	"observer_service/internal/processor"
	"observer_service/internal/services/alerter"
	"observer_service/internal/services/asn"
	"observer_service/internal/services/geodata"
	"observer_service/internal/services/geoip"
	"observer_service/internal/services/publisher"
	"observer_service/internal/services/scoring"
	"observer_service/internal/services/storage"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg := config.New()

	// Контекст для сигнализации о завершении работы фоновых процессов
	ctx, cancel := context.WithCancel(context.Background())
	// WaitGroup для ожидания завершения всех фоновых горутин
	var wg sync.WaitGroup

	redisStore, err := storage.NewRedisStore(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("Критическая ошибка: не удалось подключиться к Redis: %v", err)
	}
	defer redisStore.Close()

	rabbitPublisher, err := publisher.NewRabbitMQPublisher(cfg.RabbitMQURL, cfg.BlockingExchangeName)
	if err != nil {
		log.Fatalf("Критическая ошибка: не удалось подключиться к RabbitMQ: %v", err)
	}
	defer rabbitPublisher.Close()

	webhookAlerter := alerter.NewWebhookAlerter(cfg.AlertWebhookURL)

	// Инициализация ASN lookup сервиса (опционально)
	var asnLookup *asn.ASNLookup
	if cfg.DetectByASN {
		asnLookup, err = asn.NewASNLookup(cfg.IPtoASNDownloadURL, cfg.IPtoASNUpdateInterval)
		if err != nil {
			log.Fatalf("Критическая ошибка: не удалось загрузить ASN базу: %v", err)
		}
		defer asnLookup.Close()
		log.Printf("✅ ASN режим активирован (записей: %d)", asnLookup.Count())
	}

	// Инициализация GeoData загрузчика (опционально)
	var geoDataLoader *geodata.GeoDataLoader
	var geoService *geoip.GeoIPService
	var geoAnalyzer *geoip.GeoAnalyzer
	var asnClassifier *asn.ASNClassifier
	var scorer *scoring.Scorer

	if cfg.GeoIPEnabled || cfg.ScoringEnabled {
		// Загружаем конфигурации провайдеров и агломераций
		geoDataLoader, err = geodata.NewGeoDataLoader(cfg.GeoDataConfigDir)
		if err != nil {
			log.Fatalf("Критическая ошибка: не удалось загрузить географические данные: %v", err)
		}
		log.Printf("✅ GeoData загружен (агломерации: %d)", len(geoDataLoader.GetAgglomerations()))

		// Инициализируем GeoIP сервис если ASN lookup доступен
		if asnLookup != nil {
			geoService = geoip.NewGeoIPService(asnLookup, redisStore.GetClient(), cfg.GeoIPCacheTTL)
			geoAnalyzer = geoip.NewGeoAnalyzer(geoService, geoDataLoader)
			log.Printf("✅ GeoIP сервис инициализирован (cache TTL: %v)", cfg.GeoIPCacheTTL)
		}

		// Инициализируем классификатор провайдеров
		asnClassifier = asn.NewASNClassifier(geoDataLoader)
		log.Printf("✅ ASN классификатор инициализирован")

		// Инициализируем систему скоринга
		if cfg.ScoringEnabled {
			thresholds := scoring.ScoreThresholds{
				MonitorThreshold:   30,
				WarnThreshold:      cfg.ScoreThresholdWarn,
				SoftBlockThreshold: 70,
				BlockThreshold:     cfg.ScoreThresholdBlock,
			}
			scorer = scoring.NewScorer(thresholds)
			log.Printf("✅ Система скоринга инициализирована (warn: %.1f, block: %.1f)",
				cfg.ScoreThresholdWarn, cfg.ScoreThresholdBlock)
		}
	}

	logProcessor := processor.NewLogProcessor(
		redisStore,
		rabbitPublisher,
		webhookAlerter,
		cfg,
		asnLookup,
		geoService,
		geoAnalyzer,
		asnClassifier,
		scorer,
	)

	// Cleanup для GeoIP сервиса
	if geoService != nil {
		defer geoService.Close()
	}

	poolMonitor := monitor.NewPoolMonitor(redisStore, cfg)
	apiServer := api.NewServer(cfg.Port, logProcessor, redisStore, rabbitPublisher)

	// Сообщаем WaitGroup, что будем ждать три горутины
	wg.Add(3)
	go poolMonitor.Run(ctx, &wg)
	go logProcessor.StartWorkerPool(ctx, &wg)
	go logProcessor.StartSideEffectWorkerPool(ctx, &wg) // Запускаем новый пул воркеров

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: apiServer.GetRouter(), // Получаем роутер из нашего api.Server
	}

	go func() {
		log.Printf("Сервер Observer Service запущен на порту %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Ошибка запуска сервера: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Получен сигнал завершения, начинаю остановку сервиса...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Ошибка при остановке HTTP-сервера: %v", err)
	} else {
		log.Println("HTTP-сервер успешно остановлен.")
	}

	cancel()

	log.Println("Ожидание завершения фоновых процессов...")
	wg.Wait()

	log.Println("Все фоновые процессы остановлены. Сервис успешно остановлен.")
}