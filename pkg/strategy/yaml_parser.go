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
}

type Phase struct {
	Name              string `yaml:"name"`
	Units             []Unit `yaml:"units"`
	Pattern           string `yaml:"pattern"`            // "Line", "Point", "FourSides"
	Position          string `yaml:"position"`           // "Center", "Left", "Right", "Full"
	Offset            int    `yaml:"offset"`
	DelayAfterMS      int    `yaml:"delay_after_ms"`
	Retry             int    `yaml:"retry"`              // Max retry attempts per unit (default: 3)
	VerifyBeforeNext  bool   `yaml:"verify_before_next"` // Wait for slot empty before next phase
}

type DynamicStrategy struct {
	Name                  string  `yaml:"name"`
	Description           string  `yaml:"description"`
	TargetEdge            string  `yaml:"target_edge"`
	Phases                []Phase `yaml:"phases"`
	AutoDeployEventTroops bool    `yaml:"auto_deploy_eventTroops"` // Auto-deploy event troops not in strategy
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
