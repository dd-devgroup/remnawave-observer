package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config хранит всю конфигурацию приложения.
type Config struct {
	Port                        string
	RedisURL                    string
	RabbitMQURL                 string
	MaxIPsPerUser               int
	AlertWebhookURL             string
	UserIPTTL                   time.Duration
	AlertCooldown               time.Duration
	ClearIPsDelay               time.Duration
	BlockDuration               string
	BlockingExchangeName        string
	MonitoringInterval          time.Duration
	DebugEmail                  string
	DebugIPLimit                int
	ExcludedUsers               map[string]bool
	ExcludedIPs                 map[string]bool
	WorkerPoolSize              int
	LogChannelBufferSize        int
	SideEffectWorkerPoolSize    int
	SideEffectChannelBufferSize int
	SideEffectTimeout          time.Duration // Таймаут на одну побочную задачу (default: 10s)

	// --- ПАРАМЕТРЫ ДЛЯ РЕЖИМА ПОДСЕТЕЙ ---
	DetectBySubnet    bool          // Включить режим детекции по подсетям
	MaxSubnetsPerUser int           
	UserSubnetTTL     time.Duration 
	SubnetMaskIPv4    int           
	ExcludedSubnets   map[string]bool 

	// --- ПАРАМЕТРЫ ДЛЯ РЕЖИМА ASN ---
	DetectByASN          bool            // Включить режим детекции по ASN
	IPtoASNDownloadURL   string          // URL для скачивания базы iptoasn.com
	IPtoASNUpdateInterval time.Duration  // Интервал обновления базы ASN
	MaxASNsPerUser       int             // Лимит уникальных ASN на пользователя
	ASNFallbackMask      int             // Маска для fallback если ASN не найден (по умолчанию 16)
	ExcludedASNs         map[string]bool // ASN которые не считаются (например Cloudflare, Google)

	// --- ПАРАМЕТРЫ GEOIP ---
	GeoIPEnabled        bool          // Включить GeoIP анализ
	GeoIPCacheTTL       time.Duration // TTL для кэша GeoIP (default: 24 часа)
	GeoIPTimeout        time.Duration // Таймаут на один HTTP-запрос к ip-api.com (default: 3s)
	GeoIPRateIntervalMs         int // Минимальный интервал между запросами к ip-api.com в ms (default: 1350)
	GeoIPMonitorMaxChecksPerRun int // Макс. количество GeoIP lookup за один цикл мониторинга (default: 50)
	GeoDataConfigDir    string        // Директория с конфигами (agglomerations.yaml, providers.yaml)
	GeoDataDataDir      string        // Директория для записываемых данных (unknown_providers.json, backups)

	// --- ПАРАМЕТРЫ СКОРИНГА ---
	ScoringEnabled      bool    // Включить систему скоринга
	ScoreThresholdWarn  float64 // Порог для предупреждения (default: 50)
	ScoreThresholdBlock float64 // Порог для блокировки (default: 85)

	// --- ПАРАМЕТРЫ ВХОДЯЩИХ ЗАПРОСОВ ---
	MaxRequestBytes         int64 // Максимальный размер body POST /log-entry (default: 2MB)
	MaxLogEntriesPerRequest int   // Максимальное количество записей в одном запросе (default: 1000)

	// --- ПАРАМЕТРЫ PUBLISHER ---
	PublisherPoolSize          int // Размер пула каналов RabbitMQ (default: 5)
	RabbitPublishMaxRetries    int // Макс. количество повторов публикации (default: 5)
	RabbitPublishBackoffBaseMs int // Базовый интервал backoff в ms (default: 500)
	RabbitPublishBackoffMaxMs  int // Макс. интервал backoff в ms (default: 30000)

	// --- ПАРАМЕТРЫ АВТООБУЧЕНИЯ ---
	UnknownProvidersLogEnabled bool // Включить логирование неизвестных провайдеров (default: false)

	// Автоматическое обучение
	AutoLearningEnabled       bool          // Включить автоматическое обучение (default: false)
	AutoLearningInterval      time.Duration // Интервал проверки (default: 24 часа)
	AutoLearningMinCount      int           // Минимальное количество встреч для автодобавления (default: 10)
	AutoLearningMinConfidence string        // Минимальный уровень уверенности: high, medium (default: high)
}

// New загружает конфигурацию из переменных окружения.
func New() *Config {
	cfg := &Config{
		Port:                        getEnv("PORT", "9000"),
		RedisURL:                    getEnv("REDIS_URL", "redis://localhost:6379/0"),
		RabbitMQURL:                 getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost/"),
		MaxIPsPerUser:               getEnvInt("MAX_IPS_PER_USER", 3),
		AlertWebhookURL:             getEnv("ALERT_WEBHOOK_URL", ""),
		UserIPTTL:                   time.Duration(getEnvInt("USER_IP_TTL_SECONDS", 24*60*60)) * time.Second,
		AlertCooldown:               time.Duration(getEnvInt("ALERT_COOLDOWN_SECONDS", 60*60)) * time.Second,
		ClearIPsDelay:               time.Duration(getEnvInt("CLEAR_IPS_DELAY_SECONDS", 30)) * time.Second,
		BlockDuration:               getEnv("BLOCK_DURATION", "5m"),
		BlockingExchangeName:        getEnv("BLOCKING_EXCHANGE_NAME", "blocking_exchange"),
		MonitoringInterval:          time.Duration(getEnvInt("MONITORING_INTERVAL", 300)) * time.Second,
		DebugEmail:                  getEnv("DEBUG_EMAIL", ""),
		DebugIPLimit:                getEnvInt("DEBUG_IP_LIMIT", 1),
		ExcludedUsers:               parseSet(getEnv("EXCLUDED_USERS", "")),
		ExcludedIPs:                 parseSet(getEnv("EXCLUDED_IPS", "")),
		WorkerPoolSize:              getEnvInt("WORKER_POOL_SIZE", 20),
		LogChannelBufferSize:        getEnvInt("LOG_CHANNEL_BUFFER_SIZE", 100),
		SideEffectWorkerPoolSize:    getEnvInt("SIDE_EFFECT_WORKER_POOL_SIZE", 10),
		SideEffectChannelBufferSize: getEnvInt("SIDE_EFFECT_CHANNEL_BUFFER_SIZE", 50),
		SideEffectTimeout:          time.Duration(getEnvInt("SIDE_EFFECT_TIMEOUT_SECONDS", 10)) * time.Second,

		// --- Загрузка параметров входящих запросов ---
		MaxRequestBytes:         int64(getEnvInt("MAX_REQUEST_BYTES", 2*1024*1024)),
		MaxLogEntriesPerRequest: getEnvInt("MAX_LOG_ENTRIES_PER_REQUEST", 1000),

		// --- Загрузка параметров publisher ---
		PublisherPoolSize:          getEnvInt("PUBLISHER_POOL_SIZE", 5),
		RabbitPublishMaxRetries:    getEnvInt("RABBIT_PUBLISH_MAX_RETRIES", 5),
		RabbitPublishBackoffBaseMs: getEnvInt("RABBIT_PUBLISH_BACKOFF_BASE_MS", 500),
		RabbitPublishBackoffMaxMs:  getEnvInt("RABBIT_PUBLISH_BACKOFF_MAX_MS", 30000),

		// --- Загрузка параметров подсетей ---
		DetectBySubnet:    getEnvBool("DETECT_BY_SUBNET", false),
		MaxSubnetsPerUser: getEnvInt("MAX_SUBNETS_PER_USER", 3),
		UserSubnetTTL:     time.Duration(getEnvInt("USER_SUBNET_TTL_SECONDS", 86400)) * time.Second,
		SubnetMaskIPv4:    getEnvInt("SUBNET_MASK_IPV4", 24),
		ExcludedSubnets:   parseSet(getEnv("EXCLUDED_SUBNETS", "")),

		// --- Загрузка параметров ASN ---
		DetectByASN:           getEnvBool("DETECT_BY_ASN", false),
		IPtoASNDownloadURL:    getEnv("IPTOASN_DOWNLOAD_URL", ""),
		IPtoASNUpdateInterval: time.Duration(getEnvInt("IPTOASN_UPDATE_INTERVAL_MINUTES", 60)) * time.Minute,
		MaxASNsPerUser:        getEnvInt("MAX_ASNS_PER_USER", 4),
		ASNFallbackMask:       getEnvInt("ASN_FALLBACK_MASK", 16),
		ExcludedASNs:          parseSet(getEnv("EXCLUDED_ASNS", "")),

		// --- Загрузка параметров GeoIP ---
		GeoIPEnabled:        getEnvBool("GEOIP_ENABLED", false),
		GeoIPCacheTTL:       time.Duration(getEnvInt("GEOIP_CACHE_TTL_HOURS", 24)) * time.Hour,
		GeoIPTimeout:        time.Duration(getEnvInt("GEOIP_TIMEOUT_SECONDS", 3)) * time.Second,
		GeoIPRateIntervalMs:         getEnvInt("GEOIP_RATE_INTERVAL_MS", 1350),
		GeoIPMonitorMaxChecksPerRun: getEnvInt("GEOIP_MONITOR_MAX_CHECKS_PER_RUN", 50),
		GeoDataConfigDir:    getEnv("GEODATA_CONFIG_DIR", "/app/config"),
		GeoDataDataDir:      getEnv("GEODATA_DATA_DIR", "/app/data"),

		// --- Загрузка параметров скоринга ---
		ScoringEnabled:      getEnvBool("SCORING_ENABLED", false),
		ScoreThresholdWarn:  getEnvFloat("SCORE_THRESHOLD_WARN", 50.0),
		ScoreThresholdBlock: getEnvFloat("SCORE_THRESHOLD_BLOCK", 85.0),

		// --- Загрузка параметров автообучения ---
		UnknownProvidersLogEnabled: getEnvBool("UNKNOWN_PROVIDERS_LOG_ENABLED", false),
		AutoLearningEnabled:        getEnvBool("AUTO_LEARNING_ENABLED", false),
		AutoLearningInterval:       time.Duration(getEnvInt("AUTO_LEARNING_INTERVAL_HOURS", 24)) * time.Hour,
		AutoLearningMinCount:       getEnvInt("AUTO_LEARNING_MIN_COUNT", 10),
		AutoLearningMinConfidence:  getEnv("AUTO_LEARNING_MIN_CONFIDENCE", "high"),
	}

	log.Printf("Конфигурация загружена. Порт: %s", cfg.Port)
	if cfg.DetectByASN {
		log.Printf("!!! РЕЖИМ ОБНАРУЖЕНИЯ: по ASN (провайдерам). Лимит: %d провайдеров на пользователя.", cfg.MaxASNsPerUser)
		log.Printf("    Источник: iptoasn.com, Интервал обновления: %v, Fallback маска: /%d", cfg.IPtoASNUpdateInterval, cfg.ASNFallbackMask)
		if len(cfg.ExcludedASNs) > 0 {
			log.Printf("    Исключенные ASN: %d", len(cfg.ExcludedASNs))
		}
	} else if cfg.DetectBySubnet {
		log.Printf("!!! РЕЖИМ ОБНАРУЖЕНИЯ: по ПОДСЕТЯМ (/%d). Лимит: %d подсетей на пользователя.", cfg.SubnetMaskIPv4, cfg.MaxSubnetsPerUser)
	} else {
		log.Printf("!!! РЕЖИМ ОБНАРУЖЕНИЯ: по IP-адресам. Лимит: %d IP на пользователя.", cfg.MaxIPsPerUser)
	}
	log.Printf("Пул воркеров обработки логов: %d воркеров, размер буфера канала: %d", cfg.WorkerPoolSize, cfg.LogChannelBufferSize)
	log.Printf("Пул воркеров побочных задач (алерты, очистка): %d воркеров, размер буфера канала: %d", cfg.SideEffectWorkerPoolSize, cfg.SideEffectChannelBufferSize)
	if len(cfg.ExcludedUsers) > 0 {
		log.Printf("Загружен список исключений: %d пользователей", len(cfg.ExcludedUsers))
	}
	if len(cfg.ExcludedIPs) > 0 {
		log.Printf("Загружен список исключений IP-адресов: %d", len(cfg.ExcludedIPs))
	}
	if len(cfg.ExcludedSubnets) > 0 {
		log.Printf("Загружен список исключений подсетей: %d", len(cfg.ExcludedSubnets))
	}
	if cfg.DebugEmail != "" {
		log.Printf("Режим дебага включен для email: %s с лимитом IP: %d", cfg.DebugEmail, cfg.DebugIPLimit)
	}
	if cfg.GeoIPEnabled {
		log.Printf("GeoIP анализ включен. Cache TTL: %v, Config dir: %s, Data dir: %s", cfg.GeoIPCacheTTL, cfg.GeoDataConfigDir, cfg.GeoDataDataDir)
	}
	if cfg.ScoringEnabled {
		log.Printf("Система скоринга включена. Warn threshold: %.1f, Block threshold: %.1f", cfg.ScoreThresholdWarn, cfg.ScoreThresholdBlock)
	}
	if cfg.UnknownProvidersLogEnabled {
		log.Printf("Логирование неизвестных провайдеров включено")
	}
	if cfg.AutoLearningEnabled {
		log.Printf("Автоматическое обучение включено. Интервал: %v, Min count: %d, Min confidence: %s",
			cfg.AutoLearningInterval, cfg.AutoLearningMinCount, cfg.AutoLearningMinConfidence)
	}

	return cfg
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

func getEnvFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}

func parseSet(value string) map[string]bool {
	set := make(map[string]bool)
	if value == "" {
		return set
	}
	items := strings.Split(value, ",")
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			set[item] = true
		}
	}
	return set
}