package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"observer_service/internal/services/asn"

	"github.com/redis/go-redis/v9"
)

// GeoLocation represents the geolocation of an IP address.
type GeoLocation struct {
	IP           string
	CountryCode  string
	City         string
	Region       string
	Latitude     float64
	Longitude    float64
	ASN          string
	Organization string
	Source       string  // "mmdb", "iptoasn", "cache"
	Confidence   float64 // 0.0–1.0
	ASNAgreement bool    // true if iptoasn and GeoLite2-ASN agree on ASN
}

// GeoIPService provides geolocation using local MMDB files.
type GeoIPService struct {
	asnLookup   *asn.ASNLookup
	mmdbReader  *MMDBReader
	redisClient *redis.Client
	cacheTTL    time.Duration
	mu          sync.RWMutex // protects mmdbReader for hot-reload
}

// NewGeoIPService creates a new GeoIP service using local MMDB files.
func NewGeoIPService(asnLookup *asn.ASNLookup, mmdbReader *MMDBReader, redisClient *redis.Client, cacheTTL time.Duration) *GeoIPService {
	return &GeoIPService{
		asnLookup:   asnLookup,
		mmdbReader:  mmdbReader,
		redisClient: redisClient,
		cacheTTL:    cacheTTL,
	}
}

// Lookup performs geolocation for an IP address.
func (s *GeoIPService) Lookup(ctx context.Context, ip string) (*GeoLocation, error) {
	if s.redisClient != nil {
		cached, err := s.getFromCache(ctx, ip)
		if err == nil && cached != nil {
			cached.Source = "cache"
			return cached, nil
		}
	}

	// Start with iptoasn data (with defensive nil check)
	var asnStr, countryCode, org string
	var err error

	if s.asnLookup != nil {
		asnStr, countryCode, org, err = s.asnLookup.LookupFull(ip)
		if err != nil {
			log.Printf("[GeoIP] ASN lookup failed for %s: %v", ip, err)
			asnStr = ""
			countryCode = ""
			org = ""
		}
	} else {
		// ASN lookup service unavailable - continue with MMDB only
		log.Printf("[GeoIP] ASN lookup service unavailable for %s", ip)
	}

	location := &GeoLocation{
		IP:           ip,
		CountryCode:  countryCode,
		ASN:          asnStr,
		Organization: org,
		Source:       "iptoasn",
		Confidence:   0.5,
	}

	// Enrich with MMDB data if available (use read lock for hot-reload safety)
	s.mu.RLock()
	reader := s.mmdbReader
	s.mu.RUnlock()

	if reader != nil {
		// City/geo lookup
		mmdbCountry, city, region, lat, lon, err := reader.LookupCity(ip)
		if err == nil {
			location.City = city
			location.Region = region
			location.Latitude = lat
			location.Longitude = lon
			if mmdbCountry != "" {
				location.CountryCode = mmdbCountry
			}
			location.Source = "mmdb"
			location.Confidence = 0.8
		}

		// ASN double-check: compare iptoasn ASN with GeoLite2-ASN
		mmdbASN, mmdbOrg, err := reader.LookupASN(ip)
		if err == nil && mmdbASN != "" {
			if asnStr != "" && mmdbASN == asnStr {
				location.ASNAgreement = true
				location.Confidence = 0.95
			} else if asnStr == "" {
				// iptoasn had no result, use MMDB ASN
				location.ASN = mmdbASN
				location.Organization = mmdbOrg
				location.ASNAgreement = false
				location.Confidence = 0.7
			} else {
				// Disagreement — prefer iptoasn but note it
				location.ASNAgreement = false
				location.Confidence = 0.6
			}
		}
	}

	if s.redisClient != nil {
		s.saveToCache(ctx, ip, location)
	}

	return location, nil
}

func (s *GeoIPService) getFromCache(ctx context.Context, ip string) (*GeoLocation, error) {
	key := fmt.Sprintf("geoip:%s", ip)
	data, err := s.redisClient.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var location GeoLocation
	if err := json.Unmarshal([]byte(data), &location); err != nil {
		return nil, err
	}
	return &location, nil
}

func (s *GeoIPService) saveToCache(ctx context.Context, ip string, location *GeoLocation) {
	key := fmt.Sprintf("geoip:%s", ip)
	data, err := json.Marshal(location)
	if err != nil {
		log.Printf("[GeoIP] Failed to marshal location for cache: %v", err)
		return
	}
	if err := s.redisClient.Set(ctx, key, data, s.cacheTTL).Err(); err != nil {
		log.Printf("[GeoIP] Failed to save to cache: %v", err)
	}
}

// Close closes the service and its resources.
func (s *GeoIPService) Close() {
	s.mu.Lock()
	reader := s.mmdbReader
	s.mu.Unlock()

	if reader != nil {
		reader.Close()
	}
}

// UpdateMMDBReader atomically swaps the MMDB reader for hot-reload.
// Closes the old reader after swapping.
func (s *GeoIPService) UpdateMMDBReader(newReader *MMDBReader) error {
	s.mu.Lock()
	oldReader := s.mmdbReader
	s.mmdbReader = newReader
	s.mu.Unlock()

	if oldReader != nil {
		oldReader.Close()
	}

	return nil
}

// NormalizeCity normalizes city names for comparison.
func NormalizeCity(city string) string {
	city = strings.ToLower(strings.TrimSpace(city))
	city = strings.TrimPrefix(city, "г.")
	city = strings.TrimPrefix(city, "город ")
	return strings.TrimSpace(city)
}
