package scoring

import (
	"math"
	"strconv"
)

// Feature interface for pluggable scoring components.
type Feature interface {
	Name() string
	Weight() float64
	Calculate(input *ScoringInput) FeatureResult
}

// FeatureResult represents the output of a single feature calculation.
type FeatureResult struct {
	Name       string
	Score      float64 // 0-100
	Weight     float64 // feature weight
	Confidence float64 // 0.0–1.0
	Details    string
}

// --- ASNFeature: provider risk analysis ---

// ASNFeature scores based on provider types (modifiers).
type ASNFeature struct{}

func (f *ASNFeature) Name() string    { return "asn" }
func (f *ASNFeature) Weight() float64 { return 0.30 }

func (f *ASNFeature) Calculate(input *ScoringInput) FeatureResult {
	if len(input.ASNClassifications) == 0 {
		return FeatureResult{Name: f.Name(), Score: 0, Weight: f.Weight(), Confidence: 0.3, Details: "no ASN data"}
	}

	totalModifier := 0.0
	minConfidence := 1.0
	for _, info := range input.ASNClassifications {
		totalModifier += info.Modifier
		if info.Confidence < minConfidence {
			minConfidence = info.Confidence
		}
	}

	avgModifier := totalModifier / float64(len(input.ASNClassifications))

	// Convert modifier to score: 0.3 (mobile) -> 0, 1.0 (isp) -> ~47, 1.8 (vpn) -> 100
	score := ((avgModifier - 0.3) / (1.8 - 0.3)) * 100
	score = math.Min(100, math.Max(0, score))

	return FeatureResult{
		Name:       f.Name(),
		Score:      score,
		Weight:     f.Weight(),
		Confidence: minConfidence,
		Details:    "avg_modifier=" + formatFloat(avgModifier),
	}
}

// --- GeoFeature: geographic dispersion ---

// GeoFeature scores based on geographic analysis results.
type GeoFeature struct{}

func (f *GeoFeature) Name() string    { return "geo" }
func (f *GeoFeature) Weight() float64 { return 0.55 }

func (f *GeoFeature) Calculate(input *ScoringInput) FeatureResult {
	if input.GeoResult == nil {
		return FeatureResult{Name: f.Name(), Score: 0, Weight: f.Weight(), Confidence: 0.3, Details: "no geo data"}
	}

	score := float64(input.GeoResult.GeoScore)
	score = math.Min(100, math.Max(0, score))

	confidence := 0.7
	if len(input.GeoResult.UniqueCountries) > 1 {
		confidence = 0.9
	}

	return FeatureResult{
		Name:       f.Name(),
		Score:      score,
		Weight:     f.Weight(),
		Confidence: confidence,
		Details:    "countries=" + itoa(len(input.GeoResult.UniqueCountries)),
	}
}

// --- ProviderMixFeature: hosting/VPN ratio modifier ---

// ProviderMixFeature checks for specific provider type patterns.
type ProviderMixFeature struct{}

func (f *ProviderMixFeature) Name() string    { return "provider_mix" }
func (f *ProviderMixFeature) Weight() float64 { return 0 } // modifier only, no weight

func (f *ProviderMixFeature) Calculate(input *ScoringInput) FeatureResult {
	if len(input.ASNClassifications) == 0 {
		return FeatureResult{Name: f.Name(), Score: 0, Confidence: 0.5, Details: "no data"}
	}

	highRiskCount := 0
	mobileCount := 0
	ispCount := 0
	total := len(input.ASNClassifications)

	for _, info := range input.ASNClassifications {
		if info.IsHighRiskProvider() {
			highRiskCount++
		}
		if info.IsMobileProvider() {
			mobileCount++
		}
		if info.ProviderType == "isp" || info.ProviderType == "fixed" || info.ProviderType == "regional_isp" {
			ispCount++
		}
	}

	highRiskRatio := float64(highRiskCount) / float64(total)
	// High ratio of VPN/hosting = high score
	score := highRiskRatio * 100

	details := "high_risk=" + itoa(highRiskCount) + "/" + itoa(total)

	// Mobile+ISP pattern is benign
	if mobileCount > 0 && ispCount > 0 && highRiskCount == 0 {
		score = 0
		details += ",mobile_home_pattern"
	}

	return FeatureResult{
		Name:       f.Name(),
		Score:      score,
		Weight:     f.Weight(),
		Confidence: 0.8,
		Details:    details,
	}
}

// --- IPDensityFeature: weighted IP density for non-mobile providers ---

// IPDensityFeature detects sharing by counting unique IPs per non-mobile ASN.
// Mobile providers (modifier <= 0.6) are excluded because CGNAT causes legitimate
// users to appear with many IPs. For fixed ISPs and above, a high IP count over
// the TTL window is a strong sharing signal.
type IPDensityFeature struct{}

func (f *IPDensityFeature) Name() string    { return "ip_density" }
func (f *IPDensityFeature) Weight() float64 { return 0.15 }

func (f *IPDensityFeature) Calculate(input *ScoringInput) FeatureResult {
	if len(input.IPsPerASN) == 0 || len(input.ASNClassifications) == 0 {
		return FeatureResult{Name: f.Name(), Score: 0, Weight: f.Weight(), Confidence: 0.5, Details: "no data"}
	}

	maxScore := 0.0
	maxIPCount := 0
	maxASN := ""
	nonMobileCount := 0

	for asnStr, ipCount := range input.IPsPerASN {
		classification, ok := input.ASNClassifications[asnStr]
		if !ok {
			continue
		}
		// Skip mobile providers — CGNAT/dynamic IP makes count meaningless.
		if classification.Modifier <= 0.6 {
			continue
		}
		nonMobileCount++

		// Threshold = how many IPs means "definitely sharing" for this provider type.
		//   modifier=0.7 (regional ISP) → threshold≈71
		//   modifier=1.0 (fixed ISP)    → threshold=50
		//   modifier=1.5 (corporate)    → threshold≈33
		//   modifier=1.8 (VPN/hosting)  → threshold≈28
		threshold := 50.0 / classification.Modifier
		asnScore := math.Min(100, (float64(ipCount)/threshold)*100)

		if asnScore > maxScore {
			maxScore = asnScore
			maxIPCount = ipCount
			maxASN = asnStr
		}
	}

	if nonMobileCount == 0 {
		return FeatureResult{Name: f.Name(), Score: 0, Weight: f.Weight(), Confidence: 0.8, Details: "all_mobile"}
	}

	confidence := 0.6
	if maxIPCount >= 10 {
		confidence = 0.8
	}
	if maxIPCount >= 20 {
		confidence = 0.9
	}

	details := "max_ips=" + itoa(maxIPCount) + "(" + maxASN + "),non_mobile=" + itoa(nonMobileCount)

	return FeatureResult{
		Name:       f.Name(),
		Score:      maxScore,
		Weight:     f.Weight(),
		Confidence: confidence,
		Details:    details,
	}
}

// --- helpers ---

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
