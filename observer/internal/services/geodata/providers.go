package geodata

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UnknownProvider информация о неизвестном провайдере
type UnknownProvider struct {
	Organization string    `json:"organization"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	Count        int       `json:"count"`
	Countries    []string  `json:"countries,omitempty"`
}

// UnknownProvidersLog лог неизвестных провайдеров
type UnknownProvidersLog struct {
	mu        sync.Mutex
	filePath  string
	providers map[string]*UnknownProvider
	enabled   bool
}

var unknownLog *UnknownProvidersLog
var unknownLogOnce sync.Once

// InitUnknownProvidersLog инициализирует систему логирования неизвестных провайдеров
func InitUnknownProvidersLog(configDir string, enabled bool) {
	unknownLogOnce.Do(func() {
		if !enabled {
			unknownLog = &UnknownProvidersLog{enabled: false}
			return
		}

		logPath := filepath.Join(configDir, "unknown_providers.json")
		unknownLog = &UnknownProvidersLog{
			filePath:  logPath,
			providers: make(map[string]*UnknownProvider),
			enabled:   true,
		}

		// Загружаем существующий лог
		if data, err := os.ReadFile(logPath); err == nil {
			if err := json.Unmarshal(data, &unknownLog.providers); err != nil {
				log.Printf("[UnknownProviders] Failed to load existing log: %v", err)
			} else {
				log.Printf("[UnknownProviders] Loaded %d unknown providers from log", len(unknownLog.providers))
			}
		}
	})
}

// logUnknownProvider добавляет провайдера в лог неизвестных
func (l *GeoDataLoader) logUnknownProvider(organization string, country string) {
	if unknownLog == nil || !unknownLog.enabled {
		return
	}

	unknownLog.mu.Lock()
	defer unknownLog.mu.Unlock()

	orgLower := strings.ToLower(organization)
	if provider, exists := unknownLog.providers[orgLower]; exists {
		provider.Count++
		provider.LastSeen = time.Now()
		if country != "" && !contains(provider.Countries, country) {
			provider.Countries = append(provider.Countries, country)
		}
	} else {
		countries := []string{}
		if country != "" {
			countries = append(countries, country)
		}
		unknownLog.providers[orgLower] = &UnknownProvider{
			Organization: organization,
			FirstSeen:    time.Now(),
			LastSeen:     time.Now(),
			Count:        1,
			Countries:    countries,
		}
		log.Printf("[UnknownProvider] New: %s (country: %s)", organization, country)
	}

	// Периодически сохраняем в файл (каждые 10 новых записей)
	if len(unknownLog.providers)%10 == 0 {
		unknownLog.save()
	}
}

// save сохраняет лог в файл
func (ul *UnknownProvidersLog) save() {
	if !ul.enabled {
		return
	}

	data, err := json.MarshalIndent(ul.providers, "", "  ")
	if err != nil {
		log.Printf("[UnknownProviders] Failed to marshal log: %v", err)
		return
	}

	if err := os.WriteFile(ul.filePath, data, 0644); err != nil {
		log.Printf("[UnknownProviders] Failed to save log: %v", err)
	}
}

// FlushUnknownProvidersLog принудительно сохраняет лог
func FlushUnknownProvidersLog() {
	if unknownLog != nil {
		unknownLog.mu.Lock()
		defer unknownLog.mu.Unlock()
		unknownLog.save()
	}
}

// GetUnknownProvidersStats возвращает статистику по неизвестным провайдерам
func GetUnknownProvidersStats() map[string]*UnknownProvider {
	if unknownLog == nil || !unknownLog.enabled {
		return nil
	}

	unknownLog.mu.Lock()
	defer unknownLog.mu.Unlock()

	// Возвращаем копию
	result := make(map[string]*UnknownProvider)
	for k, v := range unknownLog.providers {
		result[k] = v
	}
	return result
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// GetProviderType определяет тип провайдера по названию организации
// Возвращает тип провайдера и его модификатор
func (l *GeoDataLoader) GetProviderType(organization string) (string, float64) {
	return l.GetProviderTypeWithCountry(organization, "")
}

// GetProviderTypeWithCountry определяет тип провайдера с учетом страны
// Возвращает тип провайдера и его модификатор
func (l *GeoDataLoader) GetProviderTypeWithCountry(organization string, country string) (string, float64) {
	orgLower := strings.ToLower(organization)

	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.providers == nil {
		return "isp", 1.0
	}

	// Приоритетный порядок проверки
	priorityOrder := []string{
		"mobile",        // Сначала мобильные пулы (самый низкий риск)
		"vpn_proxy",     // VPN важнее хостинга
		"hosting",
		"business",
		"infrastructure",
		"mobile_isp",
		"fixed",
		"isp",
		"regional_isp",
	}

	for _, provType := range priorityOrder {
		keywords, ok := l.providers.Keywords[provType]
		if !ok {
			continue
		}

		for _, keyword := range keywords {
			if strings.Contains(orgLower, strings.ToLower(keyword)) {
				if typeInfo, exists := l.providers.ProviderTypes[provType]; exists {
					return provType, typeInfo.Modifier
				}
				// Если тип не найден в ProviderTypes, используем дефолт
				return provType, 1.0
			}
		}
	}

	// По умолчанию - ISP с модификатором 1.0
	// Логируем неизвестного провайдера для последующего анализа
	l.logUnknownProvider(organization, country)
	return "isp", 1.0
}

// GetProviderTypeInfo возвращает информацию о типе провайдера
func (l *GeoDataLoader) GetProviderTypeInfo(providerType string) *ProviderTypeInfo {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.providers == nil {
		return nil
	}

	if info, ok := l.providers.ProviderTypes[providerType]; ok {
		return &info
	}

	return nil
}

// GetAllProviderTypes возвращает список всех типов провайдеров
func (l *GeoDataLoader) GetAllProviderTypes() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.providers == nil {
		return []string{}
	}

	types := make([]string, 0, len(l.providers.ProviderTypes))
	for t := range l.providers.ProviderTypes {
		types = append(types, t)
	}

	return types
}

// GetKeywordsForType возвращает ключевые слова для типа провайдера
func (l *GeoDataLoader) GetKeywordsForType(providerType string) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.providers == nil {
		return []string{}
	}

	if keywords, ok := l.providers.Keywords[providerType]; ok {
		// Возвращаем копию
		result := make([]string, len(keywords))
		copy(result, keywords)
		return result
	}

	return []string{}
}
