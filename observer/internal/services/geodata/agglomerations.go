package geodata

import (
	"strings"
)

// NormalizeCity нормализует название города
func (l *GeoDataLoader) NormalizeCity(city string) string {
	city = strings.ToLower(strings.TrimSpace(city))

	// Убираем префиксы
	city = strings.TrimPrefix(city, "г.")
	city = strings.TrimPrefix(city, "город ")
	city = strings.TrimSpace(city)

	// Проверяем алиасы
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.agglomerations != nil && l.agglomerations.CityAliases != nil {
		if canonical, ok := l.agglomerations.CityAliases[city]; ok {
			return canonical
		}
	}

	return city
}

// FindAgglomeration находит агломерацию по названию города
// Возвращает название агломерации и саму агломерацию
func (l *GeoDataLoader) FindAgglomeration(city string) (string, *Agglomeration) {
	normalized := l.NormalizeCity(city)

	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.agglomerations == nil {
		return "", nil
	}

	for name, aggl := range l.agglomerations.Agglomerations {
		for _, c := range aggl.Cities {
			if c == normalized {
				agglCopy := aggl
				return name, &agglCopy
			}
		}
	}

	return "", nil
}

// SameAgglomeration проверяет принадлежность городов к одной агломерации
func (l *GeoDataLoader) SameAgglomeration(city1, city2 string) bool {
	name1, _ := l.FindAgglomeration(city1)
	name2, _ := l.FindAgglomeration(city2)
	return name1 != "" && name1 == name2
}

// GetAgglomerationByName возвращает агломерацию по имени
func (l *GeoDataLoader) GetAgglomerationByName(name string) *Agglomeration {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.agglomerations == nil {
		return nil
	}

	if aggl, ok := l.agglomerations.Agglomerations[name]; ok {
		agglCopy := aggl
		return &agglCopy
	}

	return nil
}

// GetCountryAgglomerations возвращает все агломерации для страны
func (l *GeoDataLoader) GetCountryAgglomerations(countryCode string) map[string]Agglomeration {
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make(map[string]Agglomeration)
	if l.agglomerations == nil {
		return result
	}

	for name, aggl := range l.agglomerations.Agglomerations {
		if aggl.Country == countryCode {
			result[name] = aggl
		}
	}

	return result
}
