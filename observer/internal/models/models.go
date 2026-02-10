package models

// LogEntry представляет запись лога, поступающую в сервис.
type LogEntry struct {
	UserEmail string `json:"user_email" binding:"required"`
	SourceIP  string `json:"source_ip" binding:"required"`
}

// AlertPayload представляет данные для отправки в вебхук.
type AlertPayload struct {
	UserIdentifier   string                `json:"user_identifier"`
	Limit            int                   `json:"limit"`
	BlockDuration    string                `json:"block_duration"`
	ViolationType    string                `json:"violation_type"`

	// Поля для режима по IP и подсетям (violation_type: ip_limit_exceeded, subnet_limit_exceeded)
	DetectedIPsCount *int                  `json:"detected_ips_count,omitempty"` // Кол-во IP/подсетей
	AllUserIPs       []string              `json:"all_user_ips,omitempty"`       // Список IP/подсетей

	// Поля для режима по ASN (violation_type: asn_limit_exceeded)
	DetectedASNCount *int                  `json:"detected_asn_count,omitempty"` // Количество уникальных провайдеров (ASN)
	AllUserASNs      []string              `json:"all_user_asns,omitempty"`      // Список ASN (например: ["AS31133", "AS3267"])
	ASNDetails       map[string]*ASNInfo   `json:"asn_details,omitempty"`        // Детальная информация по каждому ASN

	// Поля для скоринга и GeoIP анализа
	Score           *float64              `json:"score,omitempty"`            // Финальный скор нарушения (0-100)
	ScoreAction     string                `json:"score_action,omitempty"`     // Действие на основе скора (none, monitor, warn, soft_block, block)
	GeoAnalysis     *GeoAnalysisResult    `json:"geo_analysis,omitempty"`     // Результат географического анализа
	ProviderTypes   map[string]string     `json:"provider_types,omitempty"`   // ASN -> тип провайдера
}

// UserIPStats содержит статистику по IP-адресам пользователя для мониторинга.
type UserIPStats struct {
	Email            string   `json:"email"`
	IPCount          int      `json:"ip_count"`
	Limit            int      `json:"limit"`
	IPs              []string `json:"ips"`
	IPsWithTTL       []string `json:"ips_with_ttl"`
	MinTTLHours      float64  `json:"min_ttl_hours"`
	MaxTTLHours      float64  `json:"max_ttl_hours"`
	Status           string   `json:"status"`
	HasAlertCooldown bool     `json:"has_alert_cooldown"`
	IsExcluded       bool     `json:"excluded"`
	IsDebug          bool     `json:"is_debug"`
	ASNDetails       map[string]*ASNInfo `json:"asn_details,omitempty"` // Детали по каждому ASN (для ASN режима)
}

// ASNInfo содержит информацию об ASN и связанных IP-адресах
type ASNInfo struct {
	ASN          string   `json:"asn"`
	Organization string   `json:"organization,omitempty"` // Название провайдера
	Country      string   `json:"country,omitempty"`      // Код страны (RU, UA, KZ, и т.д.)
	TTLSeconds   int      `json:"ttl_seconds"`
	IPs          []string `json:"ips"`
	IPCount      int      `json:"ip_count"`       // Количество IP в этом ASN
	ProviderType string   `json:"provider_type,omitempty"` // Тип провайдера (mobile, hosting, vpn_proxy, isp)
	Modifier     float64  `json:"modifier,omitempty"`      // Модификатор подозрительности (0.3-1.8)
}

// GeoAnalysisResult результат географического анализа (для AlertPayload)
type GeoAnalysisResult struct {
	UniqueCountries []string `json:"unique_countries"`
	UniqueCities    []string `json:"unique_cities"`
	Agglomerations  []string `json:"agglomerations"`
	MaxDistanceKM   float64  `json:"max_distance_km"`
	GeoScore        int      `json:"geo_score"`
	GeoFlags        []string `json:"geo_flags"`
}

// BlockMessage представляет сообщение для отправки в очередь на блокировку.
// Поля EventID / ChunkIndex / ChunkTotal / SchemaVersion присутствуют только
// при чанкинге (len(ips) > MaxIPsPerBlockEvent); одночанковые сообщения
// без них сериализуются в том же формате что и раньше (omitempty).
type BlockMessage struct {
	IPs      []string `json:"ips"`
	Duration string   `json:"duration"`

	EventID       string `json:"event_id,omitempty"`
	ChunkIndex    *int   `json:"chunk_index,omitempty"` // 0-based
	ChunkTotal    *int   `json:"chunk_total,omitempty"`
	SchemaVersion int    `json:"schema_version,omitempty"` // 2 при чанкинге
}

// CheckResult представляет результат выполнения Lua-скрипта.
type CheckResult struct {
	StatusCode   int64
	CurrentCount int64
	IsNew        bool
	AllUserItems []string
}