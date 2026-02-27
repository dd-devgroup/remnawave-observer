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
	// IPsPerASN holds the number of unique IPs seen per ASN in the current TTL window.
	// Used by IPDensityFeature to detect sharing via IP count on non-mobile providers.
	IPsPerASN map[string]int
}
