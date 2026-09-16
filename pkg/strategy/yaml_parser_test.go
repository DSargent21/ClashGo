package strategy

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "strategy.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp strategy: %v", err)
	}
	return path
}

func TestParseYAMLDefaults(t *testing.T) {
	path := writeTemp(t, `
name: "Test"
target_edge: "Random"
phases:
  - name: "P"
    units: [{ name: "Barbarian", amount: "All" }]
`)
	s, err := ParseYAML(path)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if s.EndAtPercent != 0 {
		t.Errorf("EndAtPercent default = %d, want 0", s.EndAtPercent)
	}
	if got := s.SelectedArmySlot(); got != 1 {
		t.Errorf("SelectedArmySlot() default = %d, want 1", got)
	}
}

func TestParseYAMLKnobs(t *testing.T) {
	path := writeTemp(t, `
name: "Valkyrie Earthquake Spam"
target_edge: "Random"
end_at_percent: 50
army_slot: 4
phases:
  - name: "Valkyrie Spam"
    units: [{ name: "Valkyrie", amount: "All" }]
    pattern: "FourSides"
`)
	s, err := ParseYAML(path)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if s.EndAtPercent != 50 {
		t.Errorf("EndAtPercent = %d, want 50", s.EndAtPercent)
	}
	if got := s.SelectedArmySlot(); got != 4 {
		t.Errorf("SelectedArmySlot() = %d, want 4", got)
	}
}

func TestSelectedArmySlotClamps(t *testing.T) {
	s := &DynamicStrategy{ArmySlot: -3}
	if got := s.SelectedArmySlot(); got != 1 {
		t.Errorf("SelectedArmySlot() negative = %d, want 1", got)
	}
	s = &DynamicStrategy{ArmySlot: 0}
	if got := s.SelectedArmySlot(); got != 1 {
		t.Errorf("SelectedArmySlot() zero = %d, want 1", got)
	}
}
