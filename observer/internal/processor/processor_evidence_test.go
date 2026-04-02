package processor

import (
	"context"
	"testing"
	"time"

	"observer_service/internal/config"
	"observer_service/internal/services/remnawave"
	"observer_service/internal/services/scoring"
)

type staticEvidenceProvider struct {
	evidence *remnawave.UserEvidence
	err      error
}

func (p staticEvidenceProvider) GetUserEvidenceByInternalID(_ context.Context, _ int64) (*remnawave.UserEvidence, error) {
	return p.evidence, p.err
}

func (p staticEvidenceProvider) GetUserIPSnapshotByInternalID(_ context.Context, _ int64, _, _ time.Duration) (*remnawave.UserIPSnapshotResult, error) {
	return nil, nil
}

func TestApplyRemnawaveEvidence_SingleDeviceConsistency_DampensScore(t *testing.T) {
	now := time.Now().UTC()
	proc := &LogProcessor{
		scorer: scoring.NewDefaultScorer(),
		cfg: &config.Config{
			EvidenceDeviceActivityWindow: 30 * 24 * time.Hour,
		},
		userEvidence: staticEvidenceProvider{
			evidence: &remnawave.UserEvidence{
				InternalID: 12345,
				UserUUID:   "user-uuid",
				HwidDevices: []remnawave.HwidDevice{
					{HWID: "device-1", UserAgent: "TestApp/1.0", UpdatedAt: now},
				},
				SubscriptionRequests: []remnawave.SubscriptionRequestRecord{
					{ID: 1, RequestIP: "1.2.3.4", UserAgent: "TestApp/1.0", RequestAt: now},
				},
			},
		},
	}

	score := &scoring.ViolationScore{
		FinalScore: 90,
		Confidence: 0.9,
		Action:     scoring.ActionHardDisable,
	}

	proc.applyRemnawaveEvidence(context.Background(), "12345", "1.2.3.4", score)

	if score.FinalScore >= 90 {
		t.Fatalf("expected dampened score, got %.1f", score.FinalScore)
	}
	if score.Action != scoring.ActionWarn {
		t.Fatalf("expected action downgraded to warn, got %s", score.Action)
	}
	if len(score.Features) != 2 {
		t.Fatalf("expected 2 evidence features, got %d", len(score.Features))
	}
	if len(score.Modifiers) != 1 || score.Modifiers[0] != "hwid_srh_single_device_consistency" {
		t.Fatalf("unexpected modifiers: %v", score.Modifiers)
	}
}

func TestSummarizeUserEvidence_IgnoresUnspecifiedIPs(t *testing.T) {
	now := time.Now().UTC()
	summary := summarizeUserEvidence(&remnawave.UserEvidence{
		HwidDevices: []remnawave.HwidDevice{
			{HWID: "device-1", UserAgent: "AgentA", UpdatedAt: now},
		},
		SubscriptionRequests: []remnawave.SubscriptionRequestRecord{
			{RequestIP: "0.0.0.0", UserAgent: "AgentA", RequestAt: now},
			{RequestIP: "::", UserAgent: "AgentA", RequestAt: now},
			{RequestIP: "5.6.7.8", UserAgent: "AgentB", RequestAt: now},
		},
	}, "5.6.7.8", now, 30*24*time.Hour)

	if summary.requestIPCount != 1 {
		t.Fatalf("expected 1 valid request IP, got %d", summary.requestIPCount)
	}
	if !summary.currentIPSeenInSRH {
		t.Fatal("expected current IP to be matched in SRH")
	}
}

func TestApplyRemnawaveEvidence_StaleDevicesDoNotTriggerExcess(t *testing.T) {
	now := time.Now().UTC()
	stale := now.Add(-90 * 24 * time.Hour)
	proc := &LogProcessor{
		scorer: scoring.NewDefaultScorer(),
		cfg: &config.Config{
			EvidenceSafeDeviceCount:      3,
			EvidenceDeviceGraceCount:     5,
			EvidenceDeviceActivityWindow: 30 * 24 * time.Hour,
		},
		userEvidence: staticEvidenceProvider{
			evidence: &remnawave.UserEvidence{
				InternalID: 2,
				UserUUID:   "user-uuid",
				HwidDevices: []remnawave.HwidDevice{
					{HWID: "d1", UpdatedAt: stale},
					{HWID: "d2", UpdatedAt: stale},
					{HWID: "d3", UpdatedAt: stale},
					{HWID: "d4", UpdatedAt: stale},
					{HWID: "d5", UpdatedAt: stale},
					{HWID: "d6", UpdatedAt: stale},
				},
				SubscriptionRequests: []remnawave.SubscriptionRequestRecord{
					{RequestIP: "1.2.3.4", UserAgent: "Agent", RequestAt: stale},
				},
			},
		},
	}

	score := &scoring.ViolationScore{FinalScore: 14.3, Confidence: 0.3, Action: scoring.ActionNone}
	proc.applyRemnawaveEvidence(context.Background(), "2", "1.2.3.4", score)

	if score.FinalScore != 14.3 {
		t.Fatalf("expected stale evidence to not change score, got %.1f", score.FinalScore)
	}
	if len(score.Modifiers) != 0 {
		t.Fatalf("expected no modifiers for stale evidence, got %v", score.Modifiers)
	}
}

func TestApplyRemnawaveEvidence_ActiveDeviceExcessDoesNotTriggerPenalty(t *testing.T) {
	now := time.Now().UTC()
	proc := &LogProcessor{
		scorer: scoring.NewDefaultScorer(),
		cfg: &config.Config{
			EvidenceSafeDeviceCount:      3,
			EvidenceDeviceGraceCount:     5,
			EvidenceDeviceActivityWindow: 30 * 24 * time.Hour,
		},
		userEvidence: staticEvidenceProvider{
			evidence: &remnawave.UserEvidence{
				InternalID: 2,
				UserUUID:   "user-uuid",
				HwidDevices: []remnawave.HwidDevice{
					{HWID: "d1", UpdatedAt: now},
					{HWID: "d2", UpdatedAt: now},
					{HWID: "d3", UpdatedAt: now},
					{HWID: "d4", UpdatedAt: now},
					{HWID: "d5", UpdatedAt: now},
					{HWID: "d6", UpdatedAt: now},
				},
			},
		},
	}

	score := &scoring.ViolationScore{FinalScore: 14.3, Confidence: 0.3, Action: scoring.ActionNone}
	proc.applyRemnawaveEvidence(context.Background(), "2", "1.2.3.4", score)

	if score.FinalScore != 14.3 {
		t.Fatalf("expected active device excess to stay neutral, got %.1f", score.FinalScore)
	}
	if len(score.Modifiers) != 0 {
		t.Fatalf("expected no modifiers for active device excess alone, got %v", score.Modifiers)
	}
}

func TestApplyRemnawaveEvidence_SRHFallbackDampensWhenNoActiveHWID(t *testing.T) {
	now := time.Now().UTC()
	proc := &LogProcessor{
		scorer: scoring.NewDefaultScorer(),
		cfg: &config.Config{
			EvidenceDeviceActivityWindow: 30 * 24 * time.Hour,
		},
		userEvidence: staticEvidenceProvider{
			evidence: &remnawave.UserEvidence{
				InternalID: 7,
				UserUUID:   "user-uuid",
				SubscriptionRequests: []remnawave.SubscriptionRequestRecord{
					{RequestIP: "1.2.3.4", UserAgent: "AgentA", RequestAt: now},
				},
			},
		},
	}

	score := &scoring.ViolationScore{FinalScore: 40, Confidence: 0.3, Action: scoring.ActionMonitor}
	proc.applyRemnawaveEvidence(context.Background(), "7", "1.2.3.4", score)

	if score.FinalScore >= 40 {
		t.Fatalf("expected SRH fallback to dampen score, got %.1f", score.FinalScore)
	}
	if len(score.Modifiers) != 1 || score.Modifiers[0] != "srh_fallback_consistency" {
		t.Fatalf("expected srh_fallback_consistency modifier, got %v", score.Modifiers)
	}
}
