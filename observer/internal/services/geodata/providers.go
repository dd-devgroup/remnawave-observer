package geodata

import (
	"strings"
)

// GetProviderType определяет тип провайдера по названию организации
// Возвращает тип провайдера и его модификатор
func (l *GeoDataLoader) GetProviderType(organization string) (string, float64) {
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
