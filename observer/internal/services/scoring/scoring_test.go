package scoring

import (
	"math"
	"testing"

	"observer_service/internal/services/asn"
	"observer_service/internal/services/geoip"
)

// --- ASNFeature tests ---

func TestASNFeature_NoData(t *testing.T) {
	f := &ASNFeature{}
	result := f.Calculate(&ScoringInput{})
	if result.Score != 0 {
		t.Errorf("expected score 0, got %.2f", result.Score)
	}
	if result.Confidence != 0.3 {
		t.Errorf("expected confidence 0.3, got %.2f", result.Confidence)
	}
}

func TestASNFeature_MobileOnly(t *testing.T) {
	f := &ASNFeature{}
	result := f.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 0.3, ProviderType: "mobile", Confidence: 0.9},
		},
	})
	if result.Score != 0 {
		t.Errorf("expected score 0 for mobile modifier 0.3, got %.2f", result.Score)
	}
}

func TestASNFeature_ISP(t *testing.T) {
	f := &ASNFeature{}
	result := f.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 1.0, ProviderType: "isp", Confidence: 0.8},
		},
	})
	// (1.0 - 0.3) / (1.8 - 0.3) * 100 = 46.67
	expected := ((1.0 - 0.3) / (1.8 - 0.3)) * 100
	if math.Abs(result.Score-expected) > 0.1 {
		t.Errorf("expected score ~%.2f, got %.2f", expected, result.Score)
	}
}

func TestASNFeature_VPN(t *testing.T) {
	f := &ASNFeature{}
	result := f.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 1.8, ProviderType: "vpn_proxy", Confidence: 0.7},
		},
	})
	if result.Score != 100 {
		t.Errorf("expected score 100 for VPN modifier 1.8, got %.2f", result.Score)
	}
	if result.Confidence != 0.7 {
		t.Errorf("expected confidence 0.7, got %.2f", result.Confidence)
	}
}

func TestASNFeature_MinConfidence(t *testing.T) {
	f := &ASNFeature{}
	result := f.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 1.0, Confidence: 0.9},
			"AS2": {Modifier: 1.0, Confidence: 0.2},
		},
	})
	if result.Confidence != 0.2 {
		t.Errorf("expected min confidence 0.2, got %.2f", result.Confidence)
	}
}

// --- GeoFeature tests ---

func TestGeoFeature_NoData(t *testing.T) {
	f := &GeoFeature{}
	result := f.Calculate(&ScoringInput{})
	if result.Score != 0 {
		t.Errorf("expected score 0, got %.2f", result.Score)
	}
	if result.Confidence != 0.3 {
		t.Errorf("expected confidence 0.3, got %.2f", result.Confidence)
	}
}

func TestGeoFeature_SingleCountry(t *testing.T) {
	f := &GeoFeature{}
	result := f.Calculate(&ScoringInput{
		GeoResult: &geoip.GeoAnalysisResult{
			GeoScore:        20,
			UniqueCountries: []string{"RU"},
		},
	})
	if result.Score != 20 {
		t.Errorf("expected score 20, got %.2f", result.Score)
	}
	if result.Confidence != 0.7 {
		t.Errorf("expected confidence 0.7 for single country, got %.2f", result.Confidence)
	}
}

func TestGeoFeature_MultipleCountries(t *testing.T) {
	f := &GeoFeature{}
	result := f.Calculate(&ScoringInput{
		GeoResult: &geoip.GeoAnalysisResult{
			GeoScore:        80,
			UniqueCountries: []string{"RU", "DE", "US"},
		},
	})
	if result.Score != 80 {
		t.Errorf("expected score 80, got %.2f", result.Score)
	}
	if result.Confidence != 0.9 {
		t.Errorf("expected confidence 0.9 for multiple countries, got %.2f", result.Confidence)
	}
}

func TestGeoFeature_ScoreClamped(t *testing.T) {
	f := &GeoFeature{}
	result := f.Calculate(&ScoringInput{
		GeoResult: &geoip.GeoAnalysisResult{
			GeoScore:        150, // exceeds 100
			UniqueCountries: []string{"RU"},
		},
	})
	if result.Score != 100 {
		t.Errorf("expected score clamped to 100, got %.2f", result.Score)
	}
}

// --- CountFeature tests ---

func TestCountFeature_NoLimit(t *testing.T) {
	f := &CountFeature{}
	result := f.Calculate(&ScoringInput{Limit: 0, UniqueCount: 5})
	if result.Score != 0 {
		t.Errorf("expected score 0, got %.2f", result.Score)
	}
}

func TestCountFeature_HalfLimit(t *testing.T) {
	f := &CountFeature{}
	result := f.Calculate(&ScoringInput{Limit: 10, UniqueCount: 5})
	if result.Score != 50 {
		t.Errorf("expected score 50, got %.2f", result.Score)
	}
}

func TestCountFeature_AtLimit(t *testing.T) {
	f := &CountFeature{}
	result := f.Calculate(&ScoringInput{Limit: 10, UniqueCount: 10})
	if result.Score != 100 {
		t.Errorf("expected score 100, got %.2f", result.Score)
	}
}

func TestCountFeature_OverLimit(t *testing.T) {
	f := &CountFeature{}
	result := f.Calculate(&ScoringInput{Limit: 10, UniqueCount: 15})
	if result.Score != 100 {
		t.Errorf("expected score clamped to 100, got %.2f", result.Score)
	}
}

// --- ProviderMixFeature tests ---

func TestProviderMixFeature_NoData(t *testing.T) {
	f := &ProviderMixFeature{}
	result := f.Calculate(&ScoringInput{})
	if result.Score != 0 {
		t.Errorf("expected score 0, got %.2f", result.Score)
	}
}

func TestProviderMixFeature_AllHighRisk(t *testing.T) {
	f := &ProviderMixFeature{}
	result := f.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {ProviderType: "vpn_proxy"},
			"AS2": {ProviderType: "hosting"},
		},
	})
	if result.Score != 100 {
		t.Errorf("expected score 100 for all high-risk, got %.2f", result.Score)
	}
}

func TestProviderMixFeature_MobileHomePattern(t *testing.T) {
	f := &ProviderMixFeature{}
	result := f.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {ProviderType: "mobile"},
			"AS2": {ProviderType: "isp"},
		},
	})
	if result.Score != 0 {
		t.Errorf("expected score 0 for mobile+ISP pattern, got %.2f", result.Score)
	}
	if result.Details == "" {
		t.Error("expected details to contain mobile_home_pattern")
	}
}

func TestProviderMixFeature_MixedWithHighRisk(t *testing.T) {
	f := &ProviderMixFeature{}
	result := f.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {ProviderType: "mobile"},
			"AS2": {ProviderType: "isp"},
			"AS3": {ProviderType: "vpn_proxy"},
		},
	})
	// highRisk=1/3 ≈ 33.3, mobile_home_pattern NOT triggered because highRiskCount > 0
	expectedScore := (1.0 / 3.0) * 100
	if math.Abs(result.Score-expectedScore) > 0.1 {
		t.Errorf("expected score ~%.2f, got %.2f", expectedScore, result.Score)
	}
}

// --- Threshold tests ---

func TestDetermineAction_AllLevels(t *testing.T) {
	th := DefaultThresholds()

	tests := []struct {
		score    float64
		expected ViolationAction
	}{
		{0, ActionNone},
		{24.9, ActionNone},
		{25, ActionMonitor},
		{44.9, ActionMonitor},
		{45, ActionWarn},
		{59.9, ActionWarn},
		{60, ActionSoftChallenge},
		{74.9, ActionSoftChallenge},
		{75, ActionTempDisable},
		{89.9, ActionTempDisable},
		{90, ActionHardDisable},
		{100, ActionHardDisable},
	}

	for _, tt := range tests {
		action := th.DetermineAction(tt.score)
		if action != tt.expected {
			t.Errorf("score %.1f: expected %s, got %s", tt.score, tt.expected, action)
		}
	}
}

func TestDowngradeAction(t *testing.T) {
	tests := []struct {
		input    ViolationAction
		expected ViolationAction
	}{
		{ActionHardDisable, ActionTempDisable},
		{ActionTempDisable, ActionWarn},
		{ActionSoftChallenge, ActionWarn},
		{ActionWarn, ActionMonitor},
		{ActionMonitor, ActionNone},
		{ActionNone, ActionNone},
	}

	for _, tt := range tests {
		result := DowngradeAction(tt.input)
		if result != tt.expected {
			t.Errorf("DowngradeAction(%s): expected %s, got %s", tt.input, tt.expected, result)
		}
	}
}

// --- Scorer integration tests ---

func TestScorer_FullCalculation(t *testing.T) {
	scorer := NewDefaultScorer()
	result := scorer.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 1.0, ProviderType: "isp", Confidence: 0.8},
			"AS2": {Modifier: 0.3, ProviderType: "mobile", Confidence: 0.9},
		},
		GeoResult: &geoip.GeoAnalysisResult{
			GeoScore:        30,
			UniqueCountries: []string{"RU"},
		},
		UniqueCount: 5,
		Limit:       10,
	})

	if result.FinalScore < 0 || result.FinalScore > 100 {
		t.Errorf("FinalScore out of range: %.2f", result.FinalScore)
	}
	if len(result.Features) != 4 {
		t.Errorf("expected 4 feature results, got %d", len(result.Features))
	}
	if result.Confidence <= 0 || result.Confidence > 1 {
		t.Errorf("confidence out of range: %.2f", result.Confidence)
	}
}

func TestScorer_ConfidenceGating(t *testing.T) {
	scorer := NewScorer(ScoreThresholds{
		MonitorThreshold:       25,
		WarnThreshold:          45,
		SoftChallengeThreshold: 60,
		TempDisableThreshold:   75,
		HardDisableThreshold:   90,
	})

	// VPN with very low confidence should downgrade action
	result := scorer.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 1.8, ProviderType: "vpn_proxy", Confidence: 0.1},
		},
		GeoResult: &geoip.GeoAnalysisResult{
			GeoScore:        90,
			UniqueCountries: []string{"RU", "DE", "US"},
		},
		UniqueCount: 10,
		Limit:       10,
	})

	// Without confidence gating this would be hard_disable
	// With confidence 0.1 < 0.3, it should be downgraded
	if result.Confidence >= 0.3 {
		t.Errorf("expected confidence < 0.3, got %.2f", result.Confidence)
	}

	hasDowngrade := false
	for _, m := range result.Modifiers {
		if m == "low_confidence_downgrade" {
			hasDowngrade = true
		}
	}
	if !hasDowngrade {
		t.Error("expected low_confidence_downgrade modifier")
	}
}

func TestScorer_HighRiskModifier(t *testing.T) {
	scorer := NewDefaultScorer()

	// Pure VPN traffic — provider_mix should boost to at least 50
	result := scorer.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 1.8, ProviderType: "vpn_proxy", Confidence: 0.8},
		},
		GeoResult: &geoip.GeoAnalysisResult{
			GeoScore:        10,
			UniqueCountries: []string{"RU"},
		},
		UniqueCount: 2,
		Limit:       10,
	})

	if result.FinalScore < 50 {
		t.Errorf("expected FinalScore >= 50 due to high_risk_provider modifier, got %.2f", result.FinalScore)
	}

	hasHighRisk := false
	for _, m := range result.Modifiers {
		if m == "high_risk_provider" {
			hasHighRisk = true
		}
	}
	if !hasHighRisk {
		t.Error("expected high_risk_provider modifier")
	}
}

func TestScorer_MobileHomePattern(t *testing.T) {
	scorer := NewDefaultScorer()

	result := scorer.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 0.3, ProviderType: "mobile", Confidence: 0.9},
			"AS2": {Modifier: 1.0, ProviderType: "isp", Confidence: 0.8},
		},
		GeoResult: &geoip.GeoAnalysisResult{
			GeoScore:        20,
			UniqueCountries: []string{"RU"},
		},
		UniqueCount: 2,
		Limit:       10,
	})

	hasMobileHome := false
	for _, m := range result.Modifiers {
		if m == "mobile_home_pattern" {
			hasMobileHome = true
		}
	}
	if !hasMobileHome {
		t.Error("expected mobile_home_pattern modifier")
	}
}

func TestScorer_GetFeatureScore(t *testing.T) {
	scorer := NewDefaultScorer()
	result := scorer.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 1.0, ProviderType: "isp", Confidence: 0.8},
		},
		GeoResult: &geoip.GeoAnalysisResult{
			GeoScore:        50,
			UniqueCountries: []string{"RU"},
		},
		UniqueCount: 5,
		Limit:       10,
	})

	asnScore := result.GetFeatureScore("asn")
	if asnScore <= 0 {
		t.Errorf("expected asn score > 0, got %.2f", asnScore)
	}

	geoScore := result.GetFeatureScore("geo")
	if geoScore != 50 {
		t.Errorf("expected geo score 50, got %.2f", geoScore)
	}

	unknown := result.GetFeatureScore("nonexistent")
	if unknown != 0 {
		t.Errorf("expected 0 for nonexistent feature, got %.2f", unknown)
	}
}

func TestScorer_IsBlockingAction(t *testing.T) {
	v := &ViolationScore{Action: ActionTempDisable}
	if !v.IsBlockingAction() {
		t.Error("ActionTempDisable should be blocking")
	}

	v = &ViolationScore{Action: ActionHardDisable}
	if !v.IsBlockingAction() {
		t.Error("ActionHardDisable should be blocking")
	}

	v = &ViolationScore{Action: ActionWarn}
	if v.IsBlockingAction() {
		t.Error("ActionWarn should not be blocking")
	}

	v = &ViolationScore{Action: ActionNone}
	if v.IsBlockingAction() {
		t.Error("ActionNone should not be blocking")
	}
}

func TestScorer_IsWarningAction(t *testing.T) {
	v := &ViolationScore{Action: ActionWarn}
	if !v.IsWarningAction() {
		t.Error("ActionWarn should be warning")
	}

	v = &ViolationScore{Action: ActionSoftChallenge}
	if !v.IsWarningAction() {
		t.Error("ActionSoftChallenge should be warning")
	}

	v = &ViolationScore{Action: ActionMonitor}
	if v.IsWarningAction() {
		t.Error("ActionMonitor should not be warning")
	}
}

func TestScorer_NilGeoResult(t *testing.T) {
	scorer := NewDefaultScorer()
	result := scorer.Calculate(&ScoringInput{
		ASNClassifications: map[string]*asn.ASNClassification{
			"AS1": {Modifier: 1.0, ProviderType: "isp", Confidence: 0.8},
		},
		GeoResult:   nil,
		UniqueCount: 3,
		Limit:       10,
	})

	geoScore := result.GetFeatureScore("geo")
	if geoScore != 0 {
		t.Errorf("expected geo score 0 with nil GeoResult, got %.2f", geoScore)
	}
	// Should still produce a valid result
	if result.FinalScore < 0 || result.FinalScore > 100 {
		t.Errorf("FinalScore out of range: %.2f", result.FinalScore)
	}
}

func TestScorer_EmptyInput(t *testing.T) {
	scorer := NewDefaultScorer()
	result := scorer.Calculate(&ScoringInput{})

	if result.FinalScore != 0 {
		t.Errorf("expected FinalScore 0 for empty input, got %.2f", result.FinalScore)
	}
	if result.Action != ActionNone {
		t.Errorf("expected ActionNone for empty input, got %s", result.Action)
	}
}
