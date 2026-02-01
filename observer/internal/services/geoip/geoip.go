package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"observer_service/internal/services/asn"

	"github.com/redis/go-redis/v9"
)

// GeoLocation представляет геолокацию IP адреса
type GeoLocation struct {
	IP          string
	CountryCode string
	City        string
	Region      string
	Latitude    float64
	Longitude   float64
	ASN         string
	Organization string
}

// GeoIPService сервис геолокации
type GeoIPService struct {
	asnLookup   *asn.ASNLookup
	redisClient *redis.Client
	cacheTTL    time.Duration
	httpClient  *http.Client
	rateLimiter *time.Ticker
	mu          sync.Mutex
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

// NewGeoIPService создает новый GeoIP сервис
func NewGeoIPService(asnLookup *asn.ASNLookup, redisClient *redis.Client, cacheTTL time.Duration) *GeoIPService {
	return &GeoIPService{
		asnLookup:   asnLookup,
		redisClient: redisClient,
		cacheTTL:    cacheTTL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		// ip-api.com ограничение: 45 запросов в минуту
		// Используем rate limiter для безопасности
		rateLimiter: time.NewTicker(1350 * time.Millisecond), // ~44 req/min
	}
}

// Lookup выполняет геолокацию IP адреса
func (s *GeoIPService) Lookup(ip string) (*GeoLocation, error) {
	// Проверяем кэш Redis
	if s.redisClient != nil {
		cached, err := s.getFromCache(ip)
		if err == nil && cached != nil {
			return cached, nil
		}
	}

	// Получаем ASN, CountryCode и Organization из iptoasn.com (всегда доступно)
	asnStr, countryCode, org, err := s.asnLookup.LookupFull(ip)
	if err != nil {
		// Если ASN lookup не удался, возвращаем базовую информацию
		log.Printf("[GeoIP] ASN lookup failed for %s: %v", ip, err)
		asnStr = ""
		countryCode = ""
		org = ""
	}

	// Создаем базовую локацию
	location := &GeoLocation{
		IP:           ip,
		CountryCode:  countryCode,
		ASN:          asnStr,
		Organization: org,
	}

	// Пытаемся получить расширенную информацию от ip-api.com
	if apiData := s.fetchFromIPAPI(ip); apiData != nil {
		location.City = apiData.City
		location.Region = apiData.RegionName
		location.Latitude = apiData.Lat
		location.Longitude = apiData.Lon

		// Обновляем CountryCode если получили от API
		if apiData.CountryCode != "" {
			location.CountryCode = apiData.CountryCode
		}
	}

	// Сохраняем в кэш
	if s.redisClient != nil {
		s.saveToCache(ip, location)
	}

	return location, nil
}

// fetchFromIPAPI получает данные от ip-api.com с rate limiting
func (s *GeoIPService) fetchFromIPAPI(ip string) *IPAPIResponse {
	// Rate limiting
	s.mu.Lock()
	<-s.rateLimiter.C
	s.mu.Unlock()

	url := fmt.Sprintf("http://ip-api.com/json/%s?fields=status,message,country,countryCode,region,regionName,city,lat,lon,isp,as", ip)

	resp, err := s.httpClient.Get(url)
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

// getFromCache получает данные из Redis кэша
func (s *GeoIPService) getFromCache(ip string) (*GeoLocation, error) {
	ctx := context.Background()
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

// saveToCache сохраняет данные в Redis кэш
func (s *GeoIPService) saveToCache(ip string, location *GeoLocation) {
	ctx := context.Background()
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
