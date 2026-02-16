package geoip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"observer_service/internal/metrics"
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
	Source       string  // "mmdb", "iptoasn", "2ip", "mmdb+2ip", "iptoasn+2ip"
	Confidence   float64 // 0.0–1.0
	ASNAgreement bool    // true if iptoasn and GeoLite2-ASN agree on ASN
	CacheHit     bool    // true when result was returned from Redis cache
}

type fallbackTask struct {
	IP      string
	Attempt int
}

// GeoIPService provides geolocation using local MMDB files.
type GeoIPService struct {
	asnLookup        *asn.ASNLookup
	mmdbReader       *MMDBReader
	redisClient      *redis.Client
	cacheTTL         time.Duration
	fallbackProvider FallbackProvider
	fallbackMinDelay time.Duration
	fallbackCooldown time.Duration
	fallbackQueue    chan fallbackTask
	fallbackPending  map[string]struct{}
	fallbackRetryMax int
	fallbackRetryIn  time.Duration
	fallbackNextAt   time.Time
	fallbackPauseTo  time.Time
	mu               sync.RWMutex // protects mmdbReader/fallbackProvider for hot-reload
}

// NewGeoIPService creates a new GeoIP service using local MMDB files.
func NewGeoIPService(asnLookup *asn.ASNLookup, mmdbReader *MMDBReader, redisClient *redis.Client, cacheTTL time.Duration) *GeoIPService {
	return &GeoIPService{
		asnLookup:        asnLookup,
		mmdbReader:       mmdbReader,
		redisClient:      redisClient,
		cacheTTL:         cacheTTL,
		fallbackMinDelay: 1 * time.Second,
		fallbackCooldown: 1 * time.Minute,
		fallbackQueue:    make(chan fallbackTask, 5000),
		fallbackPending:  make(map[string]struct{}),
		fallbackRetryMax: 5,
		fallbackRetryIn:  30 * time.Second,
	}
}

// SetFallbackProvider sets external fallback Geo provider (e.g. 2IP).
func (s *GeoIPService) SetFallbackProvider(provider FallbackProvider) {
	s.mu.Lock()
	s.fallbackProvider = provider
	s.fallbackNextAt = time.Time{}
	s.fallbackPauseTo = time.Time{}
	s.mu.Unlock()
}

// SetFallbackRateLimit configures global fallback throttling.
// minDelay: minimum delay between fallback requests (0 disables spacing).
// cooldown: pause duration after HTTP 429 (0 disables cooldown pause).
func (s *GeoIPService) SetFallbackRateLimit(minDelay, cooldown time.Duration) {
	if minDelay < 0 {
		minDelay = 0
	}
	if cooldown < 0 {
		cooldown = 0
	}

	s.mu.Lock()
	s.fallbackMinDelay = minDelay
	s.fallbackCooldown = cooldown
	if minDelay == 0 {
		s.fallbackNextAt = time.Time{}
	}
	if cooldown == 0 {
		s.fallbackPauseTo = time.Time{}
	}
	s.mu.Unlock()
}

// ConfigureFallbackQueue configures async fallback queue.
// queueSize must be > 0, retryMax must be >= 0, retryIn must be > 0.
// This method is expected to be called during startup before worker launch.
func (s *GeoIPService) ConfigureFallbackQueue(queueSize, retryMax int, retryIn time.Duration) {
	if queueSize <= 0 {
		queueSize = 5000
	}
	if retryMax < 0 {
		retryMax = 0
	}
	if retryIn <= 0 {
		retryIn = 30 * time.Second
	}

	s.mu.Lock()
	s.fallbackQueue = make(chan fallbackTask, queueSize)
	s.fallbackPending = make(map[string]struct{})
	s.fallbackRetryMax = retryMax
	s.fallbackRetryIn = retryIn
	s.mu.Unlock()
}

func (s *GeoIPService) enqueueFallbackTask(ip string) bool {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.fallbackProvider == nil {
		return false
	}
	if s.fallbackQueue == nil {
		s.fallbackQueue = make(chan fallbackTask, 5000)
	}
	if s.fallbackPending == nil {
		s.fallbackPending = make(map[string]struct{})
	}
	if _, exists := s.fallbackPending[ip]; exists {
		return false
	}

	task := fallbackTask{IP: ip}
	select {
	case s.fallbackQueue <- task:
		s.fallbackPending[ip] = struct{}{}
		return true
	default:
		log.Printf("[GeoIP] Fallback queue is full (%d), dropped %s", cap(s.fallbackQueue), ip)
		return false
	}
}

func (s *GeoIPService) finishFallbackTask(ip string) {
	s.mu.Lock()
	delete(s.fallbackPending, ip)
	s.mu.Unlock()
}

func (s *GeoIPService) scheduleFallbackRetry(ctx context.Context, task fallbackTask, delay time.Duration) {
	if delay <= 0 {
		delay = s.fallbackRetryAfter()
	}

	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			s.finishFallbackTask(task.IP)
			return
		case <-timer.C:
		}

		s.mu.Lock()
		defer s.mu.Unlock()

		if s.fallbackProvider == nil {
			delete(s.fallbackPending, task.IP)
			return
		}
		if s.fallbackQueue == nil {
			delete(s.fallbackPending, task.IP)
			return
		}
		if _, exists := s.fallbackPending[task.IP]; !exists {
			return
		}

		select {
		case s.fallbackQueue <- task:
		default:
			log.Printf("[GeoIP] Fallback queue is full (%d), retry dropped for %s", cap(s.fallbackQueue), task.IP)
			delete(s.fallbackPending, task.IP)
		}
	}()
}

func (s *GeoIPService) fallbackRetryAfter() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	delay := 200 * time.Millisecond
	if s.fallbackMinDelay > 0 {
		delay = s.fallbackMinDelay
	}
	if !s.fallbackPauseTo.IsZero() && now.Before(s.fallbackPauseTo) {
		remaining := time.Until(s.fallbackPauseTo)
		if remaining > delay {
			delay = remaining
		}
	}
	if !s.fallbackNextAt.IsZero() && now.Before(s.fallbackNextAt) {
		remaining := time.Until(s.fallbackNextAt)
		if remaining > delay {
			delay = remaining
		}
	}
	if delay <= 0 {
		return 200 * time.Millisecond
	}
	return delay
}

func shouldRetryFallback(err error) bool {
	if err == nil {
		return false
	}
	if isTimeoutError(err) {
		return true
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == http.StatusTooManyRequests || statusErr.StatusCode >= http.StatusInternalServerError
	}
	return false
}

func isNoFreeRequestsError(err error) bool {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	if statusErr.StatusCode != http.StatusTooManyRequests {
		return false
	}

	body := strings.ToLower(strings.TrimSpace(statusErr.Body))
	if body == "" {
		return false
	}
	return strings.Contains(body, "no free requests at this moment")
}

func (s *GeoIPService) disableFallbackProvider(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.fallbackProvider == nil {
		return
	}

	name := s.fallbackProvider.Name()
	s.fallbackProvider = nil

	if strings.TrimSpace(reason) == "" {
		log.Printf("[GeoIP] Fallback provider %s disabled", name)
		return
	}
	log.Printf("[GeoIP] Fallback provider %s disabled: %s", name, reason)
}

func (s *GeoIPService) reserveFallbackProvider() FallbackProvider {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.fallbackProvider == nil {
		return nil
	}

	now := time.Now()
	if !s.fallbackPauseTo.IsZero() && now.Before(s.fallbackPauseTo) {
		return nil
	}
	if s.fallbackMinDelay > 0 && !s.fallbackNextAt.IsZero() && now.Before(s.fallbackNextAt) {
		return nil
	}
	if s.fallbackMinDelay > 0 {
		s.fallbackNextAt = now.Add(s.fallbackMinDelay)
	}

	return s.fallbackProvider
}

func (s *GeoIPService) activateFallbackCooldown(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.fallbackProvider == nil || s.fallbackCooldown <= 0 {
		return
	}

	now := time.Now()
	if !s.fallbackPauseTo.IsZero() && now.Before(s.fallbackPauseTo) {
		return
	}

	s.fallbackPauseTo = now.Add(s.fallbackCooldown)
	name := s.fallbackProvider.Name()
	if strings.TrimSpace(reason) == "" {
		log.Printf("[GeoIP] Fallback provider %s paused for %v", name, s.fallbackCooldown)
		return
	}
	log.Printf("[GeoIP] Fallback provider %s paused for %v: %s", name, s.fallbackCooldown, reason)
}

// StartFallbackWorker runs async fallback enrichment worker.
func (s *GeoIPService) StartFallbackWorker(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	s.mu.RLock()
	queue := s.fallbackQueue
	queueSize := 0
	if queue != nil {
		queueSize = cap(queue)
	}
	s.mu.RUnlock()

	if queue == nil {
		log.Printf("[GeoIP] Fallback worker disabled: queue is not configured")
		return
	}

	log.Printf("[GeoIP] Fallback worker started (queue size: %d)", queueSize)
	for {
		select {
		case <-ctx.Done():
			log.Printf("[GeoIP] Fallback worker stopping")
			return
		case task := <-queue:
			s.processFallbackTask(ctx, task)
		}
	}
}

func (s *GeoIPService) processFallbackTask(ctx context.Context, task fallbackTask) {
	if strings.TrimSpace(task.IP) == "" {
		return
	}

	var cached *GeoLocation
	var cacheErr error
	if s.redisClient != nil {
		cached, cacheErr = s.getFromCache(ctx, task.IP)
	}
	if cacheErr == nil && cached != nil && !needsFallbackGeo(cached) {
		s.finishFallbackTask(task.IP)
		return
	}

	fallback := s.reserveFallbackProvider()
	if fallback == nil {
		s.scheduleFallbackRetry(ctx, task, s.fallbackRetryAfter())
		return
	}

	fallbackLoc, fbErr := fallback.Lookup(ctx, task.IP)
	if fbErr != nil {
		if isTimeoutError(fbErr) {
			metrics.GeoIPFallbackLookupTimeout.Add(1)
		} else {
			metrics.GeoIPFallbackLookupFail.Add(1)
		}
		log.Printf("[GeoIP] Async fallback lookup failed for %s: %v", task.IP, fbErr)

		if isHTTPStatusCode(fbErr, http.StatusUnauthorized) {
			s.disableFallbackProvider("received 401 Unauthorized; check TWOIP_TOKEN and API balance")
			s.finishFallbackTask(task.IP)
			return
		}
		if isHTTPStatusCode(fbErr, http.StatusTooManyRequests) {
			if isNoFreeRequestsError(fbErr) {
				s.disableFallbackProvider("received 429 No free requests at this moment; check 2IP plan/token balance")
				s.finishFallbackTask(task.IP)
				return
			}
			s.activateFallbackCooldown("received 429 Too Many Requests")
		}

		s.mu.RLock()
		retryMax := s.fallbackRetryMax
		retryIn := s.fallbackRetryIn
		s.mu.RUnlock()

		if shouldRetryFallback(fbErr) && task.Attempt < retryMax {
			nextTask := fallbackTask{
				IP:      task.IP,
				Attempt: task.Attempt + 1,
			}
			delay := time.Duration(task.Attempt+1) * retryIn
			s.scheduleFallbackRetry(ctx, nextTask, delay)
			return
		}

		s.finishFallbackTask(task.IP)
		return
	}

	metrics.GeoIPFallbackLookupSuccess.Add(1)

	location := cached
	if location == nil {
		location = s.buildBaseLocation(task.IP)
		if location == nil {
			location = &GeoLocation{IP: task.IP}
		}
	}
	mergeFallbackLocation(location, fallbackLoc)

	if s.redisClient != nil {
		s.saveToCache(ctx, task.IP, location)
	}
	s.finishFallbackTask(task.IP)
}

func (s *GeoIPService) buildBaseLocation(ip string) *GeoLocation {
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

	if reader == nil {
		return location
	}

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
			// Disagreement - prefer iptoasn but note it
			location.ASNAgreement = false
			location.Confidence = 0.6
		}
	}

	return location
}

// Lookup performs geolocation for an IP address.
func (s *GeoIPService) Lookup(ctx context.Context, ip string) (*GeoLocation, error) {
	if s.redisClient != nil {
		cached, err := s.getFromCache(ctx, ip)
		if err == nil && cached != nil {
			cached.CacheHit = true
			// Keep original data source. Use "cache" only for legacy entries without source field.
			if cached.Source == "" {
				cached.Source = "cache"
			}
			if needsFallbackGeo(cached) {
				s.enqueueFallbackTask(ip)
			}
			return cached, nil
		}
	}

	location := s.buildBaseLocation(ip)

	// External fallback for incomplete geo results.
	// Trigger: city is empty OR coordinates are missing.
	if needsFallbackGeo(location) {
		fallback := s.reserveFallbackProvider()
		if fallback != nil {
			fallbackLoc, fbErr := fallback.Lookup(ctx, ip)
			if fbErr != nil {
				if isTimeoutError(fbErr) {
					metrics.GeoIPFallbackLookupTimeout.Add(1)
				} else {
					metrics.GeoIPFallbackLookupFail.Add(1)
				}
				log.Printf("[GeoIP] Fallback lookup failed for %s: %v", ip, fbErr)
				if isHTTPStatusCode(fbErr, http.StatusUnauthorized) {
					s.disableFallbackProvider("received 401 Unauthorized; check TWOIP_TOKEN and API balance")
				} else if isHTTPStatusCode(fbErr, http.StatusTooManyRequests) {
					if isNoFreeRequestsError(fbErr) {
						s.disableFallbackProvider("received 429 No free requests at this moment; check 2IP plan/token balance")
					} else {
						s.activateFallbackCooldown("received 429 Too Many Requests")
					}
				}
				s.enqueueFallbackTask(ip)
			} else if fallbackLoc != nil {
				metrics.GeoIPFallbackLookupSuccess.Add(1)
				mergeFallbackLocation(location, fallbackLoc)
				s.finishFallbackTask(ip)
			}
		} else {
			s.enqueueFallbackTask(ip)
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

func needsFallbackGeo(location *GeoLocation) bool {
	if location == nil {
		return false
	}
	return strings.TrimSpace(location.City) == "" || (location.Latitude == 0 && location.Longitude == 0)
}

func mergeFallbackLocation(location *GeoLocation, fallback *FallbackLocation) {
	if location == nil || fallback == nil {
		return
	}

	merged := false
	if location.CountryCode == "" && fallback.CountryCode != "" {
		location.CountryCode = fallback.CountryCode
		merged = true
	}
	if location.City == "" && fallback.City != "" {
		location.City = fallback.City
		merged = true
	}
	if location.Region == "" && fallback.Region != "" {
		location.Region = fallback.Region
		merged = true
	}
	if location.Latitude == 0 && location.Longitude == 0 && fallback.HasCoordinates {
		location.Latitude = fallback.Latitude
		location.Longitude = fallback.Longitude
		merged = true
	}
	if location.ASN == "" && fallback.ASN != "" {
		location.ASN = fallback.ASN
		merged = true
	}
	if location.Organization == "" && fallback.Organization != "" {
		location.Organization = fallback.Organization
		merged = true
	}
	if location.Confidence < fallback.Confidence {
		location.Confidence = fallback.Confidence
	}
	if merged {
		location.Source = appendSource(location.Source, fallback.Source)
	}
}

func appendSource(base, extra string) string {
	if strings.TrimSpace(extra) == "" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return extra
	}
	for _, part := range strings.Split(base, "+") {
		if strings.TrimSpace(part) == extra {
			return base
		}
	}
	return base + "+" + extra
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
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
