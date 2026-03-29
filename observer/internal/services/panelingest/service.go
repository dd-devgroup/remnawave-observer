package panelingest

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"observer_service/internal/config"
	"observer_service/internal/iputil"
	"observer_service/internal/metrics"
	"observer_service/internal/models"
	"observer_service/internal/services/remnawave"
)

const (
	defaultNodeRefreshInterval = 5 * time.Minute
	defaultWatermarkTTL        = 10 * time.Minute
)

// EntryEnqueuer accepts normalized log entries for the existing processor.
type EntryEnqueuer interface {
	EnqueueEntries(entries []models.LogEntry) error
}

// UserExcluder decides whether a user must bypass anti-sharing.
type UserExcluder interface {
	IsExcludedByInternalID(ctx context.Context, internalID int64) (bool, error)
}

// ObservationStore stores lastSeen watermarks and node observations.
type ObservationStore interface {
	ShouldEnqueue(ctx context.Context, nodeUUID, userID, ip string, lastSeen time.Time, ttl time.Duration) (bool, error)
}

// Service polls panel jobs and converts them into processor log entries.
type Service struct {
	client       *remnawave.Client
	enqueuer     EntryEnqueuer
	excluder     UserExcluder
	observations ObservationStore
	cfg          *config.Config

	mu    sync.RWMutex
	nodes []remnawave.NodeInfo
}

// NewService creates a panel ingest service.
func NewService(client *remnawave.Client, enqueuer EntryEnqueuer, excluder UserExcluder, observations ObservationStore, cfg *config.Config) *Service {
	if client == nil || enqueuer == nil || cfg == nil {
		return nil
	}
	return &Service{
		client:       client,
		enqueuer:     enqueuer,
		excluder:     excluder,
		observations: observations,
		cfg:          cfg,
	}
}

// Run starts node refresh and polling loops.
func (s *Service) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	if err := s.refreshNodes(ctx); err != nil {
		log.Printf("[PanelIngest] Initial node refresh failed: %v", err)
	}
	s.pollOnce(ctx)

	pollTicker := time.NewTicker(s.cfg.PanelPollInterval)
	defer pollTicker.Stop()
	refreshTicker := time.NewTicker(defaultNodeRefreshInterval)
	defer refreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-refreshTicker.C:
			if err := s.refreshNodes(ctx); err != nil {
				log.Printf("[PanelIngest] Node refresh failed: %v", err)
			}
		case <-pollTicker.C:
			s.pollOnce(ctx)
		}
	}
}

func (s *Service) refreshNodes(ctx context.Context) error {
	nodes, err := s.client.ListNodes(ctx)
	if err != nil {
		return err
	}

	active := make([]remnawave.NodeInfo, 0, len(nodes))
	for _, node := range nodes {
		if node.IsDisabled {
			continue
		}
		active = append(active, node)
	}

	s.mu.Lock()
	s.nodes = active
	s.mu.Unlock()
	log.Printf("[PanelIngest] Refreshed %d active nodes", len(active))
	return nil
}

func (s *Service) snapshotNodes() []remnawave.NodeInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes := make([]remnawave.NodeInfo, len(s.nodes))
	copy(nodes, s.nodes)
	return nodes
}

func (s *Service) pollOnce(ctx context.Context) {
	nodes := s.snapshotNodes()
	if len(nodes) == 0 {
		if err := s.refreshNodes(ctx); err != nil {
			log.Printf("[PanelIngest] Poll skipped: no nodes available: %v", err)
			return
		}
		nodes = s.snapshotNodes()
		if len(nodes) == 0 {
			log.Printf("[PanelIngest] Poll skipped: zero active nodes")
			return
		}
	}

	sem := make(chan struct{}, s.cfg.PanelFetchMaxInflight)
	var wg sync.WaitGroup
	for _, node := range nodes {
		node := node
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				s.pollNode(ctx, node)
			case <-ctx.Done():
			}
		}()
	}
	wg.Wait()
}

func (s *Service) pollNode(ctx context.Context, node remnawave.NodeInfo) {
	fetchCtx, cancel := context.WithTimeout(ctx, s.cfg.PanelFetchTimeout)
	defer cancel()

	jobID, err := s.client.SubmitFetchUsersIPs(fetchCtx, node.UUID)
	if err != nil {
		metrics.PanelFetchSubmitFail.Add(1)
		log.Printf("[PanelIngest] Submit fetch-users-ips failed for node %s (%s): %v", node.Name, node.UUID, err)
		return
	}
	metrics.PanelFetchSubmitOk.Add(1)

	deadline := time.Now().Add(s.cfg.PanelFetchTimeout)
	for {
		if time.Now().After(deadline) {
			metrics.PanelFetchTimeout.Add(1)
			log.Printf("[PanelIngest] Fetch-users-ips timed out for node %s (%s), job=%s", node.Name, node.UUID, jobID)
			return
		}

		result, err := s.client.GetFetchUsersIPsResult(fetchCtx, jobID, node.UUID)
		if err != nil {
			metrics.PanelFetchResultFail.Add(1)
			log.Printf("[PanelIngest] Fetch-users-ips result failed for node %s (%s), job=%s: %v", node.Name, node.UUID, jobID, err)
			return
		}

		switch strings.ToLower(strings.TrimSpace(result.Status)) {
		case "completed", "complete", "done", "success", "finished":
			metrics.PanelFetchResultOk.Add(1)
			if len(result.Observations) == 0 {
				metrics.PanelNodesNoData.Add(1)
			}
			s.enqueueObservations(fetchCtx, node.UUID, result.Observations)
			return
		case "failed", "error", "cancelled":
			metrics.PanelFetchResultFail.Add(1)
			log.Printf("[PanelIngest] Fetch-users-ips job failed for node %s (%s), job=%s: %s", node.Name, node.UUID, jobID, result.Message)
			return
		}

		timer := time.NewTimer(s.cfg.PanelFetchResultPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) enqueueObservations(ctx context.Context, nodeUUID string, observations []remnawave.IPObservation) {
	entries := make([]models.LogEntry, 0, len(observations))
	for _, observation := range observations {
		addr, err := iputil.ParseSourceIP(observation.SourceIP)
		if err != nil {
			continue
		}
		if addr.IsUnspecified() {
			metrics.DiscardedUnspecifiedIP.Add(1)
			continue
		}

		internalID, err := s.resolveInternalID(ctx, observation.UserIdentifier)
		if err != nil {
			log.Printf("[PanelIngest] Resolve internal ID failed for identifier %q: %v", observation.UserIdentifier, err)
			continue
		}

		if s.excluder != nil {
			excluded, err := s.excluder.IsExcludedByInternalID(ctx, internalID)
			if err != nil {
				log.Printf("[PanelIngest] Internal squad exclusion check failed for %d: %v", internalID, err)
			} else if excluded {
				continue
			}
		}

		lastSeen := observation.LastSeen
		if lastSeen.IsZero() {
			lastSeen = time.Now().UTC()
		}

		userID := strconv.FormatInt(internalID, 10)
		if s.observations != nil {
			shouldEnqueue, err := s.observations.ShouldEnqueue(ctx, nodeUUID, userID, observation.SourceIP, lastSeen, defaultWatermarkTTL)
			if err != nil {
				log.Printf("[PanelIngest] Watermark update failed for %s/%s/%s: %v", nodeUUID, userID, observation.SourceIP, err)
			} else if !shouldEnqueue {
				metrics.PanelDedupHit.Add(1)
				continue
			}
		}

		lastSeenCopy := lastSeen
		entries = append(entries, models.LogEntry{
			UserEmail: userID,
			SourceIP:  observation.SourceIP,
			NodeUUID:  nodeUUID,
			LastSeen:  &lastSeenCopy,
		})
	}

	if len(entries) == 0 {
		return
	}
	if err := s.enqueuer.EnqueueEntries(entries); err != nil {
		log.Printf("[PanelIngest] Enqueue %d entries failed: %v", len(entries), err)
	}
}

func (s *Service) resolveInternalID(ctx context.Context, identifier string) (int64, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return 0, strconv.ErrSyntax
	}
	if internalID, err := strconv.ParseInt(identifier, 10, 64); err == nil {
		return internalID, nil
	}
	user, err := s.client.ResolveUser(ctx, identifier)
	if err != nil {
		return 0, err
	}
	return user.InternalID, nil
}
