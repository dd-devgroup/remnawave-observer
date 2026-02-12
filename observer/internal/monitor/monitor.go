package monitor

import (
	"context"
	"fmt"
	"log"
	"math"
	"observer_service/internal/config"
	"observer_service/internal/models"
	"observer_service/internal/services/geoip"
	"observer_service/internal/services/storage"
	"sort"
	"strings"
	"sync"
	"time"
)

type geoCacheEntry struct {
	loc *geoip.GeoLocation
	err error
}

// PoolMonitor выполняет периодический мониторинг пулов IP.
type PoolMonitor struct {
	storage       storage.IPStorage
	cfg           *config.Config
	geoService    *geoip.GeoIPService
	geoCache      map[string]*geoCacheEntry
	geoCacheMu    sync.Mutex
}

// NewPoolMonitor создает новый экземпляр PoolMonitor.
func NewPoolMonitor(s storage.IPStorage, cfg *config.Config, geoService *geoip.GeoIPService) *PoolMonitor {
	return &PoolMonitor{
		storage:     s,
		cfg:         cfg,
		geoService:  geoService,
	}
}

// Run starts the infinite monitoring loop.
func (m *PoolMonitor) Run(ctx context.Context, wg *sync.WaitGroup) {
	// Guarantee Done() call on function exit
	defer wg.Done()
	log.Printf("Pool monitoring started with interval %v", m.cfg.MonitoringInterval)
	ticker := time.NewTicker(m.cfg.MonitoringInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.performMonitoring(context.Background())
		case <-ctx.Done():
			log.Println("Pool monitoring stopped")
			return
		}
	}
}

func (m *PoolMonitor) performMonitoring(ctx context.Context) {
	// Reset per-cycle in-memory cache
	m.geoCacheMu.Lock()
	m.geoCache = make(map[string]*geoCacheEntry)
	m.geoCacheMu.Unlock()

	userEmails, err := m.storage.GetAllUserEmails(ctx)
	if err != nil {
		log.Printf("Monitoring error (GetAllUserEmails): %v", err)
		return
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	modeName := m.getMonitoringModeName()
	if len(userEmails) == 0 {
		fmt.Printf("[%s] === %s MONITORING === NO ACTIVE USERS\n", now, modeName)
		return
	}

	// Use strings.Builder for atomic output (prevents log interruptions)
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("\n[%s] === %s MONITORING START ===\n", now, modeName))

	var allStats []models.UserIPStats
	for _, email := range userEmails {
		stats, err := m.buildUserStats(ctx, email)
		if err != nil {
			log.Printf("Error collecting stats for %s: %v", email, err)
			continue
		}
		if stats != nil {
			allStats = append(allStats, *stats)
		}
	}
	sort.Slice(allStats, func(i, j int) bool {
		return allStats[i].IPCount > allStats[j].IPCount
	})

	m.printSummary(&buf, allStats)
	m.printTopUsers(ctx, &buf, allStats)
	m.printOverLimitUsers(ctx, &buf, allStats)

	buf.WriteString(fmt.Sprintf("[%s] === %s MONITORING END ===\n\n", time.Now().Format("2006-01-02 15:04:05"), modeName))

	// Atomic output - prevents interruption by log.Printf
	fmt.Print(buf.String())
}

func (m *PoolMonitor) getMonitoringModeName() string {
	if m.cfg.DetectByASN {
		return "ASN POOLS"
	}
	if m.cfg.DetectBySubnet {
		return "SUBNET POOLS"
	}
	return "IP POOLS"
}

func (m *PoolMonitor) buildUserStats(ctx context.Context, email string) (*models.UserIPStats, error) {
	if m.cfg.DetectByASN {
		return m.buildUserStatsByASN(ctx, email)
	}
	if m.cfg.DetectBySubnet {
		return m.buildUserStatsBySubnet(ctx, email)
	}
	return m.buildUserStatsByIP(ctx, email)
}

func (m *PoolMonitor) buildUserStatsByIP(ctx context.Context, email string) (*models.UserIPStats, error) {
	activeIPs, err := m.storage.GetUserActiveIPs(ctx, email)
	if err != nil {
		return nil, err
	}
	if len(activeIPs) == 0 {
		return nil, nil
	}
	userLimit := m.getUserLimit(email)
	ipCount := len(activeIPs)
	status := "NORMAL"
	if float64(ipCount) >= float64(userLimit)*0.8 {
		status = "NEAR_LIMIT"
	}
	if ipCount > userLimit {
		status = "OVER_LIMIT"
	}
	hasCooldown, _ := m.storage.HasAlertCooldown(ctx, email)
	var ips, ipsWithTTL []string
	var ttlValues []int
	for ip, ttl := range activeIPs {
		ips = append(ips, ip)
		ipsWithTTL = append(ipsWithTTL, fmt.Sprintf("%s(%.1fh)", ip, float64(ttl)/3600.0))
		ttlValues = append(ttlValues, ttl)
	}
	sort.Strings(ips)
	sort.Strings(ipsWithTTL)
	minTTL, maxTTL := 0.0, 0.0
	if len(ttlValues) > 0 {
		sort.Ints(ttlValues)
		minTTL = float64(ttlValues[0]) / 3600.0
		maxTTL = float64(ttlValues[len(ttlValues)-1]) / 3600.0
	}
	return &models.UserIPStats{
		Email:            email,
		IPCount:          ipCount,
		Limit:            userLimit,
		IPs:              ips,
		IPsWithTTL:       ipsWithTTL,
		MinTTLHours:      math.Round(minTTL*10) / 10,
		MaxTTLHours:      math.Round(maxTTL*10) / 10,
		Status:           status,
		HasAlertCooldown: hasCooldown,
		IsExcluded:       m.cfg.ExcludedUsers[email],
		IsDebug:          m.cfg.DebugEmail != "" && email == m.cfg.DebugEmail,
	}, nil
}

func (m *PoolMonitor) buildUserStatsBySubnet(ctx context.Context, email string) (*models.UserIPStats, error) {
	activeSubnets, err := m.storage.GetUserActiveSubnets(ctx, email)
	if err != nil {
		return nil, err
	}
	if len(activeSubnets) == 0 {
		return nil, nil
	}
	userLimit := m.getUserLimit(email)
	itemCount := len(activeSubnets)
	status := "NORMAL"
	if float64(itemCount) >= float64(userLimit)*0.8 {
		status = "NEAR_LIMIT"
	}
	if itemCount > userLimit {
		status = "OVER_LIMIT"
	}
	hasCooldown, _ := m.storage.HasAlertCooldown(ctx, email)
	var items, itemsWithTTL []string
	var ttlValues []int
	for item, ttl := range activeSubnets {
		items = append(items, item)
		itemsWithTTL = append(itemsWithTTL, fmt.Sprintf("%s(%.1fh)", item, float64(ttl)/3600.0))
		ttlValues = append(ttlValues, ttl)
	}
	sort.Strings(items)
	sort.Strings(itemsWithTTL)
	minTTL, maxTTL := 0.0, 0.0
	if len(ttlValues) > 0 {
		sort.Ints(ttlValues)
		minTTL = float64(ttlValues[0]) / 3600.0
		maxTTL = float64(ttlValues[len(ttlValues)-1]) / 3600.0
	}
	return &models.UserIPStats{
		Email:            email,
		IPCount:          itemCount,
		Limit:            userLimit,
		IPs:              items,
		IPsWithTTL:       itemsWithTTL,
		MinTTLHours:      math.Round(minTTL*10) / 10,
		MaxTTLHours:      math.Round(maxTTL*10) / 10,
		Status:           status,
		HasAlertCooldown: hasCooldown,
		IsExcluded:       m.cfg.ExcludedUsers[email],
		IsDebug:          m.cfg.DebugEmail != "" && email == m.cfg.DebugEmail,
	}, nil
}

func (m *PoolMonitor) buildUserStatsByASN(ctx context.Context, email string) (*models.UserIPStats, error) {
	activeASNs, err := m.storage.GetUserActiveASNs(ctx, email)
	if err != nil {
		return nil, err
	}
	if len(activeASNs) == 0 {
		return nil, nil
	}
	userLimit := m.getUserLimit(email)
	itemCount := len(activeASNs)
	status := "NORMAL"
	if float64(itemCount) >= float64(userLimit)*0.8 {
		status = "NEAR_LIMIT"
	}
	if itemCount > userLimit {
		status = "OVER_LIMIT"
	}
	hasCooldown, _ := m.storage.HasAlertCooldown(ctx, email)
	var items, itemsWithTTL []string
	var ttlValues []int

	// Собираем ASN и их TTL
	for asn, info := range activeASNs {
		items = append(items, asn)
		itemsWithTTL = append(itemsWithTTL, fmt.Sprintf("%s(%.1fh)[%d IPs]", asn, float64(info.TTLSeconds)/3600.0, len(info.IPs)))
		ttlValues = append(ttlValues, info.TTLSeconds)
	}
	sort.Strings(items)
	sort.Strings(itemsWithTTL)
	minTTL, maxTTL := 0.0, 0.0
	if len(ttlValues) > 0 {
		sort.Ints(ttlValues)
		minTTL = float64(ttlValues[0]) / 3600.0
		maxTTL = float64(ttlValues[len(ttlValues)-1]) / 3600.0
	}
	return &models.UserIPStats{
		Email:            email,
		IPCount:          itemCount,
		Limit:            userLimit,
		IPs:              items,
		IPsWithTTL:       itemsWithTTL,
		MinTTLHours:      math.Round(minTTL*10) / 10,
		MaxTTLHours:      math.Round(maxTTL*10) / 10,
		Status:           status,
		HasAlertCooldown: hasCooldown,
		IsExcluded:       m.cfg.ExcludedUsers[email],
		IsDebug:          m.cfg.DebugEmail != "" && email == m.cfg.DebugEmail,
		ASNDetails:       activeASNs,
	}, nil
}

func (m *PoolMonitor) printSummary(buf *strings.Builder, stats []models.UserIPStats) {
	var total, nearLimit, overLimit, excluded, debug int
	total = len(stats)
	for _, s := range stats {
		if s.Status == "NEAR_LIMIT" {
			nearLimit++
		}
		if s.Status == "OVER_LIMIT" {
			overLimit++
		}
		if s.IsExcluded {
			excluded++
		}
		if s.IsDebug {
			debug++
		}
	}
	buf.WriteString("📊 SUMMARY:\n")
	buf.WriteString(fmt.Sprintf("   👥 Total active users: %d\n", total))
	buf.WriteString(fmt.Sprintf("   ⚠️  Near limit: %d\n", nearLimit))
	buf.WriteString(fmt.Sprintf("   🚨 Over limit: %d\n", overLimit))
	buf.WriteString(fmt.Sprintf("   🛡️  Excluded users: %d\n", excluded))
	if m.cfg.DebugEmail != "" {
		buf.WriteString(fmt.Sprintf("   🐛 Debug users: %d\n", debug))
	}
}

func (m *PoolMonitor) printUserGeo(ctx context.Context, user models.UserIPStats, indent string) {
	if !m.cfg.GeoIPEnabled || m.geoService == nil {
		return
	}

	ips := m.collectUserIPs(user)
	if len(ips) == 0 {
		fmt.Printf("%s🌍 Geo: no data\n", indent)
		return
	}

	countriesMap := make(map[string]bool)
	citiesMap := make(map[string]bool)
	locations := make([]*geoip.GeoLocation, 0, len(ips))
	type ipGeo struct {
		ip  string
		loc *geoip.GeoLocation
	}
	ipRows := make([]ipGeo, 0, len(ips))

	for _, ip := range ips {
		loc, err := m.cachedLookup(ctx, ip)
		if err != nil || loc == nil {
			ipRows = append(ipRows, ipGeo{ip: ip, loc: nil})
			continue
		}
		ipRows = append(ipRows, ipGeo{ip: ip, loc: loc})
		locations = append(locations, loc)
		if loc.CountryCode != "" {
			countriesMap[loc.CountryCode] = true
		}
		if loc.City != "" {
			citiesMap[loc.City] = true
		}
	}

	if len(locations) == 0 {
		fmt.Printf("%s🌍 Geo: no data\n", indent)
		return
	}

	countries := formatGeoList(mapKeysToSlice(countriesMap), 3)
	cities := formatGeoList(mapKeysToSlice(citiesMap), 3)
	maxDistance := maxDistanceKM(locations)

	fmt.Printf("%s🌍 Geo: countries: %s, cities: %s, max: %.0fkm\n",
		indent, countries, cities, maxDistance)
	fmt.Printf("%s   └─ IP geo:\n", indent)
	for _, row := range ipRows {
		if row.loc == nil {
			fmt.Printf("%s      • %s -> no data\n", indent, row.ip)
			continue
		}
		country := row.loc.CountryCode
		if country == "" {
			country = "??"
		}
		city := row.loc.City
		if city == "" {
			city = "-"
		}
		fmt.Printf("%s      • %s -> %s, %s (%.2f, %.2f)\n",
			indent, row.ip, country, city, row.loc.Latitude, row.loc.Longitude)
	}
}

func (m *PoolMonitor) collectUserIPs(user models.UserIPStats) []string {
	unique := make(map[string]struct{})
	switch {
	case m.cfg.DetectByASN:
		for _, info := range user.ASNDetails {
			for _, ip := range info.IPs {
				if ip != "" {
					unique[ip] = struct{}{}
				}
			}
		}
	case m.cfg.DetectBySubnet:
		return []string{}
	default:
		for _, ip := range user.IPs {
			if ip != "" {
				unique[ip] = struct{}{}
			}
		}
	}

	ips := make([]string, 0, len(unique))
	for ip := range unique {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	return ips
}

// cachedLookup выполняет GeoIP lookup с мемоизацией в рамках текущего цикла мониторинга.
func (m *PoolMonitor) cachedLookup(ctx context.Context, ip string) (*geoip.GeoLocation, error) {
	m.geoCacheMu.Lock()
	if entry, ok := m.geoCache[ip]; ok {
		m.geoCacheMu.Unlock()
		return entry.loc, entry.err
	}
	m.geoCacheMu.Unlock()

	loc, err := m.geoService.Lookup(ctx, ip)

	m.geoCacheMu.Lock()
	m.geoCache[ip] = &geoCacheEntry{loc: loc, err: err}
	m.geoCacheMu.Unlock()

	return loc, err
}

type geoSummary struct {
	countries   []string
	cities      []string
	maxDistance float64
}

func (m *PoolMonitor) buildUserGeo(ctx context.Context, user models.UserIPStats) (*geoSummary, map[string]*geoip.GeoLocation) {
	if !m.cfg.GeoIPEnabled || m.geoService == nil {
		return nil, nil
	}

	ips := m.collectUserIPs(user)
	if len(ips) == 0 {
		return nil, map[string]*geoip.GeoLocation{}
	}

	geoByIP := make(map[string]*geoip.GeoLocation, len(ips))
	countriesMap := make(map[string]bool)
	citiesMap := make(map[string]bool)
	locations := make([]*geoip.GeoLocation, 0, len(ips))

	for _, ip := range ips {
		loc, err := m.cachedLookup(ctx, ip)
		if err != nil || loc == nil {
			geoByIP[ip] = nil
			continue
		}
		geoByIP[ip] = loc
		locations = append(locations, loc)
		if loc.CountryCode != "" {
			countriesMap[loc.CountryCode] = true
		}
		if loc.City != "" {
			citiesMap[loc.City] = true
		}
	}

	if len(locations) == 0 {
		return nil, geoByIP
	}

	return &geoSummary{
		countries:   mapKeysToSlice(countriesMap),
		cities:      mapKeysToSlice(citiesMap),
		maxDistance: maxDistanceKM(locations),
	}, geoByIP
}

func formatIPWithGeo(ip string, loc *geoip.GeoLocation) string {
	if loc == nil {
		return fmt.Sprintf("%s -> no data", ip)
	}
	country := loc.CountryCode
	if country == "" {
		country = "??"
	}
	city := loc.City
	if city == "" {
		city = "-"
	}
	return fmt.Sprintf("%s -> %s, %s (%.2f, %.2f)", ip, country, city, loc.Latitude, loc.Longitude)
}

func formatIPsWithGeo(ips []string, geoByIP map[string]*geoip.GeoLocation) string {
	if geoByIP == nil {
		return strings.Join(ips, ", ")
	}
	formatted := make([]string, 0, len(ips))
	for _, ip := range ips {
		formatted = append(formatted, formatIPWithGeo(ip, geoByIP[ip]))
	}
	return strings.Join(formatted, ", ")
}

func formatGeoList(values []string, limit int) string {
	if len(values) == 0 {
		return "none"
	}
	sort.Strings(values)
	if len(values) <= limit {
		return strings.Join(values, ", ")
	}
	return fmt.Sprintf("%s, +%d", strings.Join(values[:limit], ", "), len(values)-limit)
}

func mapKeysToSlice(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func maxDistanceKM(locations []*geoip.GeoLocation) float64 {
	if len(locations) < 2 {
		return 0
	}

	maxDist := 0.0
	for i := 0; i < len(locations); i++ {
		for j := i + 1; j < len(locations); j++ {
			loc1 := locations[i]
			loc2 := locations[j]
			if (loc1.Latitude == 0 && loc1.Longitude == 0) || (loc2.Latitude == 0 && loc2.Longitude == 0) {
				continue
			}
			dist := geoip.HaversineDistance(loc1.Latitude, loc1.Longitude, loc2.Latitude, loc2.Longitude)
			if dist > maxDist {
				maxDist = dist
			}
		}
	}
	return maxDist
}

func (m *PoolMonitor) printTopUsers(ctx context.Context, buf *strings.Builder, stats []models.UserIPStats) {
	var title, itemLabel, itemsLabel string
	if m.cfg.DetectByASN {
		title = "📈 TOP USERS BY PROVIDER COUNT (ASN):"
		itemLabel = "Providers"
		itemsLabel = "ASNs"
	} else if m.cfg.DetectBySubnet {
		title = "📈 TOP USERS BY SUBNET COUNT:"
		itemLabel = "Subnets"
		itemsLabel = "Subnets"
	} else {
		title = "📈 TOP USERS BY IP COUNT:"
		itemLabel = "IPs"
		itemsLabel = "IPs"
	}
	buf.WriteString("\n" + title + "\n")
	limit := 10
	if len(stats) < limit {
		limit = len(stats)
	}
	for i := 0; i < limit; i++ {
		user := stats[i]
		buf.WriteString(fmt.Sprintf("   %2d. %s %s%s\n", i+1, getStatusEmoji(user.Status), user.Email, getMarkers(user)))
		buf.WriteString(fmt.Sprintf("       %s: %d/%d | TTL: %.1f-%.1fh\n", itemLabel, user.IPCount, user.Limit, user.MinTTLHours, user.MaxTTLHours))
		buf.WriteString(fmt.Sprintf("       %s: %s\n", itemsLabel, strings.Join(user.IPsWithTTL, ", ")))
		var geoSummary *geoSummary
		var geoByIP map[string]*geoip.GeoLocation
		if m.cfg.GeoIPEnabled && m.geoService != nil {
			geoSummary, geoByIP = m.buildUserGeo(ctx, user)
			if geoSummary != nil {
				countries := formatGeoList(geoSummary.countries, 3)
				cities := formatGeoList(geoSummary.cities, 3)
				buf.WriteString(fmt.Sprintf("       🌍 Geo: countries: %s, cities: %s\n", countries, cities))
			} else {
				buf.WriteString("       🌍 Geo: no data\n")
			}
		}

		// For ASN mode, show IP details under each provider
		if m.cfg.DetectByASN && user.ASNDetails != nil && len(user.ASNDetails) > 0 {
			buf.WriteString("       └─ Details:\n")
			asnKeys := make([]string, 0, len(user.ASNDetails))
			for asn := range user.ASNDetails {
				asnKeys = append(asnKeys, asn)
			}
			sort.Strings(asnKeys)
			for _, asn := range asnKeys {
				info := user.ASNDetails[asn]
				buf.WriteString(fmt.Sprintf("          • %s: %d IP -> %s\n", asn, len(info.IPs), formatIPsWithGeo(info.IPs, geoByIP)))
			}
		}
	}
}

func (m *PoolMonitor) printOverLimitUsers(ctx context.Context, buf *strings.Builder, stats []models.UserIPStats) {
	var overLimitUsers []models.UserIPStats
	for _, user := range stats {
		if user.Status == "OVER_LIMIT" {
			overLimitUsers = append(overLimitUsers, user)
		}
	}
	if len(overLimitUsers) > 0 {
		var itemLabel, itemsLabel string
		if m.cfg.DetectByASN {
			itemLabel = "Providers"
			itemsLabel = "ASNs"
		} else if m.cfg.DetectBySubnet {
			itemLabel = "Subnets"
			itemsLabel = "Subnets"
		} else {
			itemLabel = "IPs"
			itemsLabel = "IPs"
		}
		buf.WriteString("\n🚨 USERS OVER LIMIT:\n")
		for _, user := range overLimitUsers {
			buf.WriteString(fmt.Sprintf("   • %s%s\n", user.Email, getMarkers(user)))
			buf.WriteString(fmt.Sprintf("     %s: %d/%d | TTL: %.1f-%.1fh\n", itemLabel, user.IPCount, user.Limit, user.MinTTLHours, user.MaxTTLHours))
			buf.WriteString(fmt.Sprintf("     %s: %s\n", itemsLabel, strings.Join(user.IPsWithTTL, ", ")))
			var geoSummary *geoSummary
			var geoByIP map[string]*geoip.GeoLocation
			if m.cfg.GeoIPEnabled && m.geoService != nil {
				geoSummary, geoByIP = m.buildUserGeo(ctx, user)
				if geoSummary != nil {
					countries := formatGeoList(geoSummary.countries, 3)
					cities := formatGeoList(geoSummary.cities, 3)
					buf.WriteString(fmt.Sprintf("     🌍 Geo: countries: %s, cities: %s\n", countries, cities))
				} else {
					buf.WriteString("     🌍 Geo: no data\n")
				}
			}

			// For ASN mode, show IP details
			if m.cfg.DetectByASN && user.ASNDetails != nil && len(user.ASNDetails) > 0 {
				buf.WriteString("     └─ Details:\n")
				asnKeys := make([]string, 0, len(user.ASNDetails))
				for asn := range user.ASNDetails {
					asnKeys = append(asnKeys, asn)
				}
				sort.Strings(asnKeys)
				for _, asn := range asnKeys {
					info := user.ASNDetails[asn]
					buf.WriteString(fmt.Sprintf("        • %s: %d IP -> %s\n", asn, len(info.IPs), formatIPsWithGeo(info.IPs, geoByIP)))
				}
			}
		}
	}
}

func (m *PoolMonitor) getUserLimit(userEmail string) int {
	if m.cfg.DebugEmail != "" && userEmail == m.cfg.DebugEmail {
		return m.cfg.DebugIPLimit
	}
	if m.cfg.DetectByASN {
		return m.cfg.MaxASNsPerUser
	}
	if m.cfg.DetectBySubnet {
		return m.cfg.MaxSubnetsPerUser
	}
	return m.cfg.MaxIPsPerUser
}

func getStatusEmoji(status string) string {
	switch status {
	case "NORMAL":
		return "✅"
	case "NEAR_LIMIT":
		return "⚠️"
	case "OVER_LIMIT":
		return "🚨"
	default:
		return "❓"
	}
}

func getMarkers(user models.UserIPStats) string {
	var markers []string
	if user.IsExcluded {
		markers = append(markers, "[EXCLUDED]")
	}
	if user.HasAlertCooldown {
		markers = append(markers, "[ALERT_COOLDOWN]")
	}
	if user.IsDebug {
		markers = append(markers, "[DEBUG]")
	}
	if len(markers) > 0 {
		return " " + strings.Join(markers, " ")
	}
	return ""
}
