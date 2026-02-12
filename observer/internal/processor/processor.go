package processor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"observer_service/internal/config"
	"observer_service/internal/metrics"
	"observer_service/internal/models"
	"observer_service/internal/services/alerter"
	"observer_service/internal/services/asn"
	"observer_service/internal/services/enforcement"
	"observer_service/internal/services/geoip"
	"observer_service/internal/services/scoring"
	"observer_service/internal/services/storage"
	"strconv"
	"sync"
	"time"
)

// LogProcessor обрабатывает входящие логи.
type LogProcessor struct {
	storage           storage.IPStorage
	enforcer          enforcement.Enforcer
	alerter           alerter.Notifier
	cfg               *config.Config
	asnLookup         *asn.ASNLookup         // Сервис для lookup ASN
	logChannel        chan []models.LogEntry // Канал для получения пачек логов
	sideEffectChannel chan func(context.Context) // Канал для побочных задач (алерты, очистка)

	// Новые сервисы для Anti-Abuse системы
	geoService    *geoip.GeoIPService   // Сервис геолокации
	geoAnalyzer   *geoip.GeoAnalyzer    // Анализатор географии
	asnClassifier *asn.ASNClassifier    // Классификатор провайдеров
	scorer        *scoring.Scorer       // Система скоринга

	// Кешированные распарсенные подсети для быстрой проверки вложенности
	excludedSubnetsParsed []*net.IPNet
	excludedIPsParsed     []*net.IPNet // Для случаев когда в ExcludedIPs указан CIDR
}

// NewLogProcessor создает новый экземпляр LogProcessor.
func NewLogProcessor(
	s storage.IPStorage,
	enf enforcement.Enforcer,
	a alerter.Notifier,
	cfg *config.Config,
	asnLookup *asn.ASNLookup,
	geoService *geoip.GeoIPService,
	geoAnalyzer *geoip.GeoAnalyzer,
	asnClassifier *asn.ASNClassifier,
	scorer *scoring.Scorer,
) *LogProcessor {
	lp := &LogProcessor{
		storage:           s,
		enforcer:          enf,
		alerter:           a,
		cfg:               cfg,
		asnLookup:         asnLookup,
		geoService:        geoService,
		geoAnalyzer:       geoAnalyzer,
		asnClassifier:     asnClassifier,
		scorer:            scorer,
		logChannel:        make(chan []models.LogEntry, cfg.LogChannelBufferSize),
		sideEffectChannel: make(chan func(context.Context), cfg.SideEffectChannelBufferSize),
	}

	// Parse excluded subnets once during initialization
	for subnetStr := range cfg.ExcludedSubnets {
		_, ipNet, err := net.ParseCIDR(subnetStr)
		if err != nil {
			log.Printf("Warning: failed to parse excluded subnet '%s': %v", subnetStr, err)
			continue
		}
		lp.excludedSubnetsParsed = append(lp.excludedSubnetsParsed, ipNet)
	}
	if len(lp.excludedSubnetsParsed) > 0 {
		log.Printf("Parsed %d excluded subnets for containment check", len(lp.excludedSubnetsParsed))
	}

	// Parse excluded IPs (supports CIDR in EXCLUDED_IPS)
	for ipStr := range cfg.ExcludedIPs {
		// Try parsing as CIDR
		if _, ipNet, err := net.ParseCIDR(ipStr); err == nil {
			lp.excludedIPsParsed = append(lp.excludedIPsParsed, ipNet)
		} else if ip := net.ParseIP(ipStr); ip != nil {
			// Single IP -> convert to /32 CIDR
			var mask net.IPMask
			if ip.To4() != nil {
				mask = net.CIDRMask(32, 32)
			} else {
				mask = net.CIDRMask(128, 128)
			}
			lp.excludedIPsParsed = append(lp.excludedIPsParsed, &net.IPNet{IP: ip, Mask: mask})
		} else {
			log.Printf("Warning: failed to parse excluded IP '%s'", ipStr)
		}
	}
	if len(lp.excludedIPsParsed) > 0 {
		log.Printf("Parsed %d excluded IPs/CIDRs for containment check", len(lp.excludedIPsParsed))
	}

	return lp
}

// StartWorkerPool запускает пул горутин-воркеров для обработки логов.
func (p *LogProcessor) StartWorkerPool(ctx context.Context, mainWg *sync.WaitGroup) {
	defer mainWg.Done()

	var workerWg sync.WaitGroup
	log.Printf("Starting log processing worker pool (size: %d)...", p.cfg.WorkerPoolSize)

	for i := 0; i < p.cfg.WorkerPoolSize; i++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			log.Printf("Log processing worker %d started", workerID)
			for entries := range p.logChannel {
				p.ProcessEntries(ctx, entries)
			}
			log.Printf("Log processing worker %d stopping", workerID)
		}(i + 1)
	}

	<-ctx.Done()
	log.Println("Stop signal received for log processing workers. Closing channel...")
	close(p.logChannel)
	workerWg.Wait()
	log.Println("All log processing workers stopped successfully")
}

// StartSideEffectWorkerPool starts the side-effect task worker pool.
func (p *LogProcessor) StartSideEffectWorkerPool(ctx context.Context, mainWg *sync.WaitGroup) {
	defer mainWg.Done()

	var workerWg sync.WaitGroup
	log.Printf("Starting side-effect worker pool (size: %d)...", p.cfg.SideEffectWorkerPoolSize)

	for i := 0; i < p.cfg.SideEffectWorkerPoolSize; i++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			log.Printf("Side-effect worker %d started", workerID)
			for task := range p.sideEffectChannel {
				select {
				case <-ctx.Done():
					log.Printf("Side-effect worker %d skipped task due to context cancellation", workerID)
				default:
					taskCtx, cancel := context.WithTimeout(ctx, p.cfg.SideEffectTimeout)
					task(taskCtx)
					if taskCtx.Err() == context.DeadlineExceeded {
						metrics.SideEffectTimeoutCount.Add(1)
					}
					cancel()
				}
			}
			log.Printf("Side-effect worker %d stopping", workerID)
		}(i + 1)
	}

	<-ctx.Done()
	log.Println("Stop signal received for side-effect workers. Closing channel...")
	close(p.sideEffectChannel)
	workerWg.Wait()
	log.Println("All side-effect workers stopped successfully")
}

// EnqueueEntries adds a batch of logs to the processing queue.
func (p *LogProcessor) EnqueueEntries(entries []models.LogEntry) error {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Attempted to write to closed log channel. Service is shutting down")
		}
	}()

	select {
	case p.logChannel <- entries:
		return nil
	default:
		return errors.New("log channel is full, rejecting new entries")
	}
}

// enqueueSideEffectTask adds a side-effect task to the execution queue.
func (p *LogProcessor) enqueueSideEffectTask(task func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Attempted to write to closed side-effect channel. Service is shutting down")
		}
	}()

	select {
	case p.sideEffectChannel <- task:
		// Task successfully queued
	default:
		log.Println("Warning: side-effect queue is full. Task dropped")
	}
}

// ProcessEntries processes a batch of log entries.
func (p *LogProcessor) ProcessEntries(ctx context.Context, entries []models.LogEntry) {
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			log.Printf("Batch processing interrupted due to context cancellation: %v", ctx.Err())
			return
		default:
			p.processSingleEntry(ctx, entry)
		}
	}
}

func (p *LogProcessor) processSingleEntry(ctx context.Context, entry models.LogEntry) {
	if p.cfg.ExcludedUsers[entry.UserEmail] {
		return // User is in exclusion list
	}

	if p.cfg.DetectByASN {
		p.processEntryByASN(ctx, entry)
	} else if p.cfg.DetectBySubnet {
		p.processEntryBySubnet(ctx, entry)
	} else {
		p.processEntryByIP(ctx, entry)
	}
}

func (p *LogProcessor) processEntryByIP(ctx context.Context, entry models.LogEntry) {
	userIPLimit := p.getUserIPLimit(entry.UserEmail)
	debugMarker := p.getDebugMarker(entry.UserEmail)

	res, err := p.storage.CheckAndAddIP(ctx, entry.UserEmail, entry.SourceIP, userIPLimit, p.cfg.UserIPTTL, p.cfg.AlertCooldown)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("CheckAndAddIP operation cancelled for %s: %v", entry.UserEmail, err)
		} else {
			log.Printf("Entry processing error for %s: %v", entry.UserEmail, err)
		}
		return
	}

	if res.StatusCode == 0 && res.IsNew {
		log.Printf("New IP for user %s%s: %s. Total IPs: %d/%d",
			entry.UserEmail, debugMarker, entry.SourceIP, res.CurrentCount, userIPLimit)
	}

	if res.StatusCode == 1 { // Limit exceeded, enforcement
		log.Printf("IP LIMIT EXCEEDED%s: User %s, IPs: %d/%d",
			debugMarker, entry.UserEmail, res.CurrentCount, userIPLimit)

		// MIG-7: User-level enforcement instead of IP-blocking
		reason := fmt.Sprintf("ip_limit_exceeded: %d/%d IPs", res.CurrentCount, userIPLimit)
		score := 85 // default score for IP limit

		if err := p.disableUser(ctx, entry.UserEmail, reason, score); err != nil {
			log.Printf("Enforcement error for %s: %v", entry.UserEmail, err)
		}

		// Webhook alert as side-effect
		ipCount := int(res.CurrentCount)
		alertPayload := models.AlertPayload{
			UserIdentifier:   entry.UserEmail,
			DetectedIPsCount: &ipCount,
			Limit:            userIPLimit,
			AllUserIPs:       res.AllUserItems,
			BlockDuration:    p.cfg.BlockDuration,
			ViolationType:    "ip_limit_exceeded",
		}
		p.enqueueSideEffectTask(func(ctx context.Context) {
			if err := p.alerter.SendAlert(ctx, alertPayload); err != nil {
				log.Printf("Webhook notification send error: %v", err)
			}
		})
	}
}

func (p *LogProcessor) processEntryBySubnet(ctx context.Context, entry models.LogEntry) {
	ip := net.ParseIP(entry.SourceIP)
	if ip == nil || ip.To4() == nil {
		return // Ignore invalid or non-IPv4 addresses
	}

	mask := net.CIDRMask(p.cfg.SubnetMaskIPv4, 32)
	subnet := &net.IPNet{IP: ip.Mask(mask), Mask: mask}
	subnetStr := subnet.String()

	userSubnetLimit := p.cfg.MaxSubnetsPerUser
	debugMarker := p.getDebugMarker(entry.UserEmail)

	res, err := p.storage.CheckAndAddSubnet(ctx, entry.UserEmail, subnetStr, userSubnetLimit, p.cfg.UserSubnetTTL, p.cfg.AlertCooldown)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("CheckAndAddSubnet operation cancelled for %s: %v", entry.UserEmail, err)
		} else {
			log.Printf("Entry processing error (Subnet) for %s: %v", entry.UserEmail, err)
		}
		return
	}

	if res.StatusCode == 0 && res.IsNew {
		log.Printf("New subnet for user %s%s: %s. Total subnets: %d/%d",
			entry.UserEmail, debugMarker, subnetStr, res.CurrentCount, userSubnetLimit)
	}

	if res.StatusCode == 1 { // Limit exceeded, enforcement
		log.Printf("SUBNET LIMIT EXCEEDED%s: User %s, subnets: %d/%d",
			debugMarker, entry.UserEmail, res.CurrentCount, userSubnetLimit)

		// MIG-7: User-level enforcement
		reason := fmt.Sprintf("subnet_limit_exceeded: %d/%d subnets", res.CurrentCount, userSubnetLimit)
		score := 85

		if err := p.disableUser(ctx, entry.UserEmail, reason, score); err != nil {
			log.Printf("Enforcement error for %s: %v", entry.UserEmail, err)
		}

		// Webhook alert
		subnetCount := int(res.CurrentCount)
		alertPayload := models.AlertPayload{
			UserIdentifier:   entry.UserEmail,
			DetectedIPsCount: &subnetCount,
			Limit:            userSubnetLimit,
			AllUserIPs:       res.AllUserItems,
			BlockDuration:    p.cfg.BlockDuration,
			ViolationType:    "subnet_limit_exceeded",
		}
		p.enqueueSideEffectTask(func(ctx context.Context) {
			if err := p.alerter.SendAlert(ctx, alertPayload); err != nil {
				log.Printf("Webhook notification send error: %v", err)
			}
		})
	}
}

func (p *LogProcessor) getUserIPLimit(userEmail string) int {
	if p.cfg.DebugEmail != "" && userEmail == p.cfg.DebugEmail {
		return p.cfg.DebugIPLimit
	}
	return p.cfg.MaxIPsPerUser
}

func (p *LogProcessor) getDebugMarker(userEmail string) string {
	if p.cfg.DebugEmail != "" && userEmail == p.cfg.DebugEmail {
		return " [DEBUG]"
	}
	return ""
}

func (p *LogProcessor) filterExcludedIPs(ips []string, email string) []string {
	if len(p.excludedIPsParsed) == 0 {
		return ips
	}

	var filtered []string
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			// Невалидный IP — пропускаем в блокировку (пусть nft разбирается)
			filtered = append(filtered, ipStr)
			continue
		}

		excluded := false
		for _, excludedNet := range p.excludedIPsParsed {
			if excludedNet.Contains(ip) {
				log.Printf("IP address %s for user %s skipped (belongs to excluded network %s)", ipStr, email, excludedNet.String())
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, ipStr)
		}
	}
	return filtered
}

// filterExcludedSubnets проверяет список подсетей на вложенность в белый список.
// Подсеть считается исключённой, если она полностью входит в любую из исключённых подсетей.
func (p *LogProcessor) filterExcludedSubnets(subnets []string, email string) []string {
	if len(p.excludedSubnetsParsed) == 0 {
		return subnets
	}

	var filtered []string
	for _, subnetStr := range subnets {
		_, subnetNet, err := net.ParseCIDR(subnetStr)
		if err != nil {
			// Невалидная подсеть — пропускаем в блокировку
			filtered = append(filtered, subnetStr)
			continue
		}

		excluded := false
		for _, excludedNet := range p.excludedSubnetsParsed {
			// Check: does the first subnet IP belong to the excluded network
			// This means subnetNet is a subset of excludedNet
			if excludedNet.Contains(subnetNet.IP) {
				// Additional check: subnetNet mask must be >= excludedNet mask
				// (i.e., subnetNet must be smaller or equal to excludedNet)
				excludedOnes, _ := excludedNet.Mask.Size()
				subnetOnes, _ := subnetNet.Mask.Size()
				if subnetOnes >= excludedOnes {
					log.Printf("Subnet %s for user %s skipped (belongs to excluded network %s)", subnetStr, email, excludedNet.String())
					excluded = true
					break
				}
			}
		}
		if !excluded {
			filtered = append(filtered, subnetStr)
		}
	}
	return filtered
}

// processEntryByASN processes connections in ASN mode (by providers)
func (p *LogProcessor) processEntryByASN(ctx context.Context, entry models.LogEntry) {
	var identifier string
	var identifierType string
	var orgName string

	var redisStore *storage.RedisStore

	// Try to get ASN for IP
	if p.asnLookup != nil {
		redisStore = p.storage.(*storage.RedisStore)
		asnStr, org, err := p.asnLookup.LookupWithOrg(entry.SourceIP)
		if err == nil && asnStr != "" {
			// Check if ASN is in exclusion list
			if p.cfg.ExcludedASNs[asnStr] {
				log.Printf("IP %s (ASN %s - %s) in excluded ASN list, skipping", entry.SourceIP, asnStr, org)
				return
			}
			identifier = asnStr
			identifierType = "ASN"
			orgName = org

			// Cache organization name for later use
			if org != "" {
				if err := redisStore.SetASNOrgName(ctx, asnStr, org, p.cfg.UserSubnetTTL); err != nil {
					log.Printf("Org caching error for ASN %s: %v", asnStr, err)
				}
			}
		} else {
			// Log for debugging but continue with fallback
			if err != nil {
				log.Printf("Failed to determine ASN for IP %s (user %s): %v. Using fallback",
					entry.SourceIP, entry.UserEmail, err)
			}
		}
	}

	// Fallback to subnet if ASN not found or service unavailable
	if identifier == "" {
		ip := net.ParseIP(entry.SourceIP)
		if ip == nil || ip.To4() == nil {
			log.Printf("Invalid IP address %s for user %s, skipping", entry.SourceIP, entry.UserEmail)
			return
		}
		mask := net.CIDRMask(p.cfg.ASNFallbackMask, 32)
		subnet := &net.IPNet{IP: ip.Mask(mask), Mask: mask}
		identifier = subnet.String()
		identifierType = "Subnet"
	}

	userASNLimit := p.cfg.MaxASNsPerUser
	debugMarker := p.getDebugMarker(entry.UserEmail)

	// IMPORTANT: Save ASN -> IP mapping BEFORE limit check
	// This ensures IP will be in Redis when we collect block data
	if identifierType == "ASN" {
		if err := redisStore.AddIPToASNMapping(ctx, entry.UserEmail, identifier, entry.SourceIP, p.cfg.UserSubnetTTL); err != nil {
			log.Printf("ASN->IP mapping save error for %s: %v. Skipping processing", entry.UserEmail, err)
			return // Abort if failed to save IP - otherwise ASN will be without IP
		}
	}

	// For ASN use special CheckAndAddASN method with "dead" ASN filtering
	// For Subnet fallback use standard CheckAndAddSubnet
	var res *models.CheckResult
	var err error
	if identifierType == "ASN" {
		res, err = redisStore.CheckAndAddASN(ctx, entry.UserEmail, identifier, userASNLimit, p.cfg.UserSubnetTTL, p.cfg.AlertCooldown)
	} else {
		res, err = p.storage.CheckAndAddSubnet(ctx, entry.UserEmail, identifier, userASNLimit, p.cfg.UserSubnetTTL, p.cfg.AlertCooldown)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("CheckAndAdd%s operation cancelled for %s: %v", identifierType, entry.UserEmail, err)
		} else {
			log.Printf("Entry processing error (%s) for %s: %v", identifierType, entry.UserEmail, err)
		}
		return
	}

	if res.StatusCode == 0 && res.IsNew {
		if identifierType == "ASN" && orgName != "" {
			// Get GeoIP data ONCE for classifier and logging
			var countryCode string
			var geoLoc *geoip.GeoLocation
			if p.geoService != nil && p.cfg.GeoIPEnabled {
				loc, lookupErr := p.geoService.Lookup(ctx, entry.SourceIP)
				if lookupErr != nil {
					if errors.Is(lookupErr, context.DeadlineExceeded) {
						metrics.GeoIPLookupTimeout.Add(1)
					} else {
						metrics.GeoIPLookupFail.Add(1)
					}
				} else if loc != nil {
					metrics.GeoIPLookupSuccess.Add(1)
					geoLoc = loc
					countryCode = loc.CountryCode
				}
			}

			// Classify provider for logging with country consideration
			var providerInfo string
			if p.asnClassifier != nil {
				classification := p.asnClassifier.ClassifyWithCountry(identifier, orgName, countryCode)
				providerInfo = fmt.Sprintf(" [%s, risk:%.1f]", classification.ProviderType, classification.Modifier)
			}
			log.Printf("New %s for user %s%s: %s (%s)%s | IP: %s. Total: %d/%d",
				identifierType, entry.UserEmail, debugMarker, identifier, orgName, providerInfo, entry.SourceIP, res.CurrentCount, userASNLimit)

			// Log GeoIP analysis (using already obtained data)
			if geoLoc != nil {
				log.Printf("[GeoIP] %s: %s -> %s, %s (%.2f, %.2f)",
					entry.UserEmail, entry.SourceIP, geoLoc.CountryCode, geoLoc.City,
					geoLoc.Latitude, geoLoc.Longitude)
			}
		} else {
			log.Printf("New %s for user %s%s: %s | IP: %s. Total: %d/%d",
				identifierType, entry.UserEmail, debugMarker, identifier, entry.SourceIP, res.CurrentCount, userASNLimit)
		}
	}

	if res.StatusCode == 1 { // Limit exceeded, enforcement
		log.Printf("⚠️  %s LIMIT EXCEEDED%s: User %s, count: %d/%d",
			identifierType, debugMarker, entry.UserEmail, res.CurrentCount, userASNLimit)

		// Формируем алерт
		violationType := "asn_limit_exceeded"
		if identifierType == "Subnet" {
			violationType = "subnet_limit_exceeded_fallback"
		}

		alertPayload := models.AlertPayload{
			UserIdentifier: entry.UserEmail,
			Limit:          userASNLimit,
			BlockDuration:  p.cfg.BlockDuration,
			ViolationType:  violationType,
		}

		// Заполняем специфичные поля в зависимости от типа идентификатора
		if identifierType == "ASN" {
			// Для ASN режима: только ASN-специфичные поля
			asnCount := int(res.CurrentCount)
			alertPayload.DetectedASNCount = &asnCount
			alertPayload.AllUserASNs = res.AllUserItems
			// Передаём текущий ASN и IP для гарантированного включения в детали
			alertPayload.ASNDetails = p.collectASNDetails(ctx, entry.UserEmail, res.AllUserItems, identifier, entry.SourceIP)

			// Выполняем расширенную аналитику если включена
			if p.cfg.ScoringEnabled || p.cfg.GeoIPEnabled {
				geoResult, providerTypes, violationScore := p.performEnhancedAnalytics(
					ctx,
					entry.UserEmail,
					res.AllUserItems,
					alertPayload.ASNDetails,
				)

				// Add results to alert payload
				if geoResult != nil {
					alertPayload.GeoAnalysis = geoResult
				}
				if providerTypes != nil {
					alertPayload.ProviderTypes = providerTypes
				}
				if violationScore != nil {
					score := violationScore.FinalScore
					alertPayload.Score = &score
					alertPayload.ScoreAction = string(violationScore.Action)

					// Check action based on score
					if violationScore.Action == scoring.ActionNone {
						log.Printf("[Anti-Abuse] Score %.1f < 30 for %s, block cancelled",
							violationScore.FinalScore, entry.UserEmail)
						return // Don't block and don't send alert
					}
				}
			}
		} else {
			// For Subnet fallback: use IP fields
			subnetCount := int(res.CurrentCount)
			alertPayload.DetectedIPsCount = &subnetCount
			alertPayload.AllUserIPs = res.AllUserItems
		}

		// MIG-7: User-level enforcement (before alert send)
		var enfScore int
		var enfReason string

		if identifierType == "ASN" {
			enfReason = fmt.Sprintf("asn_limit_exceeded: %d/%d ASNs", res.CurrentCount, userASNLimit)
			// Use violationScore if available
			if alertPayload.Score != nil {
				enfScore = int(*alertPayload.Score)
			} else {
				enfScore = 85
			}
		} else {
			enfReason = fmt.Sprintf("subnet_limit_exceeded_fallback: %d/%d", res.CurrentCount, userASNLimit)
			enfScore = 85
		}

		if err := p.disableUser(ctx, entry.UserEmail, enfReason, enfScore); err != nil {
			log.Printf("Enforcement error for %s: %v", entry.UserEmail, err)
		}

		p.enqueueSideEffectTask(func(ctx context.Context) {
			if err := p.alerter.SendAlert(ctx, alertPayload); err != nil {
				log.Printf("Webhook notification send error: %v", err)
			}
		})
	}
}

// collectASNDetails collects details for each ASN (organization, IPs, count)
// currentASN and currentIP - current ASN and IP for guaranteed inclusion in result
func (p *LogProcessor) collectASNDetails(ctx context.Context, email string, asns []string, currentASN, currentIP string) map[string]*models.ASNInfo {
	result := make(map[string]*models.ASNInfo)
	redisStore := p.storage.(*storage.RedisStore)

	for _, asn := range asns {
		// Skip non-ASN identifiers (subnets)
		if len(asn) < 2 || asn[:2] != "AS" {
			continue
		}

		// Get IP addresses for this ASN
		ips, err := redisStore.GetIPsForUserASN(ctx, email, asn)
		if err != nil {
			log.Printf("Error getting IPs for ASN %s user %s: %v", asn, email, err)
			ips = []string{} // Continue with empty list instead of skip
		}

		// If this is current ASN and current IP not in list - add it
		if asn == currentASN && currentIP != "" {
			found := false
			for _, ip := range ips {
				if ip == currentIP {
					found = true
					break
				}
			}
			if !found {
				ips = append(ips, currentIP)
			}
		}

		// Get organization name and country for ASN
		// First try from Redis cache
		org, err := redisStore.GetASNOrgName(ctx, asn)
		if err != nil {
			log.Printf("Error getting org from cache for ASN %s: %v", asn, err)
		}

		// Get country code via lookup
		var countryCode string
		if p.asnLookup != nil && len(ips) > 0 {
			if org == "" {
				// If no org in cache - use LookupFull
				_, orgName, country, err := p.asnLookup.LookupFull(ips[0])
				if err == nil {
					if orgName != "" {
						org = orgName
						// Save to cache for future use
						if cacheErr := redisStore.SetASNOrgName(ctx, asn, orgName, p.cfg.UserSubnetTTL); cacheErr != nil {
							log.Printf("Org caching error for ASN %s: %v", asn, cacheErr)
						}
					}
					countryCode = country
				}
			} else {
				// If org already exists - get only country
				_, _, country, err := p.asnLookup.LookupFull(ips[0])
				if err == nil {
					countryCode = country
				}
			}
		}

		// If org still empty - set fallback
		if org == "" {
			org = "Unknown"
		}

		result[asn] = &models.ASNInfo{
			ASN:          asn,
			Organization: org,
			Country:      countryCode,
			IPs:          ips,
			IPCount:      len(ips),
		}
	}

	return result
}

// collectIPsForASNBlock collects all IPs for blocking based on ASN/subnets
// currentIP - current IP for guaranteed inclusion in result
func (p *LogProcessor) collectIPsForASNBlock(ctx context.Context, email string, identifiers []string, currentIP string) []string {
	var result []string
	seenIPs := make(map[string]struct{})
	redisStore := p.storage.(*storage.RedisStore)

	// First add current IP to guarantee its blocking
	if currentIP != "" {
		seenIPs[currentIP] = struct{}{}
		result = append(result, currentIP)
	}

	for _, item := range identifiers {
		// Check if not in excluded ASN list
		if len(item) > 2 && item[:2] == "AS" {
			if p.cfg.ExcludedASNs[item] {
				log.Printf("ASN %s in excluded list, skipping during IP collection for block", item)
				continue
			}

			ips, err := redisStore.GetIPsForUserASN(ctx, email, item)
			if err != nil {
				log.Printf("Error getting IPs for ASN %s user %s: %v", item, email, err)
				continue
			}
			for _, ip := range ips {
				if _, exists := seenIPs[ip]; !exists {
					seenIPs[ip] = struct{}{}
					result = append(result, ip)
				}
			}
		} else {
			// This is subnet (fallback) - add as-is for CIDR blocking
			if _, exists := seenIPs[item]; !exists {
				seenIPs[item] = struct{}{}
				result = append(result, item)
			}
		}
	}

	if len(result) == 0 {
		log.Printf("⚠️  No IP addresses found for blocking user %s", email)
	}

	return result
}

// generateEventID возвращает 16-байтовый hex-идентификатор события из crypto/rand.
// --- MIG-7: User-level enforcement helpers ---

// parseInternalID парсит LogEntry.UserEmail как internal numeric ID.
func parseInternalID(userEmail string) (int64, error) {
	id, err := strconv.ParseInt(userEmail, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid user_email (not numeric): %q", userEmail)
	}
	return id, nil
}

// parseBlockDuration парсит строку duration (например "5m") в time.Duration.
func parseBlockDuration(durationStr string) (time.Duration, error) {
	dur, err := time.ParseDuration(durationStr)
	if err != nil {
		return 0, fmt.Errorf("invalid block duration %q: %w", durationStr, err)
	}
	return dur, nil
}

// disableUser performs user-level enforcement via Remnawave API.
// Parses internal ID, calls enforcer, logs result.
func (p *LogProcessor) disableUser(ctx context.Context, userEmail string, reason string, score int) error {
	// Parse internal ID
	internalID, err := parseInternalID(userEmail)
	if err != nil {
		metrics.RejectedRequestsTotal.Add(1)
		return err
	}

	// Parse duration
	duration, err := parseBlockDuration(p.cfg.BlockDuration)
	if err != nil {
		log.Printf("Warning: invalid BlockDuration config %q, using default 5m", p.cfg.BlockDuration)
		duration = 5 * time.Minute
	}

	// Call enforcer (with timeout)
	enfCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := p.enforcer.DisableTempByInternalID(enfCtx, internalID, duration, reason, score); err != nil {
		return fmt.Errorf("disable user %d: %w", internalID, err)
	}

	log.Printf("✅ User %d disabled for %v (reason: %s, score: %d)", internalID, duration, reason, score)
	return nil
}

// scheduleASNClear schedules delayed ASN data cleanup
func (p *LogProcessor) scheduleASNClear(ctx context.Context, userEmail string) {
	log.Printf("Scheduling delayed ASN data cleanup for %s in %v", userEmail, p.cfg.ClearIPsDelay)

	time.AfterFunc(p.cfg.ClearIPsDelay, func() {
		if ctx.Err() != nil {
			log.Printf("Delayed ASN data cleanup for %s cancelled due to service shutdown", userEmail)
			return
		}

		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		cleared, err := p.storage.(*storage.RedisStore).ClearUserASNData(opCtx, userEmail)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				log.Printf("Delayed ASN data cleanup for %s cancelled due to service shutdown during execution", userEmail)
			} else if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Timeout during delayed ASN data cleanup for %s", userEmail)
			} else {
				log.Printf("Error during delayed ASN data cleanup for %s: %v", userEmail, err)
			}
			return
		}
		log.Printf("✅ Delayed ASN data cleanup for %s%s completed. Keys cleared: %d",
			userEmail, p.getDebugMarker(userEmail), cleared)
	})
}

func (p *LogProcessor) scheduleIPsClear(ctx context.Context, userEmail string) {
	log.Printf("Scheduling delayed IP cleanup for %s in %v", userEmail, p.cfg.ClearIPsDelay)

	time.AfterFunc(p.cfg.ClearIPsDelay, func() {
		if ctx.Err() != nil {
			log.Printf("Delayed IP cleanup for %s cancelled due to service shutdown", userEmail)
			return
		}

		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		cleared, err := p.storage.ClearUserIPs(opCtx, userEmail)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				log.Printf("Delayed IP cleanup for %s cancelled due to service shutdown during execution", userEmail)
			} else if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Timeout during delayed IP cleanup for %s", userEmail)
			} else {
				log.Printf("Error during delayed IP cleanup for %s: %v", userEmail, err)
			}
			return
		}
		log.Printf("Delayed IP cleanup for %s%s completed. Keys cleared: %d",
			userEmail, p.getDebugMarker(userEmail), cleared)
	})
}

func (p *LogProcessor) scheduleSubnetsClear(ctx context.Context, userEmail string) {
	log.Printf("Scheduling delayed SUBNET cleanup for %s in %v", userEmail, p.cfg.ClearIPsDelay)

	time.AfterFunc(p.cfg.ClearIPsDelay, func() {
		if ctx.Err() != nil {
			log.Printf("Delayed SUBNET cleanup for %s cancelled due to service shutdown", userEmail)
			return
		}

		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		cleared, err := p.storage.ClearUserSubnets(opCtx, userEmail)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				log.Printf("Delayed SUBNET cleanup for %s cancelled due to service shutdown during execution", userEmail)
			} else if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Timeout during delayed SUBNET cleanup for %s", userEmail)
			} else {
				log.Printf("Error during delayed SUBNET cleanup for %s: %v", userEmail, err)
			}
			return
		}
		log.Printf("Delayed SUBNET cleanup for %s%s completed. Keys cleared: %d",
			userEmail, p.getDebugMarker(userEmail), cleared)
	})
}

// performEnhancedAnalytics выполняет расширенную аналитику с GeoIP и скорингом
func (p *LogProcessor) performEnhancedAnalytics(
	ctx context.Context,
	email string,
	allASNs []string,
	asnDetails map[string]*models.ASNInfo,
) (
	*models.GeoAnalysisResult,
	map[string]string,
	*scoring.ViolationScore,
) {
	// Если сервисы не настроены - возвращаем nil
	if p.geoService == nil || p.geoAnalyzer == nil || p.asnClassifier == nil || p.scorer == nil {
		return nil, nil, nil
	}

	// 1. Собираем все IP-адреса пользователя
	allIPs := make([]string, 0)
	for _, info := range asnDetails {
		allIPs = append(allIPs, info.IPs...)
	}

	// 2. Выполняем географический анализ
	geoResultInternal := p.geoAnalyzer.AnalyzeUserIPs(ctx, allIPs)

	// Конвертируем в models.GeoAnalysisResult
	geoResult := &models.GeoAnalysisResult{
		UniqueCountries: geoResultInternal.UniqueCountries,
		UniqueCities:    geoResultInternal.UniqueCities,
		Agglomerations:  geoResultInternal.Agglomerations,
		MaxDistanceKM:   geoResultInternal.MaxDistanceKM,
		GeoScore:        geoResultInternal.GeoScore,
		GeoFlags:        geoResultInternal.GeoFlags,
	}

	// 3. Классифицируем провайдеров и обогащаем ASNInfo
	asnClassifications := make(map[string]*asn.ASNClassification)
	providerTypes := make(map[string]string)

	for asnStr, info := range asnDetails {
		// Используем ClassifyWithCountry для более точной классификации
		classification := p.asnClassifier.ClassifyWithCountry(asnStr, info.Organization, info.Country)
		asnClassifications[asnStr] = classification
		providerTypes[asnStr] = classification.ProviderType

		// Обогащаем ASNInfo
		info.ProviderType = classification.ProviderType
		info.Modifier = classification.Modifier
	}

	// 4. Рассчитываем скор (конвертируем обратно в geoip.GeoAnalysisResult для scorer)
	geoResultForScorer := &geoip.GeoAnalysisResult{
		UniqueCountries: geoResult.UniqueCountries,
		UniqueCities:    geoResult.UniqueCities,
		Agglomerations:  geoResult.Agglomerations,
		MaxDistanceKM:   geoResult.MaxDistanceKM,
		GeoScore:        geoResult.GeoScore,
		GeoFlags:        geoResult.GeoFlags,
	}

	violationScore := p.scorer.Calculate(
		asnClassifications,
		geoResultForScorer,
		len(allASNs),
		p.cfg.MaxASNsPerUser,
	)

	log.Printf("[Anti-Abuse] Analysis for %s: GeoScore=%d, ASNScore=%.1f, FinalScore=%.1f, Action=%s",
		email, geoResult.GeoScore, violationScore.Components.ASNScore,
		violationScore.FinalScore, violationScore.Action)

	return geoResult, providerTypes, violationScore
}
