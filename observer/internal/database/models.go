package database

import (
	"time"

	"gorm.io/gorm"
)

// UserConnection records each new ASN connection event for a user.
type UserConnection struct {
	gorm.Model
	UserID       string  `gorm:"index;not null"`
	SourceIP     string  `gorm:"not null"`
	ASN          string  `gorm:"index"`
	OrgName      string
	ProviderType string
	Country      string
	City         string
	Lat          float64
	Lon          float64
}

// UserDailyProfile stores aggregated daily stats for a user.
type UserDailyProfile struct {
	DayDate         string `gorm:"primaryKey;not null"` // YYYY-MM-DD
	UserID          string `gorm:"primaryKey;not null"`
	UniqueASNs      int
	UniqueCountries int
	UniqueIPs       int
	Score           float64
	Action          string
	UpdatedAt       time.Time
}

// UserProviderFingerprint stores provider usage patterns for a user.
type UserProviderFingerprint struct {
	UserID         string `gorm:"primaryKey;not null"`
	TopASNs        string `gorm:"type:jsonb;default:'{}'"`
	TopCountries   string `gorm:"type:jsonb;default:'{}'"`
	StabilityScore float64
	UpdatedAt      time.Time
}

// AntiAbuseAction records enforcement actions taken against a user.
type AntiAbuseAction struct {
	gorm.Model
	UserID         string `gorm:"index;not null"`
	ActionType     string `gorm:"not null"` // warn, soft_challenge, temp_disable, hard_disable
	Reason         string
	Score          float64
	ScoreBreakdown string `gorm:"type:jsonb;default:'{}'"`
}

// UserScoreEvent records each scoring event for a user.
type UserScoreEvent struct {
	gorm.Model
	UserID         string `gorm:"index;not null"`
	SourceIP       string
	ASN            string
	IsNewASN       bool
	ScoreTotal     float64
	ScoreAction    string
	ScoreBreakdown string `gorm:"type:jsonb;default:'{}'"`
}

// LearningCandidate represents a provider classification candidate for auto-learning.
type LearningCandidate struct {
	gorm.Model
	ASN            string  `gorm:"index"`
	OrgName        string
	NormalizedName string
	ProposedType   string
	Confidence     float64
	Evidence       string
	DistinctUsers  int
	TotalConns     int
	Countries      string `gorm:"type:text"` // comma-separated
	Status         string `gorm:"index;default:'pending'"` // pending, approved, rejected
}

// ASNOrgStats holds aggregated connection stats per ASN/org pair.
type ASNOrgStats struct {
	ASN           string
	OrgName       string
	DistinctUsers int64
	TotalConns    int64
}

// MonitorUserStats holds aggregated stats for monitoring (query result, not a table).
type MonitorUserStats struct {
	UserID          string
	UniqueASNs      int64
	UniqueIPs       int64
	UniqueCountries int64
}

// IPEnrichmentCache stores enrichment data for IP addresses.
type IPEnrichmentCache struct {
	IP         string    `gorm:"primaryKey;not null"`
	ASN        string
	Org        string
	Country    string
	City       string
	Region     string
	Lat        float64
	Lon        float64
	Source     string  // "mmdb", "iptoasn"
	Confidence float64 // 0.0–1.0
	ExpiresAt  time.Time `gorm:"index"`
	UpdatedAt  time.Time
}
