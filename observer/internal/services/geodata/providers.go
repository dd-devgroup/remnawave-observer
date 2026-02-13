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
	ASN          string    `json:"asn,omitempty"` // "AS34533" — заполняется из ClassifyWithCountry
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
func InitUnknownProvidersLog(dataDir string, enabled bool) {
	unknownLogOnce.Do(func() {
		if !enabled {
			unknownLog = &UnknownProvidersLog{enabled: false}
			return
		}

		// Создаем директорию если не существует
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			log.Printf("[UnknownProviders] Failed to create data directory: %v", err)
			unknownLog = &UnknownProvidersLog{enabled: false}
			return
		}

		logPath := filepath.Join(dataDir, "unknown_providers.json")
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

// UpdateUnknownProviderASN обновляет ASN поле для уже залогированного unknown provider.
// Если провайдер не найден в логе — no-op. Вызывается из ClassifyWithCountry.
func UpdateUnknownProviderASN(asn, organization string) {
	if unknownLog == nil || !unknownLog.enabled {
		return
	}
	unknownLog.mu.Lock()
	defer unknownLog.mu.Unlock()

	orgLower := strings.ToLower(organization)
	if provider, exists := unknownLog.providers[orgLower]; exists {
		if provider.ASN == "" {
			provider.ASN = asn
		}
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// ClassificationResult represents the result of provider classification.
type ClassificationResult struct {
	ProviderType    string
	Modifier        float64
	Confidence      float64  // 0.0–1.0
	Evidence        string   // "token_match", "heuristic", "default"
	MatchedKeywords []string
}

// legalSuffixes to strip from organization names during normalization.
var legalSuffixes = map[string]bool{
	"llc": true, "ltd": true, "inc": true, "gmbh": true, "ag": true,
	"sa": true, "pjsc": true, "ojsc": true, "jsc": true, "ooo": true,
	"zao": true, "pao": true, "corp": true, "corporation": true, "co": true,
}

// tokenStopwords are common words filtered during tokenization.
var tokenStopwords = map[string]bool{
	"the": true, "and": true, "or": true, "of": true, "for": true, "in": true,
	"de": true, "des": true, "du": true, "la": true, "le": true,
}

// normalizeOrgName normalizes an organization name for classification:
// casefold, remove legal suffixes, strip trailing punctuation/digits.
func normalizeOrgName(org string) string {
	org = strings.ToLower(strings.TrimSpace(org))
	words := strings.Fields(org)

	var filtered []string
	for _, w := range words {
		clean := strings.Trim(w, ".,;:\"'()")
		if legalSuffixes[clean] {
			continue
		}
		if clean != "" {
			filtered = append(filtered, clean)
		}
	}

	result := strings.Join(filtered, " ")
	result = strings.TrimRight(result, ".,;: 0123456789")
	return strings.TrimSpace(result)
}

// tokenizeOrg splits an organization name into tokens.
// Splits on spaces, hyphens, dots, underscores. Filters empty tokens and stopwords.
func tokenizeOrg(org string) []string {
	f := func(c rune) bool {
		return c == ' ' || c == '-' || c == '.' || c == '_' || c == ',' || c == ';'
	}
	parts := strings.FieldsFunc(org, f)

	var tokens []string
	for _, p := range parts {
		p = strings.Trim(p, ".,;:!?()[]{}\"'")
		if p == "" || len(p) < 2 {
			continue
		}
		if tokenStopwords[p] {
			continue
		}
		tokens = append(tokens, p)
	}
	return tokens
}

// matchTokens checks if all keyword tokens appear in the org tokens.
func matchTokens(orgTokens, keywordTokens []string) bool {
	if len(keywordTokens) == 0 {
		return false
	}
	orgSet := make(map[string]bool, len(orgTokens))
	for _, t := range orgTokens {
		orgSet[t] = true
	}
	for _, kt := range keywordTokens {
		if !orgSet[kt] {
			return false
		}
	}
	return true
}

// containsAnyToken checks if any needle token appears in the tokens slice.
func containsAnyToken(tokens []string, needles []string) bool {
	for _, t := range tokens {
		for _, n := range needles {
			if t == n {
				return true
			}
		}
	}
	return false
}

// classificationPriorityOrder defines the order in which provider types are checked.
var classificationPriorityOrder = []string{
	"mobile",
	"vpn_proxy",
	"hosting",
	"business",
	"infrastructure",
	"mobile_isp",
	"fixed",
	"isp",
	"regional_isp",
	"education",
	"cdn",
	"satellite",
}

// GetProviderType classifies a provider by organization name.
func (l *GeoDataLoader) GetProviderType(organization string) *ClassificationResult {
	return l.GetProviderTypeWithCountry(organization, "")
}

// GetProviderTypeWithCountry classifies a provider by organization name and country.
// Uses normalized token matching instead of substring matching.
func (l *GeoDataLoader) GetProviderTypeWithCountry(organization string, country string) *ClassificationResult {
	normalized := normalizeOrgName(organization)
	orgTokens := tokenizeOrg(normalized)

	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.providers == nil {
		return &ClassificationResult{
			ProviderType: "isp",
			Modifier:     1.0,
			Confidence:   0.3,
			Evidence:     "default",
		}
	}

	for _, provType := range classificationPriorityOrder {
		keywords, ok := l.providers.Keywords[provType]
		if !ok {
			continue
		}

		for _, keyword := range keywords {
			kwNormalized := strings.ToLower(strings.TrimSpace(keyword))
			kwTokens := tokenizeOrg(kwNormalized)

			if matchTokens(orgTokens, kwTokens) {
				modifier := 1.0
				if typeInfo, exists := l.providers.ProviderTypes[provType]; exists {
					modifier = typeInfo.Modifier
				}

				confidence := 0.8
				if provType == "vpn_proxy" || provType == "hosting" {
					confidence = 0.9
				}

				return &ClassificationResult{
					ProviderType:    provType,
					Modifier:        modifier,
					Confidence:      confidence,
					Evidence:        "token_match",
					MatchedKeywords: []string{keyword},
				}
			}
		}
	}

	// Heuristic analysis on normalized text
	if strings.Contains(normalized, "cloud") || strings.Contains(normalized, "server") ||
		strings.Contains(normalized, "datacenter") || strings.Contains(normalized, "data center") {
		modifier := 1.5
		if typeInfo, exists := l.providers.ProviderTypes["hosting"]; exists {
			modifier = typeInfo.Modifier
		}
		return &ClassificationResult{
			ProviderType:    "hosting",
			Modifier:        modifier,
			Confidence:      0.5,
			Evidence:        "heuristic",
			MatchedKeywords: []string{"pattern:cloud/server/datacenter"},
		}
	}

	if strings.Contains(normalized, "mobile") || strings.Contains(normalized, "cellular") ||
		strings.Contains(normalized, "wireless") {
		modifier := 0.5
		if typeInfo, exists := l.providers.ProviderTypes["mobile_isp"]; exists {
			modifier = typeInfo.Modifier
		}
		return &ClassificationResult{
			ProviderType:    "mobile_isp",
			Modifier:        modifier,
			Confidence:      0.5,
			Evidence:        "heuristic",
			MatchedKeywords: []string{"pattern:mobile/cellular/wireless"},
		}
	}

	if strings.Contains(normalized, "telecom") || strings.Contains(normalized, "telekom") {
		modifier := 1.0
		if typeInfo, exists := l.providers.ProviderTypes["isp"]; exists {
			modifier = typeInfo.Modifier
		}
		return &ClassificationResult{
			ProviderType:    "isp",
			Modifier:        modifier,
			Confidence:      0.5,
			Evidence:        "heuristic",
			MatchedKeywords: []string{"pattern:telecom"},
		}
	}

	// Default — unknown provider
	l.logUnknownProvider(organization, country)
	return &ClassificationResult{
		ProviderType: "isp",
		Modifier:     1.0,
		Confidence:   0.3,
		Evidence:     "default",
	}
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
