package scoring

import (
	"math"

	"observer_service/internal/services/asn"
	"observer_service/internal/services/geoip"
)

// ViolationScore результат расчета скора нарушения
type ViolationScore struct {
	RawScore   float64
	FinalScore float64
	Action     ViolationAction
	Components ScoreComponents
	Modifiers  []string
}

// ScoreComponents компоненты скора
type ScoreComponents struct {
	ASNScore   float64 // 0-100, вес 40%
	GeoScore   float64 // 0-100, вес 35%
	CountScore float64 // 0-100, вес 25%
}

// Scorer калькулятор скора
type Scorer struct {
	thresholds ScoreThresholds
}

// NewScorer создает новый калькулятор скора
func NewScorer(thresholds ScoreThresholds) *Scorer {
	return &Scorer{
		thresholds: thresholds,
	}
}

// NewDefaultScorer создает калькулятор с дефолтными порогами
func NewDefaultScorer() *Scorer {
	return NewScorer(DefaultThresholds())
}

// Calculate рассчитывает скор нарушения
func (s *Scorer) Calculate(
	asnInfos map[string]*asn.ASNClassification,
	geoResult *geoip.GeoAnalysisResult,
	uniqueCount int,
	limit int,
) *ViolationScore {
	// 1. ASN Score (0-100)
	asnScore := s.calculateASNScore(asnInfos)

	// 2. Geo Score (0-100)
	geoScore := float64(geoResult.GeoScore)

	// 3. Count Score (близость к лимиту, 0-100)
	countRatio := float64(uniqueCount) / float64(limit)
	countScore := math.Min(100, countRatio*100)

	// 4. Взвешенная сумма
	// Веса: ASN=40%, Geo=35%, Count=25%
	rawScore := asnScore*0.40 + geoScore*0.35 + countScore*0.25

	// 5. Применяем модификаторы
	finalScore := rawScore
	modifiers := make([]string, 0)

	// Паттерн mobile + home ISP = легитимно
	if s.hasMobileHomePattern(asnInfos) {
		finalScore *= 0.6
		modifiers = append(modifiers, "mobile_home_pattern")
	}

	// Один провайдер доминирует (≥70%)
	if s.singleProviderDominates(asnInfos) {
		finalScore *= 0.8
		modifiers = append(modifiers, "single_provider")
	}

	// VPN/Hosting = минимум 50 баллов
	if s.hasHighRiskProvider(asnInfos) {
		finalScore = math.Max(finalScore, 50)
		modifiers = append(modifiers, "high_risk_provider")
	}

	// Ограничиваем диапазон 0-100
	finalScore = math.Min(100, math.Max(0, finalScore))

	return &ViolationScore{
		RawScore:   rawScore,
		FinalScore: finalScore,
		Action:     s.thresholds.DetermineAction(finalScore),
		Components: ScoreComponents{
			ASNScore:   asnScore,
			GeoScore:   geoScore,
			CountScore: countScore,
		},
		Modifiers: modifiers,
	}
}

// calculateASNScore рассчитывает скор на основе типов провайдеров
func (s *Scorer) calculateASNScore(asnInfos map[string]*asn.ASNClassification) float64 {
	if len(asnInfos) == 0 {
		return 0
	}

	// Считаем средневзвешенный модификатор
	totalModifier := 0.0
	for _, info := range asnInfos {
		totalModifier += info.Modifier
	}

	avgModifier := totalModifier / float64(len(asnInfos))

	// Конвертируем модификатор в скор (0-100)
	// Модификаторы: 0.3 (mobile) - 1.8 (vpn_proxy)
	// 0.3 -> 0 баллов
	// 1.0 (isp) -> ~50 баллов
	// 1.8 -> 100 баллов
	score := ((avgModifier - 0.3) / (1.8 - 0.3)) * 100

	return math.Min(100, math.Max(0, score))
}

// hasMobileHomePattern проверяет паттерн mobile + home ISP
func (s *Scorer) hasMobileHomePattern(asnInfos map[string]*asn.ASNClassification) bool {
	hasMobile := false
	hasISP := false

	for _, info := range asnInfos {
		if info.IsMobileProvider() {
			hasMobile = true
		}
		if info.ProviderType == "isp" || info.ProviderType == "fixed" {
			hasISP = true
		}
	}

	return hasMobile && hasISP
}

// singleProviderDominates проверяет доминирует ли один провайдер
func (s *Scorer) singleProviderDominates(asnInfos map[string]*asn.ASNClassification) bool {
	if len(asnInfos) <= 1 {
		return true
	}

	// Подсчитываем частоту каждого ASN
	// Для упрощения считаем что каждый ASN встречается один раз
	// В реальной реализации нужно передавать частоты
	// threshold := float64(len(asnInfos)) * 0.7
	return false // Упрощенная реализация
}

// hasHighRiskProvider проверяет наличие VPN/Hosting провайдеров
func (s *Scorer) hasHighRiskProvider(asnInfos map[string]*asn.ASNClassification) bool {
	for _, info := range asnInfos {
		if info.IsHighRiskProvider() {
			return true
		}
	}
	return false
}

// GetScoreSummary возвращает текстовое описание скора
func (v *ViolationScore) GetScoreSummary() string {
	return GetActionDescription(v.Action)
}

// IsBlockingAction проверяет требуется ли блокировка
func (v *ViolationScore) IsBlockingAction() bool {
	return v.Action == ActionSoftBlock || v.Action == ActionBlock
}

// IsWarningAction проверяет требуется ли предупреждение
func (v *ViolationScore) IsWarningAction() bool {
	return v.Action == ActionWarn || v.Action == ActionSoftBlock || v.Action == ActionBlock
}
