package processor

import (
	"context"
	"testing"
	"time"

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
	proc := &LogProcessor{
		scorer: scoring.NewDefaultScorer(),
		userEvidence: staticEvidenceProvider{
			evidence: &remnawave.UserEvidence{
				InternalID: 12345,
				UserUUID:   "user-uuid",
				HwidDevices: []remnawave.HwidDevice{
					{HWID: "device-1", UserAgent: "TestApp/1.0"},
				},
				SubscriptionRequests: []remnawave.SubscriptionRequestRecord{
					{ID: 1, RequestIP: "1.2.3.4", UserAgent: "TestApp/1.0", RequestAt: time.Now().UTC()},
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
	summary := summarizeUserEvidence(&remnawave.UserEvidence{
		HwidDevices: []remnawave.HwidDevice{
			{HWID: "device-1", UserAgent: "AgentA"},
		},
		SubscriptionRequests: []remnawave.SubscriptionRequestRecord{
			{RequestIP: "0.0.0.0", UserAgent: "AgentA"},
			{RequestIP: "::", UserAgent: "AgentA"},
			{RequestIP: "5.6.7.8", UserAgent: "AgentB"},
		},
	}, "5.6.7.8")

	if summary.requestIPCount != 1 {
		t.Fatalf("expected 1 valid request IP, got %d", summary.requestIPCount)
	}
	if !summary.currentIPSeenInSRH {
		t.Fatal("expected current IP to be matched in SRH")
	}
}
