package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
)

type BotConfig struct {
	Device      DeviceConfig      `json:"device"`
	Training    TrainingConfig    `json:"training"`
	Attack      AttackConfig      `json:"attack"`
	Search      SearchConfig      `json:"search"`
	Storage     StorageConfig     `json:"storage"`
	Schedule    ScheduleConfig    `json:"schedule"`
	Upgrade     UpgradeConfig     `json:"upgrade"`
	Debug       DebugConfig       `json:"debug"`
	Performance PerformanceConfig `json:"performance"`
	Notify      NotifyConfig      `json:"notify"`
	BuilderBase BuilderBaseConfig `json:"builder_base"`
}

// NotifyConfig is where an unattended run reports in. Off by default: the bot
// must never make an outbound request an operator did not ask for.
//
// A webhook is the only way a 24/7 farm on a headless Mac is visible away from
// that Mac — the log file needs someone to open it, and the keepalive wrapper's
// desktop notification only reaches the same machine.
type NotifyConfig struct {
	Enabled bool `json:"enabled"`

	// Format selects the payload shape: "discord" ({"content"}), "telegram"
	// (Bot API sendMessage) or "generic" ({"text","kind","at"}, which Slack
	// and most self-hosted receivers accept).
	Format string `json:"format"`

	// WebhookURL is the endpoint for "discord" and "generic".
	WebhookURL string `json:"webhook_url"`

	// TelegramToken and TelegramChatID address the chat for "telegram". The
	// token is a secret: it lives in config.json, is never logged, and is
	// redacted out of transport errors (see internal/notify).
	TelegramToken  string `json:"telegram_token"`
	TelegramChatID string `json:"telegram_chat_id"`
}

// BuilderBaseConfig is the optional Builder Base battle pass. Off by default: it
// is a different village, a different troop bar and a different screen flow, so a
// half-calibrated version of it would fail in the middle of a run.
type BuilderBaseConfig struct {
	Enabled bool `json:"enabled"`

	// Mode is the farming style. Only "elixir" is implemented: drop one troop,
	// surrender, keep whatever the raid paid out. "gold" (a full attack) needs
	// Builder Base deployment templates that do not exist yet, so it is rejected
	// at the start of the sequence rather than attempted blind.
	Mode string `json:"mode"`

	// EveryNHomeAttacks spaces Builder Base battles between home-village raids.
	// 1 means one after every home attack.
	EveryNHomeAttacks int `json:"every_n_home_attacks"`

	// MaxPerDay caps them; 0 disables the cap. Counted per local day in memory,
	// so a restart starts the day over, exactly like the scheduler's own view.
	MaxPerDay int `json:"max_per_day"`

	// SurrenderAfterSeconds is how long the single troop is left alone before the
	// battle is ended. Builder Base loot is paid on destruction, so this is a
	// cheap raid, not a win attempt.
	SurrenderAfterSeconds int `json:"surrender_after_seconds"`

	// BoatX/Y, TroopSlotX/Y and DeployX/Y are reference-geometry anchors (860x732
	// — the geometry every other authored anchor uses): the boat to the Builder
	// Base in the home village, the first troop card, and the drop point. They are
	// the part of this feature that has to be checked on the live emulator:
	// `see look` renders a frame with a coordinate ruler, and the bot logs the
	// scaled points it taps. The boat default was measured on a live 1024x768
	// frame (bottom-left of the home village, next to the calendar); the troop
	// card and drop defaults are still authored geometry and must be checked.
	BoatX      int `json:"boat_x"`
	BoatY      int `json:"boat_y"`
	TroopSlotX int `json:"troop_slot_x"`
	TroopSlotY int `json:"troop_slot_y"`
	DeployX    int `json:"deploy_x"`
	DeployY    int `json:"deploy_y"`
}

// PerformanceConfig tunes work the bot can simply skip. Both settings are
// estimates from code analysis plus measurements on recorded frames
// (docs/PERFORMANCE.md, "Audit 2"); the defaults are deliberately conservative.
type PerformanceConfig struct {
	// PerfMode is the CLI `-perf` switch: the lean attack path. It sheds
	// OBSERVABILITY work only — one troop-bar frame instead of three before the
	// first drop, no post-attack evidence PNGs — and never changes what reaches
	// the device. Use it for unattended farming where the deploy window and CPU
	// matter more than review artifacts.
	PerfMode bool `json:"perf_mode"`

	// SkipUnchangedClassify skips the classifier on a captured frame that has
	// not changed since the previous one. The classifier is a pure function of
	// the pixels, so an unchanged frame cannot have a different verdict, and a
	// classify costs 200-490 ms on a 720p device while the capture loop runs
	// every 300 ms. Set false to restore classifying every frame.
	SkipUnchangedClassify bool `json:"skip_unchanged_classify"`

	// ClassifyChangeThreshold is the percentage of the frame's comparison
	// thumbnail that must change before the frame is classified again.
	// Measured: consecutive frames of the same live scene differ by ~3 %
	// (animation), every real state transition by 55-95 %. Raise it if the bot
	// is slow to notice something, lower it if the gate never fires
	// (bot stats report classify_reused and last_frame_change_pct).
	ClassifyChangeThreshold float64 `json:"classify_change_threshold"`

	// ClassifyMaxStaleSec bounds how long a cached verdict may be reused, so a
	// slow drift the thumbnail cannot see is re-checked at least this often.
	// Lower = safer and less saving.
	ClassifyMaxStaleSec float64 `json:"classify_max_stale_sec"`

	// CoalesceCaptures serves several consumers that ask for the screen inside
	// one short window from a single device capture. A capture is a guest-side
	// `screencap` process plus 2.5 MB of guest memory traffic, and during a
	// battle the capture loop, the deploy verifier and the battle-end watcher
	// all read the same screen. The cache never crosses one of the bot's own
	// input events (a tap invalidates it), so a post-tap verification always
	// gets a frame taken after the tap. Set false to capture for every caller.
	CoalesceCaptures bool `json:"coalesce_captures"`

	// TuneGuestAnimations zeroes the guest's Android animation scales at boot
	// (`adb shell settings put global window_animation_scale 0`, and the
	// transition/animator equivalents). Animated transitions are per-frame guest
	// GPU and CPU work the bot can never use — it reads the screen at 4 Hz — and
	// zeroing them is the one item in docs/PERFORMANCE.md's emulator lever table
	// the bot can apply itself. The device keeps the setting until the guest is
	// wiped, so it is skipped when a previous run already set it. Set false to
	// leave the device's animation settings alone (a human sharing the instance
	// may prefer the animations).
	TuneGuestAnimations bool `json:"tune_guest_animations"`

	// CaptureCacheMs is the window in which two capture requests share one
	// device capture. It must stay well under the capture loop's own interval
	// (250 ms in battle) so the loop keeps seeing fresh frames; raise it to
	// share more, lower it to share less. Bot stats report captures_coalesced
	// and captures_coalesced_invalidated, so the window can be tuned live.
	CaptureCacheMs float64 `json:"capture_cache_ms"`
}

const (
	// DefaultClassifyChangeThreshold sits above the ~3 % animation floor
	// measured on live frames and far below the 55 % + every transition
	// measured. See docs/PERFORMANCE.md.
	DefaultClassifyChangeThreshold = 6.0

	// DefaultClassifyMaxStaleSec bounds the gate's blind spot to 2.5 s, which is
	// inside the pause the bot already leaves around taps and dialogs.
	DefaultClassifyMaxStaleSec = 2.5

	// DefaultCaptureCacheMs sits below the capture loop's 250 ms battle
	// interval, so the loop's own captures are never served from the cache (it
	// always sees a fresh frame) while a second consumer reading the same screen
	// inside that interval — which is exactly the redundancy this removes — is.
	//
	// The second bound on it is safety, not perf: two observations that are
	// *supposed* to be able to disagree must never be answered by one frame. The
	// shortest such gap in the bot is the battle-result settle machine's retry
	// pause (resultSettleAnimatingPause, 400 ms), and
	// TestCaptureCacheCannotBridgeSettleAttempts fails if this ever exceeds it.
	DefaultCaptureCacheMs = 120.0
)

// CaptureCacheTTL is the effective capture-sharing window: it is zero (disabled)
// when coalescing is off, and the documented default otherwise.
func (p PerformanceConfig) CaptureCacheTTL() time.Duration {
	if !p.CoalesceCaptures {
		return 0
	}
	ms := p.CaptureCacheMs
	if ms <= 0 {
		ms = DefaultCaptureCacheMs
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// ClassifyThreshold is the effective change threshold, with the default applied
// when the config leaves it unset (a zero or negative percentage).
func (p PerformanceConfig) ClassifyThreshold() float64 {
	if p.ClassifyChangeThreshold > 0 {
		return p.ClassifyChangeThreshold
	}
	return DefaultClassifyChangeThreshold
}

// ClassifyMaxStale is the effective staleness bound, defaulted the same way.
func (p PerformanceConfig) ClassifyMaxStale() time.Duration {
	sec := p.ClassifyMaxStaleSec
	if sec <= 0 {
		sec = DefaultClassifyMaxStaleSec
	}
	return time.Duration(sec * float64(time.Second))
}

type DeviceConfig struct {
	ADBHost               string `json:"adb_host"`
	ADBPort               int    `json:"adb_port"`
	DeviceID              string `json:"device_id"`
	PackageName           string `json:"package_name"`
	ZoomOutKey            string `json:"zoom_out_key"` // Key to press for zoom out (e.g., "-")
	ZoomInKey             string `json:"zoom_in_key"`  // Key to press for zoom in (e.g., "+")
	Width                 int    `json:"width"`
	Height                int    `json:"height"`
	DPI                   int    `json:"dpi"`
	RestartOnStartup      bool   `json:"restart_on_startup"`
	DisableChestDismissal bool   `json:"disable_chest_dismissal"`

	// DisplayScale pins the display scale: the factor the game renders HUD
	// chrome and world content at, relative to the 860x732 reference frame.
	// 0 derives it from the screen diagonal, which measured 1.9% off the live
	// value at 1280x720; pin a measured one for anything but the reference
	// geometry. Measure with `cmd/resprobe` (reference frame + target frame),
	// then check the anchors land with `cmd/screendump -k <value> -anchors`.
	// See docs/RESOLUTION.md.
	DisplayScale float64 `json:"display_scale"`
}

type TrainingConfig struct {
	Enabled              bool `json:"enabled"`
	FullArmyBeforeAttack bool `json:"full_army_before_attack"`
	// TrainDeadTroops arms the training loop (internal/bot/training.go): when
	// the attack panel reports an empty army the bot re-queues the configured
	// saved recipe instead of idling until a human trains. Without it a 24/7
	// run stops farming after the army it started with is spent.
	TrainDeadTroops bool     `json:"train_dead_troops"`
	SleepAfterTrain Duration `json:"sleep_after_train"`
}

type AttackConfig struct {
	Enabled        bool   `json:"enabled"`
	StrategyFile   string `json:"strategy_file"`
	AttackWhenFull bool   `json:"attack_when_full"`
	// MaxAttackPerSession caps how many attacks one session runs before the bot
	// shuts itself down cleanly (see Bot.executeAttackSequence's cap check).
	// 0 or negative means UNLIMITED: the session keeps attacking until the user
	// stops it. The GUI has one Start button and one Stop button, so unlimited is
	// what those two imply — the cap is opt-in, and only a positive value ever
	// ends a session without the user asking.
	MaxAttackPerSession int             `json:"max_attack_per_session"`
	DropDelay           Duration        `json:"drop_delay"`
	SpellDelay          Duration        `json:"spell_delay"`
	EndBattleDelay      Duration        `json:"end_battle_delay"`
	UseQueen            bool            `json:"use_queen"`
	UseWarden           bool            `json:"use_warden"`
	UseClanCastle       bool            `json:"use_clan_castle"`
	QueenChargeAtPct    int             `json:"queen_charge_at_pct"`
	WardenUseAtPct      int             `json:"warden_use_at_pct"`
	ReserveDEPercent    int             `json:"reserve_de_percent"`
	StallTimerSeconds   int             `json:"stall_timer_seconds"`
	StrategySlots       map[string]int  `json:"strategy_slots,omitempty"`
	StrategyAutoEnd     map[string]bool `json:"strategy_auto_end,omitempty"`
	// MinSecondsBetweenAttacks is the minimum pause between the end of one
	// battle (Return Home) and the start of the next attack sequence.
	// Armies take real time to retrain; without this gate the bot attacked
	// back-to-back ~8s apart with whatever the camps held (observed live:
	// three near-identical defeats in under four minutes). 0 disables the
	// pause.
	MinSecondsBetweenAttacks int `json:"min_seconds_between_attacks"`
}

// SearchConfig is the base-selection policy. It holds only knobs the search
// loop reads: trophy and Town-Hall filters used to live here and were never
// consumed by anything, so a user (or a CLI flag) could change them and see no
// effect at all. They were removed rather than wired up because the search
// screen gives the bot no trophy or TH reading to filter on.
type SearchConfig struct {
	Enabled              bool `json:"enabled"`
	AttackIfDarkElixirGT int  `json:"attack_if_de_gt"`
	MinLootGold          int  `json:"min_loot_gold"`
	MinLootElixir        int  `json:"min_loot_elixir"`
	MinLootDarkElixir    int  `json:"min_loot_de"`
	MaxSearchSkips       int  `json:"max_search_skips"`
	// AutoLoot lets internal/autotune set the three minimums above from the
	// loot the bot reads on every searched base, trading search time against
	// raid time to maximise loot per hour. The manual values stay the starting
	// point and the fallback until enough bases have been read.
	AutoLoot bool `json:"auto_loot"`

	// UseSuggestedFloors replaces the three minimums above with the curve for
	// the account's Town Hall (internal/village), once that level is known from
	// search.town_hall_level or an imported Data Export. A static 750k is a
	// routine base at TH15 and a base that does not exist at TH6, where every
	// Next is a skip. No-op while the Town Hall is unknown, and autotune still
	// overrides both once it has enough samples.
	UseSuggestedFloors bool `json:"use_suggested_floors"`

	// TownHallLevel is the account's Town Hall when known by hand. 0 falls back
	// to the imported village export (bot_cli -import-village <file>).
	TownHallLevel int `json:"town_hall_level"`
}

type DebugConfig struct {
	CaptureDebug    bool `json:"capture_debug"`
	SaveScreenshots bool `json:"save_screenshots"`
	TemplateDebug   bool `json:"template_debug"`
	StateDebug      bool `json:"state_debug"`
	// UseShellPipe enables a persistent adb "shell:sh" connection during
	// attack cycles, amortizing the per-command `app_process` JVM spin-up
	// cost (100-300ms tax per tap). Default false; safe fallback to legacy
	// per-command transport.Exec when disabled or when the pipe breaks
	// mid-cycle. Stable on BlueStacks / macOS AVD; do not flip on hosts
	// where persistent shells are aggressively terminated.
	UseShellPipe bool `json:"use_shell_pipe"`
	// ShellPipeSyncFlush controls Tap semantics under the persistent pipe:
	//   true  = Tap blocks until the bytes are flushed to the socket (safer;
	//           preserves ordering relative to CaptureToMat).
	//   false = Tap is fire-and-forget (fastest; rely on HumanSleep between
	//           batches to preserve ordering).
	// Only consulted when UseShellPipe is true.
	ShellPipeSyncFlush bool    `json:"shell_pipe_sync_flush"`
	JitterTaps         bool    `json:"jitter_taps"`
	JitterDelays       bool    `json:"jitter_delays"`
	MaxJitterPixels    float64 `json:"max_jitter_pixels"`
	JitterFraction     float64 `json:"jitter_fraction"`
}

type StorageConfig struct {
	HaltWhenGoldFull       bool    `json:"halt_when_gold_full"`
	HaltWhenElixirFull     bool    `json:"halt_when_elixir_full"`
	HaltWhenDEFull         bool    `json:"halt_when_de_full"`
	HaltRule               string  `json:"halt_rule"` // "ANY" or "ALL"
	FullThresholdPercent   float64 `json:"full_threshold_percent"`
	ResumeThresholdPercent float64 `json:"resume_threshold_percent"`
	DumpToWallsBeforeHalt  bool    `json:"dump_to_walls_before_halt"`
	AutoCollect            bool    `json:"auto_collect"`
}

type ScheduleConfig struct {
	Enabled                bool   `json:"enabled"`
	ActiveHoursStart       string `json:"active_hours_start"`
	ActiveHoursEnd         string `json:"active_hours_end"`
	SessionDurationMinutes int    `json:"session_duration_minutes"`
	SessionJitterMinutes   int    `json:"session_jitter_minutes"`
	BreakDurationMinutes   int    `json:"break_duration_minutes"`
	BreakJitterMinutes     int    `json:"break_jitter_minutes"`
	ForceStopOnBreak       bool   `json:"force_stop_on_break"`
	MaxAttacksPerHour      int    `json:"max_attacks_per_hour"`
	MaxAttacksPerDay       int    `json:"max_attacks_per_day"`
}

type UpgradeConfig struct {
	UpgradeWalls         bool     `json:"upgrade_walls"`
	WallResource         string   `json:"wall_resource"` // "gold", "elixir", "both"
	// Reservations are persisted for config/binding compatibility but the
	// wall loop does not enforce them: it is blind-tap (assets + cost colour)
	// and never reads the player's absolute gold/elixir, so there is nothing
	// reliable to compare a reserve against. They are not exposed in the UI
	// until a HUD loot reader exists. ponytail: ceiling = no absolute loot
	// read; upgrade path = enforce in WallUpgradeHooks once one exists.
	WallMinGoldReserve   int `json:"wall_min_gold_reserve"`
	WallMinElixirReserve int `json:"wall_min_elixir_reserve"`
	AutoBuilders         bool     `json:"auto_builders"`
	ReserveBuilders      int      `json:"reserve_builders"`
	UpgradeStrategy      string   `json:"upgrade_strategy"` // "strategic_rush", "max", "heroes_first", "cheapest_first", "most_expensive_first", "balanced"
	UpgradeDefenses      bool     `json:"upgrade_defenses"`
	UpgradeResources     bool     `json:"upgrade_resources"`
	UpgradeArmy          bool     `json:"upgrade_army"`
	UpgradeTraps         bool     `json:"upgrade_traps"`
	UpgradeHeroes        bool     `json:"upgrade_heroes"`
	MaxGoldPerUpgrade    int      `json:"max_gold_per_upgrade"`
	MaxElixirPerUpgrade  int      `json:"max_elixir_per_upgrade"`
	MaxDEPerUpgrade      int      `json:"max_de_per_upgrade"`
	AutoLab              bool     `json:"auto_lab"`
	LabStrategy          string   `json:"lab_strategy"` // "troops_first", "spells_first", "siege_first", "cheapest_first"
	LabAllowDE           bool     `json:"lab_allow_de"`
	AutoPets             bool     `json:"auto_pets"`
	PetPriorities        []string `json:"pet_priorities"`
	// HeroPriorities orders hero rows inside strategic_rush. Empty means the
	// researched default below; a custom queue is never reordered.
	HeroPriorities []string `json:"hero_priorities"`
	// AllowTownHall lets the builder automation start a Town Hall upgrade. It
	// is off by default and deliberately explicit: a Town Hall upgrade is the
	// one irreversible decision in the upgrade screen (it changes the match
	// pool and hides loot for days), so it is never inferred from a generic
	// "upgrade everything" toggle.
	AllowTownHall bool `json:"allow_town_hall_upgrade"`
	DryRun        bool `json:"dry_run"`
}

// DefaultPetPriorities is the upgrade queue for the current pet roster. It
// front-loads pets that work in most armies: Phoenix for the extra life,
// Spirit Fox for invisibility, Sneezy and Angry Jelly for air/defense control,
// then the slower utility pets. Greedy Raven sits ahead of Unicorn because the
// bot farms: its resource-building damage pays back before queen-walk healing.
func DefaultPetPriorities() []string {
	return []string{
		"Phoenix", "Spirit Fox", "Sneezy", "Angry Jelly", "Frosty", "Diggy",
		"Greedy Raven", "Unicorn", "Electro Owl", "L.A.S.S.I", "Mighty Yak", "Poison Lizard",
	}
}

// DefaultHeroPriorities is the hero order strategic_rush ranks against, from
// docs/STRATEGIC_RUSH.md: the hero closest to 8-day upgrades first. Queen and
// Warden carry the widest set of armies, Champion is the next finisher, Prince
// comes before King for the air plan, and Duke last.
func DefaultHeroPriorities() []string {
	return []string{"Archer Queen", "Grand Warden", "Royal Champion", "Minion Prince", "Barbarian King", "Dragon Duke"}
}

// IsLegacyPetOrder reports whether a queue is the old five-pet default. The
// setup wizard uses it to migrate that default without overwriting a custom
// order.
func IsLegacyPetOrder(priorities []string) bool {
	legacy := []string{"Diggy", "Unicorn", "Frosty", "Phoenix", "Spirit Fox"}
	if len(priorities) != len(legacy) {
		return false
	}
	for i := range legacy {
		if priorities[i] != legacy[i] {
			return false
		}
	}
	return true
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	// Two encodings are in the wild and both must load: the Wails GUI
	// marshals the embedded time.Duration as {"Duration": <nanoseconds>},
	// while hand-written configs use "5s". Accepting only the string made
	// Load fail on every GUI-written file, and LoadOrDefault then silently
	// fell back to DefaultConfig — which dropped strategy_slots, so
	// resolveArmySlot fell through to the strategy YAML's stale army_slot and
	// armed the wrong recipe (observed live: valk_spam resolved slot 4 while
	// config.json said 2, because the file did not parse at all).
	var obj struct {
		Duration *int64 `json:"Duration"`
	}
	if err := json.Unmarshal(b, &obj); err == nil && obj.Duration != nil {
		d.Duration = time.Duration(*obj.Duration)
		return nil
	}

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
			ADBHost:               "127.0.0.1",
			ADBPort:               5037,
			DeviceID:              "localhost:5555",
			PackageName:           "com.supercell.clashofclans",
			ZoomOutKey:            "i",
			ZoomInKey:             "o",
			Width:                 1024,
			Height:                768,
			DPI:                   320,
			RestartOnStartup:      true,
			DisableChestDismissal: false, // chest dismissal loop IS on by default
			DisplayScale:          1.1334,
		},
		Training: TrainingConfig{
			Enabled:              true,
			FullArmyBeforeAttack: true,
			TrainDeadTroops:      true,
			SleepAfterTrain:      Duration{5 * time.Second},
		},
		Attack: AttackConfig{
			Enabled:                  true,
			StrategyFile:             paths.Resolve("strategies/auto_edrag_rush.yaml"),
			MaxAttackPerSession:      0, // 0 = unlimited: run until the user stops the session
			DropDelay:                Duration{500 * time.Millisecond},
			SpellDelay:               Duration{2 * time.Second},
			EndBattleDelay:           Duration{30 * time.Second},
			QueenChargeAtPct:         50,
			WardenUseAtPct:           30,
			ReserveDEPercent:         200,
			StallTimerSeconds:        30,
			MinSecondsBetweenAttacks: 30,
		},
		Search: SearchConfig{
			Enabled:              true,
			AttackIfDarkElixirGT: 0,
			MinLootGold:          750000,
			MinLootElixir:        750000,
			MinLootDarkElixir:    2000,
			MaxSearchSkips:       60,
			// Safe as a default: it only acts once the Town Hall is known, so an
			// install that never imports an export keeps exactly these manual
			// values.
			UseSuggestedFloors: true,
		},
		Storage: StorageConfig{
			HaltWhenGoldFull:       true,
			HaltWhenElixirFull:     true,
			HaltWhenDEFull:         false,
			HaltRule:               "ANY",
			FullThresholdPercent:   95.0,
			ResumeThresholdPercent: 80.0,
			DumpToWallsBeforeHalt:  true,
			AutoCollect:            true,
		},
		// Safe pacing is the default. A brand-new install used to farm 24 h a
		// day with no breaks and no rate caps, which is both the loudest
		// behavioural signature the account can have and the least human
		// pattern in the app. These defaults are what the roadmap calls the
		// "24/7 preset": an 18 h waking window, 55-95 min sessions, 13-27 min
		// breaks that stop the game, and caps a very active player could hit.
		// Every number stays editable in Config → Schedule.
		Schedule: ScheduleConfig{
			Enabled:                true,
			ActiveHoursStart:       "07:00",
			ActiveHoursEnd:         "01:00",
			SessionDurationMinutes: 75,
			SessionJitterMinutes:   20,
			BreakDurationMinutes:   20,
			BreakJitterMinutes:     7,
			ForceStopOnBreak:       true,
			MaxAttacksPerHour:      8,
			MaxAttacksPerDay:       50,
		},
		Upgrade: UpgradeConfig{
			UpgradeWalls:         false,
			WallResource:         "both",
			WallMinGoldReserve:   2000000,
			WallMinElixirReserve: 2000000,
			AutoBuilders:         false,
			ReserveBuilders:      1,
			UpgradeStrategy:      "heroes_first",
			UpgradeDefenses:      true,
			UpgradeResources:     true,
			UpgradeArmy:          true,
			UpgradeTraps:         true,
			UpgradeHeroes:        true,
			MaxGoldPerUpgrade:    0,
			MaxElixirPerUpgrade:  0,
			MaxDEPerUpgrade:      0,
			AutoLab:              false,
			LabStrategy:          "cheapest_first",
			LabAllowDE:           true,
			AutoPets:             false,
			PetPriorities:        DefaultPetPriorities(),
			HeroPriorities:       DefaultHeroPriorities(),
			AllowTownHall:        false,
		},
		Debug: DebugConfig{
			CaptureDebug:       false,
			SaveScreenshots:    true,
			TemplateDebug:      false,
			StateDebug:         false,
			UseShellPipe:       true,
			ShellPipeSyncFlush: true,
			JitterTaps:         true,
			JitterDelays:       true,
			MaxJitterPixels:    2.0,
			JitterFraction:     0.15,
		},
		Performance: PerformanceConfig{
			SkipUnchangedClassify:   true,
			ClassifyChangeThreshold: DefaultClassifyChangeThreshold,
			ClassifyMaxStaleSec:     DefaultClassifyMaxStaleSec,
			TuneGuestAnimations:     true,
			CoalesceCaptures:        true,
			CaptureCacheMs:          DefaultCaptureCacheMs,
		},
		Notify: NotifyConfig{
			Enabled: false,
			Format:  "discord",
		},
		BuilderBase: BuilderBaseConfig{
			Enabled:               false,
			Mode:                  "elixir",
			EveryNHomeAttacks:     3,
			MaxPerDay:             20,
			SurrenderAfterSeconds: 20,
			BoatX:                 185,
			BoatY:                 550,
			TroopSlotX:            45,
			TroopSlotY:            660,
			DeployX:               430,
			DeployY:               560,
		},
	}
}

func Load(path string) (*BotConfig, error) {
	if path == "config.json" {
		path = paths.ResolveConfig("config.json")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := *DefaultConfig()

	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return &cfg, nil
}

// PinnedDisplayScale returns the display scale the bot will actually run with,
// plus a phrase describing where the number came from (for tool output).
//
// A harness that maps reference coordinates onto a live frame must use this,
// not a ratio it derives itself from the screen size. At 1280x720 the derived
// value is 1.3004 while this device pins 1.325, and that 2% moves a mapped
// probe by up to 30 px — enough to change classifier verdicts on real frames
// (see internal/game/testdata/corpus.json and docs/SEEING.md). A tool that
// reports in the wrong geometry invents bugs that do not exist.
//
// A zero return means the config pins nothing, so the caller should derive the
// scale from the screen.
func PinnedDisplayScale() (float64, string) {
	cfg := LoadOrDefault("config.json")
	if cfg.Device.DisplayScale > 0 {
		return cfg.Device.DisplayScale, "pinned by device.display_scale"
	}
	return 0, "no device.display_scale pinned"
}

// ResolveDisplayScale picks the display scale a harness should use: an
// explicit override, else the value the bot pins for this device, else 0
// (meaning "derive it from the screen"). The returned phrase describes the
// choice for tool output.
//
// departs reports whether the result is a geometry the bot does not run. That
// is not a detail a caller should have to parse out of the message: a report in
// a different geometry than the bot's is worse than no report, so every caller
// is expected to say so, and a returned bool cannot drift from the message the
// way a substring match on it can.
//
// A derived scale is NOT a departure when nothing is pinned, because the bot
// derives the same value from the same screen — that is the one case where the
// tools and the bot are guaranteed to agree.
func ResolveDisplayScale(override float64) (scale float64, source string, departs bool) {
	pinned, pinSource := PinnedDisplayScale()
	switch {
	case override > 0 && pinned > 0 && override != pinned:
		return override, fmt.Sprintf("-k override; the bot pins %.4f, so this does NOT describe live behaviour", pinned), true
	case override > 0:
		// Nothing is pinned, so the bot derives its own scale and this override
		// may well differ from it — we cannot tell without the frame size, and
		// silence would imply a confidence we do not have.
		return override, "-k override; " + pinSource + ", so the bot derives its own scale and this may differ from it", true
	case pinned > 0:
		return pinned, pinSource + ", as the bot runs it", false
	default:
		return 0, "derived from the screen size (" + pinSource + "), as the bot does", false
	}
}

func LoadOrDefault(path string) *BotConfig {
	cfg, err := Load(path)
	if err != nil {
		return DefaultConfig()
	}
	return cfg
}
