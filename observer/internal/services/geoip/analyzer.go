package geoip

import (
	"context"
	"math"

	"observer_service/internal/services/geodata"
)

// GeoAnalysisResult результат анализа географии IP-адресов
type GeoAnalysisResult struct {
	UniqueCountries []string  `json:"unique_countries"`
	UniqueCities    []string  `json:"unique_cities"`
	Agglomerations  []string  `json:"agglomerations"`
	MaxDistanceKM   float64   `json:"max_distance_km"`
	GeoScore        int       `json:"geo_score"`        // 0-100
	GeoFlags        []string  `json:"geo_flags"`
}

// GeoAnalyzer анализатор географии
type GeoAnalyzer struct {
	geoService *GeoIPService
	geoData    *geodata.GeoDataLoader
}

// NewGeoAnalyzer создает новый анализатор географии
func NewGeoAnalyzer(geoService *GeoIPService, geoData *geodata.GeoDataLoader) *GeoAnalyzer {
	return &GeoAnalyzer{
		geoService: geoService,
		geoData:    geoData,
	}
}

// AnalyzeUserIPs анализирует список IP-адресов пользователя
func (a *GeoAnalyzer) AnalyzeUserIPs(ctx context.Context, ips []string) *GeoAnalysisResult {
	if len(ips) == 0 {
		return &GeoAnalysisResult{
			UniqueCountries: []string{},
			UniqueCities:    []string{},
			Agglomerations:  []string{},
			MaxDistanceKM:   0,
			GeoScore:        0,
			GeoFlags:        []string{},
		}
	}

	// Получаем геолокации для всех IP
	locations := make([]*GeoLocation, 0, len(ips))
	for _, ip := range ips {
		loc, err := a.geoService.Lookup(ctx, ip)
		if err == nil && loc != nil {
			locations = append(locations, loc)
		}
	}

	if len(locations) == 0 {
		return &GeoAnalysisResult{
			UniqueCountries: []string{},
			UniqueCities:    []string{},
			Agglomerations:  []string{},
			MaxDistanceKM:   0,
			GeoScore:        0,
			GeoFlags:        []string{},
		}
	}

	// Анализируем страны
	countries := make(map[string]bool)
	for _, loc := range locations {
		if loc.CountryCode != "" {
			countries[loc.CountryCode] = true
		}
	}

	// Анализируем города и агломерации
	cities := make(map[string]bool)
	agglomerations := make(map[string]bool)

	for _, loc := range locations {
		if loc.City != "" {
			normalizedCity := NormalizeCity(loc.City)
			cities[normalizedCity] = true

			// Проверяем принадлежность к агломерации
			agglName, _ := a.geoData.FindAgglomeration(normalizedCity)
			if agglName != "" {
				agglomerations[agglName] = true
			}
		}
	}

	// Вычисляем максимальное расстояние между точками
	maxDistance := a.calculateMaxDistance(locations)

	// Вычисляем geo score
	geoScore, geoFlags := a.calculateGeoScore(countries, cities, agglomerations, maxDistance)

	return &GeoAnalysisResult{
		UniqueCountries: mapKeysToSlice(countries),
		UniqueCities:    mapKeysToSlice(cities),
		Agglomerations:  mapKeysToSlice(agglomerations),
		MaxDistanceKM:   maxDistance,
		GeoScore:        geoScore,
		GeoFlags:        geoFlags,
	}
}

// calculateMaxDistance вычисляет максимальное расстояние между любыми двумя точками
func (a *GeoAnalyzer) calculateMaxDistance(locations []*GeoLocation) float64 {
	if len(locations) < 2 {
		return 0
	}

	maxDist := 0.0
	for i := 0; i < len(locations); i++ {
		for j := i + 1; j < len(locations); j++ {
			loc1 := locations[i]
			loc2 := locations[j]

			// Пропускаем если нет координат
			if loc1.Latitude == 0 && loc1.Longitude == 0 {
				continue
			}
			if loc2.Latitude == 0 && loc2.Longitude == 0 {
				continue
			}

			dist := HaversineDistance(loc1.Latitude, loc1.Longitude, loc2.Latitude, loc2.Longitude)
			if dist > maxDist {
				maxDist = dist
			}
		}
	}

	return maxDist
}

// calculateGeoScore вычисляет географический скор (0-100)
// Скоринг географии согласно плану:
// - Все IP в одном городе/агломерации: 0
// - Разные города в одной стране: +5
// - 2 страны: +15
// - 3+ страны: +30
// - 5+ разных городов: +20
func (a *GeoAnalyzer) calculateGeoScore(countries, cities, agglomerations map[string]bool, maxDistance float64) (int, []string) {
	score := 0
	flags := make([]string, 0)

	numCountries := len(countries)
	numCities := len(cities)
	numAgglomerations := len(agglomerations)

	// Проверка агломераций
	if numAgglomerations == 1 {
		// Все города в одной агломерации - это нормально
		score = 0
		flags = append(flags, "same_agglomeration")
		return score, flags
	}

	// Множественные страны
	if numCountries >= 3 {
		score += 30
		flags = append(flags, "multiple_countries")
	} else if numCountries == 2 {
		score += 15
		flags = append(flags, "two_countries")
	}

	// Разные города в одной стране
	if numCountries == 1 && numCities > 1 {
		score += 5
		flags = append(flags, "different_cities")
	}

	// Много разных городов
	if numCities >= 5 {
		score += 20
		flags = append(flags, "many_cities")
	}

	// Бонус за большое расстояние (> 1000 км)
	if maxDistance > 1000 {
		score += 10
		flags = append(flags, "long_distance")
	}

	// Ограничиваем максимальный скор
	if score > 100 {
		score = 100
	}

	return score, flags
}

// mapKeysToSlice конвертирует ключи карты в слайс
func mapKeysToSlice(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// CalculateGeoScoreSimple упрощенный расчет geo score без необходимости в GeoIPService
// Используется когда есть только базовая информация о странах и городах
func CalculateGeoScoreSimple(countries []string, cities []string) int {
	score := 0

	numCountries := len(countries)
	numCities := len(cities)

	// Множественные страны
	if numCountries >= 3 {
		score += 30
	} else if numCountries == 2 {
		score += 15
	}

	// Разные города в одной стране
	if numCountries == 1 && numCities > 1 {
		score += 5
	}

	// Много разных городов
	if numCities >= 5 {
		score += 20
	}

	return int(math.Min(float64(score), 100))
}
