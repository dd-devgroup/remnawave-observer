package geodata

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// AutoLearner автоматическое обучение провайдеров
type AutoLearner struct {
	geoDataLoader *GeoDataLoader
	configDir     string // read-only: base providers.yaml
	dataDir       string // writable: overlay + backups
	interval      time.Duration
	minCount      int
	minConfidence string
	maxAddsPerRun int           // макс. добавлений за один цикл
	outputFile    string        // имя overlay файла в dataDir
	as2org        *AS2OrgLoader // CAIDA AS2Org (может быть nil)
	mu            sync.Mutex
}

// NewAutoLearner создает новый AutoLearner.
// as2org может быть nil — тогда CAIDA-нормализация не применяется.
func NewAutoLearner(geoDataLoader *GeoDataLoader, configDir, dataDir string, interval time.Duration, minCount int, minConfidence string, maxAddsPerRun int, outputFile string, as2org *AS2OrgLoader) *AutoLearner {
	if outputFile == "" {
		outputFile = "providers.learned.yaml"
	}
	if maxAddsPerRun <= 0 {
		maxAddsPerRun = 20
	}
	return &AutoLearner{
		geoDataLoader: geoDataLoader,
		configDir:     configDir,
		dataDir:       dataDir,
		interval:      interval,
		minCount:      minCount,
		minConfidence: minConfidence,
		maxAddsPerRun: maxAddsPerRun,
		outputFile:    outputFile,
		as2org:        as2org,
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
		confidence = al.boostConfidenceByCount(confidence, provider.Count)

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

	// Записываем overlay (base providers.yaml не трогается)
	if err := al.writeOverlay(newKeywords); err != nil {
		log.Printf("[AutoLearner] Ошибка записи overlay: %v", err)
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

// boostConfidenceByCount повышает уверенность на основе количества наблюдений.
// count >= 1000 → high (провайдер виден ≥1000 раз, достаточно для автодобавления).
// count >= 100  → не ниже medium.
func (al *AutoLearner) boostConfidenceByCount(confidence string, count int) string {
	switch {
	case count >= 1000:
		return "high"
	case count >= 100:
		if confidence == "very_low" || confidence == "low" {
			return "medium"
		}
		return confidence
	default:
		return confidence
	}
}

// extractKeyword извлекает ключевое слово из названия организации
func (al *AutoLearner) extractKeyword(org string) string {
	orgLower := strings.ToLower(org)

	// Убираем типичные префиксы и суффиксы ("asn" раньше "as", иначе "asn-..." → "n-...")
	orgLower = strings.TrimPrefix(orgLower, "asn")
	orgLower = strings.TrimPrefix(orgLower, "as")

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

// writeOverlay атомарно записывает новые ключевые слова в overlay файл (dataDir/providers.learned.yaml).
// Существующий overlay зачитывается и обновляется — base providers.yaml не трогается.
func (al *AutoLearner) writeOverlay(newKeywords map[string][]string) error {
	if err := os.MkdirAll(al.dataDir, 0755); err != nil {
		return fmt.Errorf("mkdirAll dataDir: %w", err)
	}

	overlayPath := filepath.Join(al.dataDir, al.outputFile)

	// Зачитываем существующий overlay (может не существовать)
	existing := &ProvidersConfig{Keywords: make(map[string][]string)}
	if data, err := os.ReadFile(overlayPath); err == nil {
		_ = yaml.Unmarshal(data, existing)
		if existing.Keywords == nil {
			existing.Keywords = make(map[string][]string)
		}
	}

	// Мержим новые ключевые слова
	for provType, keywords := range newKeywords {
		existing.Keywords[provType] = append(existing.Keywords[provType], keywords...)
	}

	data, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshal overlay: %w", err)
	}

	// Атомарная запись: tmp + rename
	tmpPath := overlayPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, overlayPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename tmp→overlay: %w", err)
	}

	log.Printf("[AutoLearner] Overlay обновлён: %s", overlayPath)
	return nil
}

// ReloadProviders перезагружает base providers.yaml + overlay (если есть).
func (l *GeoDataLoader) ReloadProviders() error {
	merged, err := l.loadProvidersWithOverlay()
	if err != nil {
		return err
	}

	l.mu.Lock()
	l.providers = merged
	l.mu.Unlock()

	log.Println("[GeoDataLoader] Конфигурация провайдеров успешно перезагружена (base + overlay)")
	return nil
}
