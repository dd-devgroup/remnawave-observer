package userpolicy

import (
	"context"
	"strings"

	"observer_service/internal/metrics"
	"observer_service/internal/services/remnawave"
)

// Excluder checks whether a user should be bypassed based on internal squad membership.
type Excluder struct {
	client         *remnawave.Client
	excludedSquads map[string]bool
}

// NewExcluder creates a squad-based exclusion service.
func NewExcluder(client *remnawave.Client, excludedSquads map[string]bool) *Excluder {
	if client == nil || len(excludedSquads) == 0 {
		return nil
	}
	normalized := make(map[string]bool, len(excludedSquads))
	for squadUUID := range excludedSquads {
		squadUUID = strings.ToLower(strings.TrimSpace(squadUUID))
		if squadUUID != "" {
			normalized[squadUUID] = true
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return &Excluder{client: client, excludedSquads: normalized}
}

// IsExcludedByInternalID returns true when the user belongs to any excluded internal squad.
func (e *Excluder) IsExcludedByInternalID(ctx context.Context, internalID int64) (bool, error) {
	if e == nil || internalID <= 0 {
		return false, nil
	}
	user, err := e.client.GetUserByInternalID(ctx, internalID)
	if err != nil {
		return false, err
	}
	for _, squad := range user.ActiveInternalSquads {
		if e.excludedSquads[strings.ToLower(strings.TrimSpace(squad.UUID))] {
			metrics.ExcludedSquadUserTotal.Add(1)
			return true, nil
		}
	}
	return false, nil
}

// ValidateConfiguredSquads returns configured UUIDs missing from panel internal squads.
func (e *Excluder) ValidateConfiguredSquads(ctx context.Context) ([]string, error) {
	if e == nil {
		return nil, nil
	}
	squads, err := e.client.ListInternalSquads(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(squads))
	for _, squad := range squads {
		known[strings.ToLower(strings.TrimSpace(squad.UUID))] = true
	}
	var missing []string
	for configured := range e.excludedSquads {
		if !known[configured] {
			missing = append(missing, configured)
		}
	}
	return missing, nil
}
