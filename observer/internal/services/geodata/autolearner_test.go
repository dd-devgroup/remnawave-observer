package geodata

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// --- test helpers ---

func writeProvidersYAML(t *testing.T, dir string, cfg *ProvidersConfig) {
	t.Helper()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal providers: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "providers.yaml"), data, 0644); err != nil {
		t.Fatalf("write providers.yaml: %v", err)
	}
}

func writeAgglYAML(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "agglomerations.yaml"), []byte("agglomerations: {}\n"), 0644); err != nil {
		t.Fatalf("write agglomerations.yaml: %v", err)
	}
}

// setupTestUnknownLog подменяет глобальный unknownLog для теста.
// Тестирование — белый ящик (package geodata), параллельных тестов с этим синглтоном не запускаем.
func setupTestUnknownLog(t *testing.T, providers map[string]*UnknownProvider) {
	t.Helper()
	unknownLog = &UnknownProvidersLog{
		enabled:   true,
		providers: providers,
	}
	t.Cleanup(func() { unknownLog = nil })
}

func readOverlay(t *testing.T, dataDir string) *ProvidersConfig {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "providers.learned.yaml"))
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	var cfg ProvidersConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse overlay: %v", err)
	}
	return &cfg
}

// --- unit tests: pure functions ---

func TestParseASNToInt(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"AS34533", 34533},
		{"AS1", 1},
		{"34533", 34533}, // без префикса тоже работает
		{"AS0", 0},
		{"", 0},
		{"invalid", 0},
		{"AS", 0},
		{"ASXYZ", 0},
	}
	for _, tt := range tests {
		if got := parseASNToInt(tt.input); got != tt.want {
			t.Errorf("parseASNToInt(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestConfidenceRank(t *testing.T) {
	want := map[string]int{"very_low": 0, "low": 1, "medium": 2, "high": 3}
	for c, r := range want {
		if got := confidenceRank(c); got != r {
			t.Errorf("confidenceRank(%q) = %d, want %d", c, got, r)
		}
	}
	if got := confidenceRank("unknown"); got != 0 {
		t.Errorf("confidenceRank(\"unknown\") = %d, want 0", got)
	}
}

func TestEvidenceFromMatch(t *testing.T) {
	tests := []struct {
		name    string
		matched []string
		want    string
	}{
		{"nil", nil, "default"},
		{"default_marker", []string{"default"}, "default"},
		{"heuristic", []string{"pattern: cloud/server/datacenter"}, "heuristic"},
		{"keyword_single", []string{"digitalocean"}, "keyword"},
		{"keyword_multi", []string{"vpn", "proxy"}, "keyword"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evidenceFromMatch(tt.matched); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractKeyword(t *testing.T) {
	al := &AutoLearner{}
	tests := []struct {
		name string
		org  string
		want string
	}{
		{"simple", "Cloudflare Inc", "cloudflare"},
		{"AS_prefix", "AS12345 Hosting LLC", "hosting"},
		{"ASN_prefix", "ASN12345 Provider Corp", "provider"},
		{"all_stop_words", "Network Communications Inc", ""},
		{"stop_word_first_skip", "LLC Fastnet Corp", "fastnet"},
		{"suffix_as", "Megacloud-as Ltd", "megacloud"},
		{"suffix_net", "Fastnet-net ISP", "fastnet"},
		{"suffix_isp", "Telecom-isp Ltd", "telecom"},
		{"short_word", "AB Inc", ""},
		{"numeric_prefix", "123Meganet Ltd", "meganet"},
		{"pure_numbers", "12345 LLC Inc", ""},
		{"empty", "", ""},
		{"as_only", "AS", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := al.extractKeyword(tt.org); got != tt.want {
				t.Errorf("extractKeyword(%q) = %q, want %q", tt.org, got, tt.want)
			}
		})
	}
}

// TestAutoLearner_ClassifyProvider проверяет classifyProvider:
// iptoasn-only, CAIDA-wins, iptoasn-wins, оба-default.
func TestAutoLearner_ClassifyProvider(t *testing.T) {
	config := &ProvidersConfig{
		Keywords: map[string][]string{
			"hosting":   {"digitalocean"},
			"vpn_proxy": {"nordvpn"},
		},
	}
	al := &AutoLearner{}

	// iptoasn matches → keyword
	provType, conf, ev := al.classifyProvider("DigitalOcean LLC", "", config)
	if provType != "hosting" || conf != "high" || ev != "keyword" {
		t.Errorf("iptoasn hosting: type=%s conf=%s ev=%s", provType, conf, ev)
	}

	// CAIDA wins (iptoasn = default, CAIDA matches vpn_proxy)
	provType, conf, ev = al.classifyProvider("RandomOrg", "NordVPN Server Ltd", config)
	if provType != "vpn_proxy" || conf != "high" || ev != "caida" {
		t.Errorf("CAIDA vpn_proxy: type=%s conf=%s ev=%s", provType, conf, ev)
	}

	// iptoasn wins (hosting/high vs CAIDA very_low)
	provType, conf, ev = al.classifyProvider("DigitalOcean LLC", "SomeRandom", config)
	if provType != "hosting" || conf != "high" || ev != "keyword" {
		t.Errorf("iptoasn wins: type=%s conf=%s ev=%s", provType, conf, ev)
	}

	// Neither matches → default
	_, _, ev = al.classifyProvider("RandomOrg", "AnotherRandom", config)
	if ev != "default" {
		t.Errorf("both default: ev=%s", ev)
	}
}

// --- integration tests: performLearningCycle ---

// TestAutoLearner_MinCountThreshold: count=9 пропускается, count=10 добавляется.
// Заодно проверяется, что overlay идёт в dataDir, а не в configDir.
func TestAutoLearner_MinCountThreshold(t *testing.T) {
	configDir := t.TempDir()
	dataDir := t.TempDir()

	writeProvidersYAML(t, configDir, &ProvidersConfig{
		ProviderTypes: map[string]ProviderTypeInfo{"hosting": {Modifier: 1.5}},
		Keywords:      map[string][]string{},
	})
	writeAgglYAML(t, configDir)

	loader, err := NewGeoDataLoader(configDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	// Оба содержат "cloud" → heuristic evidence (anti-spam пропускает).
	// Различие только в count: 9 vs 10 при minCount=10.
	setupTestUnknownLog(t, map[string]*UnknownProvider{
		"below cloud": {Organization: "Below Cloud", Count: 9},
		"above cloud": {Organization: "Above Cloud", Count: 10},
	})

	al := NewAutoLearner(loader, configDir, dataDir, time.Hour, 10, "low", 20, "", nil)
	al.performLearningCycle()

	// overlay должен быть в dataDir, не в configDir
	if _, err := os.Stat(filepath.Join(configDir, "providers.learned.yaml")); !os.IsNotExist(err) {
		t.Error("overlay must not be written to configDir (read-only)")
	}

	overlay := readOverlay(t, dataDir)

	totalKeywords := 0
	for _, kws := range overlay.Keywords {
		totalKeywords += len(kws)
	}
	if totalKeywords != 1 {
		t.Errorf("expected 1 keyword, got %d: %v", totalKeywords, overlay.Keywords)
	}

	// extractKeyword("Above Cloud") → "above"
	found := false
	for _, kws := range overlay.Keywords {
		for _, kw := range kws {
			if kw == "above" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected keyword 'above', overlay: %v", overlay.Keywords)
	}
}

// TestAutoLearner_MaxAddsPerRun: 5 валидных провайдеров, maxAddsPerRun=2 → ровно 2 ключевых слова.
func TestAutoLearner_MaxAddsPerRun(t *testing.T) {
	configDir := t.TempDir()
	dataDir := t.TempDir()

	writeProvidersYAML(t, configDir, &ProvidersConfig{
		ProviderTypes: map[string]ProviderTypeInfo{"hosting": {Modifier: 1.5}},
		Keywords:      map[string][]string{},
	})
	writeAgglYAML(t, configDir)

	loader, err := NewGeoDataLoader(configDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	// 5 провайдеров: уникальные ключевые слова, все содержат server/cloud/datacenter → heuristic
	setupTestUnknownLog(t, map[string]*UnknownProvider{
		"megaserver inc":   {Organization: "Megaserver Inc", Count: 100},
		"serverbox ltd":    {Organization: "Serverbox Ltd", Count: 100},
		"cloudvault corp":  {Organization: "Cloudvault Corp", Count: 100},
		"datacenter pro":   {Organization: "Datacenter Pro", Count: 100},
		"supercloud group": {Organization: "Supercloud Group", Count: 100},
	})

	al := NewAutoLearner(loader, configDir, dataDir, time.Hour, 10, "low", 2, "", nil)
	al.performLearningCycle()

	overlay := readOverlay(t, dataDir)
	totalKeywords := 0
	for _, kws := range overlay.Keywords {
		totalKeywords += len(kws)
	}
	if totalKeywords != 2 {
		t.Errorf("maxAddsPerRun=2, expected 2 keywords, got %d: %v", totalKeywords, overlay.Keywords)
	}
}

// TestAutoLearner_AntiSpamNoCAIDA: провайдер без keyword/heuristic и без CAIDA → антиспам блокирует.
func TestAutoLearner_AntiSpamNoCAIDA(t *testing.T) {
	configDir := t.TempDir()
	dataDir := t.TempDir()

	writeProvidersYAML(t, configDir, &ProvidersConfig{
		ProviderTypes: map[string]ProviderTypeInfo{"isp": {Modifier: 1.0}},
		Keywords:      map[string][]string{},
	})
	writeAgglYAML(t, configDir)

	loader, err := NewGeoDataLoader(configDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	// "RandomXYZ Corp" — не содержит cloud/server/datacenter/mobile/telecom → evidence=default.
	// as2org=nil → hasCaida=false. Антиспам должен заблокировать.
	setupTestUnknownLog(t, map[string]*UnknownProvider{
		"randomxyz corp": {Organization: "RandomXYZ Corp", Count: 5000, ASN: "AS99999"},
	})

	al := NewAutoLearner(loader, configDir, dataDir, time.Hour, 10, "low", 20, "", nil)
	al.performLearningCycle()

	// overlay не должен существовать
	if _, err := os.Stat(filepath.Join(dataDir, "providers.learned.yaml")); !os.IsNotExist(err) {
		t.Error("overlay must not be created: anti-spam blocks provider with no CAIDA and default evidence")
	}
}

// TestAutoLearner_CAIDADrivenClassification: iptoasn org — нет match, но CAIDA orgName содержит
// vpncloud keyword → провайдер добавляется как vpn_proxy, keyword из iptoasn org.
func TestAutoLearner_CAIDADrivenClassification(t *testing.T) {
	configDir := t.TempDir()
	dataDir := t.TempDir()
	caidaDir := t.TempDir()

	writeProvidersYAML(t, configDir, &ProvidersConfig{
		ProviderTypes: map[string]ProviderTypeInfo{
			"vpn_proxy": {Modifier: 1.8},
			"isp":       {Modifier: 1.0},
		},
		Keywords: map[string][]string{
			"vpn_proxy": {"vpncloud"},
		},
	})
	writeAgglYAML(t, configDir)

	loader, err := NewGeoDataLoader(configDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	// CAIDA: ASN 88888 → orgName "VPNCloud Server Ltd"
	writeGzFixture(t, caidaDir, "# format:org_id|changed|org_name|country|source\nVPN-TEST|20200101|VPNCloud Server Ltd|US|TEST\n# format:aut|changed|aut_name|org_id|opaque_id|source\n88888|20200101|Test-AS|VPN-TEST|AS88888|TEST\n")
	as2org := NewAS2OrgLoader(caidaDir, "", 24*time.Hour)
	if err := as2org.loadFromFile(); err != nil {
		t.Fatal(err)
	}

	// iptoasn org "AS88888 RandomCorp" → no match (default evidence).
	// CAIDA orgName "VPNCloud Server Ltd" → matches "vpncloud" → vpn_proxy/high/caida.
	// Keyword из iptoasn: extractKeyword("AS88888 RandomCorp") → "randomcorp".
	setupTestUnknownLog(t, map[string]*UnknownProvider{
		"as88888 randomcorp": {Organization: "AS88888 RandomCorp", Count: 100, ASN: "AS88888"},
	})

	al := NewAutoLearner(loader, configDir, dataDir, time.Hour, 10, "low", 20, "", as2org)
	al.performLearningCycle()

	overlay := readOverlay(t, dataDir)

	// keyword "randomcorp" должен быть под vpn_proxy
	found := false
	for _, kw := range overlay.Keywords["vpn_proxy"] {
		if kw == "randomcorp" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected keyword 'randomcorp' under vpn_proxy, overlay: %v", overlay.Keywords)
	}
}

// --- PR6: Jaccard clustering and confidence conversion tests ---

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []string
		expected float64
	}{
		{"identical", []string{"mega", "telecom"}, []string{"mega", "telecom"}, 1.0},
		{"disjoint", []string{"foo"}, []string{"bar"}, 0.0},
		{"partial", []string{"mega", "telecom", "net"}, []string{"mega", "telecom"}, 2.0 / 3.0},
		{"empty_both", nil, nil, 0.0},
		{"empty_a", nil, []string{"foo"}, 0.0},
		{"single_overlap", []string{"cloud", "net"}, []string{"cloud", "host"}, 1.0 / 3.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jaccardSimilarity(tt.a, tt.b)
			if diff := got - tt.expected; diff > 0.001 || diff < -0.001 {
				t.Errorf("jaccardSimilarity(%v, %v) = %.4f, want %.4f", tt.a, tt.b, got, tt.expected)
			}
		})
	}
}

func TestGroupSimilarOrgs(t *testing.T) {
	orgs := []string{
		"MegaTelecom LLC",
		"MegaTelecom Inc",
		"TotallyDifferent Corp",
		"MegaTelecom Networks",
	}

	clusters := groupSimilarOrgs(orgs, 0.5)

	// MegaTelecom variants should cluster together
	if len(clusters) != 2 {
		t.Errorf("expected 2 clusters, got %d: %v", len(clusters), clusters)
		for i, c := range clusters {
			t.Logf("  cluster[%d]: canonical=%q members=%v", i, c.CanonicalName, c.Members)
		}
		return
	}

	// First cluster should have the 3 MegaTelecom variants
	megaCluster := clusters[0]
	if len(megaCluster.Members) != 3 {
		t.Errorf("expected MegaTelecom cluster with 3 members, got %d: %v", len(megaCluster.Members), megaCluster.Members)
	}
}

func TestGroupSimilarOrgs_AllDifferent(t *testing.T) {
	orgs := []string{"Alpha Corp", "Beta Corp", "Gamma Corp"}
	clusters := groupSimilarOrgs(orgs, 0.7)

	// With threshold 0.7, all should be separate (they only share stopwords removed by normalize)
	if len(clusters) != 3 {
		t.Errorf("expected 3 clusters for dissimilar orgs, got %d", len(clusters))
	}
}

func TestConfidenceToFloat(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"high", 0.9},
		{"medium", 0.6},
		{"low", 0.4},
		{"very_low", 0.2},
		{"unknown", 0.2},
	}
	for _, tt := range tests {
		if got := confidenceToFloat(tt.input); got != tt.want {
			t.Errorf("confidenceToFloat(%q) = %.1f, want %.1f", tt.input, got, tt.want)
		}
	}
}
