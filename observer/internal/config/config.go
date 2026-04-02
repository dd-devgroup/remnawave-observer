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

	// --- ASN/provider hot-window settings ---
	IPtoASNDownloadURL    string          // URL для скачивания базы iptoasn.com
	IPtoASNUpdateInterval time.Duration   // Интервал обновления базы ASN
	MaxASNsPerUser        int             // Legacy monitoring threshold, not used for blocking/scoring
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
	ScoreThresholdWarn  float64 // Порог для предупреждения (default: 50)
	ScoreThresholdBlock float64 // Порог для блокировки (default: 85)
	IPRescoringEnabled  bool
	IPRescoringBase     int
	IPRescoringDeepCheckEnabled    bool
	IPRescoringDeepCheckTimeout    time.Duration
	IPRescoringDeepCheckResultPoll time.Duration
	EvidenceSafeDeviceCount        int
	EvidenceDeviceGraceCount       int
	EvidenceDeviceActivityWindow   time.Duration

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
	RemnawaveBaseURL         string // Base URL Remnawave панели (например: https://panel.example.com)
	RemnawaveAPIToken        string // API token for Remnawave Authorization (Bearer)
	RemnawaveTimeoutSeconds  int    // Таймаут HTTP запросов к Remnawave (default: 5)
	RemnawaveHeader          string // Optional reverse-proxy gate KEY=VALUE (added as query+cookie on each API request)
	UserIDUUIDCacheTTLHours  int    // TTL кэша internal_id→uuid в часах (default: 24)
	PanelPollInterval        time.Duration
	PanelFetchTimeout        time.Duration
	PanelFetchResultPoll     time.Duration
	PanelFetchMaxInflight    int
	NodeExecutorBlockEnabled bool
	ExcludedInternalSquads   map[string]bool
	ReenableTickSeconds      int // Интервал проверки просроченных disable в секундах (default: 10)
	ReenableBatchSize        int // Максимальное количество enable за одну итерацию (default: 100)
}

const (
	defaultMonitoringIntervalSeconds = 300
	defaultSideEffectTimeoutSeconds  = 10
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

// New загружает конфигурацию из переменных окружения.
func New() *Config {
	logWorkers := defaultLogWorkerPoolSize()
	logChannelBuffer := defaultLogChannelBufferSize(logWorkers)
	sideEffectWorkers := defaultSideEffectWorkerPoolSize(logWorkers)
	sideEffectChannelBuffer := defaultSideEffectChannelBufferSize(sideEffectWorkers)
	remnawaveBaseURL := getEnv("REMNAWAVE_BASE_URL", "")
	remnawaveHeader := getEnv("REMNAWAVE_HEADER", "")
	remnawaveHeaderInvalid := false
	if remnawaveHeader != "" {
		if _, _, ok := parseNameValue(remnawaveHeader); !ok {
			remnawaveHeaderInvalid = true
			remnawaveHeader = ""
		}
	}

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
		ScoreThresholdWarn:  getEnvFloat("SCORE_THRESHOLD_WARN", 50.0),
		ScoreThresholdBlock: getEnvFloat("SCORE_THRESHOLD_BLOCK", 85.0),
		IPRescoringEnabled:               getEnvBool("IP_RESCORING_ENABLED", true),
		IPRescoringBase:                  getEnvInt("IP_RESCORING_BASE_THRESHOLD", 3),
		IPRescoringDeepCheckEnabled:      getEnvBool("IP_RESCORING_DEEP_CHECK_ENABLED", true),
		IPRescoringDeepCheckTimeout:      time.Duration(getEnvInt("IP_RESCORING_DEEP_CHECK_TIMEOUT_SECONDS", 10)) * time.Second,
		IPRescoringDeepCheckResultPoll:   time.Duration(getEnvInt("IP_RESCORING_DEEP_CHECK_RESULT_POLL_SECONDS", 2)) * time.Second,
		EvidenceSafeDeviceCount:          getEnvInt("EVIDENCE_SAFE_DEVICE_COUNT", 3),
		EvidenceDeviceGraceCount:         getEnvInt("EVIDENCE_DEVICE_GRACE_COUNT", 5),
		EvidenceDeviceActivityWindow:     time.Duration(getEnvInt("EVIDENCE_DEVICE_ACTIVITY_WINDOW_DAYS", 30)) * 24 * time.Hour,

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
		RemnawaveBaseURL:         remnawaveBaseURL,
		RemnawaveAPIToken:        getEnv("REMNAWAVE_API_TOKEN", ""),
		RemnawaveTimeoutSeconds:  getEnvInt("REMNAWAVE_TIMEOUT_SECONDS", 5),
		RemnawaveHeader:          remnawaveHeader,
		UserIDUUIDCacheTTLHours:  getEnvInt("USERID_UUID_CACHE_TTL_HOURS", 24),
		PanelPollInterval:        time.Duration(getEnvInt("PANEL_POLL_INTERVAL_SECONDS", 60)) * time.Second,
		PanelFetchTimeout:        time.Duration(getEnvInt("PANEL_FETCH_TIMEOUT_SECONDS", 20)) * time.Second,
		PanelFetchResultPoll:     time.Duration(getEnvInt("PANEL_FETCH_RESULT_POLL_SECONDS", 2)) * time.Second,
		PanelFetchMaxInflight:    getEnvInt("PANEL_FETCH_MAX_INFLIGHT", 3),
		NodeExecutorBlockEnabled: getEnvBool("NODE_EXECUTOR_BLOCK_ENABLED", true),
		ExcludedInternalSquads:   parseSet(getEnv("EXCLUDED_INTERNAL_SQUAD_UUIDS", "")),
		ReenableTickSeconds:      defaultReenableTickSeconds,
		ReenableBatchSize:        defaultReenableBatchSize,
	}

	// Validation: timeout не может быть отрицательным
	if cfg.RemnawaveTimeoutSeconds < 1 {
		cfg.RemnawaveTimeoutSeconds = 5
	}
	if cfg.UserIDUUIDCacheTTLHours < 1 {
		cfg.UserIDUUIDCacheTTLHours = 24
	}
	if cfg.PanelPollInterval < 1*time.Second {
		cfg.PanelPollInterval = 60 * time.Second
	}
	if cfg.PanelFetchTimeout < 1*time.Second {
		cfg.PanelFetchTimeout = 20 * time.Second
	}
	if cfg.PanelFetchResultPoll < 1*time.Second {
		cfg.PanelFetchResultPoll = 2 * time.Second
	}
	if cfg.PanelFetchMaxInflight < 1 {
		cfg.PanelFetchMaxInflight = 3
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
	if cfg.IPRescoringBase < 1 {
		cfg.IPRescoringBase = 3
	}
	if cfg.IPRescoringDeepCheckTimeout < 1*time.Second {
		cfg.IPRescoringDeepCheckTimeout = 10 * time.Second
	}
	if cfg.IPRescoringDeepCheckResultPoll < 1*time.Second {
		cfg.IPRescoringDeepCheckResultPoll = 2 * time.Second
	}
	if cfg.EvidenceSafeDeviceCount < 1 {
		cfg.EvidenceSafeDeviceCount = 3
	}
	if cfg.EvidenceDeviceGraceCount < cfg.EvidenceSafeDeviceCount {
		cfg.EvidenceDeviceGraceCount = cfg.EvidenceSafeDeviceCount + 2
	}
	if cfg.EvidenceDeviceActivityWindow < 24*time.Hour {
		cfg.EvidenceDeviceActivityWindow = 30 * 24 * time.Hour
	}
	if remnawaveHeaderInvalid {
		log.Printf("Warning: REMNAWAVE_HEADER must be in KEY=VALUE format; ignored")
	}

	log.Printf("Configuration loaded. Port: %s", cfg.Port)
	log.Printf("Log source mode: panel-only")
	log.Printf("Provider tracking mode: scoring-only over ASN hot window")
	log.Printf("    Source: iptoasn.com, Update interval: %v, ASN TTL: %v", cfg.IPtoASNUpdateInterval, cfg.UserASNTTL)
	if cfg.MaxASNsPerUser > 0 {
		log.Printf("    MAX_ASNS_PER_USER=%d is legacy monitoring-only and does not trigger bans or scoring", cfg.MaxASNsPerUser)
	}
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
	if len(cfg.ExcludedInternalSquads) > 0 {
		log.Printf("Internal squad exclusion list loaded: %d", len(cfg.ExcludedInternalSquads))
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
	log.Printf("Scoring system enabled. Warn threshold: %.1f, Block threshold: %.1f", cfg.ScoreThresholdWarn, cfg.ScoreThresholdBlock)
	log.Printf("IP rescoring: enabled=%v base=%d deep_check=%v timeout=%v result_poll=%v",
		cfg.IPRescoringEnabled, cfg.IPRescoringBase, cfg.IPRescoringDeepCheckEnabled,
		cfg.IPRescoringDeepCheckTimeout, cfg.IPRescoringDeepCheckResultPoll)
	log.Printf("Evidence device thresholds: safe<=%d, grace<=%d, suspicious>%d, activity_window=%v",
		cfg.EvidenceSafeDeviceCount, cfg.EvidenceDeviceGraceCount, cfg.EvidenceDeviceGraceCount, cfg.EvidenceDeviceActivityWindow)
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
	if cfg.RemnawaveHeader != "" {
		if name, _, ok := parseNameValue(cfg.RemnawaveHeader); ok {
			log.Printf("Remnawave gate header enabled from REMNAWAVE_HEADER: %s=<hidden>", name)
		}
	}
	log.Printf("Panel ingest enabled. poll=%v fetch_timeout=%v result_poll=%v inflight=%d executor_block=%v",
		cfg.PanelPollInterval, cfg.PanelFetchTimeout, cfg.PanelFetchResultPoll, cfg.PanelFetchMaxInflight, cfg.NodeExecutorBlockEnabled)

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

func parseNameValue(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	name := strings.TrimSpace(parts[0])
	val := strings.TrimSpace(parts[1])
	if name == "" || val == "" {
		return "", "", false
	}
	return name, val, true
}
