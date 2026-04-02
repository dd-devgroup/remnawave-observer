package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"observer_service/internal/config"
	"observer_service/internal/database"
	"observer_service/internal/iputil"
	"observer_service/internal/metrics"
	"observer_service/internal/models"
	"observer_service/internal/services/alerter"
	"observer_service/internal/services/asn"
	"observer_service/internal/services/enforcement"
	"observer_service/internal/services/geoip"
	"observer_service/internal/services/remnawave"
	"observer_service/internal/services/scoring"
	"observer_service/internal/services/storage"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LogProcessor обрабатывает входящие логи.
type LogProcessor struct {
	storage           storage.Storage
	enforcer          enforcement.Enforcer
	alerter           alerter.Notifier
	cfg               *config.Config
	asnLookup         *asn.ASNLookup             // Сервис для lookup ASN
	repo              database.Repository        // PostgreSQL repository (optional)
	batchWriter       *BatchWriter               // Batched connection writer (optional)
	logChannel        chan []models.LogEntry     // Канал для получения пачек логов
	sideEffectChannel chan func(context.Context) // Канал для побочных задач (алерты, очистка)

	// Сервисы для Anti-Abuse системы
	geoService    *geoip.GeoIPService // Сервис геолокации
	geoAnalyzer   *geoip.GeoAnalyzer  // Анализатор географии
	asnClassifier *asn.ASNClassifier  // Классификатор провайдеров
	scorer        *scoring.Scorer     // Система скоринга
	userEvidence  UserEvidenceProvider
	userExcluder  UserExcluder
	nodeRecorder  NodeObservationRecorder
	ipMitigator   UserIPMitigator

	excludedIPsParsed []*net.IPNet // Для случаев когда в ExcludedIPs указан CIDR

	logWorkerBase  int
	logWorkerMax   int
	logWorkerCount atomic.Int32
	logWorkerSeq   atomic.Int64
}

const (
	logWorkerScaleInterval    = 500 * time.Millisecond
	elasticWorkerIdleTimeout  = 3 * time.Second
	enqueueBackpressureMin    = 20 * time.Millisecond
	enqueueBackpressureMax    = 120 * time.Millisecond
	defaultEnforcementTimeout = 10 * time.Second
	defaultASNClearTimeout    = 10 * time.Second
)

// UserExcluder decides whether a user should bypass anti-sharing.
type UserExcluder interface {
	IsExcludedByInternalID(ctx context.Context, internalID int64) (bool, error)
}

// NodeObservationRecorder stores user/IP -> node observations.
type NodeObservationRecorder interface {
	RecordNodeObservation(ctx context.Context, userID, ip, nodeUUID string, ttl time.Duration) error
}

// UserIPMitigator applies local IP mitigation on nodes.
type UserIPMitigator interface {
	BlockUserIPs(ctx context.Context, userID string, ips []string, duration time.Duration) error
}

// UserEvidenceProvider loads Remnawave evidence for scoring decisions.
type UserEvidenceProvider interface {
	GetUserEvidenceByInternalID(ctx context.Context, internalID int64) (*remnawave.UserEvidence, error)
	GetUserIPSnapshotByInternalID(ctx context.Context, internalID int64, timeout, pollInterval time.Duration) (*remnawave.UserIPSnapshotResult, error)
}

type scoringTriggerContext struct {
	TriggerKind    string
	TriggerIPCount int
	ObserveOnly    bool
	DeepCheckUsed  bool
	DeepCheck      *models.DeepCheckSummary
}

func resolveBaseLogWorkerCount(configured int) int {
	if configured > 0 {
		return configured
	}

	base := runtime.GOMAXPROCS(0)
	if base < 2 {
		base = 2
	}

	return base
}

func resolveMaxLogWorkerCount(base, queueCap int) int {
	maxWorkers := base * 4
	if cpuDriven := runtime.GOMAXPROCS(0) * 8; cpuDriven > maxWorkers {
		maxWorkers = cpuDriven
	}
	if queueDriven := queueCap / 2; queueDriven > maxWorkers {
		maxWorkers = queueDriven
	}
	if maxWorkers < base {
		maxWorkers = base
	}
	if maxWorkers > 256 {
		maxWorkers = 256
	}

	return maxWorkers
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

	logWorkerBase := resolveBaseLogWorkerCount(cfg.WorkerPoolSize)

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
		logWorkerBase:     logWorkerBase,
		logWorkerMax:      resolveMaxLogWorkerCount(logWorkerBase, cfg.LogChannelBufferSize),
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
	log.Printf("Starting log processing worker pool (base: %d, max: %d, queue buffer: %d)...",
		p.logWorkerBase, p.logWorkerMax, cap(p.logChannel))

	for i := 0; i < p.logWorkerBase; i++ {
		p.startLogWorker(ctx, &workerWg, false)
	}

	scaleTicker := time.NewTicker(logWorkerScaleInterval)
	defer scaleTicker.Stop()

	for {
		select {
		case <-scaleTicker.C:
			p.scaleLogWorkers(ctx, &workerWg)
		case <-ctx.Done():
			log.Println("Stop signal received for log processing workers. Closing channel...")
			close(p.logChannel)
			workerWg.Wait()
			log.Println("All log processing workers stopped successfully")
			return
		}
	}
}

func (p *LogProcessor) startLogWorker(ctx context.Context, workerWg *sync.WaitGroup, elastic bool) {
	workerID := p.logWorkerSeq.Add(1)
	workerType := "base"
	if elastic {
		workerType = "elastic"
	}

	p.logWorkerCount.Add(1)
	workerWg.Add(1)
	go func(id int64, typ string, isElastic bool) {
		defer workerWg.Done()
		defer p.logWorkerCount.Add(-1)

		log.Printf("Log processing %s worker %d started", typ, id)
		if isElastic {
			p.runElasticLogWorker(ctx)
		} else {
			p.runBaseLogWorker(ctx)
		}
		log.Printf("Log processing %s worker %d stopping", typ, id)
	}(workerID, workerType, elastic)
}

func (p *LogProcessor) runBaseLogWorker(ctx context.Context) {
	for entries := range p.logChannel {
		p.ProcessEntries(ctx, entries)
	}
}

func (p *LogProcessor) runElasticLogWorker(ctx context.Context) {
	idleTimer := time.NewTimer(elasticWorkerIdleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case entries, ok := <-p.logChannel:
			if !ok {
				return
			}
			p.ProcessEntries(ctx, entries)
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(elasticWorkerIdleTimeout)
		case <-idleTimer.C:
			// Shrink only when queue is empty and we are above base size.
			if len(p.logChannel) == 0 && int(p.logWorkerCount.Load()) > p.logWorkerBase {
				return
			}
			idleTimer.Reset(elasticWorkerIdleTimeout)
		case <-ctx.Done():
			return
		}
	}
}

func (p *LogProcessor) targetLogWorkerCount() int {
	queueLen := len(p.logChannel)
	queueCap := cap(p.logChannel)
	target := p.logWorkerBase
	if queueCap <= 0 || queueLen == 0 {
		return target
	}

	fillRatio := float64(queueLen) / float64(queueCap)
	if fillRatio >= 0.7 {
		dynamicRange := p.logWorkerMax - p.logWorkerBase
		ramp := int(fillRatio * float64(dynamicRange))
		if ramp < 1 {
			ramp = 1
		}
		target = p.logWorkerBase + ramp
	}

	if queueDriven := p.logWorkerBase + queueLen/4; queueDriven > target {
		target = queueDriven
	}
	if target > p.logWorkerMax {
		target = p.logWorkerMax
	}
	if target < p.logWorkerBase {
		target = p.logWorkerBase
	}

	return target
}

func (p *LogProcessor) scaleLogWorkers(ctx context.Context, workerWg *sync.WaitGroup) {
	target := p.targetLogWorkerCount()
	current := int(p.logWorkerCount.Load())
	if target <= current {
		return
	}

	toSpawn := target - current
	for i := 0; i < toSpawn; i++ {
		p.startLogWorker(ctx, workerWg, true)
	}

	log.Printf("Log worker auto-scale: queue %d/%d, workers %d -> %d",
		len(p.logChannel), cap(p.logChannel), current, target)
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
func (p *LogProcessor) EnqueueEntries(entries []models.LogEntry) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Attempted to write to closed log channel. Service is shutting down")
			err = errors.New("log channel is closed, service is shutting down")
		}
	}()

	select {
	case p.logChannel <- entries:
		return nil
	default:
		backpressureWait := p.calculateEnqueueBackpressureWait()
		timer := time.NewTimer(backpressureWait)
		defer timer.Stop()

		select {
		case p.logChannel <- entries:
			return nil
		case <-timer.C:
			return errors.New("log channel is full, rejecting new entries")
		}
	}
}

func (p *LogProcessor) calculateEnqueueBackpressureWait() time.Duration {
	queueCap := cap(p.logChannel)
	if queueCap <= 0 {
		return enqueueBackpressureMin
	}

	fillRatio := float64(len(p.logChannel)) / float64(queueCap)
	wait := enqueueBackpressureMin

	switch {
	case fillRatio >= 0.95:
		wait = enqueueBackpressureMax
	case fillRatio >= 0.80:
		wait = 80 * time.Millisecond
	case fillRatio >= 0.60:
		wait = 50 * time.Millisecond
	}

	if extraWorkers := int(p.logWorkerCount.Load()) - p.logWorkerBase; extraWorkers > 0 {
		relief := time.Duration(extraWorkers*2) * time.Millisecond
		wait -= relief
		if wait < 15*time.Millisecond {
			wait = 15 * time.Millisecond
		}
	}

	if wait < enqueueBackpressureMin {
		wait = enqueueBackpressureMin
	}
	if wait > enqueueBackpressureMax {
		wait = enqueueBackpressureMax
	}

	return wait
}

// SetUserExcluder configures optional squad-based user exclusions.
func (p *LogProcessor) SetUserExcluder(excluder UserExcluder) {
	p.userExcluder = excluder
}

// SetNodeObservationRecorder configures optional user/IP -> node tracking.
func (p *LogProcessor) SetNodeObservationRecorder(recorder NodeObservationRecorder) {
	p.nodeRecorder = recorder
}

// SetUserIPMitigator configures optional local IP mitigation.
func (p *LogProcessor) SetUserIPMitigator(mitigator UserIPMitigator) {
	p.ipMitigator = mitigator
}

// SetUserEvidenceProvider configures optional Remnawave evidence lookups.
func (p *LogProcessor) SetUserEvidenceProvider(provider UserEvidenceProvider) {
	p.userEvidence = provider
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
	if p.isExcludedByInternalSquad(ctx, entry.UserEmail) {
		return
	}
	if p.nodeRecorder != nil && entry.NodeUUID != "" {
		if err := p.nodeRecorder.RecordNodeObservation(ctx, entry.UserEmail, entry.SourceIP, entry.NodeUUID, p.cfg.UserASNTTL); err != nil {
			log.Printf("Node observation record error for %s/%s on %s: %v", entry.UserEmail, entry.SourceIP, entry.NodeUUID, err)
		}
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

func (p *LogProcessor) isExcludedByInternalSquad(ctx context.Context, userEmail string) bool {
	if p.userExcluder == nil {
		return false
	}
	internalID, err := strconv.ParseInt(userEmail, 10, 64)
	if err != nil {
		return false
	}
	excluded, err := p.userExcluder.IsExcludedByInternalID(ctx, internalID)
	if err != nil {
		log.Printf("Internal squad exclusion check failed for %s: %v", userEmail, err)
		return false
	}
	return excluded
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

	debugMarker := p.getDebugMarker(entry.UserEmail)

	// Track user IPs inside the ASN hot window before ASN scoring checks.
	ipTrack, err := p.storage.TrackIPForASN(ctx, entry.UserEmail, identifier, entry.SourceIP, p.cfg.UserASNTTL)
	if err != nil {
		log.Printf("ASN->IP tracking error for %s: %v. Skipping processing", entry.UserEmail, err)
		return
	}

	// Check and add ASN
	res, err := p.storage.CheckAndAddASN(ctx, entry.UserEmail, identifier, 0, p.cfg.UserASNTTL, 0)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("CheckAndAddASN operation cancelled for %s: %v", entry.UserEmail, err)
		} else {
			log.Printf("Entry processing error (ASN) for %s: %v", entry.UserEmail, err)
		}
		return
	}

	if res.StatusCode != 0 {
		log.Printf("[Anti-Abuse] Ignoring legacy ASN status=%d for %s; scoring-only mode is active", res.StatusCode, entry.UserEmail)
		return
	}

	if res.IsNew {
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
			log.Printf("New ASN for user %s%s: %s (%s)%s | IP: %s. Active providers: %d",
				entry.UserEmail, debugMarker, identifier, orgName, providerInfo, entry.SourceIP, res.CurrentCount)

			// Log GeoIP analysis (using already obtained data)
			if geoLoc != nil {
				log.Printf("[GeoIP] %s: %s -> %s, %s (%.2f, %.2f)",
					entry.UserEmail, entry.SourceIP, geoLoc.CountryCode, geoLoc.City,
					geoLoc.Latitude, geoLoc.Longitude)
			}
		} else {
			log.Printf("New ASN for user %s%s: %s | IP: %s. Active providers: %d",
				entry.UserEmail, debugMarker, identifier, entry.SourceIP, res.CurrentCount)
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

		// Calculate and persist scoring for the new ASN
		// This enables progressive enforcement (monitor/warn/temp_disable) based on score
		if p.scorer != nil && p.repo != nil {
			scoreASNs := res.AllUserItems
			if len(scoreASNs) == 0 && identifier != "" {
				scoreASNs = []string{identifier}
				log.Printf("[Scoring] AllUserItems is empty for %s, using current ASN fallback: %s", entry.UserEmail, identifier)
			}
			_, _, _ = p.calculateAndPersistScore(ctx, entry, identifier, orgName, true, scoreASNs, scoringTriggerContext{
				TriggerKind: "new_asn",
			})
		}
		return
	}

	if !p.cfg.IPRescoringEnabled || ipTrack == nil || !ipTrack.IsNewIP || p.scorer == nil || p.repo == nil {
		return
	}

	scoreASNs := res.AllUserItems
	if len(scoreASNs) == 0 && identifier != "" {
		scoreASNs = []string{identifier}
	}

	if !p.shouldTriggerIPRescore(ctx, entry, identifier, orgName, scoreASNs, int(ipTrack.CurrentCount)) {
		return
	}

	_, _, _ = p.calculateAndPersistScore(ctx, entry, identifier, orgName, false, scoreASNs, scoringTriggerContext{
		TriggerKind:    "ip_threshold",
		TriggerIPCount: int(ipTrack.CurrentCount),
		ObserveOnly:    true,
	})
}

func (p *LogProcessor) shouldTriggerIPRescore(
	ctx context.Context,
	entry models.LogEntry,
	currentASN string,
	orgName string,
	allASNs []string,
	ipCount int,
) bool {
	if ipCount <= 0 || p.asnClassifier == nil || p.cfg == nil {
		return false
	}

	asnDetails := p.collectASNDetails(ctx, entry.UserEmail, allASNs, currentASN, entry.SourceIP)
	currentInfo, ok := asnDetails[currentASN]
	if !ok {
		currentInfo = &models.ASNInfo{ASN: currentASN, Organization: orgName}
		asnDetails[currentASN] = currentInfo
	}
	if currentInfo.Organization == "" {
		currentInfo.Organization = orgName
	}

	classification := p.asnClassifier.ClassifyWithCountry(currentASN, currentInfo.Organization, currentInfo.Country)
	if classification == nil {
		return false
	}

	isMobile := classification.IsMobileProvider()
	if isMobile && !p.hasRiskyProviderMix(asnDetails, currentASN) {
		return false
	}

	base := int(math.Ceil(float64(p.cfg.IPRescoringBase) / math.Max(classification.Modifier, 0.1)))
	if base < 2 {
		base = 2
	}
	return ipCount >= base && ipCount%base == 0
}

func (p *LogProcessor) hasRiskyProviderMix(asnDetails map[string]*models.ASNInfo, currentASN string) bool {
	if p.asnClassifier == nil {
		return false
	}
	for asnID, info := range asnDetails {
		if asnID == currentASN {
			continue
		}
		classification := p.asnClassifier.ClassifyWithCountry(asnID, info.Organization, info.Country)
		if classification != nil && classification.Modifier >= 1.2 {
			return true
		}
	}
	return false
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

	enfCtx, cancel := context.WithTimeout(ctx, defaultEnforcementTimeout)
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

		opCtx, cancel := context.WithTimeout(ctx, defaultASNClearTimeout)
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
	currentSourceIP string,
	allASNs []string,
	asnDetails map[string]*models.ASNInfo,
	trigger scoringTriggerContext,
) (
	*models.GeoAnalysisResult,
	map[string]string,
	*scoring.ViolationScore,
	scoringTriggerContext,
) {
	if p.asnClassifier == nil || p.scorer == nil {
		return nil, nil, nil, trigger
	}

	if trigger.TriggerKind == "ip_threshold" && p.cfg.IPRescoringDeepCheckEnabled {
		trigger = p.applyDeepCheckSnapshot(ctx, email, asnDetails, trigger)
	}

	// 1. Collect all user IPs
	allIPs := make([]string, 0)
	for _, info := range asnDetails {
		allIPs = append(allIPs, info.IPs...)
	}

	// 2. Geographic analysis (optional).
	var geoResultInternal *geoip.GeoAnalysisResult
	var geoResult *models.GeoAnalysisResult
	if p.geoService != nil && p.geoAnalyzer != nil {
		geoResultInternal = p.geoAnalyzer.AnalyzeUserIPs(ctx, allIPs)
		geoResult = &models.GeoAnalysisResult{
			UniqueCountries: geoResultInternal.UniqueCountries,
			UniqueCities:    geoResultInternal.UniqueCities,
			Agglomerations:  geoResultInternal.Agglomerations,
			MaxDistanceKM:   geoResultInternal.MaxDistanceKM,
			GeoScore:        geoResultInternal.GeoScore,
			GeoFlags:        geoResultInternal.GeoFlags,
		}
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
	var geoResultForScorer *geoip.GeoAnalysisResult
	if geoResult != nil {
		geoResultForScorer = &geoip.GeoAnalysisResult{
			UniqueCountries: geoResult.UniqueCountries,
			UniqueCities:    geoResult.UniqueCities,
			Agglomerations:  geoResult.Agglomerations,
			MaxDistanceKM:   geoResult.MaxDistanceKM,
			GeoScore:        geoResult.GeoScore,
			GeoFlags:        geoResult.GeoFlags,
		}
	}

	// Build per-ASN IP count map for IPDensityFeature.
	ipsPerASN := make(map[string]int, len(asnDetails))
	for asnStr, info := range asnDetails {
		ipsPerASN[asnStr] = len(info.IPs)
	}

	violationScore := p.scorer.Calculate(&scoring.ScoringInput{
		ASNClassifications: asnClassifications,
		GeoResult:          geoResultForScorer,
		IPsPerASN:          ipsPerASN,
	})
	p.applyRemnawaveEvidence(ctx, email, currentSourceIP, violationScore)
	if trigger.TriggerKind == "ip_threshold" && !trigger.DeepCheckUsed {
		violationScore.Confidence = math.Max(0.2, violationScore.Confidence-0.2)
		violationScore.Action = p.scorer.DetermineAction(violationScore.FinalScore, violationScore.Confidence)
		violationScore.Modifiers = append(violationScore.Modifiers, "deep_check_unavailable")
	}

	geoScore := 0
	if geoResult != nil {
		geoScore = geoResult.GeoScore
	}
	log.Printf("[Anti-Abuse] Analysis for %s: GeoScore=%d, ASNScore=%.1f, FinalScore=%.1f, Confidence=%.2f, Action=%s",
		email, geoScore, violationScore.GetFeatureScore("asn"),
		violationScore.FinalScore, violationScore.Confidence, violationScore.Action)

	return geoResult, providerTypes, violationScore, trigger
}

// calculateAndPersistScore performs scoring analysis for a user ASN event,
// persists the result to database, and applies progressive enforcement actions.
// Returns: geoResult, providerTypes, violationScore
func (p *LogProcessor) calculateAndPersistScore(
	ctx context.Context,
	entry models.LogEntry,
	identifier string, // ASN
	orgName string,
	isNewASN bool,
	allASNs []string,
	trigger scoringTriggerContext,
) (*models.GeoAnalysisResult, map[string]string, *scoring.ViolationScore) {
	// Skip if scoring disabled or components not initialized
	if p.scorer == nil || p.repo == nil || p.asnClassifier == nil {
		return nil, nil, nil
	}

	// Collect ASN details for analytics
	asnDetails := p.collectASNDetails(ctx, entry.UserEmail, allASNs, identifier, entry.SourceIP)
	if len(asnDetails) == 0 {
		log.Printf("[Scoring] No ASN details available for %s", entry.UserEmail)
		return nil, nil, nil
	}

	// Perform enhanced analytics (geo analysis + provider classification + scoring)
	geoResultInternal, providerTypes, violationScore, trigger := p.performEnhancedAnalytics(
		ctx,
		entry.UserEmail,
		entry.SourceIP,
		allASNs,
		asnDetails,
		trigger,
	)

	if violationScore == nil {
		return nil, nil, nil
	}

	// Convert geoResult to model type
	var geoResult *models.GeoAnalysisResult
	if geoResultInternal != nil {
		geoResult = &models.GeoAnalysisResult{
			UniqueCountries: geoResultInternal.UniqueCountries,
			UniqueCities:    geoResultInternal.UniqueCities,
			Agglomerations:  geoResultInternal.Agglomerations,
			MaxDistanceKM:   geoResultInternal.MaxDistanceKM,
			GeoScore:        geoResultInternal.GeoScore,
			GeoFlags:        geoResultInternal.GeoFlags,
		}
	}

	// Convert feature results to model for persistence
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

	// Persist scoring event to user_score_events table
	breakdownJSON, _ := json.Marshal(breakdown)
	scoreEvent := &database.UserScoreEvent{
		UserID:         entry.UserEmail,
		SourceIP:       entry.SourceIP,
		ASN:            identifier,
		IsNewASN:       isNewASN,
		TriggerKind:    trigger.TriggerKind,
		TriggerIPCount: trigger.TriggerIPCount,
		ObserveOnly:    trigger.ObserveOnly,
		DeepCheckUsed:  trigger.DeepCheckUsed,
		ScoreTotal:     violationScore.FinalScore,
		ScoreAction:    string(violationScore.Action),
		ScoreBreakdown: string(breakdownJSON),
	}
	if err := p.repo.InsertScoreEvent(ctx, scoreEvent); err != nil {
		log.Printf("[Scoring] Error saving score event for %s: %v", entry.UserEmail, err)
	}

	log.Printf("[Scoring] User %s: Score=%.1f, Confidence=%.2f, Action=%s (isNew=%v)",
		entry.UserEmail, violationScore.FinalScore, violationScore.Confidence,
		violationScore.Action, isNewASN)

	// Persist antiabuse action if action is significant (>= monitor)
	// Actions: none < monitor < warn < soft_challenge < temp_disable < hard_disable
	if violationScore.Action != scoring.ActionNone && !trigger.ObserveOnly {
		actionRecord := &database.AntiAbuseAction{
			UserID:         entry.UserEmail,
			ActionType:     string(violationScore.Action),
			Reason:         fmt.Sprintf("%s_scoring: ASN=%s, score=%.1f", trigger.TriggerKind, identifier, violationScore.FinalScore),
			Score:          violationScore.FinalScore,
			ScoreBreakdown: string(breakdownJSON),
		}
		if err := p.repo.InsertAction(ctx, actionRecord); err != nil {
			log.Printf("[Scoring] Error saving antiabuse action for %s: %v", entry.UserEmail, err)
		}
	}

	p.maybeQueueScoringAlert(ctx, entry, allASNs, asnDetails, geoResult, providerTypes, violationScore, trigger)

	// Apply enforcement for temp_disable or hard_disable actions
	if !trigger.ObserveOnly && (violationScore.Action == scoring.ActionTempDisable || violationScore.Action == scoring.ActionHardDisable) {
		enfReason := fmt.Sprintf("scoring_threshold_exceeded: ASN=%s, score=%.1f, action=%s",
			identifier, violationScore.FinalScore, violationScore.Action)
		enfScore := int(violationScore.FinalScore)

		if err := p.applyBlockingEnforcement(ctx, entry, enfReason, enfScore, allASNs); err != nil {
			log.Printf("[Scoring] Enforcement error for %s: %v", entry.UserEmail, err)
		} else {
			log.Printf("[Scoring] User %s disabled due to score=%.1f, action=%s",
				entry.UserEmail, violationScore.FinalScore, violationScore.Action)
		}

		// Log for debugging (include geo/provider info if available)
		debugInfo := fmt.Sprintf("ASN=%s", identifier)
		if geoResult != nil {
			debugInfo += fmt.Sprintf(", GeoScore=%d, Countries=%d", geoResult.GeoScore, len(geoResult.UniqueCountries))
		}
		if providerTypes != nil && len(providerTypes) > 0 {
			debugInfo += fmt.Sprintf(", Providers=%v", providerTypes)
		}
		log.Printf("[Scoring] Enforcement details for %s: %s", entry.UserEmail, debugInfo)
	}

	return geoResult, providerTypes, violationScore
}

func (p *LogProcessor) applyDeepCheckSnapshot(
	ctx context.Context,
	userID string,
	asnDetails map[string]*models.ASNInfo,
	trigger scoringTriggerContext,
) scoringTriggerContext {
	if p.userEvidence == nil {
		return p.markDeepCheckUnavailable(trigger)
	}
	internalID, err := strconv.ParseInt(strings.TrimSpace(userID), 10, 64)
	if err != nil || internalID <= 0 {
		return p.markDeepCheckUnavailable(trigger)
	}
	snapshot, err := p.userEvidence.GetUserIPSnapshotByInternalID(ctx, internalID, p.cfg.IPRescoringDeepCheckTimeout, p.cfg.IPRescoringDeepCheckResultPoll)
	if err != nil || snapshot == nil || snapshot.Status == "failed" {
		if err != nil {
			log.Printf("[Scoring] Remnawave deep-check failed for %s: %v", userID, err)
		}
		return p.markDeepCheckUnavailable(trigger)
	}

	seenCountries := make(map[string]struct{})
	seenIPs := make(map[string]struct{})
	now := time.Now().UTC()
	var maxAge time.Duration
	for _, node := range snapshot.Nodes {
		if node.CountryCode != "" {
			seenCountries[node.CountryCode] = struct{}{}
		}
		for _, nodeIP := range node.IPs {
			if nodeIP.IP == "" {
				continue
			}
			seenIPs[nodeIP.IP] = struct{}{}
			if !nodeIP.LastSeen.IsZero() {
				age := now.Sub(nodeIP.LastSeen.UTC())
				if age > maxAge {
					maxAge = age
				}
			}
			p.mergeDeepCheckIP(ctx, asnDetails, nodeIP.IP, node.CountryCode)
		}
	}

	countries := make([]string, 0, len(seenCountries))
	for country := range seenCountries {
		countries = append(countries, country)
	}
	sort.Strings(countries)
	ips := make([]string, 0, len(seenIPs))
	for ip := range seenIPs {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	trigger.DeepCheckUsed = len(snapshot.Nodes) > 0
	trigger.DeepCheck = &models.DeepCheckSummary{
		NodeCount:             len(snapshot.Nodes),
		Countries:             countries,
		IPs:                   ips,
		MaxLastSeenAgeSeconds: int64(maxAge / time.Second),
		CrossNodeSpread:       len(snapshot.Nodes) > 1,
	}
	return trigger
}

func (p *LogProcessor) mergeDeepCheckIP(ctx context.Context, asnDetails map[string]*models.ASNInfo, ip, fallbackCountry string) {
	if strings.TrimSpace(ip) == "" || p.asnLookup == nil {
		return
	}
	asnID, orgName, country, err := p.asnLookup.LookupFull(ip)
	if err != nil || asnID == "" {
		return
	}
	if country == "" {
		country = fallbackCountry
	}
	info, exists := asnDetails[asnID]
	if !exists {
		info = &models.ASNInfo{
			ASN:          asnID,
			Organization: orgName,
			Country:      country,
			IPs:          []string{},
		}
		asnDetails[asnID] = info
	}
	if info.Organization == "" && orgName != "" {
		info.Organization = orgName
	}
	if info.Country == "" && country != "" {
		info.Country = country
	}
	for _, existing := range info.IPs {
		if existing == ip {
			info.IPCount = len(info.IPs)
			return
		}
	}
	info.IPs = append(info.IPs, ip)
	info.IPCount = len(info.IPs)
	if orgName != "" && p.storage != nil {
		if err := p.storage.SetASNOrgName(ctx, asnID, orgName, p.cfg.UserASNTTL); err != nil {
			log.Printf("Org caching error for ASN %s during deep-check merge: %v", asnID, err)
		}
	}
}

func (p *LogProcessor) markDeepCheckUnavailable(trigger scoringTriggerContext) scoringTriggerContext {
	trigger.DeepCheckUsed = false
	if trigger.DeepCheck == nil {
		trigger.DeepCheck = &models.DeepCheckSummary{}
	}
	return trigger
}

func toModelScoreBreakdown(features []scoring.FeatureResult) []models.ScoreFeatureResult {
	breakdown := make([]models.ScoreFeatureResult, 0, len(features))
	for _, f := range features {
		breakdown = append(breakdown, models.ScoreFeatureResult{
			Name:       f.Name,
			Score:      f.Score,
			Weight:     f.Weight,
			Confidence: f.Confidence,
			Details:    f.Details,
		})
	}
	return breakdown
}

func (p *LogProcessor) maybeQueueScoringAlert(
	ctx context.Context,
	entry models.LogEntry,
	allASNs []string,
	asnDetails map[string]*models.ASNInfo,
	geoResult *models.GeoAnalysisResult,
	providerTypes map[string]string,
	violationScore *scoring.ViolationScore,
	trigger scoringTriggerContext,
) {
	if p.alerter == nil || violationScore == nil || !violationScore.IsWarningAction() {
		return
	}

	if p.storage != nil {
		allowed, err := p.storage.AcquireAlertPermit(ctx, entry.UserEmail, p.cfg.AlertCooldown)
		if err != nil {
			log.Printf("[Scoring] Alert cooldown acquisition failed for %s: %v", entry.UserEmail, err)
		} else if !allowed {
			return
		}
	}

	score := violationScore.FinalScore
	confidence := violationScore.Confidence
	alertPayload := models.AlertPayload{
		UserIdentifier:  entry.UserEmail,
		ViolationType:   "scoring_action",
		TriggerKind:     trigger.TriggerKind,
		ObserveOnly:     trigger.ObserveOnly,
		DeepCheckUsed:   trigger.DeepCheckUsed,
		AllUserASNs:     append([]string(nil), allASNs...),
		ASNDetails:      asnDetails,
		Score:           &score,
		ScoreAction:     string(violationScore.Action),
		ScoreConfidence: &confidence,
		ScoreBreakdown:  toModelScoreBreakdown(violationScore.Features),
		ScoreModifiers:  append([]string(nil), violationScore.Modifiers...),
		GeoAnalysis:     geoResult,
		ProviderTypes:   providerTypes,
		DeepCheck:       trigger.DeepCheck,
	}
	if trigger.TriggerIPCount > 0 {
		ipCount := trigger.TriggerIPCount
		alertPayload.TriggerIPCount = &ipCount
	}
	if violationScore.IsBlockingAction() {
		alertPayload.BlockDuration = p.cfg.BlockDuration
	}

	p.enqueueSideEffectTask(func(ctx context.Context) {
		if err := p.alerter.SendAlert(ctx, alertPayload); err != nil {
			log.Printf("Webhook notification send error: %v", err)
		}
	})
}

func (p *LogProcessor) applyBlockingEnforcement(ctx context.Context, entry models.LogEntry, reason string, score int, allASNs []string) error {
	if err := p.disableUser(ctx, entry.UserEmail, reason, score); err != nil {
		return err
	}

	blockIPs := p.filterExcludedIPs(p.collectIPsForASNBlock(ctx, entry.UserEmail, allASNs, entry.SourceIP), entry.UserEmail)
	if p.ipMitigator != nil && len(blockIPs) > 0 {
		duration, err := parseBlockDuration(p.cfg.BlockDuration)
		if err != nil {
			log.Printf("Warning: invalid BlockDuration config %q for local mitigation, using default 5m", p.cfg.BlockDuration)
			duration = 5 * time.Minute
		}
		if err := p.ipMitigator.BlockUserIPs(ctx, entry.UserEmail, blockIPs, duration); err != nil {
			log.Printf("Local IP mitigation error for %s: %v", entry.UserEmail, err)
		}
	}

	p.scheduleASNClear(ctx, entry.UserEmail)
	return nil
}

func (p *LogProcessor) applyRemnawaveEvidence(ctx context.Context, userIdentifier, currentSourceIP string, violationScore *scoring.ViolationScore) {
	if p.userEvidence == nil || p.scorer == nil || violationScore == nil {
		return
	}

	internalID, err := strconv.ParseInt(strings.TrimSpace(userIdentifier), 10, 64)
	if err != nil || internalID <= 0 {
		return
	}

	evidence, err := p.userEvidence.GetUserEvidenceByInternalID(ctx, internalID)
	if err != nil {
		log.Printf("[Scoring] Remnawave evidence fetch failed for %s: %v", userIdentifier, err)
		return
	}
	if evidence == nil {
		return
	}

	summary := summarizeUserEvidence(evidence, currentSourceIP)
	if !summary.hasSignals() {
		return
	}

	violationScore.Features = append(violationScore.Features,
		scoring.FeatureResult{
			Name:       "hwid_evidence",
			Score:      evidenceDeviceScore(summary.hwidCount, summary.hwidAgentCount),
			Weight:     0,
			Confidence: 0.9,
			Details:    fmt.Sprintf("devices=%d,agents=%d", summary.hwidCount, summary.hwidAgentCount),
		},
		scoring.FeatureResult{
			Name:       "srh_evidence",
			Score:      evidenceSRHScore(summary.requestIPCount, summary.requestAgentCount),
			Weight:     0,
			Confidence: 0.9,
			Details:    fmt.Sprintf("records=%d,ips=%d,agents=%d,current_ip_match=%t", summary.requestCount, summary.requestIPCount, summary.requestAgentCount, summary.currentIPSeenInSRH),
		},
	)

	originalScore := violationScore.FinalScore
	safeDevices := 3
	graceDevices := 5
	if p.cfg != nil {
		safeDevices = p.cfg.EvidenceSafeDeviceCount
		graceDevices = p.cfg.EvidenceDeviceGraceCount
	}
	switch {
	case summary.hwidCount == 1 && summary.requestCount > 0 && summary.requestAgentCount <= 1 && summary.currentIPSeenInSRH:
		violationScore.FinalScore *= 0.60
		violationScore.Modifiers = append(violationScore.Modifiers, "hwid_srh_single_device_consistency")
	case summary.hwidCount > 0 && summary.requestCount > 0 && summary.hwidCount <= safeDevices && summary.requestAgentCount <= 2 && summary.requestIPCount <= 2:
		violationScore.FinalScore *= 0.80
		violationScore.Modifiers = append(violationScore.Modifiers, "hwid_srh_low_device_diversity")
	case summary.hwidCount > graceDevices:
		violationScore.FinalScore = math.Min(100, violationScore.FinalScore+float64((summary.hwidCount-graceDevices)*8))
		violationScore.Modifiers = append(violationScore.Modifiers, "hwid_device_excess")
	default:
		return
	}

	violationScore.FinalScore = math.Min(100, math.Max(0, violationScore.FinalScore))
	violationScore.Action = p.scorer.DetermineAction(violationScore.FinalScore, violationScore.Confidence)
	log.Printf("[Scoring] Applied Remnawave evidence for %s: score %.1f -> %.1f, action=%s",
		userIdentifier, originalScore, violationScore.FinalScore, violationScore.Action)
}

type userEvidenceSummary struct {
	hwidCount          int
	hwidAgentCount     int
	requestCount       int
	requestIPCount     int
	requestAgentCount  int
	currentIPSeenInSRH bool
}

func (s userEvidenceSummary) hasSignals() bool {
	return s.hwidCount > 0 || s.requestCount > 0
}

func summarizeUserEvidence(evidence *remnawave.UserEvidence, currentSourceIP string) userEvidenceSummary {
	var summary userEvidenceSummary
	hwidSet := make(map[string]struct{})
	hwidAgentSet := make(map[string]struct{})
	requestIPSet := make(map[string]struct{})
	requestAgentSet := make(map[string]struct{})
	currentSourceIP = strings.TrimSpace(currentSourceIP)

	for _, device := range evidence.HwidDevices {
		if hwid := strings.TrimSpace(device.HWID); hwid != "" {
			hwidSet[hwid] = struct{}{}
		}
		if agent := normalizeUserAgent(device.UserAgent); agent != "" {
			hwidAgentSet[agent] = struct{}{}
		}
	}

	for _, record := range evidence.SubscriptionRequests {
		summary.requestCount++
		if ip := strings.TrimSpace(record.RequestIP); ip != "" && !iputil.IsUnspecified(ip) {
			requestIPSet[ip] = struct{}{}
			if currentSourceIP != "" && ip == currentSourceIP {
				summary.currentIPSeenInSRH = true
			}
		}
		if agent := normalizeUserAgent(record.UserAgent); agent != "" {
			requestAgentSet[agent] = struct{}{}
		}
	}

	summary.hwidCount = len(hwidSet)
	summary.hwidAgentCount = len(hwidAgentSet)
	summary.requestIPCount = len(requestIPSet)
	summary.requestAgentCount = len(requestAgentSet)
	return summary
}

func normalizeUserAgent(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func evidenceDeviceScore(deviceCount, agentCount int) float64 {
	if deviceCount <= 5 {
		return 0
	}
	score := float64(maxInt(deviceCount-5, 0)*20 + maxInt(agentCount-2, 0)*10)
	if score > 100 {
		return 100
	}
	return score
}

func evidenceSRHScore(ipCount, agentCount int) float64 {
	score := float64(maxInt(ipCount-1, 0)*20 + maxInt(agentCount-1, 0)*20)
	if score > 100 {
		return 100
	}
	return score
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
