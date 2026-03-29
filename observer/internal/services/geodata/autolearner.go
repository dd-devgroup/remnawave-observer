package geodata

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"observer_service/internal/database"

	"gopkg.in/yaml.v3"
)

// OrgCluster represents a group of similar organization names.
type OrgCluster struct {
	CanonicalName string
	Members       []string
	ProposedType  string
	Confidence    float64
	Evidence      string
}

// AutoLearner автоматическое обучение провайдеров
type AutoLearner struct {
	geoDataLoader         *GeoDataLoader
	configDir             string // read-only: base providers.yaml
	dataDir               string // writable: overlay + backups
	interval              time.Duration
	minCount              int
	minConfidence         string
	maxAddsPerRun         int                 // макс. добавлений за один цикл
	outputFile            string              // имя overlay файла в dataDir
	as2org                *AS2OrgLoader       // CAIDA AS2Org (может быть nil)
	repo                  database.Repository // Postgres repository (может быть nil)
	minDistinctUsers      int                 // min distinct users for Postgres learning
	autoApproveThreshold  float64             // auto-approve confidence threshold
	mu                    sync.Mutex
}

// NewAutoLearner создает новый AutoLearner.
// as2org и repo могут быть nil.
func NewAutoLearner(geoDataLoader *GeoDataLoader, configDir, dataDir string, interval time.Duration, minCount int, minConfidence string, maxAddsPerRun int, outputFile string, as2org *AS2OrgLoader) *AutoLearner {
	if outputFile == "" {
		outputFile = "providers.learned.yaml"
	}
	if maxAddsPerRun <= 0 {
		maxAddsPerRun = 20
	}
	return &AutoLearner{
		geoDataLoader:        geoDataLoader,
		configDir:            configDir,
		dataDir:              dataDir,
		interval:             interval,
		minCount:             minCount,
		minConfidence:        minConfidence,
		maxAddsPerRun:        maxAddsPerRun,
		outputFile:           outputFile,
		as2org:               as2org,
		minDistinctUsers:     3,
		autoApproveThreshold: 0.8,
	}
}

// SetRepo sets the Postgres repository for Postgres-based learning.
func (al *AutoLearner) SetRepo(repo database.Repository) {
	al.repo = repo
}

// SetMinDistinctUsers sets the minimum distinct users threshold.
func (al *AutoLearner) SetMinDistinctUsers(n int) {
	if n > 0 {
		al.minDistinctUsers = n
	}
}

// SetAutoApproveThreshold sets the auto-approve confidence threshold.
func (al *AutoLearner) SetAutoApproveThreshold(t float64) {
	if t > 0 {
		al.autoApproveThreshold = t
	}
}

// Run запускает фоновую задачу автообучения
func (al *AutoLearner) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	log.Printf("[AutoLearner] Started with interval: %v, min count: %d, min confidence: %s",
		al.interval, al.minCount, al.minConfidence)

	// First run 1 minute after startup
	firstRun := time.NewTimer(1 * time.Minute)
	defer firstRun.Stop()

	ticker := time.NewTicker(al.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[AutoLearner] Auto-learning stopped")
			return

		case <-firstRun.C:
			log.Println("[AutoLearner] Starting initial learning cycle...")
			al.performLearningCycle()

		case <-ticker.C:
			log.Println("[AutoLearner] Starting scheduled learning cycle...")
			al.performLearningCycle()
		}
	}
}

// performLearningCycle выполняет цикл обучения.
// Uses Postgres-based learning when repo is available, falls back to in-memory log.
func (al *AutoLearner) performLearningCycle() {
	al.mu.Lock()
	defer al.mu.Unlock()

	// Prefer Postgres-based learning when repository is available
	if al.repo != nil {
		al.performPostgresLearningCycle()
		return
	}

	unknownProviders := GetUnknownProvidersStats()
	if len(unknownProviders) == 0 {
		log.Println("[AutoLearner] No unknown providers to learn")
		return
	}

	log.Printf("[AutoLearner] Found %d unknown providers, analyzing...", len(unknownProviders))

	// Snapshot merged config (base + overlay) — copy-on-write under RLock in GetProviders
	providersConfig := al.geoDataLoader.GetProviders()
	if providersConfig == nil {
		log.Println("[AutoLearner] Providers config not loaded")
		return
	}

	addedCount := 0
	newKeywords := make(map[string][]string)

	for _, provider := range unknownProviders {
		if addedCount >= al.maxAddsPerRun {
			log.Printf("[AutoLearner] Reached max adds per cycle (%d)", al.maxAddsPerRun)
			break
		}

		if provider.Count < al.minCount {
			continue
		}

		// CAIDA lookup по ASN
		asnNum := parseASNToInt(provider.ASN)
		var caidaOrgName, caidaCountry string
		hasCaida := false
		if al.as2org != nil && asnNum > 0 {
			orgName, country, _, ok := al.as2org.LookupOrgByASN(asnNum)
			if ok {
				caidaOrgName = orgName
				caidaCountry = country
				hasCaida = true
			}
		}

		// Классифицируем: iptoasn org + CAIDA orgName (если доступен)
		suggestedType, confidence, evidence := al.classifyProvider(provider.Organization, caidaOrgName, providersConfig)

		// Антиспам: без CAIDA и без сильных признаков (keyword/heuristic) — пропускаем
		if !hasCaida && evidence == "default" {
			continue
		}

		// Повышаем уверенность по количеству наблюдений
		boosted := al.boostConfidenceByCount(confidence, provider.Count)
		if boosted != confidence {
			evidence += "+count_boost"
			confidence = boosted
		}

		if !al.meetsConfidenceThreshold(confidence) {
			continue
		}

		// Keyword — из iptoasn org (именно то, что встречается в трафике)
		keyword := al.extractKeyword(provider.Organization)
		if keyword == "" {
			continue
		}

		if al.keywordExists(keyword, providersConfig) {
			continue
		}

		newKeywords[suggestedType] = append(newKeywords[suggestedType], keyword)
		addedCount++

		log.Printf("[AutoLearner] Added: %s -> %s (type: %s, confidence: %s, evidence: %s, count: %d, ASN: %s, CAIDA: %s/%s)",
			provider.Organization, keyword, suggestedType, confidence, evidence, provider.Count, provider.ASN, caidaOrgName, caidaCountry)
	}

	if addedCount == 0 {
		log.Println("[AutoLearner] No new providers to add")
		return
	}

	if err := al.writeOverlay(newKeywords); err != nil {
		log.Printf("[AutoLearner] Overlay write error: %v", err)
		return
	}

	if err := al.geoDataLoader.ReloadProviders(); err != nil {
		log.Printf("[AutoLearner] Providers config reload error: %v", err)
		return
	}

	log.Printf("[AutoLearner] ✅ Successfully added %d new provider keywords", addedCount)
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

// classifyProvider классифицирует провайдера, используя iptoasn org и (опционально) CAIDA orgName.
// Если CAIDA даёт более высокую уверенность — используется её тип; keyword всё равно берётся из iptoasn org.
// Возвращает: тип провайдера, уверенность, evidence ("keyword" | "caida" | "heuristic" | "default").
func (al *AutoLearner) classifyProvider(org, caidaOrg string, config *ProvidersConfig) (provType, confidence, evidence string) {
	t1, c1, matched1 := al.suggestProviderType(org, config)
	ev1 := evidenceFromMatch(matched1)

	if caidaOrg == "" {
		return t1, c1, ev1
	}

	t2, c2, matched2 := al.suggestProviderType(caidaOrg, config)
	ev2 := evidenceFromMatch(matched2)
	if ev2 != "default" {
		ev2 = "caida" // весь результат CAIDA-driven
	}

	if confidenceRank(c2) > confidenceRank(c1) {
		return t2, c2, ev2
	}
	return t1, c1, ev1
}

// evidenceFromMatch определяет источник доказательства по matched-списку из suggestProviderType.
func evidenceFromMatch(matched []string) string {
	if len(matched) == 0 || (len(matched) == 1 && matched[0] == "default") {
		return "default"
	}
	if len(matched) == 1 && strings.HasPrefix(matched[0], "pattern:") {
		return "heuristic"
	}
	return "keyword"
}

// confidenceRank возвращает численный ранг уверенности для сравнения.
func confidenceRank(c string) int {
	switch c {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default: // "very_low"
		return 0
	}
}

// parseASNToInt парсит строку вида "AS34533" в число 34533. Возвращает 0 при ошибке или пустой строке.
func parseASNToInt(asnStr string) int {
	s := strings.TrimPrefix(asnStr, "AS")
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// stopWords слова, которые не подходят в качестве ключевых слов провайдеров
var stopWords = map[string]bool{
	"llc": true, "ltd": true, "inc": true,
	"pjsc": true, "ojsc": true,
	"company": true, "corp": true, "corporation": true,
	"network": true, "communications": true,
	"group": true, "holding": true, "holdings": true,
}

// asnSuffixes суффиксы для удаления из ключевых слов провайдеров
var asnSuffixes = []string{"-as", "-net", "-isp"}

// extractKeyword extracts a keyword from an organization name.
// Uses normalizeOrgName for consistent normalization, then picks the first
// non-stopword token with length 3-30, stripping ASN suffixes (-as, -net, -isp).
func (al *AutoLearner) extractKeyword(org string) string {
	normalized := normalizeOrgName(org)

	// Strip AS/ASN-prefixes from start ("asn" before "as")
	normalized = strings.TrimPrefix(normalized, "asn")
	normalized = strings.TrimPrefix(normalized, "as")
	normalized = strings.TrimLeft(normalized, " -_")

	parts := strings.Fields(normalized)
	for _, part := range parts {
		word := strings.Trim(part, "-_.,;:!?()[]{}\"'")
		word = strings.TrimLeft(word, "0123456789")

		if len(word) < 3 || len(word) > 30 {
			continue
		}

		if stopWords[word] {
			continue
		}

		// Strip suffixes -as / -net / -isp if remainder >= 3 chars
		for _, suffix := range asnSuffixes {
			if strings.HasSuffix(word, suffix) && len(word)-len(suffix) >= 3 {
				word = strings.TrimSuffix(word, suffix)
				break
			}
		}

		if len(word) < 3 {
			continue
		}

		return word
	}

	return ""
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

	log.Printf("[AutoLearner] Overlay updated: %s", overlayPath)
	return nil
}

// jaccardSimilarity computes the Jaccard similarity between two token sets.
func jaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	setA := make(map[string]bool, len(a))
	for _, t := range a {
		setA[t] = true
	}
	setB := make(map[string]bool, len(b))
	for _, t := range b {
		setB[t] = true
	}

	intersection := 0
	for t := range setA {
		if setB[t] {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// groupSimilarOrgs groups organization names by Jaccard similarity on their tokens.
// Organizations with similarity >= threshold are placed in the same cluster.
func groupSimilarOrgs(orgs []string, threshold float64) []OrgCluster {
	tokenized := make([][]string, len(orgs))
	for i, org := range orgs {
		tokenized[i] = tokenizeOrg(normalizeOrgName(org))
	}

	used := make([]bool, len(orgs))
	var clusters []OrgCluster

	for i := 0; i < len(orgs); i++ {
		if used[i] {
			continue
		}
		cluster := OrgCluster{
			CanonicalName: orgs[i],
			Members:       []string{orgs[i]},
		}
		used[i] = true

		for j := i + 1; j < len(orgs); j++ {
			if used[j] {
				continue
			}
			if jaccardSimilarity(tokenized[i], tokenized[j]) >= threshold {
				cluster.Members = append(cluster.Members, orgs[j])
				used[j] = true
			}
		}

		clusters = append(clusters, cluster)
	}

	return clusters
}

// performPostgresLearningCycle runs a learning cycle using Postgres data.
// Queries distinct users per ASN/org, classifies unknowns, creates candidates,
// auto-approves high-confidence ones, and applies them to overlay.
func (al *AutoLearner) performPostgresLearningCycle() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stats, err := al.repo.GetASNOrgStats(ctx, al.minDistinctUsers)
	if err != nil {
		log.Printf("[AutoLearner] Postgres query failed: %v", err)
		return
	}

	if len(stats) == 0 {
		log.Println("[AutoLearner] No ASN/org stats meeting threshold")
		return
	}

	log.Printf("[AutoLearner] Found %d ASN/org pairs with >= %d distinct users", len(stats), al.minDistinctUsers)

	providersConfig := al.geoDataLoader.GetProviders()
	if providersConfig == nil {
		log.Println("[AutoLearner] Providers config not loaded")
		return
	}

	// Collect unknown orgs (those that classify as "default")
	var unknownOrgs []string
	orgStatsMap := make(map[string]database.ASNOrgStats)
	for _, stat := range stats {
		result := al.geoDataLoader.GetProviderTypeWithCountry(stat.OrgName, "")
		if result.Evidence == "default" {
			unknownOrgs = append(unknownOrgs, stat.OrgName)
			orgStatsMap[stat.OrgName] = stat
		}
	}

	if len(unknownOrgs) == 0 {
		log.Println("[AutoLearner] No unknown providers found in Postgres data")
		return
	}

	log.Printf("[AutoLearner] Found %d unknown orgs, clustering...", len(unknownOrgs))

	// Group similar org names
	clusters := groupSimilarOrgs(unknownOrgs, 0.7)

	addedCount := 0
	newKeywords := make(map[string][]string)

	for _, cluster := range clusters {
		if addedCount >= al.maxAddsPerRun {
			log.Printf("[AutoLearner] Reached max adds per cycle (%d)", al.maxAddsPerRun)
			break
		}

		// Use canonical (first) member for classification
		orgName := cluster.CanonicalName
		stat := orgStatsMap[orgName]

		// CAIDA lookup
		asnNum := parseASNToInt(stat.ASN)
		var caidaOrgName string
		hasCaida := false
		if al.as2org != nil && asnNum > 0 {
			cOrgName, _, _, ok := al.as2org.LookupOrgByASN(asnNum)
			if ok {
				caidaOrgName = cOrgName
				hasCaida = true
			}
		}

		suggestedType, confidence, evidence := al.classifyProvider(orgName, caidaOrgName, providersConfig)

		// Anti-spam: skip if no CAIDA and default evidence
		if !hasCaida && evidence == "default" {
			continue
		}

		boosted := al.boostConfidenceByCount(confidence, int(stat.TotalConns))
		if boosted != confidence {
			evidence += "+count_boost"
			confidence = boosted
		}

		if !al.meetsConfidenceThreshold(confidence) {
			continue
		}

		keyword := al.extractKeyword(orgName)
		if keyword == "" {
			continue
		}
		if al.keywordExists(keyword, providersConfig) {
			continue
		}

		// Create candidate in Postgres
		confFloat := confidenceToFloat(confidence)
		candidate := &database.LearningCandidate{
			ASN:            stat.ASN,
			OrgName:        orgName,
			NormalizedName: normalizeOrgName(orgName),
			ProposedType:   suggestedType,
			Confidence:     confFloat,
			Evidence:       evidence,
			DistinctUsers:  int(stat.DistinctUsers),
			TotalConns:     int(stat.TotalConns),
			Status:         "pending",
		}

		if err := al.repo.InsertCandidate(ctx, candidate); err != nil {
			log.Printf("[AutoLearner] Failed to insert candidate: %v", err)
			continue
		}

		// Auto-approve if confidence meets threshold
		if confFloat >= al.autoApproveThreshold {
			if err := al.repo.UpdateCandidateStatus(ctx, candidate.ID, "approved"); err != nil {
				log.Printf("[AutoLearner] Failed to approve candidate: %v", err)
				continue
			}

			newKeywords[suggestedType] = append(newKeywords[suggestedType], keyword)
			addedCount++

			log.Printf("[AutoLearner] Auto-approved: %s -> %s (type: %s, confidence: %.2f, evidence: %s, distinct_users: %d, cluster_size: %d)",
				orgName, keyword, suggestedType, confFloat, evidence, stat.DistinctUsers, len(cluster.Members))
		} else {
			log.Printf("[AutoLearner] Pending: %s (type: %s, confidence: %.2f, evidence: %s, distinct_users: %d)",
				orgName, suggestedType, confFloat, evidence, stat.DistinctUsers)
		}
	}

	if addedCount == 0 {
		log.Println("[AutoLearner] No new providers auto-approved")
		return
	}

	if err := al.writeOverlay(newKeywords); err != nil {
		log.Printf("[AutoLearner] Overlay write error: %v", err)
		return
	}

	if err := al.geoDataLoader.ReloadProviders(); err != nil {
		log.Printf("[AutoLearner] Providers config reload error: %v", err)
		return
	}

	log.Printf("[AutoLearner] Successfully auto-approved %d new provider keywords", addedCount)
}

// confidenceToFloat converts string confidence level to float64.
func confidenceToFloat(c string) float64 {
	switch c {
	case "high":
		return 0.9
	case "medium":
		return 0.6
	case "low":
		return 0.4
	default:
		return 0.2
	}
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

	log.Println("[GeoDataLoader] Providers config successfully reloaded (base + overlay)")
	return nil
}
