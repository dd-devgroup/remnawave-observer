package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTwoIPBaseURL = "https://api.2ip.io"
)

// FallbackProvider enriches geolocation for IPs where MMDB data is incomplete.
type FallbackProvider interface {
	Name() string
	Lookup(ctx context.Context, ip string) (*FallbackLocation, error)
}

// FallbackLocation contains geolocation fields returned by fallback providers.
type FallbackLocation struct {
	CountryCode    string
	City           string
	Region         string
	Latitude       float64
	Longitude      float64
	HasCoordinates bool
	ASN            string
	Organization   string
	Source         string
	Confidence     float64
}

// TwoIPProvider enriches IP geo fields via api.2ip.io.
type TwoIPProvider struct {
	baseURL string
	token   string
	client  *http.Client
}

type twoIPASN struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type twoIPResponse struct {
	IP       string          `json:"ip"`
	City     string          `json:"city"`
	Lat      json.RawMessage `json:"lat"`
	Lon      json.RawMessage `json:"lon"`
	Country  string          `json:"country"`
	Code     string          `json:"code"`
	Timezone string          `json:"timezone"`
	ASN      twoIPASN        `json:"asn"`
}

// NewTwoIPProvider creates 2IP fallback provider.
func NewTwoIPProvider(baseURL, token string, timeout time.Duration) *TwoIPProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultTwoIPBaseURL
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &TwoIPProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   strings.TrimSpace(token),
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *TwoIPProvider) Name() string {
	return "2ip"
}

// Lookup fetches geolocation data for a specific IP via:
//
//	<baseURL>/<IP>?token=<token>
func (p *TwoIPProvider) Lookup(ctx context.Context, ip string) (*FallbackLocation, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil, fmt.Errorf("2ip lookup: empty ip")
	}
	if p.token == "" {
		return nil, fmt.Errorf("2ip lookup: token is empty")
	}

	endpoint := fmt.Sprintf("%s/%s", p.baseURL, url.PathEscape(ip))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("2ip lookup: new request: %w", err)
	}
	q := req.URL.Query()
	q.Set("token", p.token)
	req.URL.RawQuery = q.Encode()

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("2ip lookup: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("2ip lookup: bad status: %d", resp.StatusCode)
	}

	var payload twoIPResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("2ip lookup: decode response: %w", err)
	}

	lat, latSet := parseFlexibleFloat(payload.Lat)
	lon, lonSet := parseFlexibleFloat(payload.Lon)

	return &FallbackLocation{
		CountryCode:    strings.TrimSpace(payload.Code),
		City:           strings.TrimSpace(payload.City),
		Region:         "",
		Latitude:       lat,
		Longitude:      lon,
		HasCoordinates: latSet && lonSet,
		ASN:            normalizeASN(payload.ASN.ID),
		Organization:   strings.TrimSpace(payload.ASN.Name),
		Source:         p.Name(),
		Confidence:     0.85,
	}, nil
}

func parseFlexibleFloat(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	if string(raw) == "null" {
		return 0, false
	}

	var asFloat float64
	if err := json.Unmarshal(raw, &asFloat); err == nil {
		return asFloat, true
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return 0, false
	}
	asString = strings.TrimSpace(asString)
	if asString == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(asString, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func normalizeASN(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	upper := strings.ToUpper(id)
	if strings.HasPrefix(upper, "AS") {
		return upper
	}
	return "AS" + upper
}
