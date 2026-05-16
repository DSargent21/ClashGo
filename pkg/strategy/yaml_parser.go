package strategy

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Unit struct {
	Name    string `yaml:"name"`
	Amount  string `yaml:"amount"`  // Can be "All" or a number
	Pattern string `yaml:"pattern"` // Optional: Override phase pattern (e.g., "Ability")
}

type Phase struct {
	Name         string `yaml:"name"`
	Units        []Unit `yaml:"units"`
	Pattern      string `yaml:"pattern"` // "Line", "Point", "Surround"
	Position     string `yaml:"position"` // "Center", "Left", "Right", "Full"
	Offset       int    `yaml:"offset"`
	DelayAfterMS int    `yaml:"delay_after_ms"`
}

type DynamicStrategy struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description"`
	TargetEdge  string  `yaml:"target_edge"`
	Phases      []Phase `yaml:"phases"`
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
