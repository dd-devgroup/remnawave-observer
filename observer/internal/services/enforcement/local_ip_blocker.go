package enforcement

import (
	"context"
	"strings"
	"time"

	"observer_service/internal/metrics"
	"observer_service/internal/services/remnawave"
)

// ObservationLookup resolves nodes where a user/IP pair was observed.
type ObservationLookup interface {
	GetNodesByUserIP(ctx context.Context, userID, ip string) ([]string, error)
}

// LocalIPBlocker executes temporary IP blocks on nodes where offending IPs were seen.
type LocalIPBlocker struct {
	client       *remnawave.Client
	observations ObservationLookup
}

// NewLocalIPBlocker creates a local IP blocker.
func NewLocalIPBlocker(client *remnawave.Client, observations ObservationLookup) *LocalIPBlocker {
	if client == nil || observations == nil {
		return nil
	}
	return &LocalIPBlocker{client: client, observations: observations}
}

// BlockUserIPs blocks IPs on nodes where they were observed.
func (b *LocalIPBlocker) BlockUserIPs(ctx context.Context, userID string, ips []string, duration time.Duration) error {
	if b == nil {
		return nil
	}

	nodeSet := make(map[string]struct{})
	ipSet := make(map[string]struct{})
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		nodes, err := b.observations.GetNodesByUserIP(ctx, userID, ip)
		if err != nil {
			metrics.ExecutorBlockFail.Add(1)
			return err
		}
		if len(nodes) == 0 {
			metrics.PanelNodesNoData.Add(1)
			continue
		}
		ipSet[ip] = struct{}{}
		for _, nodeUUID := range nodes {
			nodeUUID = strings.TrimSpace(nodeUUID)
			if nodeUUID != "" {
				nodeSet[nodeUUID] = struct{}{}
			}
		}
	}

	if len(nodeSet) == 0 || len(ipSet) == 0 {
		return nil
	}

	nodeUUIDs := make([]string, 0, len(nodeSet))
	for nodeUUID := range nodeSet {
		nodeUUIDs = append(nodeUUIDs, nodeUUID)
	}
	ipList := make([]string, 0, len(ipSet))
	for ip := range ipSet {
		ipList = append(ipList, ip)
	}

	if err := b.client.ExecuteTemporaryIPBlock(ctx, nodeUUIDs, ipList, duration); err != nil {
		metrics.ExecutorBlockFail.Add(1)
		return err
	}

	metrics.ExecutorBlockOk.Add(1)
	return nil
}
