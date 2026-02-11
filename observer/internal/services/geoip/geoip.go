package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"observer_service/internal/services/asn"

	"github.com/redis/go-redis/v9"
)

// GeoLocation представляет геолокацию IP адреса
type GeoLocation struct {
	IP           string
	CountryCode  string
	City         string
	Region       string
	Latitude     float64
	Longitude    float64
	ASN          string
	Organization string
}

// GeoIPService сервис геолокации
type GeoIPService struct {
	asnLookup   *asn.ASNLookup
	redisClient *redis.Client
	cacheTTL    time.Duration
	httpClient  *http.Client
	rateLimiter *time.Ticker
	timeout     time.Duration // таймаут на один запрос к ip-api.com
}

// IPAPIResponse структура ответа от ip-api.com
type IPAPIResponse struct {
	Status      string  `json:"status"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	Region      string  `json:"region"`
	RegionName  string  `json:"regionName"`
	City        string  `json:"city"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	ISP         string  `json:"isp"`
	AS          string  `json:"as"`
	Message     string  `json:"message,omitempty"`
}

// NewGeoIPService создает новый GeoIP сервис.
// rateIntervalMs — минимальный интервал между запросами к ip-api.com в миллисекундах.
// timeout — таймаут на один HTTP-запрос.
func NewGeoIPService(asnLookup *asn.ASNLookup, redisClient *redis.Client, cacheTTL time.Duration, timeout time.Duration, rateIntervalMs int) *GeoIPService {
	if rateIntervalMs <= 0 {
		rateIntervalMs = 1350
	}
	return &GeoIPService{
		asnLookup:   asnLookup,
		redisClient: redisClient,
		cacheTTL:    cacheTTL,
		httpClient:  &http.Client{},
		timeout:     timeout,
		rateLimiter: time.NewTicker(time.Duration(rateIntervalMs) * time.Millisecond),
	}
}

// Lookup выполняет геолокацию IP адреса с учётом контекста.
func (s *GeoIPService) Lookup(ctx context.Context, ip string) (*GeoLocation, error) {
	if s.redisClient != nil {
		cached, err := s.getFromCache(ctx, ip)
		if err == nil && cached != nil {
			return cached, nil
		}
	}

	asnStr, countryCode, org, err := s.asnLookup.LookupFull(ip)
	if err != nil {
		log.Printf("[GeoIP] ASN lookup failed for %s: %v", ip, err)
		asnStr = ""
		countryCode = ""
		org = ""
	}

	location := &GeoLocation{
		IP:           ip,
		CountryCode:  countryCode,
		ASN:          asnStr,
		Organization: org,
	}

	if apiData := s.fetchFromIPAPI(ctx, ip); apiData != nil {
		location.City = apiData.City
		location.Region = apiData.RegionName
		location.Latitude = apiData.Lat
		location.Longitude = apiData.Lon
		if apiData.CountryCode != "" {
			location.CountryCode = apiData.CountryCode
		}
	}

	if s.redisClient != nil {
		s.saveToCache(ctx, ip, location)
	}

	return location, nil
}

// fetchFromIPAPI получает данные от ip-api.com с rate limiting без глобального мьютекса.
// Тикер-канал потоко-безопасен: каждый <-s.rateLimiter.C забирает ровно один тик,
// поэтому параллельные горутины ожидают свой тик независимо друг от друга.
func (s *GeoIPService) fetchFromIPAPI(ctx context.Context, ip string) *IPAPIResponse {
	select {
	case <-s.rateLimiter.C:
	case <-ctx.Done():
		log.Printf("[GeoIP] rate-limit wait cancelled for %s: %v", ip, ctx.Err())
		return nil
	}

	url := fmt.Sprintf("http://ip-api.com/json/%s?fields=status,message,country,countryCode,region,regionName,city,lat,lon,isp,as", ip)

	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		log.Printf("[GeoIP] request creation failed for %s: %v", ip, err)
		return nil
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		log.Printf("[GeoIP] ip-api.com request failed for %s: %v", ip, err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[GeoIP] ip-api.com returned status %d for %s", resp.StatusCode, ip)
		return nil
	}

	var apiResp IPAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		log.Printf("[GeoIP] Failed to decode ip-api.com response for %s: %v", ip, err)
		return nil
	}

	if apiResp.Status != "success" {
		log.Printf("[GeoIP] ip-api.com returned error for %s: %s", ip, apiResp.Message)
		return nil
	}

	return &apiResp
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

// Close закрывает сервис
func (s *GeoIPService) Close() {
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}
}

// NormalizeCity нормализует название города для сравнения
func NormalizeCity(city string) string {
	city = strings.ToLower(strings.TrimSpace(city))
	city = strings.TrimPrefix(city, "г.")
	city = strings.TrimPrefix(city, "город ")
	return strings.TrimSpace(city)
}
