package config

import (
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Config хранит всю конфигурацию приложения.
type Config struct {
	Port                        string
	PostgresDSN                 string // DSN for PostgreSQL (required, fatal if empty)
	RedisURL                    string
	ScanMaxKeys                 int // Макс. количество ключей при SCAN (default: 10000)
	ScanCount                   int // Hint COUNT для Redis SCAN (default: 100)
	ScanTimeBudgetSeconds       int // Макс. время одной SCAN операции в секундах (default: 30)
	AlertWebhookURL             string
	AlertCooldown               time.Duration
	ClearIPsDelay               time.Duration
	BlockDuration               string
	MonitoringInterval          time.Duration
	DebugEmail                  string
	DebugIPLimit                int
	ExcludedUsers               map[string]bool
	ExcludedIPs                 map[string]bool
	WorkerPoolSize              int
	LogChannelBufferSize        int
	SideEffectWorkerPoolSize    int
	SideEffectChannelBufferSize int
	SideEffectTimeout           time.Duration // Таймаут на одну побочную задачу (default: 10s)

	// --- ПАРАМЕТРЫ ДЛЯ РЕЖИМА ASN ---
	IPtoASNDownloadURL    string          // URL для скачивания базы iptoasn.com
	IPtoASNUpdateInterval time.Duration   // Интервал обновления базы ASN
	MaxASNsPerUser        int             // Лимит уникальных ASN на пользователя
	UserASNTTL            time.Duration   // TTL для ASN записей пользователя
	ExcludedASNs          map[string]bool // ASN которые не считаются (например Cloudflare, Google)

	// --- ПАРАМЕТРЫ GEOIP ---
	GeoIPEnabled           bool          // Включить GeoIP анализ
	GeoIPCacheTTL          time.Duration // TTL для кэша GeoIP (default: 24 часа)
	GeoFallbackEnabled     bool          // Enable fallback Geo API (2IP)
	GeoFallbackTimeout     time.Duration // Timeout for fallback API requests (default: 3s)
	TwoIPToken             string        // API token for 2IP fallback lookups
	TwoIPBaseURL           string        // Base URL for 2IP API (default: https://api.2ip.io)
	GeoLiteASNPath         string        // Path to GeoLite2-ASN.mmdb (default: /app/data/GeoLite2-ASN.mmdb)
	GeoLiteCityPath        string        // Path to GeoLite2-City.mmdb (default: /app/data/GeoLite2-City.mmdb)
	GeoLiteASNDownloadURL  string        // P3TERX GitHub mirror URL for GeoLite2-ASN.mmdb auto-download
	GeoLiteCityDownloadURL string        // P3TERX GitHub mirror URL for GeoLite2-City.mmdb auto-download
	GeoLiteUpdateInterval  time.Duration // Auto-update interval for GeoLite2 MMDB files (default: 168 hours = 7 days)
	GeoDataConfigDir       string        // Директория с конфигами (agglomerations.yaml, providers.yaml)
	GeoDataDataDir         string        // Директория для записываемых данных (unknown_providers.json, backups)

	// --- ПАРАМЕТРЫ СКОРИНГА ---
	ScoringEnabled      bool    // Включить систему скоринга
	ScoreThresholdWarn  float64 // Порог для предупреждения (default: 50)
	ScoreThresholdBlock float64 // Порог для блокировки (default: 85)

	// --- ПАРАМЕТРЫ ВХОДЯЩИХ ЗАПРОСОВ ---
	MaxRequestBytes         int64 // Максимальный размер body POST /log-entry (default: 2MB)
	MaxLogEntriesPerRequest int   // Максимальное количество записей в одном запросе (default: 1000)

	// --- ПАРАМЕТРЫ АВТООБУЧЕНИЯ ---
	UnknownProvidersLogEnabled bool // Включить логирование неизвестных провайдеров (default: false)

	// Автоматическое обучение
	AutoLearningEnabled           bool          // Включить автоматическое обучение (default: false)
	AutoLearningInterval          time.Duration // Интервал проверки (default: 24 часа)
	AutoLearningMinCount          int           // Минимальное количество встреч для автодобавления (default: 10)
	AutoLearningMinConfidence     string        // Минимальный уровень уверенности: high, medium (default: high)
	AutoLearningMaxAddsPerRun     int           // Макс. добавлений за один цикл (default: 20)
	AutoLearningOutputFile        string        // Имя overlay файла в dataDir (default: providers.learned.yaml)
	AutoLearnMinDistinctUsers     int           // Min distinct users per ASN for Postgres learning (default: 3)
	AutoLearnAutoApproveThreshold float64       // Auto-approve confidence threshold (default: 0.8)

	// --- ПАРАМЕТРЫ CAIDA AS2Org ---
	CAIDAEnabled      bool   // Включить загрузку CAIDA AS-Organizations (default: true)
	CAIDADownloadURL  string // URL файла CAIDA (default: из as2org.go)
	CAIDARefreshHours int    // Интервал обновления в часах (default: 168 = 7 дней)

	// --- ПАРАМЕТРЫ REMNAWAVE ENFORCEMENT ---
	RemnawaveBaseURL        string // Base URL Remnawave панели (например: https://panel.example.com)
	RemnawaveAPIToken       string // API token for Remnawave Authorization (Bearer)
	RemnawaveTimeoutSeconds int    // Таймаут HTTP запросов к Remnawave (default: 5)
	UserIDUUIDCacheTTLHours int    // TTL кэша internal_id→uuid в часах (default: 24)
	ReenableTickSeconds     int    // Интервал проверки просроченных disable в секундах (default: 10)
	ReenableBatchSize       int    // Максимальное количество enable за одну итерацию (default: 100)
}

const (
	defaultMonitoringIntervalSeconds = 300
	defaultSideEffectTimeoutSeconds  = 10
	defaultMaxRequestBytes           = 2 * 1024 * 1024
	defaultReenableTickSeconds       = 10
	defaultReenableBatchSize         = 100
)

func defaultLogWorkerPoolSize() int {
	workers := runtime.GOMAXPROCS(0)
	if workers < 2 {
		workers = 2
	}
	return workers
}

func defaultLogChannelBufferSize(logWorkers int) int {
	buffer := logWorkers * 20
	if buffer < 100 {
		buffer = 100
	}
	if buffer > 5000 {
		buffer = 5000
	}
	return buffer
}

func defaultSideEffectWorkerPoolSize(logWorkers int) int {
	workers := logWorkers / 2
	if workers < 2 {
		workers = 2
	}
	if workers > 32 {
		workers = 32
	}
	return workers
}

func defaultSideEffectChannelBufferSize(sideEffectWorkers int) int {
	buffer := sideEffectWorkers * 10
	if buffer < 50 {
		buffer = 50
	}
	if buffer > 1000 {
		buffer = 1000
	}
	return buffer
}

func defaultMaxLogEntriesPerRequest(maxRequestBytes int64) int {
	// Conservative estimate: one JSON log entry is ~2KB in heavy payload scenarios.
	limit := int(maxRequestBytes / 2048)
	if limit < 200 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	return limit
}

// New загружает конфигурацию из переменных окружения.
func New() *Config {
	logWorkers := defaultLogWorkerPoolSize()
	logChannelBuffer := defaultLogChannelBufferSize(logWorkers)
	sideEffectWorkers := defaultSideEffectWorkerPoolSize(logWorkers)
	sideEffectChannelBuffer := defaultSideEffectChannelBufferSize(sideEffectWorkers)
	maxRequestBytes := int64(defaultMaxRequestBytes)

	cfg := &Config{
		Port:                        getEnv("PORT", "9000"),
		PostgresDSN:                 getEnv("POSTGRES_DSN", ""),
		RedisURL:                    getEnv("REDIS_URL", "redis://localhost:6379/0"),
		ScanMaxKeys:                 getEnvInt("SCAN_MAX_KEYS", 10000),
		ScanCount:                   getEnvInt("SCAN_COUNT", 100),
		ScanTimeBudgetSeconds:       getEnvInt("SCAN_TIME_BUDGET_SECONDS", 30),
		AlertWebhookURL:             getEnv("ALERT_WEBHOOK_URL", ""),
		AlertCooldown:               time.Duration(getEnvInt("ALERT_COOLDOWN_SECONDS", 60*60)) * time.Second,
		ClearIPsDelay:               time.Duration(getEnvInt("CLEAR_IPS_DELAY_SECONDS", 30)) * time.Second,
		BlockDuration:               getEnv("BLOCK_DURATION", "5m"),
		MonitoringInterval:          time.Duration(defaultMonitoringIntervalSeconds) * time.Second,
		DebugEmail:                  getEnv("DEBUG_EMAIL", ""),
		DebugIPLimit:                getEnvInt("DEBUG_IP_LIMIT", 1),
		ExcludedUsers:               parseSet(getEnv("EXCLUDED_USERS", "")),
		ExcludedIPs:                 parseSet(getEnv("EXCLUDED_IPS", "")),
		WorkerPoolSize:              logWorkers,
		LogChannelBufferSize:        logChannelBuffer,
		SideEffectWorkerPoolSize:    sideEffectWorkers,
		SideEffectChannelBufferSize: sideEffectChannelBuffer,
		SideEffectTimeout:           time.Duration(defaultSideEffectTimeoutSeconds) * time.Second,

		// --- Загрузка параметров входящих запросов ---
		MaxRequestBytes:         maxRequestBytes,
		MaxLogEntriesPerRequest: defaultMaxLogEntriesPerRequest(maxRequestBytes),

		// --- Загрузка параметров ASN ---
		IPtoASNDownloadURL:    getEnv("IPTOASN_DOWNLOAD_URL", ""),
		IPtoASNUpdateInterval: time.Duration(getEnvInt("IPTOASN_UPDATE_INTERVAL_MINUTES", 60)) * time.Minute,
		MaxASNsPerUser:        getEnvInt("MAX_ASNS_PER_USER", 4),
		UserASNTTL:            time.Duration(getEnvInt("USER_ASN_TTL_SECONDS", 86400)) * time.Second,
		ExcludedASNs:          parseSet(getEnv("EXCLUDED_ASNS", "")),

		// --- Загрузка параметров GeoIP ---
		GeoIPEnabled:           getEnvBool("GEOIP_ENABLED", false),
		GeoIPCacheTTL:          time.Duration(getEnvInt("GEOIP_CACHE_TTL_HOURS", 24)) * time.Hour,
		GeoFallbackEnabled:     getEnvBool("GEO_FALLBACK_ENABLED", false),
		GeoFallbackTimeout:     time.Duration(getEnvInt("GEO_FALLBACK_TIMEOUT_SECONDS", 3)) * time.Second,
		TwoIPToken:             getEnv("TWOIP_TOKEN", ""),
		TwoIPBaseURL:           getEnv("TWOIP_BASE_URL", "https://api.2ip.io"),
		GeoLiteASNPath:         getEnv("GEOLITE_ASN_PATH", "/app/data/GeoLite2-ASN.mmdb"),
		GeoLiteCityPath:        getEnv("GEOLITE_CITY_PATH", "/app/data/GeoLite2-City.mmdb"),
		GeoLiteASNDownloadURL:  getEnv("GEOLITE_ASN_DOWNLOAD_URL", "https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/download/GeoLite2-ASN.mmdb"),
		GeoLiteCityDownloadURL: getEnv("GEOLITE_CITY_DOWNLOAD_URL", "https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/download/GeoLite2-City.mmdb"),
		GeoLiteUpdateInterval:  time.Duration(getEnvInt("GEOLITE_UPDATE_INTERVAL_HOURS", 168)) * time.Hour,
		GeoDataConfigDir:       getEnv("GEODATA_CONFIG_DIR", "/app/config"),
		GeoDataDataDir:         getEnv("GEODATA_DATA_DIR", "/app/data"),

		// --- Загрузка параметров скоринга ---
		ScoringEnabled:      getEnvBool("SCORING_ENABLED", false),
		ScoreThresholdWarn:  getEnvFloat("SCORE_THRESHOLD_WARN", 50.0),
		ScoreThresholdBlock: getEnvFloat("SCORE_THRESHOLD_BLOCK", 85.0),

		// --- Загрузка параметров автообучения ---
		UnknownProvidersLogEnabled:    getEnvBool("UNKNOWN_PROVIDERS_LOG_ENABLED", false),
		AutoLearningEnabled:           getEnvBool("AUTO_LEARNING_ENABLED", false),
		AutoLearningInterval:          time.Duration(getEnvInt("AUTO_LEARNING_INTERVAL_HOURS", 24)) * time.Hour,
		AutoLearningMinCount:          getEnvInt("AUTO_LEARNING_MIN_COUNT", 10),
		AutoLearningMinConfidence:     getEnv("AUTO_LEARNING_MIN_CONFIDENCE", "high"),
		AutoLearningMaxAddsPerRun:     getEnvInt("AUTO_LEARNING_MAX_ADDS_PER_RUN", 20),
		AutoLearningOutputFile:        getEnv("AUTO_LEARNING_OUTPUT_FILE", "providers.learned.yaml"),
		AutoLearnMinDistinctUsers:     getEnvInt("AUTO_LEARN_MIN_DISTINCT_USERS", 3),
		AutoLearnAutoApproveThreshold: getEnvFloat("AUTO_LEARN_AUTO_APPROVE_THRESHOLD", 0.8),

		// --- CAIDA AS2Org ---
		CAIDAEnabled:      getEnvBool("CAIDA_ENABLED", true),
		CAIDADownloadURL:  getEnv("CAIDA_DOWNLOAD_URL", ""),
		CAIDARefreshHours: getEnvInt("CAIDA_REFRESH_HOURS", 168),

		// --- Загрузка параметров Remnawave enforcement ---
		RemnawaveBaseURL:        getEnv("REMNAWAVE_BASE_URL", ""),
		RemnawaveAPIToken:       getEnv("REMNAWAVE_API_TOKEN", ""),
		RemnawaveTimeoutSeconds: getEnvInt("REMNAWAVE_TIMEOUT_SECONDS", 5),
		UserIDUUIDCacheTTLHours: getEnvInt("USERID_UUID_CACHE_TTL_HOURS", 24),
		ReenableTickSeconds:     defaultReenableTickSeconds,
		ReenableBatchSize:       defaultReenableBatchSize,
	}

	// Validation: timeout не может быть отрицательным
	if cfg.RemnawaveTimeoutSeconds < 1 {
		cfg.RemnawaveTimeoutSeconds = 5
	}
	if cfg.UserIDUUIDCacheTTLHours < 1 {
		cfg.UserIDUUIDCacheTTLHours = 24
	}
	if cfg.ReenableTickSeconds < 1 {
		cfg.ReenableTickSeconds = 10
	}
	if cfg.ReenableBatchSize < 1 {
		cfg.ReenableBatchSize = 100
	}
	if cfg.GeoFallbackTimeout < 1*time.Second {
		cfg.GeoFallbackTimeout = 3 * time.Second
	}

	log.Printf("Configuration loaded. Port: %s", cfg.Port)
	log.Printf("Detection mode: ASN (providers). Limit: %d providers per user", cfg.MaxASNsPerUser)
	log.Printf("    Source: iptoasn.com, Update interval: %v", cfg.IPtoASNUpdateInterval)
	if len(cfg.ExcludedASNs) > 0 {
		log.Printf("    Excluded ASNs: %d", len(cfg.ExcludedASNs))
	}
	log.Printf("Log processing worker pool: %d workers, channel buffer: %d", cfg.WorkerPoolSize, cfg.LogChannelBufferSize)
	log.Printf("Side-effect worker pool (alerts, cleanup): %d workers, channel buffer: %d", cfg.SideEffectWorkerPoolSize, cfg.SideEffectChannelBufferSize)
	if len(cfg.ExcludedUsers) > 0 {
		log.Printf("Exclusion list loaded: %d users", len(cfg.ExcludedUsers))
	}
	if len(cfg.ExcludedIPs) > 0 {
		log.Printf("IP exclusion list loaded: %d", len(cfg.ExcludedIPs))
	}
	if cfg.DebugEmail != "" {
		log.Printf("Debug mode enabled for email: %s with limit: %d", cfg.DebugEmail, cfg.DebugIPLimit)
	}
	if cfg.GeoIPEnabled {
		log.Printf("GeoIP analysis enabled. Cache TTL: %v, Config dir: %s, Data dir: %s", cfg.GeoIPCacheTTL, cfg.GeoDataConfigDir, cfg.GeoDataDataDir)
		if cfg.GeoFallbackEnabled && cfg.TwoIPToken != "" {
			log.Printf("GeoIP fallback via 2IP enabled. Timeout: %v, Base URL: %s", cfg.GeoFallbackTimeout, cfg.TwoIPBaseURL)
		} else if cfg.GeoFallbackEnabled {
			log.Printf("GeoIP fallback enabled but TWOIP_TOKEN is empty; fallback lookups are disabled")
		}
	}
	if cfg.ScoringEnabled {
		log.Printf("Scoring system enabled. Warn threshold: %.1f, Block threshold: %.1f", cfg.ScoreThresholdWarn, cfg.ScoreThresholdBlock)
	}
	if cfg.UnknownProvidersLogEnabled {
		log.Printf("Unknown providers logging enabled")
	}
	if cfg.AutoLearningEnabled {
		log.Printf("Auto-learning enabled. Interval: %v, Min count: %d, Min confidence: %s, Max adds/cycle: %d, Output: %s",
			cfg.AutoLearningInterval, cfg.AutoLearningMinCount, cfg.AutoLearningMinConfidence, cfg.AutoLearningMaxAddsPerRun, cfg.AutoLearningOutputFile)
	}
	if cfg.CAIDAEnabled {
		log.Printf("CAIDA AS2Org enabled. Refresh every %dh", cfg.CAIDARefreshHours)
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
