package geodata

import (
	"testing"
)

// --- normalizeOrgName ---

func TestNormalizeOrgName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "Cloudflare Inc", "cloudflare"},
		{"multiple_suffixes", "PJSC MegaFon LLC", "megafon"},
		{"trailing_punct", "Example Corp.", "example"},
		{"trailing_digits", "Provider 123", "provider"},
		{"mixed_case", "DigitalOcean, LLC", "digitalocean"},
		{"empty", "", ""},
		{"only_suffixes", "LLC Inc Corp", ""},
		{"no_suffixes", "Google Cloud Platform", "google cloud platform"},
		{"gmbh", "Hetzner Online GmbH", "hetzner online"},
		{"ooo", "OOO Rostelecom", "rostelecom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeOrgName(tt.input); got != tt.want {
				t.Errorf("normalizeOrgName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- tokenizeOrg ---

func TestTokenizeOrg(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"simple", "google cloud platform", []string{"google", "cloud", "platform"}},
		{"hyphens", "er-telecom", []string{"er", "telecom"}},
		{"dots", "reg.ru", []string{"reg", "ru"}},
		{"underscores", "my_provider", []string{"my", "provider"}},
		{"stopwords", "the cloud of things", []string{"cloud", "things"}},
		{"short_filtered", "a b cd efg", []string{"cd", "efg"}},
		{"empty", "", nil},
		{"mixed_seps", "foo-bar.baz_qux", []string{"foo", "bar", "baz", "qux"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenizeOrg(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("tokenizeOrg(%q) = %v, want %v", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenizeOrg(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- matchTokens ---

func TestMatchTokens(t *testing.T) {
	tests := []struct {
		name     string
		org, kw  []string
		expected bool
	}{
		{"exact_single", []string{"megafon"}, []string{"megafon"}, true},
		{"subset", []string{"google", "cloud", "platform"}, []string{"google", "cloud"}, true},
		{"no_match", []string{"megafon"}, []string{"beeline"}, false},
		{"partial_missing", []string{"google", "cloud"}, []string{"google", "azure"}, false},
		{"empty_kw", []string{"megafon"}, nil, false},
		{"empty_org", nil, []string{"megafon"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchTokens(tt.org, tt.kw); got != tt.expected {
				t.Errorf("matchTokens(%v, %v) = %v, want %v", tt.org, tt.kw, got, tt.expected)
			}
		})
	}
}

// --- containsAnyToken ---

func TestContainsAnyToken(t *testing.T) {
	tokens := []string{"google", "cloud", "platform"}
	if !containsAnyToken(tokens, []string{"cloud"}) {
		t.Error("expected true for 'cloud' in tokens")
	}
	if containsAnyToken(tokens, []string{"azure"}) {
		t.Error("expected false for 'azure' not in tokens")
	}
	if containsAnyToken(nil, []string{"cloud"}) {
		t.Error("expected false for nil tokens")
	}
}

// --- Classification with evidence ---

func TestClassification_TokenMatch(t *testing.T) {
	configDir := t.TempDir()
	dataDir := t.TempDir()

	writeProvidersYAML(t, configDir, &ProvidersConfig{
		ProviderTypes: map[string]ProviderTypeInfo{
			"hosting":   {Modifier: 1.5},
			"vpn_proxy": {Modifier: 1.8},
			"isp":       {Modifier: 1.0},
			"mobile":    {Modifier: 0.3},
		},
		Keywords: map[string][]string{
			"hosting":   {"digitalocean", "hetzner", "google cloud"},
			"vpn_proxy": {"nordvpn", "mullvad"},
			"isp":       {"rostelecom"},
			"mobile":    {"megafon"},
		},
	})
	writeAgglYAML(t, configDir)

	loader, err := NewGeoDataLoader(configDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		org          string
		wantType     string
		wantEvidence string
		wantMinConf  float64
	}{
		{"hosting_simple", "DigitalOcean LLC", "hosting", "token_match", 0.9},
		{"vpn_exact", "NordVPN Ltd", "vpn_proxy", "token_match", 0.9},
		{"isp_match", "PJSC Rostelecom", "isp", "token_match", 0.8},
		{"mobile_match", "MegaFon PJSC", "mobile", "token_match", 0.8},
		{"multi_word_keyword", "Google Cloud Platform Inc", "hosting", "token_match", 0.9},
		{"heuristic_cloud", "AcmeCloud Services", "hosting", "heuristic", 0.5},
		{"heuristic_telecom", "NewTelecom Group", "isp", "heuristic", 0.5},
		{"default_unknown", "Random Company XYZ", "isp", "default", 0.3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := loader.GetProviderTypeWithCountry(tt.org, "")
			if result.ProviderType != tt.wantType {
				t.Errorf("ProviderType = %q, want %q", result.ProviderType, tt.wantType)
			}
			if result.Evidence != tt.wantEvidence {
				t.Errorf("Evidence = %q, want %q", result.Evidence, tt.wantEvidence)
			}
			if result.Confidence < tt.wantMinConf {
				t.Errorf("Confidence = %.2f, want >= %.2f", result.Confidence, tt.wantMinConf)
			}
		})
	}
}

func TestClassification_NoSubstringFalsePositive(t *testing.T) {
	configDir := t.TempDir()
	dataDir := t.TempDir()

	writeProvidersYAML(t, configDir, &ProvidersConfig{
		ProviderTypes: map[string]ProviderTypeInfo{
			"hosting": {Modifier: 1.5},
			"isp":     {Modifier: 1.0},
		},
		Keywords: map[string][]string{
			"hosting": {"hetzner"},
		},
	})
	writeAgglYAML(t, configDir)

	loader, err := NewGeoDataLoader(configDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	// "hetzner" should NOT match "nethetznerdata" with token matching
	// (it would with strings.Contains)
	result := loader.GetProviderTypeWithCountry("Nethetznerdata Corp", "")
	if result.ProviderType == "hosting" && result.Evidence == "token_match" {
		t.Error("expected no false positive: 'hetzner' should not match 'nethetznerdata' via token match")
	}
}

func TestClassification_NilProviders(t *testing.T) {
	loader := &GeoDataLoader{}
	result := loader.GetProviderTypeWithCountry("Any Org", "US")
	if result.ProviderType != "isp" {
		t.Errorf("expected 'isp' for nil providers, got %q", result.ProviderType)
	}
	if result.Confidence != 0.3 {
		t.Errorf("expected confidence 0.3 for nil providers, got %.2f", result.Confidence)
	}
}

func TestClassification_MatchedKeywords(t *testing.T) {
	configDir := t.TempDir()
	dataDir := t.TempDir()

	writeProvidersYAML(t, configDir, &ProvidersConfig{
		ProviderTypes: map[string]ProviderTypeInfo{
			"hosting": {Modifier: 1.5},
		},
		Keywords: map[string][]string{
			"hosting": {"digitalocean"},
		},
	})
	writeAgglYAML(t, configDir)

	loader, err := NewGeoDataLoader(configDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	result := loader.GetProviderTypeWithCountry("DigitalOcean, LLC", "")
	if len(result.MatchedKeywords) != 1 || result.MatchedKeywords[0] != "digitalocean" {
		t.Errorf("MatchedKeywords = %v, want [\"digitalocean\"]", result.MatchedKeywords)
	}
}
