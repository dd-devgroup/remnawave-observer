package asn

import (
	"observer_service/internal/services/geodata"
)

// ASNClassification результат классификации ASN
type ASNClassification struct {
	ASN          string
	Organization string
	ProviderType string  // "mobile", "hosting", "vpn_proxy", "isp" и т.д.
	Modifier     float64 // 0.3 - 1.8
	Country      string
}

// ASNClassifier классификатор провайдеров
type ASNClassifier struct {
	geoData *geodata.GeoDataLoader
}

// NewASNClassifier создает новый классификатор провайдеров
func NewASNClassifier(geoData *geodata.GeoDataLoader) *ASNClassifier {
	return &ASNClassifier{
		geoData: geoData,
	}
}

// Classify классифицирует провайдера по ASN и названию организации
func (c *ASNClassifier) Classify(asn string, org string) *ASNClassification {
	return c.ClassifyWithCountry(asn, org, "")
}

// ClassifyWithCountry классифицирует провайдера с учетом страны
func (c *ASNClassifier) ClassifyWithCountry(asn string, org string, country string) *ASNClassification {
	providerType, modifier := c.geoData.GetProviderType(org)

	return &ASNClassification{
		ASN:          asn,
		Organization: org,
		ProviderType: providerType,
		Modifier:     modifier,
		Country:      country,
	}
}

// IsMobileProvider проверяет является ли провайдер мобильным
func (c *ASNClassification) IsMobileProvider() bool {
	return c.ProviderType == "mobile" || c.ProviderType == "mobile_isp"
}

// IsHighRiskProvider проверяет является ли провайдер высокорисковым (VPN/Hosting)
func (c *ASNClassification) IsHighRiskProvider() bool {
	return c.ProviderType == "vpn_proxy" || c.ProviderType == "hosting"
}

// IsBusinessProvider проверяет является ли провайдер корпоративным
func (c *ASNClassification) IsBusinessProvider() bool {
	return c.ProviderType == "business" || c.ProviderType == "infrastructure"
}

// GetRiskLevel возвращает уровень риска (low, medium, high)
func (c *ASNClassification) GetRiskLevel() string {
	if c.Modifier < 0.6 {
		return "low"
	} else if c.Modifier < 1.2 {
		return "medium"
	}
	return "high"
}
