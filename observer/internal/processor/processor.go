package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"observer_service/internal/config"
	"observer_service/internal/database"
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
	storage           storage.Storage
	enforcer          enforcement.Enforcer
	alerter           alerter.Notifier
	cfg               *config.Config
	asnLookup         *asn.ASNLookup         // Сервис для lookup ASN
	repo              database.Repository    // PostgreSQL repository (optional)
	batchWriter       *BatchWriter           // Batched connection writer (optional)
	logChannel        chan []models.LogEntry  // Канал для получения пачек логов
	sideEffectChannel chan func(context.Context) // Канал для побочных задач (алерты, очистка)

	// Сервисы для Anti-Abuse системы
	geoService    *geoip.GeoIPService   // Сервис геолокации
	geoAnalyzer   *geoip.GeoAnalyzer    // Анализатор географии
	asnClassifier *asn.ASNClassifier    // Классификатор провайдеров
	scorer        *scoring.Scorer       // Система скоринга

	excludedIPsParsed []*net.IPNet // Для случаев когда в ExcludedIPs указан CIDR
}

// NewLogProcessor создает новый экземпляр LogProcessor.
func NewLogProcessor(
	s storage.Storage,
	enf enforcement.Enforcer,
	a alerter.Notifier,
	cfg *config.Config,
	asnLookup *asn.ASNLookup,
	repo database.Repository,
	geoService *geoip.GeoIPService,
	geoAnalyzer *geoip.GeoAnalyzer,
	asnClassifier *asn.ASNClassifier,
	scorer *scoring.Scorer,
) *LogProcessor {
	var bw *BatchWriter
	if repo != nil {
		bw = NewBatchWriter(repo)
	}

	lp := &LogProcessor{
		storage:           s,
		enforcer:          enf,
		alerter:           a,
		cfg:               cfg,
		asnLookup:         asnLookup,
		repo:              repo,
		batchWriter:       bw,
		geoService:        geoService,
		geoAnalyzer:       geoAnalyzer,
		asnClassifier:     asnClassifier,
		scorer:            scorer,
		logChannel:        make(chan []models.LogEntry, cfg.LogChannelBufferSize),
		sideEffectChannel: make(chan func(context.Context), cfg.SideEffectChannelBufferSize),
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

// StartBatchWriter starts the batch writer goroutine for persisting connections.
func (p *LogProcessor) StartBatchWriter(ctx context.Context, wg *sync.WaitGroup) {
	if p.batchWriter != nil {
		p.batchWriter.Run(ctx, wg)
	} else {
		defer wg.Done()
		log.Println("BatchWriter not configured (no PostgreSQL repo), skipping")
	}
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

	p.processEntryByASN(ctx, entry)
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

// processEntryByASN processes connections by ASN (providers)
func (p *LogProcessor) processEntryByASN(ctx context.Context, entry models.LogEntry) {
	var identifier string
	var orgName string

	// Try to get ASN for IP
	if p.asnLookup != nil {
		asnStr, org, err := p.asnLookup.LookupWithOrg(entry.SourceIP)
		if err == nil && asnStr != "" {
			// Check if ASN is in exclusion list
			if p.cfg.ExcludedASNs[asnStr] {
				log.Printf("IP %s (ASN %s - %s) in excluded ASN list, skipping", entry.SourceIP, asnStr, org)
				return
			}
			identifier = asnStr
			orgName = org

			// Cache organization name for later use
			if org != "" {
				if err := p.storage.SetASNOrgName(ctx, asnStr, org, p.cfg.UserASNTTL); err != nil {
					log.Printf("Org caching error for ASN %s: %v", asnStr, err)
				}
			}
		} else {
			// Log for debugging but continue with UNKNOWN fallback
			if err != nil {
				log.Printf("Failed to determine ASN for IP %s (user %s): %v. Using UNKNOWN fallback",
					entry.SourceIP, entry.UserEmail, err)
			}
		}
	}

	// Fallback: use UNKNOWN identifier when ASN not found
	if identifier == "" {
		identifier = "UNKNOWN"
		log.Printf("Warning: ASN not found for IP %s (user %s), using UNKNOWN identifier",
			entry.SourceIP, entry.UserEmail)
	}

	userASNLimit := p.cfg.MaxASNsPerUser
	debugMarker := p.getDebugMarker(entry.UserEmail)

	// Save ASN -> IP mapping BEFORE limit check
	if err := p.storage.AddIPToASNMapping(ctx, entry.UserEmail, identifier, entry.SourceIP, p.cfg.UserASNTTL); err != nil {
		log.Printf("ASN->IP mapping save error for %s: %v. Skipping processing", entry.UserEmail, err)
		return
	}

	// Check and add ASN
	res, err := p.storage.CheckAndAddASN(ctx, entry.UserEmail, identifier, userASNLimit, p.cfg.UserASNTTL, p.cfg.AlertCooldown)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("CheckAndAddASN operation cancelled for %s: %v", entry.UserEmail, err)
		} else {
			log.Printf("Entry processing error (ASN) for %s: %v", entry.UserEmail, err)
		}
		return
	}

	if res.StatusCode == 0 && res.IsNew {
		if orgName != "" {
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
			log.Printf("New ASN for user %s%s: %s (%s)%s | IP: %s. Total: %d/%d",
				entry.UserEmail, debugMarker, identifier, orgName, providerInfo, entry.SourceIP, res.CurrentCount, userASNLimit)

			// Log GeoIP analysis (using already obtained data)
			if geoLoc != nil {
				log.Printf("[GeoIP] %s: %s -> %s, %s (%.2f, %.2f)",
					entry.UserEmail, entry.SourceIP, geoLoc.CountryCode, geoLoc.City,
					geoLoc.Latitude, geoLoc.Longitude)
			}
		} else {
			log.Printf("New ASN for user %s%s: %s | IP: %s. Total: %d/%d",
				entry.UserEmail, debugMarker, identifier, entry.SourceIP, res.CurrentCount, userASNLimit)
		}

		// Persist connection to PostgreSQL via batch writer
		if p.batchWriter != nil {
			var providerType, country, city string
			var lat, lon float64
			if p.asnClassifier != nil {
				classification := p.asnClassifier.ClassifyWithCountry(identifier, orgName, "")
				providerType = string(classification.ProviderType)
			}
			if p.geoService != nil && p.cfg.GeoIPEnabled {
				if loc, err := p.geoService.Lookup(ctx, entry.SourceIP); err == nil && loc != nil {
					country = loc.CountryCode
					city = loc.City
					lat = loc.Latitude
					lon = loc.Longitude
				}
			}
			p.batchWriter.Write(database.UserConnection{
				UserID:       entry.UserEmail,
				SourceIP:     entry.SourceIP,
				ASN:          identifier,
				OrgName:      orgName,
				ProviderType: providerType,
				Country:      country,
				City:         city,
				Lat:          lat,
				Lon:          lon,
			})
		}
	}

	if res.StatusCode == 1 { // Limit exceeded, enforcement
		log.Printf("ASN LIMIT EXCEEDED%s: User %s, count: %d/%d",
			debugMarker, entry.UserEmail, res.CurrentCount, userASNLimit)

		alertPayload := models.AlertPayload{
			UserIdentifier: entry.UserEmail,
			Limit:          userASNLimit,
			BlockDuration:  p.cfg.BlockDuration,
			ViolationType:  "asn_limit_exceeded",
		}

		asnCount := int(res.CurrentCount)
		alertPayload.DetectedASNCount = &asnCount
		alertPayload.AllUserASNs = res.AllUserItems
		alertPayload.ASNDetails = p.collectASNDetails(ctx, entry.UserEmail, res.AllUserItems, identifier, entry.SourceIP)

		// Perform enhanced analytics if enabled
		if p.cfg.ScoringEnabled || p.cfg.GeoIPEnabled {
			geoResult, providerTypes, violationScore := p.performEnhancedAnalytics(
				ctx,
				entry.UserEmail,
				res.AllUserItems,
				alertPayload.ASNDetails,
			)

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
				conf := violationScore.Confidence
				alertPayload.ScoreConfidence = &conf
				alertPayload.ScoreModifiers = violationScore.Modifiers

				// Convert feature results to model
				breakdown := make([]models.ScoreFeatureResult, 0, len(violationScore.Features))
				for _, f := range violationScore.Features {
					breakdown = append(breakdown, models.ScoreFeatureResult{
						Name:       f.Name,
						Score:      f.Score,
						Weight:     f.Weight,
						Confidence: f.Confidence,
						Details:    f.Details,
					})
				}
				alertPayload.ScoreBreakdown = breakdown

				// Persist scoring event to database
				if p.repo != nil {
					breakdownJSON, _ := json.Marshal(breakdown)
					scoreEvent := &database.UserScoreEvent{
						UserID:         entry.UserEmail,
						SourceIP:       entry.SourceIP,
						ASN:            identifier,
						IsNewASN:       res.IsNew,
						ScoreTotal:     violationScore.FinalScore,
						ScoreAction:    string(violationScore.Action),
						ScoreBreakdown: string(breakdownJSON),
					}
					if err := p.repo.InsertScoreEvent(ctx, scoreEvent); err != nil {
						log.Printf("Error saving score event for %s: %v", entry.UserEmail, err)
					}
				}

				if violationScore.Action == scoring.ActionNone {
					log.Printf("[Anti-Abuse] Score %.1f for %s, action=none, block cancelled",
						violationScore.FinalScore, entry.UserEmail)
					return
				}
			}
		}

		// User-level enforcement
		enfReason := fmt.Sprintf("asn_limit_exceeded: %d/%d ASNs", res.CurrentCount, userASNLimit)
		var enfScore int
		if alertPayload.Score != nil {
			enfScore = int(*alertPayload.Score)
		} else {
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
func (p *LogProcessor) collectASNDetails(ctx context.Context, email string, asns []string, currentASN, currentIP string) map[string]*models.ASNInfo {
	result := make(map[string]*models.ASNInfo)

	for _, asnID := range asns {
		// Get IP addresses for this ASN
		ips, err := p.storage.GetIPsForUserASN(ctx, email, asnID)
		if err != nil {
			log.Printf("Error getting IPs for ASN %s user %s: %v", asnID, email, err)
			ips = []string{}
		}

		// If this is current ASN and current IP not in list - add it
		if asnID == currentASN && currentIP != "" {
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

		// Get organization name from Redis cache
		org, err := p.storage.GetASNOrgName(ctx, asnID)
		if err != nil {
			log.Printf("Error getting org from cache for ASN %s: %v", asnID, err)
		}

		// Get country code via lookup
		var countryCode string
		if p.asnLookup != nil && len(ips) > 0 {
			if org == "" {
				_, orgName, country, err := p.asnLookup.LookupFull(ips[0])
				if err == nil {
					if orgName != "" {
						org = orgName
						if cacheErr := p.storage.SetASNOrgName(ctx, asnID, orgName, p.cfg.UserASNTTL); cacheErr != nil {
							log.Printf("Org caching error for ASN %s: %v", asnID, cacheErr)
						}
					}
					countryCode = country
				}
			} else {
				_, _, country, err := p.asnLookup.LookupFull(ips[0])
				if err == nil {
					countryCode = country
				}
			}
		}

		if org == "" {
			org = "Unknown"
		}

		result[asnID] = &models.ASNInfo{
			ASN:          asnID,
			Organization: org,
			Country:      countryCode,
			IPs:          ips,
			IPCount:      len(ips),
		}
	}

	return result
}

// collectIPsForASNBlock collects all IPs for blocking based on ASNs
func (p *LogProcessor) collectIPsForASNBlock(ctx context.Context, email string, identifiers []string, currentIP string) []string {
	var result []string
	seenIPs := make(map[string]struct{})

	if currentIP != "" {
		seenIPs[currentIP] = struct{}{}
		result = append(result, currentIP)
	}

	for _, item := range identifiers {
		if p.cfg.ExcludedASNs[item] {
			log.Printf("ASN %s in excluded list, skipping during IP collection for block", item)
			continue
		}

		ips, err := p.storage.GetIPsForUserASN(ctx, email, item)
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
	}

	if len(result) == 0 {
		log.Printf("Warning: No IP addresses found for blocking user %s", email)
	}

	return result
}

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
func (p *LogProcessor) disableUser(ctx context.Context, userEmail string, reason string, score int) error {
	internalID, err := parseInternalID(userEmail)
	if err != nil {
		metrics.RejectedRequestsTotal.Add(1)
		return err
	}

	duration, err := parseBlockDuration(p.cfg.BlockDuration)
	if err != nil {
		log.Printf("Warning: invalid BlockDuration config %q, using default 5m", p.cfg.BlockDuration)
		duration = 5 * time.Minute
	}

	enfCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := p.enforcer.DisableTempByInternalID(enfCtx, internalID, duration, reason, score); err != nil {
		return fmt.Errorf("disable user %d: %w", internalID, err)
	}

	log.Printf("User %d disabled for %v (reason: %s, score: %d)", internalID, duration, reason, score)
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

		cleared, err := p.storage.ClearUserASNData(opCtx, userEmail)
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
		log.Printf("Delayed ASN data cleanup for %s%s completed. Keys cleared: %d",
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
	if p.geoService == nil || p.geoAnalyzer == nil || p.asnClassifier == nil || p.scorer == nil {
		return nil, nil, nil
	}

	// 1. Collect all user IPs
	allIPs := make([]string, 0)
	for _, info := range asnDetails {
		allIPs = append(allIPs, info.IPs...)
	}

	// 2. Geographic analysis
	geoResultInternal := p.geoAnalyzer.AnalyzeUserIPs(ctx, allIPs)

	geoResult := &models.GeoAnalysisResult{
		UniqueCountries: geoResultInternal.UniqueCountries,
		UniqueCities:    geoResultInternal.UniqueCities,
		Agglomerations:  geoResultInternal.Agglomerations,
		MaxDistanceKM:   geoResultInternal.MaxDistanceKM,
		GeoScore:        geoResultInternal.GeoScore,
		GeoFlags:        geoResultInternal.GeoFlags,
	}

	// 3. Classify providers
	asnClassifications := make(map[string]*asn.ASNClassification)
	providerTypes := make(map[string]string)

	for asnStr, info := range asnDetails {
		classification := p.asnClassifier.ClassifyWithCountry(asnStr, info.Organization, info.Country)
		asnClassifications[asnStr] = classification
		providerTypes[asnStr] = classification.ProviderType

		info.ProviderType = classification.ProviderType
		info.Modifier = classification.Modifier
	}

	// 4. Calculate score
	geoResultForScorer := &geoip.GeoAnalysisResult{
		UniqueCountries: geoResult.UniqueCountries,
		UniqueCities:    geoResult.UniqueCities,
		Agglomerations:  geoResult.Agglomerations,
		MaxDistanceKM:   geoResult.MaxDistanceKM,
		GeoScore:        geoResult.GeoScore,
		GeoFlags:        geoResult.GeoFlags,
	}

	violationScore := p.scorer.Calculate(&scoring.ScoringInput{
		ASNClassifications: asnClassifications,
		GeoResult:          geoResultForScorer,
		UniqueCount:        len(allASNs),
		Limit:              p.cfg.MaxASNsPerUser,
	})

	log.Printf("[Anti-Abuse] Analysis for %s: GeoScore=%d, ASNScore=%.1f, FinalScore=%.1f, Confidence=%.2f, Action=%s",
		email, geoResult.GeoScore, violationScore.GetFeatureScore("asn"),
		violationScore.FinalScore, violationScore.Confidence, violationScore.Action)

	return geoResult, providerTypes, violationScore
}
