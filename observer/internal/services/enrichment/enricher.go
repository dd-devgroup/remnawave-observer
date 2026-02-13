package enrichment

import (
	"context"
	"log"
	"time"

	"observer_service/internal/database"
	"observer_service/internal/services/geoip"
)

// EnrichmentResult represents the result of IP enrichment.
type EnrichmentResult struct {
	ASN          string
	Org          string
	Country      string
	City         string
	Region       string
	Lat          float64
	Lon          float64
	Source       string  // "postgres", "iptoasn", "mmdb", "cache"
	Confidence   float64 // 0.0–1.0
	ASNAgreement bool
}

// Enricher provides tiered IP enrichment: Redis → Postgres → iptoasn → GeoLite2 MMDB.
// Redis and the fresh lookup (iptoasn + MMDB) are handled by GeoIPService.
// Enricher adds Postgres as a persistent cache tier between Redis and fresh lookups.
type Enricher struct {
	repo       database.Repository
	geoService *geoip.GeoIPService
	cacheTTL   time.Duration
}

// NewEnricher creates a new Enricher.
func NewEnricher(repo database.Repository, geoService *geoip.GeoIPService, cacheTTL time.Duration) *Enricher {
	return &Enricher{
		repo:       repo,
		geoService: geoService,
		cacheTTL:   cacheTTL,
	}
}

// Enrich performs tiered lookup for an IP address.
// Order: Postgres cache → GeoIPService (Redis cache → iptoasn → MMDB).
// On fresh lookup, result is persisted to Postgres.
func (e *Enricher) Enrich(ctx context.Context, ip string) (*EnrichmentResult, error) {
	// Tier 2: Postgres persistent cache
	if e.repo != nil {
		cached, err := e.repo.GetEnrichment(ctx, ip)
		if err == nil && cached != nil {
			return &EnrichmentResult{
				ASN:        cached.ASN,
				Org:        cached.Org,
				Country:    cached.Country,
				City:       cached.City,
				Region:     cached.Region,
				Lat:        cached.Lat,
				Lon:        cached.Lon,
				Source:     "postgres",
				Confidence: cached.Confidence,
			}, nil
		}
	}

	// Tier 1+3+4: GeoIPService (Redis cache → iptoasn → MMDB)
	if e.geoService == nil {
		return &EnrichmentResult{Source: "none", Confidence: 0}, nil
	}

	loc, err := e.geoService.Lookup(ctx, ip)
	if err != nil {
		return nil, err
	}

	result := &EnrichmentResult{
		ASN:          loc.ASN,
		Org:          loc.Organization,
		Country:      loc.CountryCode,
		City:         loc.City,
		Region:       loc.Region,
		Lat:          loc.Latitude,
		Lon:          loc.Longitude,
		Source:       loc.Source,
		Confidence:   loc.Confidence,
		ASNAgreement: loc.ASNAgreement,
	}

	// Write-through: persist to Postgres cache
	if e.repo != nil {
		cache := &database.IPEnrichmentCache{
			IP:         ip,
			ASN:        loc.ASN,
			Org:        loc.Organization,
			Country:    loc.CountryCode,
			City:       loc.City,
			Region:     loc.Region,
			Lat:        loc.Latitude,
			Lon:        loc.Longitude,
			Source:     loc.Source,
			Confidence: loc.Confidence,
			ExpiresAt:  time.Now().Add(e.cacheTTL),
		}
		if err := e.repo.UpsertEnrichment(ctx, cache); err != nil {
			log.Printf("[Enricher] Failed to persist enrichment to Postgres: %v", err)
		}
	}

	return result, nil
}

// CleanExpired removes expired enrichment records from Postgres.
func (e *Enricher) CleanExpired(ctx context.Context) (int64, error) {
	if e.repo == nil {
		return 0, nil
	}
	return e.repo.CleanExpiredEnrichments(ctx)
}
