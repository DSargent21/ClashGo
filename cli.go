//go:build cli
// +build cli

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/logger"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog/log"
)

var deployOnly bool

func main() {
	configPath := findConfigArg(os.Args[1:])
	cfg := loadConfig(configPath)

	fs := flag.NewFlagSet("bot_cli", flag.ContinueOnError)
	fs.Usage = printUsage(fs)

	// Mode flags
	var (
		showVersion   bool
		listDevices   bool
		listStrats    bool
		quiet         bool
		debugLog      bool
		noScreenshots bool
	)
	fs.BoolVar(&showVersion, "version", false, "Print version and exit")
	fs.BoolVar(&showVersion, "v", false, "Print version and exit (shorthand)")
	fs.BoolVar(&listDevices, "devices", false, "List connected ADB devices and exit")
	fs.BoolVar(&listStrats, "strategies", false, "List available attack strategies and exit")
	fs.BoolVar(&quiet, "quiet", false, "Suppress periodic 10s status ticker")
	fs.BoolVar(&quiet, "q", false, "Suppress periodic 10s status ticker (shorthand)")
	fs.BoolVar(&debugLog, "debug", os.Getenv("DEBUG") != "", "Enable verbose debug logging")
	fs.BoolVar(&noScreenshots, "no-screenshots", false, "Disable saving debug screenshots to disk")

	// Target config file flag (already parsed by findConfigArg, registered here for flag validity)
	var flagConfig string
	fs.StringVar(&flagConfig, "config", configPath, "Path to configuration file")
	fs.StringVar(&flagConfig, "c", configPath, "Path to configuration file (shorthand)")

	// Connection flags
	deviceID := fs.String("device", cfg.Device.DeviceID, "ADB device serial (e.g. localhost:5555)")
	adbHost := fs.String("adb-host", cfg.Device.ADBHost, "ADB server host")
	adbPort := fs.Int("adb-port", cfg.Device.ADBPort, "ADB server port")

	// Attack & search flags
	strategy := fs.String("strategy", cfg.Attack.StrategyFile, "Strategy YAML file")
	fs.StringVar(strategy, "s", cfg.Attack.StrategyFile, "Strategy YAML file (shorthand)")
	minGold := fs.Int("gold", cfg.Search.MinLootGold, "Minimum gold loot threshold to attack")
	minElixir := fs.Int("elixir", cfg.Search.MinLootElixir, "Minimum elixir loot threshold to attack")
	minDE := fs.Int("de", cfg.Search.MinLootDarkElixir, "Minimum dark elixir loot threshold to attack")
	minTrophies := fs.Int("min-trophies", cfg.Search.MinTrophies, "Minimum trophies target")
	maxTrophies := fs.Int("max-trophies", cfg.Search.MaxTrophies, "Maximum trophies target")
	maxAttacks := fs.Int("max-attacks", cfg.Attack.MaxAttackPerSession, "Max attacks before clean exit (0 = unlimited)")
	fs.IntVar(maxAttacks, "n", cfg.Attack.MaxAttackPerSession, "Max attacks before clean exit (shorthand)")
	once := fs.Bool("once", false, "Run single attack then exit cleanly")
	fs.BoolVar(&deployOnly, "deploy-only", false, "Skip search; deploy immediately on current screen")

	// Village management flags
	upgradeWalls := fs.Bool("upgrade-walls", cfg.Upgrade.UpgradeWalls, "Enable automatic wall upgrades")
	trainArmy := fs.Bool("train", cfg.Training.Enabled, "Enable automatic army training")
	fullArmy := fs.Bool("full-army", cfg.Training.FullArmyBeforeAttack, "Wait for full army before attacking")
	armySlot := fs.Int("army", 0, "Saved army slot (1-4) for this strategy (overrides strategy_slots)")

	// Performance & lifecycle flags
	noRestart := fs.Bool("no-restart", false, "Keep current game session; do not restart Clash of Clans on boot")
	perf := fs.Bool("perf", cfg.Performance.PerfMode, "Performance mode: lean attack path, skip extra frames & evidence PNGs")

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2)
	}

	if showVersion {
		fmt.Printf("ClashGO v%s (commit: %.7s)\n", version, commit)
		return
	}

	if listDevices {
		runListDevices(cfg.Device.ADBHost, cfg.Device.ADBPort)
		return
	}

	if listStrats {
		runListStrategies()
		return
	}

	// Logging initialization
	logger.Init(debugLog)
	fmt.Printf("ClashGO v%s (commit: %.7s) [CLI Mode]\n", version, commit)

	// Apply parsed flags onto cfg
	cfg.Device.DeviceID = *deviceID
	cfg.Device.ADBHost = *adbHost
	cfg.Device.ADBPort = *adbPort
	cfg.Attack.StrategyFile = *strategy
	cfg.Search.MinLootGold = *minGold
	cfg.Search.MinLootElixir = *minElixir
	cfg.Search.MinLootDarkElixir = *minDE
	cfg.Search.MinTrophies = *minTrophies
	cfg.Search.MaxTrophies = *maxTrophies
	cfg.Attack.MaxAttackPerSession = *maxAttacks
	cfg.Upgrade.UpgradeWalls = *upgradeWalls
	cfg.Training.Enabled = *trainArmy
	cfg.Training.FullArmyBeforeAttack = *fullArmy
	if *armySlot > 0 {
		if cfg.Attack.StrategySlots == nil {
			cfg.Attack.StrategySlots = make(map[string]int)
		}
		cfg.Attack.StrategySlots[cfg.Attack.StrategyFile] = *armySlot
	}
	cfg.Performance.PerfMode = *perf

	if *noRestart {
		cfg.Device.RestartOnStartup = false
	}

	if *perf {
		cfg.Performance.SkipUnchangedClassify = true
		cfg.Performance.CoalesceCaptures = true
		cfg.Performance.TuneGuestAnimations = true
		if !noScreenshots {
			// Perf mode defaults to no screenshot noise
			cfg.Debug.SaveScreenshots = false
		}
	}

	if noScreenshots {
		cfg.Debug.SaveScreenshots = false
	}

	if *once {
		cfg.Attack.MaxAttackPerSession = 1
		log.Info().Msg("mode: single attack (--once); will exit cleanly after first battle")
	}

	if deployOnly {
		cfg.Device.RestartOnStartup = false
		cfg.Attack.MaxAttackPerSession = 1
		log.Info().Msg("mode: deploy-only; skipping game restart, deploying on current screen")
	}

	// Context and graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sigCount int32
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range sigCh {
			count := atomic.AddInt32(&sigCount, 1)
			if count == 1 {
				log.Info().Str("signal", sig.String()).Msg("shutdown signal received; finishing current cycle (press Ctrl+C again to force exit)")
				cancel()
			} else {
				fmt.Println("\nForced exit requested.")
				os.Exit(1)
			}
		}
	}()

	b, err := bot.NewBot(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize bot")
	}

	if deployOnly {
		log.Info().Msg("deploy-only mode: capturing current screen and deploying once")
		qdErr := b.QuickDeploy()
		b.Stop()
		if qdErr != nil {
			log.Fatal().Err(qdErr).Msg("deploy-only failed")
		}
		printSessionSummary(b.Stats())
		return
	}

	// Periodic stats reporter
	if !quiet {
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					stats := b.Stats()
					health := b.Health()
					log.Info().
						Int32("attacks", stats.AttacksCompleted).
						Int32("skips", stats.SearchSkips).
						Str("uptime", stats.Uptime.Round(time.Second).String()).
						Float64("avg_ms", health.AvgCaptureMs).
						Msg("bot stats")
				}
			}
		}()
	}

	if err := b.Start(); err != nil {
		log.Fatal().Err(err).Msg("failed to start bot")
	}

	select {
	case <-ctx.Done():
	case <-b.Done():
		log.Info().Msg("bot finished its session")
	}

	log.Info().Msg("stopping bot cleanly...")
	b.Stop()
	log.Info().Msg("shutdown complete")

	printSessionSummary(b.Stats())
}

func findConfigArg(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-config" || arg == "--config" || arg == "-c" {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
		if strings.HasPrefix(arg, "-config=") || strings.HasPrefix(arg, "--config=") {
			return strings.SplitN(arg, "=", 2)[1]
		}
		if strings.HasPrefix(arg, "-c=") {
			return strings.SplitN(arg, "=", 2)[1]
		}
	}
	return ""
}

func loadConfig(customPath string) *config.BotConfig {
	if customPath != "" {
		cfg, err := config.Load(customPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config from %q: %v\n", customPath, err)
			os.Exit(1)
		}
		return cfg
	}

	// Check local directory first
	if _, err := os.Stat("config.json"); err == nil {
		cfg, err := config.Load("config.json")
		if err == nil {
			return cfg
		}
	}

	// Fallback to app config dir or defaults
	return config.LoadOrDefault("config.json")
}

func runListDevices(host string, port int) {
	if host == "" {
		host = "127.0.0.1"
	}
	if port <= 0 {
		port = 5037
	}
	client := adb.NewClient(adb.WithHost(host), adb.WithPort(port))
	devs, err := client.Devices()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to query ADB devices (%s:%d): %v\n", host, port, err)
		os.Exit(1)
	}

	fmt.Printf("Connected ADB Devices (%s:%d):\n", host, port)
	if len(devs) == 0 {
		fmt.Println("  (no devices connected)")
		return
	}
	for _, d := range devs {
		fmt.Printf("  • %s\n", d)
	}
}

func runListStrategies() {
	stratDir := paths.Resolve("strategies")
	entries, err := os.ReadDir(stratDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read strategies directory %q: %v\n", stratDir, err)
		os.Exit(1)
	}

	fmt.Printf("Available Strategies (%s):\n", stratDir)
	var count int
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml")) {
			fmt.Printf("  • %s\n", e.Name())
			count++
		}
	}
	if count == 0 {
		fmt.Println("  (no YAML strategies found)")
	}
}

func printSessionSummary(s bot.BotStats) {
	fmt.Println("\n========================================")
	fmt.Println("        ClashGO Session Summary")
	fmt.Println("========================================")
	fmt.Printf("  Uptime:             %s\n", s.Uptime.Round(time.Second))
	fmt.Printf("  Attacks Completed:  %d\n", s.AttacksCompleted)
	fmt.Printf("  Searches Skipped:   %d\n", s.SearchSkips)
	fmt.Printf("  Total Loot Gained:  Gold: %s | Elixir: %s | DE: %s\n",
		formatNumber(s.TotalGold), formatNumber(s.TotalElixir), formatNumber(s.TotalDE))
	fmt.Printf("  Stars:              [0*] %d | [1*] %d | [2*] %d | [3*] %d\n",
		s.Stars0, s.Stars1, s.Stars2, s.Stars3)
	if s.ClassifyRan > 0 {
		pct := float64(s.ClassifyReused) / float64(s.ClassifyRan+s.ClassifyReused) * 100
		fmt.Printf("  Vision Optim:       %d reused / %d ran (%.1f%% cached)\n",
			s.ClassifyReused, s.ClassifyRan, pct)
	}
	if s.CaptureCache.Hits > 0 || s.CaptureCache.Misses > 0 {
		fmt.Printf("  Capture Cache:      %d hits / %d misses\n",
			s.CaptureCache.Hits, s.CaptureCache.Misses)
	}
	fmt.Println("========================================")
}

func formatNumber(n int64) string {
	in := fmt.Sprintf("%d", n)
	if len(in) <= 3 {
		return in
	}
	var res []string
	rem := len(in) % 3
	if rem > 0 {
		res = append(res, in[:rem])
	}
	for i := rem; i < len(in); i += 3 {
		res = append(res, in[i:i+3])
	}
	return strings.Join(res, ",")
}

func printUsage(fs *flag.FlagSet) func() {
	return func() {
		fmt.Printf("ClashGO CLI v%s — High-Performance Headless CoC Automation\n\n", version)
		fmt.Printf("Usage: %s [options]\n\n", filepath.Base(os.Args[0]))

		fmt.Println("Commands & Information:")
		fmt.Println("  -devices           List connected ADB devices and exit")
		fmt.Println("  -strategies        List available strategy YAML files and exit")
		fmt.Println("  -v, -version       Print version and exit")
		fmt.Println("  -h, --help         Show this help message")
		fmt.Println()

		fmt.Println("Core Automation:")
		fmt.Println("  -c, -config <file> Custom config JSON file (default: config.json or app config)")
		fmt.Println("  -s, -strategy <f>  Strategy YAML file (default: valk_spam.yaml)")
		fmt.Println("  -n, -max-attacks N Max attacks before exiting (default: 0, unlimited)")
		fmt.Println("  -once              Run exactly 1 attack, then exit cleanly")
		fmt.Println("  -deploy-only       Deploy immediately on current attack screen without searching")
		fmt.Println()

		fmt.Println("Loot Search Criteria:")
		fmt.Println("  -gold <amount>     Minimum gold required to attack")
		fmt.Println("  -elixir <amount>   Minimum elixir required to attack")
		fmt.Println("  -de <amount>       Minimum dark elixir required to attack")
		fmt.Println("  -min-trophies N    Minimum trophies target")
		fmt.Println("  -max-trophies N    Maximum trophies target")
		fmt.Println()

		fmt.Println("Village Management:")
		fmt.Println("  -train             Enable auto-training armies")
		fmt.Println("  -army <1-4>        Army recipe slot to train")
		fmt.Println("  -upgrade-walls     Enable automatic wall upgrading")
		fmt.Println()

		fmt.Println("Performance & Connection:")
		fmt.Println("  -perf              Performance mode (leanest attack path, zero guest animation, coalesced captures)")
		fmt.Println("  -no-screenshots    Do not save debug screenshots to disk")
		fmt.Println("  -no-restart        Do not force-restart Clash of Clans on startup")
		fmt.Println("  -device <serial>   Target ADB device serial (default: localhost:5555)")
		fmt.Println("  -adb-host <host>   ADB server host (default: 127.0.0.1)")
		fmt.Println("  -adb-port <port>   ADB server port (default: 5037)")
		fmt.Println("  -q, -quiet         Suppress 10-second progress log ticker")
		fmt.Println("  -debug             Enable verbose debug logging")
		fmt.Println()

		fmt.Println("Examples:")
		fmt.Println("  # Check connected ADB devices")
		fmt.Println("  bot_cli -devices")
		fmt.Println()
		fmt.Println("  # Unattended farming with max performance")
		fmt.Println("  bot_cli -perf -gold 600000 -elixir 600000")
		fmt.Println()
		fmt.Println("  # Test a single attack with edrag rush")
		fmt.Println("  bot_cli -once -perf -strategy auto_edrag_rush.yaml")
		fmt.Println()
		fmt.Println("  # Deploy instantly on current battle screen")
		fmt.Println("  bot_cli -deploy-only -strategy valk_spam.yaml")
	}
}
