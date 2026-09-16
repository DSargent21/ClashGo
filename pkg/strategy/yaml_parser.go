package strategy

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Unit struct {
	Name         string `yaml:"name"`
	Amount       string `yaml:"amount"`        // Can be "All" or a number
	Pattern      string `yaml:"pattern"`       // Optional: Override phase pattern (e.g., "Ability")
	FallbackSlot int    `yaml:"fallback_slot"` // Optional: Deterministic slot index (1-based)
	Offset       int    `yaml:"offset"`        // Optional: Per-unit inward offset

	// PhaseOffset is the phase-level Offset copied in by DeployPlanner at
	// plan time (never authored in YAML — hence "-"). Deployers read it
	// as the fallback when the unit's own Offset is unset, so a phase
	// pin like "offset: 130 # Deeper in for EQs" applies to every unit
	// in that phase without each unit having to repeat it.
	PhaseOffset int `yaml:"-"`
}

type Phase struct {
	Name             string `yaml:"name"`
	Units            []Unit `yaml:"units"`
	Pattern          string `yaml:"pattern"`  // "Line", "Point", "FourSides"
	Position         string `yaml:"position"` // "Center", "Left", "Right", "Full"
	Offset           int    `yaml:"offset"`
	DelayAfterMS     int    `yaml:"delay_after_ms"`
	Retry            int    `yaml:"retry"`              // Max retry attempts per unit (default: 3)
	VerifyBeforeNext bool   `yaml:"verify_before_next"` // Wait for slot empty before next phase
}

type DynamicStrategy struct {
	Name                  string  `yaml:"name"`
	Description           string  `yaml:"description"`
	TargetEdge            string  `yaml:"target_edge"`
	Phases                []Phase `yaml:"phases"`
	AutoDeployEventTroops *bool   `yaml:"auto_deploy_eventTroops"` // Auto-deploy event troops not in strategy. nil = enabled (default).
	// EndAtPercent ends the battle automatically once the destruction
	// percentage reaches this value (0 = disabled, the default). Useful
	// for armies that secure the win early (e.g. Valkyrie spam at 50%)
	// and should not wait out the full stall timer.
	EndAtPercent int `yaml:"end_at_percent"`

	// ArmySlot is the saved-army recipe to arm before attacking
	// (1-based, default 1). Each saved recipe holds a different
	// composition, so a strategy must declare which recipe its unit
	// phases expect — e.g. valk_spam.yaml targets the 4th saved recipe.
	// 0 means the same as 1 (first recipe).
	ArmySlot int `yaml:"army_slot"`
}

// SelectedArmySlot returns the 1-based army recipe to arm, clamping
// the default to 1 when the YAML omits the key.
func (s *DynamicStrategy) SelectedArmySlot() int {
	if s.ArmySlot <= 0 {
		return 1
	}
	return s.ArmySlot
}

// EventTroopsAutoDeployEnabled reports whether the strategy wants the
// end-of-attack event-troop dump. Defaults to TRUE when the YAML key is
// absent so every army auto-dumps bonus/seasonal troops on the bar;
// set `auto_deploy_eventTroops: false` to opt out.
func (s *DynamicStrategy) EventTroopsAutoDeployEnabled() bool {
	return s.AutoDeployEventTroops == nil || *s.AutoDeployEventTroops
}

func ParseYAML(path string) (*DynamicStrategy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read yaml: %w", err)
	}

	var s DynamicStrategy
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal yaml: %w", err)
	}

	return &s, nil
}
