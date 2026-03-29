package observations

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store keeps panel ingest watermarks and user/IP -> node observations.
type Store struct {
	client *redis.Client
}

// NewStore creates a new observation store.
func NewStore(client *redis.Client) *Store {
	return &Store{client: client}
}

// ShouldEnqueue stores the latest seen timestamp for a node/user/ip tuple and
// returns true only when the current observation is newer than the stored one.
func (s *Store) ShouldEnqueue(ctx context.Context, nodeUUID, userID, ip string, lastSeen time.Time, ttl time.Duration) (bool, error) {
	if s == nil || s.client == nil || nodeUUID == "" || userID == "" || ip == "" {
		return true, nil
	}
	if lastSeen.IsZero() {
		lastSeen = time.Now().UTC()
	}

	key := fmt.Sprintf("rw:obs:lastseen:%s:%s:%s", nodeUUID, userID, ip)
	stored, err := s.client.Get(ctx, key).Result()
	if err == nil && stored != "" {
		unix, parseErr := strconv.ParseInt(stored, 10, 64)
		if parseErr == nil && unix >= lastSeen.Unix() {
			return false, nil
		}
	} else if err != nil && err != redis.Nil {
		return false, err
	}

	if err := s.client.Set(ctx, key, strconv.FormatInt(lastSeen.Unix(), 10), ttl).Err(); err != nil {
		return false, err
	}

	return true, nil
}

// RecordNodeObservation remembers that a user/IP pair was observed on a node.
func (s *Store) RecordNodeObservation(ctx context.Context, userID, ip, nodeUUID string, ttl time.Duration) error {
	if s == nil || s.client == nil || userID == "" || ip == "" || nodeUUID == "" {
		return nil
	}

	key := fmt.Sprintf("rw:obs:nodes:%s:%s", userID, ip)
	pipe := s.client.Pipeline()
	pipe.SAdd(ctx, key, nodeUUID)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// GetNodesByUserIP returns nodes on which the user/IP pair was recently seen.
func (s *Store) GetNodesByUserIP(ctx context.Context, userID, ip string) ([]string, error) {
	if s == nil || s.client == nil || userID == "" || ip == "" {
		return nil, nil
	}
	key := fmt.Sprintf("rw:obs:nodes:%s:%s", userID, ip)
	nodes, err := s.client.SMembers(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	filtered := nodes[:0]
	for _, node := range nodes {
		node = strings.TrimSpace(node)
		if node != "" {
			filtered = append(filtered, node)
		}
	}
	return filtered, nil
}
