package geodata

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
)

// AutoLearner автоматическое обучение провайдеров
type AutoLearner struct {
	geoDataLoader   *GeoDataLoader
	configDir       string
	dataDir         string
	interval        time.Duration
	minCount        int
	minConfidence   string
	mu              sync.Mutex
}

// NewAutoLearner создает новый AutoLearner
func NewAutoLearner(geoDataLoader *GeoDataLoader, configDir string, dataDir string, interval time.Duration, minCount int, minConfidence string) *AutoLearner {
	return &AutoLearner{
		geoDataLoader: geoDataLoader,
		configDir:     configDir,
		dataDir:       dataDir,
		interval:      interval,
		minCount:      minCount,
		minConfidence: minConfidence,
	}
}

// Run запускает фоновую задачу автообучения
func (al *AutoLearner) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	log.Printf("[AutoLearner] Запущен с интервалом: %v, мин. количество: %d, мин. уверенность: %s",
		al.interval, al.minCount, al.minConfidence)

	// Первый запуск через 1 минуту после старта
	firstRun := time.NewTimer(1 * time.Minute)
	defer firstRun.Stop()

	ticker := time.NewTicker(al.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[AutoLearner] Остановка автообучения...")
			return

		case <-firstRun.C:
			log.Println("[AutoLearner] Запуск начального цикла обучения...")
			al.performLearningCycle()

		case <-ticker.C:
			log.Println("[AutoLearner] Запуск планового цикла обучения...")
			al.performLearningCycle()
		}
	}
}

// performLearningCycle выполняет цикл обучения
func (al *AutoLearner) performLearningCycle() {
	al.mu.Lock()
	defer al.mu.Unlock()

	// Получаем статистику неизвестных провайдеров
	unknownProviders := GetUnknownProvidersStats()
	if unknownProviders == nil || len(unknownProviders) == 0 {
		log.Println("[AutoLearner] Нет неизвестных провайдеров для обучения")
		return
	}

	log.Printf("[AutoLearner] Найдено %d неизвестных провайдеров, анализирую...", len(unknownProviders))

	// Загружаем текущий конфиг провайдеров
	providersFile := filepath.Join(al.configDir, "providers.yaml")
	providersData, err := os.ReadFile(providersFile)
	if err != nil {
		log.Printf("[AutoLearner] Ошибка чтения providers.yaml: %v", err)
		return
	}

	providersConfig := &ProvidersConfig{}
	if err := yaml.Unmarshal(providersData, providersConfig); err != nil {
		log.Printf("[AutoLearner] Ошибка парсинга providers.yaml: %v", err)
		return
	}

	// Анализируем и добавляем новых провайдеров
	addedCount := 0
	newKeywords := make(map[string][]string)

	for _, provider := range unknownProviders {
		if provider.Count < al.minCount {
			continue
		}

		suggestedType, confidence, _ := al.suggestProviderType(provider.Organization, providersConfig)

		// Проверяем уровень уверенности
		if !al.meetsConfidenceThreshold(confidence) {
			continue
		}

		// Извлекаем ключевое слово из названия организации
		keyword := al.extractKeyword(provider.Organization)
		if keyword == "" || len(keyword) < 3 {
			continue
		}

		// Проверяем, не существует ли уже такое ключевое слово
		if al.keywordExists(keyword, providersConfig) {
			continue
		}

		// Добавляем в список новых ключевых слов
		if newKeywords[suggestedType] == nil {
			newKeywords[suggestedType] = []string{}
		}
		newKeywords[suggestedType] = append(newKeywords[suggestedType], keyword)
		addedCount++

		log.Printf("[AutoLearner] Добавлен: %s -> %s (тип: %s, уверенность: %s, количество: %d)",
			provider.Organization, keyword, suggestedType, confidence, provider.Count)
	}

	if addedCount == 0 {
		log.Println("[AutoLearner] Нет новых провайдеров для добавления (все ниже порога уверенности)")
		return
	}

	// Обновляем конфиг
	if err := al.updateProvidersConfig(providersConfig, newKeywords); err != nil {
		log.Printf("[AutoLearner] Ошибка обновления providers.yaml: %v", err)
		return
	}

	// Перезагружаем конфиг
	if err := al.geoDataLoader.ReloadProviders(); err != nil {
		log.Printf("[AutoLearner] Ошибка перезагрузки конфига провайдеров: %v", err)
		return
	}

	log.Printf("[AutoLearner] ✅ Успешно добавлено %d новых ключевых слов провайдеров", addedCount)
}

// suggestProviderType предлагает тип провайдера
func (al *AutoLearner) suggestProviderType(org string, config *ProvidersConfig) (string, string, []string) {
	orgLower := strings.ToLower(org)
	matchedWords := []string{}

	// Проверяем ключевые слова для каждого типа
	typeMatches := make(map[string]int)

	priorityOrder := []string{
		"vpn_proxy",
		"hosting",
		"mobile",
		"mobile_isp",
		"business",
		"infrastructure",
		"fixed",
		"isp",
		"regional_isp",
	}

	for _, provType := range priorityOrder {
		keywords, ok := config.Keywords[provType]
		if !ok {
			continue
		}

		for _, keyword := range keywords {
			keywordLower := strings.ToLower(keyword)
			if strings.Contains(orgLower, keywordLower) {
				typeMatches[provType]++
				matchedWords = append(matchedWords, keyword)
				// Для VPN/hosting сразу возвращаем с высокой уверенностью
				if provType == "vpn_proxy" || provType == "hosting" {
					return provType, "high", matchedWords
				}
			}
		}
	}

	// Если есть совпадения - возвращаем тип с наибольшим количеством совпадений
	if len(typeMatches) > 0 {
		maxMatches := 0
		bestType := ""
		for provType, count := range typeMatches {
			if count > maxMatches {
				maxMatches = count
				bestType = provType
			}
		}
		return bestType, "medium", matchedWords
	}

	// Эвристический анализ
	if strings.Contains(orgLower, "cloud") || strings.Contains(orgLower, "server") ||
		strings.Contains(orgLower, "datacenter") || strings.Contains(orgLower, "data center") {
		return "hosting", "low", []string{"pattern: cloud/server/datacenter"}
	}

	if strings.Contains(orgLower, "mobile") || strings.Contains(orgLower, "cellular") ||
		strings.Contains(orgLower, "wireless") {
		return "mobile_isp", "low", []string{"pattern: mobile/cellular/wireless"}
	}

	if strings.Contains(orgLower, "telecom") || strings.Contains(orgLower, "telekom") {
		return "isp", "low", []string{"pattern: telecom"}
	}

	return "isp", "very_low", []string{"default"}
}

// meetsConfidenceThreshold проверяет уровень уверенности
func (al *AutoLearner) meetsConfidenceThreshold(confidence string) bool {
	switch al.minConfidence {
	case "high":
		return confidence == "high"
	case "medium":
		return confidence == "high" || confidence == "medium"
	case "low":
		return confidence == "high" || confidence == "medium" || confidence == "low"
	default:
		return confidence == "high"
	}
}

// extractKeyword извлекает ключевое слово из названия организации
func (al *AutoLearner) extractKeyword(org string) string {
	orgLower := strings.ToLower(org)

	// Убираем типичные префиксы и суффиксы
	orgLower = strings.TrimPrefix(orgLower, "as")
	orgLower = strings.TrimPrefix(orgLower, "asn")

	// Берем первое слово
	parts := strings.Fields(orgLower)
	if len(parts) == 0 {
		return ""
	}

	keyword := parts[0]

	// Убираем специальные символы
	keyword = strings.Trim(keyword, "-_.,;:!?()[]{}\"'")

	// Убираем числа в начале
	keyword = strings.TrimLeft(keyword, "0123456789")

	// Если слово слишком короткое или слишком длинное - пропускаем
	if len(keyword) < 3 || len(keyword) > 30 {
		return ""
	}

	return keyword
}

// keywordExists проверяет, существует ли уже ключевое слово
func (al *AutoLearner) keywordExists(keyword string, config *ProvidersConfig) bool {
	keywordLower := strings.ToLower(keyword)

	for _, keywords := range config.Keywords {
		for _, existingKeyword := range keywords {
			if strings.ToLower(existingKeyword) == keywordLower {
				return true
			}
		}
	}

	return false
}

// updateProvidersConfig обновляет конфиг провайдеров
func (al *AutoLearner) updateProvidersConfig(config *ProvidersConfig, newKeywords map[string][]string) error {
	// Добавляем новые ключевые слова
	for provType, keywords := range newKeywords {
		if config.Keywords[provType] == nil {
			config.Keywords[provType] = []string{}
		}
		config.Keywords[provType] = append(config.Keywords[provType], keywords...)
	}

	// Сохраняем обновленный конфиг
	providersFile := filepath.Join(al.configDir, "providers.yaml")

	// Создаем директорию для данных если не существует
	if err := os.MkdirAll(al.dataDir, 0755); err != nil {
		log.Printf("[AutoLearner] Предупреждение: не удалось создать data директорию: %v", err)
	}

	// Создаем резервную копию в data директории
	backupFile := filepath.Join(al.dataDir, "providers.yaml.backup")
	originalData, err := os.ReadFile(providersFile)
	if err != nil {
		return err
	}
	if err := os.WriteFile(backupFile, originalData, 0644); err != nil {
		log.Printf("[AutoLearner] Предупреждение: не удалось создать резервную копию: %v", err)
	}

	// Сохраняем обновленный конфиг
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	if err := os.WriteFile(providersFile, data, 0644); err != nil {
		return err
	}

	log.Printf("[AutoLearner] Обновлен providers.yaml (резервная копия сохранена в providers.yaml.backup)")
	return nil
}

// ReloadProviders перезагружает конфиг провайдеров
func (l *GeoDataLoader) ReloadProviders() error {
	providersFile := filepath.Join(l.configDir, "providers.yaml")
	providersData, err := os.ReadFile(providersFile)
	if err != nil {
		return err
	}

	newProviders := &ProvidersConfig{}
	if err := yaml.Unmarshal(providersData, newProviders); err != nil {
		return err
	}

	l.mu.Lock()
	l.providers = newProviders
	l.mu.Unlock()

	log.Println("[GeoDataLoader] Конфигурация провайдеров успешно перезагружена")
	return nil
}
