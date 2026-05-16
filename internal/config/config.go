package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type BotConfig struct {
	Device    DeviceConfig    `json:"device"`
	Training  TrainingConfig  `json:"training"`
	Attack    AttackConfig    `json:"attack"`
	Search    SearchConfig    `json:"search"`
	Debug     DebugConfig     `json:"debug"`
}

type DeviceConfig struct {
	ADBHost     string `json:"adb_host"`
	ADBPort     int    `json:"adb_port"`
	DeviceID    string `json:"device_id"`
	PackageName string `json:"package_name"`
	ZoomOutKey  string `json:"zoom_out_key"` // Key to press for zoom out (e.g., "-")
	ZoomInKey   string `json:"zoom_in_key"`  // Key to press for zoom in (e.g., "+")
}

type TrainingConfig struct {
	Enabled           bool     `json:"enabled"`
	FullArmyBeforeAttack bool  `json:"full_army_before_attack"`
	TrainDeadTroops   bool     `json:"train_dead_troops"`
	MinBarracksLevel  int      `json:"min_barracks_level"`
	SleepAfterTrain   Duration `json:"sleep_after_train"`
}

type AttackConfig struct {
	Enabled              bool          `json:"enabled"`
	StrategyFile         string        `json:"strategy_file"`
	AttackWhenFull       bool          `json:"attack_when_full"`
	MaxAttackPerSession  int           `json:"max_attack_per_session"`
	DropDelay            Duration      `json:"drop_delay"`
	SpellDelay           Duration      `json:"spell_delay"`
	EndBattleDelay       Duration      `json:"end_battle_delay"`
	UseQueen             bool          `json:"use_queen"`
	UseWarden            bool          `json:"use_warden"`
	UseClanCastle        bool          `json:"use_clan_castle"`
	QueenChargeAtPct      int           `json:"queen_charge_at_pct"`
	WardenUseAtPct        int           `json:"warden_use_at_pct"`
	ReserveDEPercent      int           `json:"reserve_de_percent"`
}

type SearchConfig struct {
	Enabled             bool   `json:"enabled"`
	MinTrophies         int    `json:"min_trophies"`
	MaxTrophies         int    `json:"max_trophies"`
	MinTownHall         int    `json:"min_town_hall"`
	MaxTownHall         int    `json:"max_town_hall"`
	SkipBigBase         bool   `json:"skip_big_base"`
	SkipMaxTH           bool   `json:"skip_max_th"`
	AttackIfDarkElixirGT int   `json:"attack_if_de_gt"`
	AttackIfTrophiesGT  int    `json:"attack_if_trophies_gt"`
	MinLootGold         int    `json:"min_loot_gold"`
	MinLootElixir       int    `json:"min_loot_elixir"`
	MinLootDarkElixir   int    `json:"min_loot_de"`
}

type DebugConfig struct {
	CaptureDebug   bool `json:"capture_debug"`
	SaveScreenshots bool `json:"save_screenshots"`
	TemplateDebug   bool `json:"template_debug"`
	StateDebug      bool `json:"state_debug"`
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	s := string(b)
	s = s[1 : len(s)-1]

	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}
func DefaultConfig() *BotConfig {
	return &BotConfig{
		Device: DeviceConfig{
			ADBHost:     "127.0.0.1",
			ADBPort:     5037,
			DeviceID:    "localhost:5555",
			PackageName: "com.supercell.clashofclans",
			ZoomOutKey:  "i",
			ZoomInKey:   "o",
		},
		Training: TrainingConfig{
			Enabled:            true,
			FullArmyBeforeAttack: true,
			SleepAfterTrain:    Duration{5 * time.Second},
		},
		Attack: AttackConfig{
			Enabled:            true,
			StrategyFile:       "assets/strategies/auto_edrag_rush.yaml",
			MaxAttackPerSession: 100,
			DropDelay:          Duration{500 * time.Millisecond},
			SpellDelay:         Duration{2 * time.Second},
			EndBattleDelay:     Duration{30 * time.Second},
			QueenChargeAtPct:   50,
			WardenUseAtPct:     30,
			ReserveDEPercent:   200,
		},
		Search: SearchConfig{
			Enabled:       true,
			MinTrophies:   0,
			MaxTrophies:   3000,
			MinTownHall:   7,
			MaxTownHall:   13,
			SkipMaxTH:     false,
			AttackIfDarkElixirGT: 0,
			MinLootGold:   750000,
			MinLootElixir: 750000,
			MinLootDarkElixir: 2000,
		},
		Debug: DebugConfig{
			CaptureDebug:   false,
			SaveScreenshots: true,
			TemplateDebug:   false,
			StateDebug:      false,
		},
	}
}

func Load(path string) (*BotConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg BotConfig
	cfg = *DefaultConfig()

	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return &cfg, nil
}

func LoadOrDefault(path string) *BotConfig {
	cfg, err := Load(path)
	if err != nil {
		return DefaultConfig()
	}
	return cfg
}