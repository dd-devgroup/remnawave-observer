package scoring

// ViolationAction тип действия при превышении порога
type ViolationAction string

const (
	ActionNone      ViolationAction = "none"       // < 30
	ActionMonitor   ViolationAction = "monitor"    // 30-50
	ActionWarn      ViolationAction = "warn"       // 50-70
	ActionSoftBlock ViolationAction = "soft_block" // 70-85
	ActionBlock     ViolationAction = "block"      // > 85
)

// ScoreThresholds пороги для действий
type ScoreThresholds struct {
	MonitorThreshold   float64 // default: 30
	WarnThreshold      float64 // default: 50
	SoftBlockThreshold float64 // default: 70
	BlockThreshold     float64 // default: 85
}

// DefaultThresholds возвращает дефолтные пороги
func DefaultThresholds() ScoreThresholds {
	return ScoreThresholds{
		MonitorThreshold:   30,
		WarnThreshold:      50,
		SoftBlockThreshold: 70,
		BlockThreshold:     85,
	}
}

// DetermineAction определяет действие на основе скора
func (t ScoreThresholds) DetermineAction(score float64) ViolationAction {
	if score < t.MonitorThreshold {
		return ActionNone
	} else if score < t.WarnThreshold {
		return ActionMonitor
	} else if score < t.SoftBlockThreshold {
		return ActionWarn
	} else if score < t.BlockThreshold {
		return ActionSoftBlock
	}
	return ActionBlock
}

// GetActionDescription возвращает описание действия
func GetActionDescription(action ViolationAction) string {
	switch action {
	case ActionNone:
		return "Нет действий - активность в пределах нормы"
	case ActionMonitor:
		return "Мониторинг - активность под наблюдением"
	case ActionWarn:
		return "Предупреждение - подозрительная активность"
	case ActionSoftBlock:
		return "Мягкая блокировка - временное ограничение доступа"
	case ActionBlock:
		return "Блокировка - доступ запрещен"
	default:
		return "Неизвестное действие"
	}
}
