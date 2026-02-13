package scoring

import (
	"math"
	"strings"
)

// ViolationScore is the result of a scoring calculation.
type ViolationScore struct {
	RawScore   float64
	FinalScore float64
	Confidence float64 // min confidence across all features
	Action     ViolationAction
	Features   []FeatureResult
	Modifiers  []string
}

// Scorer calculates violation scores using a feature registry.
type Scorer struct {
	thresholds ScoreThresholds
	features   []Feature
}

// NewScorer creates a new scorer with the given thresholds and default features.
func NewScorer(thresholds ScoreThresholds) *Scorer {
	return &Scorer{
		thresholds: thresholds,
		features: []Feature{
			&ASNFeature{},
			&GeoFeature{},
			&CountFeature{},
			&ProviderMixFeature{},
		},
	}
}

// NewDefaultScorer creates a scorer with default thresholds.
func NewDefaultScorer() *Scorer {
	return NewScorer(DefaultThresholds())
}

// Calculate computes the violation score from a ScoringInput.
func (s *Scorer) Calculate(input *ScoringInput) *ViolationScore {
	results := make([]FeatureResult, 0, len(s.features))
	for _, f := range s.features {
		results = append(results, f.Calculate(input))
	}

	// Weighted sum (only features with weight > 0)
	rawScore := 0.0
	for _, r := range results {
		rawScore += r.Score * r.Weight
	}

	// Apply modifiers from zero-weight features (e.g. ProviderMixFeature)
	finalScore := rawScore
	modifiers := make([]string, 0)

	for _, r := range results {
		if r.Weight == 0 && r.Score > 0 {
			// High-risk provider mix boosts minimum score
			if r.Name == "provider_mix" && r.Score > 50 {
				finalScore = math.Max(finalScore, 50)
				modifiers = append(modifiers, "high_risk_provider")
			}
		}
		if r.Weight == 0 && r.Score == 0 && r.Details != "" {
			if strings.Contains(r.Details, "mobile_home_pattern") {
				finalScore *= 0.6
				modifiers = append(modifiers, "mobile_home_pattern")
			}
		}
	}

	finalScore = math.Min(100, math.Max(0, finalScore))

	// Min confidence across all features
	minConfidence := 1.0
	for _, r := range results {
		if r.Confidence < minConfidence {
			minConfidence = r.Confidence
		}
	}

	// Determine action from score
	action := s.thresholds.DetermineAction(finalScore)

	// Confidence gating: if min confidence < 0.3, downgrade action
	if minConfidence < 0.3 {
		action = DowngradeAction(action)
		modifiers = append(modifiers, "low_confidence_downgrade")
	}

	return &ViolationScore{
		RawScore:   rawScore,
		FinalScore: finalScore,
		Confidence: minConfidence,
		Action:     action,
		Features:   results,
		Modifiers:  modifiers,
	}
}

// GetFeatureScore returns the score for a named feature, or 0 if not found.
func (v *ViolationScore) GetFeatureScore(name string) float64 {
	for _, f := range v.Features {
		if f.Name == name {
			return f.Score
		}
	}
	return 0
}

// GetScoreSummary returns a human-readable description of the action.
func (v *ViolationScore) GetScoreSummary() string {
	return GetActionDescription(v.Action)
}

// IsBlockingAction returns true if the action disables the user.
func (v *ViolationScore) IsBlockingAction() bool {
	return IsBlockingAction(v.Action)
}

// IsWarningAction returns true if the action is a warning or higher.
func (v *ViolationScore) IsWarningAction() bool {
	return IsWarningAction(v.Action)
}
