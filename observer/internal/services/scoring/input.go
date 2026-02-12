package scoring

import (
	"observer_service/internal/services/asn"
	"observer_service/internal/services/geoip"
)

// ScoringInput contains all data needed for scoring calculation.
type ScoringInput struct {
	ASNClassifications map[string]*asn.ASNClassification
	GeoResult          *geoip.GeoAnalysisResult
	UniqueCount        int
	Limit              int
}
