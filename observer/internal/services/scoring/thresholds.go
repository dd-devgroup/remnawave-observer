package scoring

// ViolationAction represents the enforcement action for a given score.
type ViolationAction string

const (
	ActionNone          ViolationAction = "none"           // < 25
	ActionMonitor       ViolationAction = "monitor"        // 25-45
	ActionWarn          ViolationAction = "warn"           // 45-60
	ActionSoftChallenge ViolationAction = "soft_challenge" // 60-75
	ActionTempDisable   ViolationAction = "temp_disable"   // 75-90
	ActionHardDisable   ViolationAction = "hard_disable"   // 90+
)

// ScoreThresholds defines the score boundaries for each action.
type ScoreThresholds struct {
	MonitorThreshold       float64 // default: 25
	WarnThreshold          float64 // default: 45
	SoftChallengeThreshold float64 // default: 60
	TempDisableThreshold   float64 // default: 75
	HardDisableThreshold   float64 // default: 90
}

// DefaultThresholds returns the default score thresholds.
func DefaultThresholds() ScoreThresholds {
	return ScoreThresholds{
		MonitorThreshold:       25,
		WarnThreshold:          45,
		SoftChallengeThreshold: 60,
		TempDisableThreshold:   75,
		HardDisableThreshold:   90,
	}
}

// DetermineAction returns the action for a given score.
func (t ScoreThresholds) DetermineAction(score float64) ViolationAction {
	if score < t.MonitorThreshold {
		return ActionNone
	} else if score < t.WarnThreshold {
		return ActionMonitor
	} else if score < t.SoftChallengeThreshold {
		return ActionWarn
	} else if score < t.TempDisableThreshold {
		return ActionSoftChallenge
	} else if score < t.HardDisableThreshold {
		return ActionTempDisable
	}
	return ActionHardDisable
}

// DowngradeAction returns the next less severe action.
func DowngradeAction(action ViolationAction) ViolationAction {
	switch action {
	case ActionHardDisable:
		return ActionTempDisable
	case ActionTempDisable:
		return ActionWarn
	case ActionSoftChallenge:
		return ActionWarn
	case ActionWarn:
		return ActionMonitor
	case ActionMonitor:
		return ActionNone
	default:
		return ActionNone
	}
}

// GetActionDescription returns a human-readable description of the action.
func GetActionDescription(action ViolationAction) string {
	switch action {
	case ActionNone:
		return "No action - activity within normal range"
	case ActionMonitor:
		return "Monitor - activity under observation"
	case ActionWarn:
		return "Warning - suspicious activity detected"
	case ActionSoftChallenge:
		return "Soft challenge - temporary access restriction"
	case ActionTempDisable:
		return "Temporary disable - account suspended"
	case ActionHardDisable:
		return "Hard disable - account blocked"
	default:
		return "Unknown action"
	}
}

// IsBlockingAction returns true if the action results in user disable.
func IsBlockingAction(action ViolationAction) bool {
	return action == ActionTempDisable || action == ActionHardDisable
}

// IsWarningAction returns true if the action is a warning or higher.
func IsWarningAction(action ViolationAction) bool {
	return action == ActionWarn || action == ActionSoftChallenge ||
		action == ActionTempDisable || action == ActionHardDisable
}
