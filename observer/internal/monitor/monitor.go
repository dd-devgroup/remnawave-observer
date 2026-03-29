package monitor

import (
	"context"
	"fmt"
	"log"
	"math"
	"observer_service/internal/config"
	"observer_service/internal/database"
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

// PoolMonitor performs periodic ASN pool monitoring.
type PoolMonitor struct {
	storage    storage.Storage
	repo       database.Repository
	cfg        *config.Config
	geoService *geoip.GeoIPService
	geoCache   map[string]*geoCacheEntry
	geoCacheMu sync.Mutex
}

const monitoringCycleTimeout = 60 * time.Second

// NewPoolMonitor creates a new PoolMonitor instance.
func NewPoolMonitor(s storage.Storage, repo database.Repository, cfg *config.Config, geoService *geoip.GeoIPService) *PoolMonitor {
	return &PoolMonitor{
		storage:    s,
		repo:       repo,
		cfg:        cfg,
		geoService: geoService,
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
			cycleCtx, cancel := context.WithTimeout(ctx, monitoringCycleTimeout)
			m.performMonitoring(cycleCtx)
			cancel()
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

	// Get active users from Postgres (no Redis SCAN)
	userEmails, err := m.getActiveUserEmails(ctx)
	if err != nil {
		log.Printf("Monitoring error (getActiveUserEmails): %v", err)
		return
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	if len(userEmails) == 0 {
		fmt.Printf("[%s] === ASN POOLS MONITORING === NO ACTIVE USERS\n", now)
		return
	}

	// Use strings.Builder for atomic output (prevents log interruptions)
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("\n[%s] === ASN POOLS MONITORING START ===\n", now))

	var allStats []models.UserIPStats
	for _, email := range userEmails {
		stats, err := m.buildUserStatsByASN(ctx, email)
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
	m.printUsersWithBlockingActions(ctx, &buf, allStats)

	buf.WriteString(fmt.Sprintf("[%s] === ASN POOLS MONITORING END ===\n\n", time.Now().Format("2006-01-02 15:04:05")))

	// Atomic output - prevents interruption by log.Printf
	fmt.Print(buf.String())
}

// getActiveUserEmails returns active user emails from Postgres.
func (m *PoolMonitor) getActiveUserEmails(ctx context.Context) ([]string, error) {
	if m.repo == nil {
		return nil, fmt.Errorf("repository not available for monitoring")
	}

	since := time.Now().Add(-24 * time.Hour)
	stats, err := m.repo.GetActiveUsersForMonitor(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("postgres monitor query: %w", err)
	}

	emails := make([]string, 0, len(stats))
	for _, s := range stats {
		emails = append(emails, s.UserID)
	}
	return emails, nil
}

func (m *PoolMonitor) buildUserStatsByASN(ctx context.Context, email string) (*models.UserIPStats, error) {
	activeASNs, err := m.storage.GetUserActiveASNs(ctx, email)
	if err != nil {
		return nil, err
	}
	if len(activeASNs) == 0 {
		return nil, nil
	}
	itemCount := len(activeASNs)
	hasCooldown, _ := m.storage.HasAlertCooldown(ctx, email)
	var items, itemsWithTTL []string
	var ttlValues []int

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

	// Fetch latest score event for this user
	var latestScore *float64
	var latestScoreAction string
	if m.repo != nil {
		scoreEvent, _ := m.repo.GetLatestScoreEvent(ctx, email)
		if scoreEvent != nil {
			latestScore = &scoreEvent.ScoreTotal
			latestScoreAction = scoreEvent.ScoreAction
		}
	}
	status := deriveMonitorStatus(latestScoreAction)

	return &models.UserIPStats{
		Email:             email,
		IPCount:           itemCount,
		IPs:               items,
		IPsWithTTL:        itemsWithTTL,
		MinTTLHours:       math.Round(minTTL*10) / 10,
		MaxTTLHours:       math.Round(maxTTL*10) / 10,
		Status:            status,
		HasAlertCooldown:  hasCooldown,
		IsExcluded:        m.cfg.ExcludedUsers[email],
		IsDebug:           m.cfg.DebugEmail != "" && email == m.cfg.DebugEmail,
		LatestScore:       latestScore,
		LatestScoreAction: latestScoreAction,
		ASNDetails:        activeASNs,
	}, nil
}

func (m *PoolMonitor) printSummary(buf *strings.Builder, stats []models.UserIPStats) {
	var total, monitorCount, warnCount, blockingCount, excluded, debug int
	total = len(stats)
	for _, s := range stats {
		switch s.Status {
		case "monitor":
			monitorCount++
		case "warn", "soft_challenge":
			warnCount++
		case "temp_disable", "hard_disable":
			blockingCount++
		}
		if s.IsExcluded {
			excluded++
		}
		if s.IsDebug {
			debug++
		}
	}
	buf.WriteString("SUMMARY:\n")
	buf.WriteString(fmt.Sprintf("   Total active users: %d\n", total))
	buf.WriteString(fmt.Sprintf("   Monitor actions: %d\n", monitorCount))
	buf.WriteString(fmt.Sprintf("   Warn or challenge actions: %d\n", warnCount))
	buf.WriteString(fmt.Sprintf("   Blocking actions: %d\n", blockingCount))
	buf.WriteString(fmt.Sprintf("   Excluded users: %d\n", excluded))
	if m.cfg.DebugEmail != "" {
		buf.WriteString(fmt.Sprintf("   Debug users: %d\n", debug))
	}
}

func (m *PoolMonitor) collectUserIPs(user models.UserIPStats) []string {
	unique := make(map[string]struct{})
	for _, info := range user.ASNDetails {
		for _, ip := range info.IPs {
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
		return fmt.Sprintf("%s -> no data [src:none]", ip)
	}

	source := loc.Source
	if source == "" {
		source = "unknown"
	}

	country := loc.CountryCode
	if country == "" {
		country = "??"
	}
	city := loc.City
	if city == "" {
		city = "-"
	}
	if loc.Latitude == 0 && loc.Longitude == 0 {
		return fmt.Sprintf("%s -> %s, %s (no geo position) [src:%s]", ip, country, city, source)
	}
	return fmt.Sprintf("%s -> %s, %s (%.2f, %.2f) [src:%s]", ip, country, city, loc.Latitude, loc.Longitude, source)
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
	buf.WriteString("\nTOP USERS BY PROVIDER COUNT AND SCORE:\n")
	limit := 10
	if len(stats) < limit {
		limit = len(stats)
	}
	for i := 0; i < limit; i++ {
		user := stats[i]
		buf.WriteString(fmt.Sprintf("   %2d. %s %s%s\n", i+1, getStatusEmoji(user.Status), user.Email, getMarkers(user)))
		buf.WriteString(fmt.Sprintf("       Providers: %d | TTL: %.1f-%.1fh\n", user.IPCount, user.MinTTLHours, user.MaxTTLHours))
		if user.LatestScore != nil {
			buf.WriteString(fmt.Sprintf("       Score: %.1f [%s]\n", *user.LatestScore, user.LatestScoreAction))
		}
		buf.WriteString(fmt.Sprintf("       ASNs: %s\n", strings.Join(user.IPsWithTTL, ", ")))
		var geoSummary *geoSummary
		var geoByIP map[string]*geoip.GeoLocation
		if m.cfg.GeoIPEnabled && m.geoService != nil {
			geoSummary, geoByIP = m.buildUserGeo(ctx, user)
			if geoSummary != nil {
				countries := formatGeoList(geoSummary.countries, 3)
				cities := formatGeoList(geoSummary.cities, 3)
				buf.WriteString(fmt.Sprintf("       Geo: countries: %s, cities: %s\n", countries, cities))
			} else {
				buf.WriteString("       Geo: no data\n")
			}
		}

		if user.ASNDetails != nil && len(user.ASNDetails) > 0 {
			buf.WriteString("       Details:\n")
			asnKeys := make([]string, 0, len(user.ASNDetails))
			for asn := range user.ASNDetails {
				asnKeys = append(asnKeys, asn)
			}
			sort.Strings(asnKeys)
			for _, asn := range asnKeys {
				info := user.ASNDetails[asn]
				buf.WriteString(fmt.Sprintf("          %s: %d IP -> %s\n", asn, len(info.IPs), formatIPsWithGeo(info.IPs, geoByIP)))
			}
		}
	}
}

func (m *PoolMonitor) printUsersWithBlockingActions(ctx context.Context, buf *strings.Builder, stats []models.UserIPStats) {
	var blockingUsers []models.UserIPStats
	for _, user := range stats {
		if user.Status == "temp_disable" || user.Status == "hard_disable" {
			blockingUsers = append(blockingUsers, user)
		}
	}
	if len(blockingUsers) > 0 {
		buf.WriteString("\nUSERS WITH BLOCKING SCORE ACTIONS:\n")
		for _, user := range blockingUsers {
			buf.WriteString(fmt.Sprintf("   %s%s\n", user.Email, getMarkers(user)))
			buf.WriteString(fmt.Sprintf("     Providers: %d | TTL: %.1f-%.1fh\n", user.IPCount, user.MinTTLHours, user.MaxTTLHours))
			if user.LatestScore != nil {
				buf.WriteString(fmt.Sprintf("     Score: %.1f [%s]\n", *user.LatestScore, user.LatestScoreAction))
			}
			buf.WriteString(fmt.Sprintf("     ASNs: %s\n", strings.Join(user.IPsWithTTL, ", ")))
			var geoSummary *geoSummary
			var geoByIP map[string]*geoip.GeoLocation
			if m.cfg.GeoIPEnabled && m.geoService != nil {
				geoSummary, geoByIP = m.buildUserGeo(ctx, user)
				if geoSummary != nil {
					countries := formatGeoList(geoSummary.countries, 3)
					cities := formatGeoList(geoSummary.cities, 3)
					buf.WriteString(fmt.Sprintf("     Geo: countries: %s, cities: %s\n", countries, cities))
				} else {
					buf.WriteString("     Geo: no data\n")
				}
			}

			if user.ASNDetails != nil && len(user.ASNDetails) > 0 {
				buf.WriteString("     Details:\n")
				asnKeys := make([]string, 0, len(user.ASNDetails))
				for asn := range user.ASNDetails {
					asnKeys = append(asnKeys, asn)
				}
				sort.Strings(asnKeys)
				for _, asn := range asnKeys {
					info := user.ASNDetails[asn]
					buf.WriteString(fmt.Sprintf("        %s: %d IP -> %s\n", asn, len(info.IPs), formatIPsWithGeo(info.IPs, geoByIP)))
				}
			}
		}
	}
}

func getStatusEmoji(status string) string {
	switch status {
	case "monitor":
		return "[MONITOR]"
	case "warn":
		return "[WARN]"
	case "soft_challenge":
		return "[CHALLENGE]"
	case "temp_disable":
		return "[TEMP]"
	case "hard_disable":
		return "[BLOCK]"
	default:
		return "[ACTIVE]"
	}
}

func deriveMonitorStatus(latestScoreAction string) string {
	switch latestScoreAction {
	case "monitor", "warn", "soft_challenge", "temp_disable", "hard_disable":
		return latestScoreAction
	default:
		return "active"
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
