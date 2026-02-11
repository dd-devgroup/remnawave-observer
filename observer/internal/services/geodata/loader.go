package geodata

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// Coords представляет координаты (широта, долгота)
type Coords struct {
	Lat float64 `yaml:"lat"`
	Lon float64 `yaml:"lon"`
}

// Agglomeration представляет агломерацию городов
type Agglomeration struct {
	Country  string   `yaml:"country"`
	Center   Coords   `yaml:"center"`
	RadiusKM float64  `yaml:"radius_km"`
	Cities   []string `yaml:"cities"`
}

// AgglomerationsConfig конфигурация агломераций
type AgglomerationsConfig struct {
	Agglomerations map[string]Agglomeration `yaml:"agglomerations"`
	CityAliases    map[string]string        `yaml:"city_aliases"`
}

// ProviderTypeInfo информация о типе провайдера
type ProviderTypeInfo struct {
	Modifier    float64 `yaml:"modifier"`
	Description string  `yaml:"description"`
}

// ProvidersConfig конфигурация провайдеров
type ProvidersConfig struct {
	ProviderTypes map[string]ProviderTypeInfo `yaml:"provider_types"`
	Keywords      map[string][]string         `yaml:"keywords"`
}

// GeoDataLoader загрузчик географических данных
type GeoDataLoader struct {
	configDir      string // read-only: agglomerations.yaml, providers.yaml (base)
	dataDir        string // writable: providers.learned.yaml (overlay), backups
	agglomerations *AgglomerationsConfig
	providers      *ProvidersConfig
	mu             sync.RWMutex
}

// NewGeoDataLoader создает новый загрузчик географических данных.
// dataDir может быть пустым — тогда overlay не загружается.
func NewGeoDataLoader(configDir, dataDir string) (*GeoDataLoader, error) {
	loader := &GeoDataLoader{
		configDir: configDir,
		dataDir:   dataDir,
	}
	if err := loader.Load(); err != nil {
		return nil, err
	}
	return loader, nil
}

// Load загружает конфигурационные файлы
func (l *GeoDataLoader) Load() error {
	// Загрузка agglomerations.yaml
	agglPath := filepath.Join(l.configDir, "agglomerations.yaml")
	agglData, err := os.ReadFile(agglPath)
	if err != nil {
		return fmt.Errorf("failed to read agglomerations.yaml: %w", err)
	}

	var aggl AgglomerationsConfig
	if err := yaml.Unmarshal(agglData, &aggl); err != nil {
		return fmt.Errorf("failed to parse agglomerations.yaml: %w", err)
	}

	// Загрузка providers.yaml (base) + overlay (если есть)
	prov, err := l.loadProvidersWithOverlay()
	if err != nil {
		return err
	}

	l.mu.Lock()
	l.agglomerations = &aggl
	l.providers = prov
	l.mu.Unlock()

	log.Printf("[GeoData] Loaded %d agglomerations, %d provider types",
		len(aggl.Agglomerations), len(prov.ProviderTypes))
	return nil
}

// Reload перезагружает конфигурационные файлы без рестарта (hot reload)
func (l *GeoDataLoader) Reload() error {
	log.Printf("[GeoData] Reloading configuration...")
	return l.Load()
}

// GetAgglomerations возвращает копию агломераций
func (l *GeoDataLoader) GetAgglomerations() map[string]Agglomeration {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.agglomerations == nil {
		return make(map[string]Agglomeration)
	}

	// Возвращаем копию для безопасности
	result := make(map[string]Agglomeration, len(l.agglomerations.Agglomerations))
	for k, v := range l.agglomerations.Agglomerations {
		result[k] = v
	}
	return result
}

// GetProviders возвращает конфигурацию провайдеров
func (l *GeoDataLoader) GetProviders() *ProvidersConfig {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.providers
}

// loadProvidersWithOverlay загружает базовый providers.yaml и мержит overlay из dataDir.
// Overlay файл (providers.learned.yaml) — необязателен: если не существует, работаем только с базой.
func (l *GeoDataLoader) loadProvidersWithOverlay() (*ProvidersConfig, error) {
	provPath := filepath.Join(l.configDir, "providers.yaml")
	provData, err := os.ReadFile(provPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read providers.yaml: %w", err)
	}

	var prov ProvidersConfig
	if err := yaml.Unmarshal(provData, &prov); err != nil {
		return nil, fmt.Errorf("failed to parse providers.yaml: %w", err)
	}

	if l.dataDir == "" {
		return &prov, nil
	}

	overlayPath := filepath.Join(l.dataDir, "providers.learned.yaml")
	overlayData, err := os.ReadFile(overlayPath)
	if err != nil {
		// файл ещё не создан — нормально
		return &prov, nil
	}

	var overlay ProvidersConfig
	if err := yaml.Unmarshal(overlayData, &overlay); err != nil {
		log.Printf("[GeoData] Warning: failed to parse overlay %s: %v", overlayPath, err)
		return &prov, nil
	}

	if prov.Keywords == nil {
		prov.Keywords = make(map[string][]string)
	}
	merged := 0
	for provType, keywords := range overlay.Keywords {
		prov.Keywords[provType] = append(prov.Keywords[provType], keywords...)
		merged += len(keywords)
	}
	if merged > 0 {
		log.Printf("[GeoData] Merged %d keywords from overlay (%s)", merged, overlayPath)
	}

	return &prov, nil
}

// GetCityAliases возвращает копию алиасов городов
func (l *GeoDataLoader) GetCityAliases() map[string]string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.agglomerations == nil || l.agglomerations.CityAliases == nil {
		return make(map[string]string)
	}

	// Возвращаем копию
	result := make(map[string]string, len(l.agglomerations.CityAliases))
	for k, v := range l.agglomerations.CityAliases {
		result[k] = v
	}
	return result
}
