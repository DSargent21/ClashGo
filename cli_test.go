//go:build cli
// +build cli

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/bot"
)

func TestFindConfigArg(t *testing.T) {
	tests := []struct {
		args     []string
		expected string
	}{
		{[]string{"-gold", "500000"}, ""},
		{[]string{"-config", "custom.json"}, "custom.json"},
		{[]string{"--config", "custom.json"}, "custom.json"},
		{[]string{"-c", "custom.json"}, "custom.json"},
		{[]string{"-config=custom.json"}, "custom.json"},
		{[]string{"--config=custom.json"}, "custom.json"},
		{[]string{"-c=custom.json"}, "custom.json"},
		{[]string{"-perf", "-c", "mycfg.json", "-gold", "100"}, "mycfg.json"},
	}

	for _, tc := range tests {
		got := findConfigArg(tc.args)
		if got != tc.expected {
			t.Errorf("findConfigArg(%v) = %q; want %q", tc.args, got, tc.expected)
		}
	}
}

func TestLoadConfigCustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "custom_config.json")
	content := `{
		"device": {
			"device_id": "test_device:7777"
		},
		"search": {
			"min_loot_gold": 123456
		}
	}`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg := loadConfig(cfgPath)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Device.DeviceID != "test_device:7777" {
		t.Errorf("expected DeviceID 'test_device:7777', got %q", cfg.Device.DeviceID)
	}
	if cfg.Search.MinLootGold != 123456 {
		t.Errorf("expected min_loot_gold 123456, got %d", cfg.Search.MinLootGold)
	}
}

func TestFormatNumber(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{1000000000, "1,000,000,000"},
	}
	for _, c := range cases {
		got := formatNumber(c.in)
		if got != c.want {
			t.Errorf("formatNumber(%d) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestPrintSessionSummary(t *testing.T) {
	// Verify that printSessionSummary runs without panic on zero and populated stats
	s := bot.BotStats{
		AttacksCompleted: 2,
		SearchSkips:      5,
		TotalGold:        500000,
		TotalElixir:      400000,
		TotalDE:          2500,
		Stars0:           0,
		Stars1:           1,
		Stars2:           1,
		Stars3:           0,
		Uptime:           5 * time.Minute,
		ClassifyRan:      10,
		ClassifyReused:   5,
	}
	printSessionSummary(s)
}
