package bus

import (
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
)

func SystemHealthFrom(h adb.Health) *SystemHealth {
	return &SystemHealth{
		AdbConnected:     h.LastCapture.After(time.Now().Add(-30 * time.Second)),
		LastCaptureMs:    h.LastCapture.UnixMilli(),
		AvgCaptureMs:     h.AvgCaptureMs,
		ConsecutiveFails: int32(h.ConsecutiveFails),
	}
}

func GameStateToBus(s interface{ String() string }) GameState {
	if s == nil {
		return GameState_UNKNOWN
	}
	switch s.String() {
	case "MainVillage":
		return GameState_MAIN_VILLAGE
	case "BuilderBase":
		return GameState_BUILDER_BASE
	case "Battle":
		return GameState_BATTLE
	case "BattleEnd":
		return GameState_BATTLE_END
	case "ArmyCamp":
		return GameState_ARMY_CAMP
	case "SearchMap":
		return GameState_SEARCH_MAP
	case "FindMatch":
		return GameState_FIND_MATCH
	case "Settings":
		return GameState_SETTINGS
	case "ObstacleDialog":
		return GameState_OBSTACLE_DIALOG
	case "GemDialog":
		return GameState_GEM_DIALOG
	case "ChatOpen":
		return GameState_CHAT_OPEN
	case "ShieldInfo":
		return GameState_SHIELD_INFO
	case "ReturnHome":
		return GameState_RETURN_HOME
	case "Loading":
		return GameState_LOADING
	case "WelcomeBack":
		return GameState_WELCOME_BACK
	case "ArmySelection":
		return GameState_ARMY_SELECTION
	default:
		return GameState_UNKNOWN
	}
}
