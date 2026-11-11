package accounts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	if err := paths.SetProfile(""); err != nil {
		t.Fatalf("reset profile: %v", err)
	}
	t.Cleanup(func() { _ = paths.SetProfile("") })

	cfg := config.DefaultConfig()
	cfg.Search.MinLootGold = 123456
	cfg.Device.DeviceID = "localhost:5555"
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st, dir
}

func TestOpenBootstrapsLegacyProfile(t *testing.T) {
	st, dir := newTestStore(t)

	acct, ok := st.Active()
	if !ok {
		t.Fatal("no active account after bootstrap")
	}
	if acct.ID != DefaultAccountID || acct.Name != "Main" {
		t.Fatalf("unexpected bootstrap account: %+v", acct)
	}
	if acct.Device.DeviceID != "localhost:5555" {
		t.Fatalf("device not inherited from config: %q", acct.Device.DeviceID)
	}

	profileCfg := paths.ResolveProfileConfig(DefaultAccountID, "config.json")
	cfg, err := config.Load(profileCfg)
	if err != nil {
		t.Fatalf("profile config missing: %v", err)
	}
	if cfg.Search.MinLootGold != 123456 {
		t.Fatalf("legacy config not copied: gold=%d", cfg.Search.MinLootGold)
	}
	if _, err := os.Stat(filepath.Join(dir, "accounts.json")); err != nil {
		t.Fatalf("registry not persisted: %v", err)
	}

	// Reload must preserve the same account, not bootstrap a second one.
	st2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := len(st2.List()); got != 1 {
		t.Fatalf("reopen produced %d accounts, want 1", got)
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	st, _ := newTestStore(t)

	acct, err := st.Create(Input{
		Name:      "Mini",
		PlayerTag: "#2P0Y8LQGR",
		TownHall:  12,
		Device:    Device{DeviceID: "localhost:5556"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if acct.ID != "mini" {
		t.Fatalf("id = %q, want mini", acct.ID)
	}
	if acct.Device.ADBHost != "127.0.0.1" || acct.Device.ADBPort != 5037 {
		t.Fatalf("device defaults not applied: %+v", acct.Device)
	}

	profileCfg, err := config.Load(paths.ResolveProfileConfig(acct.ID, "config.json"))
	if err != nil {
		t.Fatalf("profile config: %v", err)
	}
	if profileCfg.Device.DeviceID != "localhost:5556" {
		t.Fatalf("profile device = %q, want localhost:5556", profileCfg.Device.DeviceID)
	}

	// Duplicate non-empty player tags are an identity hazard.
	_, err = st.Create(Input{
		Name:      "Dupe",
		PlayerTag: "#2P0Y8LQGR",
		Device:    Device{DeviceID: "localhost:5557"},
		Enabled:   true,
	})
	if err == nil {
		t.Fatal("duplicate player tag accepted")
	}

	// The same serial cannot back two villages: a session would farm
	// whichever account is signed in and book it as the other one.
	_, err = st.Create(Input{
		Name:    "Clash",
		Device:  Device{DeviceID: "localhost:5556"},
		Enabled: true,
	})
	if err == nil {
		t.Fatal("duplicate device binding accepted")
	}

	updated, err := st.Update(acct.ID, Input{
		Name:                 "Mini II",
		PlayerTag:            "#2P0Y8LQGR",
		TownHall:             13,
		Device:               Device{DeviceID: "localhost:5556"},
		Enabled:              false,
		CooldownMinutes:      30,
		MaxAttacksPerDay:     25,
		MaxAttacksPerSession: 5,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Mini II" || updated.Enabled || updated.MaxAttacksPerDay != 25 {
		t.Fatalf("update not applied: %+v", updated)
	}

	if err := st.Delete(acct.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := st.Get(acct.ID); ok {
		t.Fatal("deleted account still present")
	}
	trash := filepath.Join(paths.GetConfigDir(), "accounts", ".trash")
	entries, err := os.ReadDir(trash)
	if err != nil || len(entries) == 0 {
		t.Fatalf("profile not archived in trash: entries=%d err=%v", len(entries), err)
	}
}

func TestDeleteGuardsActiveAndLast(t *testing.T) {
	st, _ := newTestStore(t)
	if err := st.Delete(DefaultAccountID); err == nil {
		t.Fatal("deleting the active account was allowed")
	}

	acct, err := st.Create(Input{Name: "Mini", Device: Device{DeviceID: "localhost:5556"}, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := st.SetActive(acct.ID); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := st.Delete(acct.ID); err == nil {
		t.Fatal("deleting the active account was allowed")
	}
}

func TestRotationRoundRobinCooldownAndCap(t *testing.T) {
	st, _ := newTestStore(t)
	mini, err := st.Create(Input{Name: "Mini", Device: Device{DeviceID: "localhost:5556"}, Enabled: true, CooldownMinutes: 30})
	if err != nil {
		t.Fatalf("Create mini: %v", err)
	}
	alt, err := st.Create(Input{Name: "Alt", Device: Device{DeviceID: "localhost:5557"}, Enabled: true, MaxAttacksPerDay: 5})
	if err != nil {
		t.Fatalf("Create alt: %v", err)
	}
	if err := st.SetRotation(Rotation{Enabled: true, Order: OrderRoundRobin, AutoStart: true, AttacksPerAccount: 10}); err != nil {
		t.Fatalf("SetRotation: %v", err)
	}

	now := time.Now()
	next, ok := st.Next(DefaultAccountID, now)
	if !ok || next.ID != mini.ID {
		t.Fatalf("round robin picked %q ok=%v, want %q", next.ID, ok, mini.ID)
	}

	// A used account is cooling down and must be skipped.
	if _, err := st.SetActive(mini.ID); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	next, ok = st.Next(DefaultAccountID, now)
	if !ok || next.ID != alt.ID {
		t.Fatalf("cooldown not honored: picked %q ok=%v, want %q", next.ID, ok, alt.ID)
	}

	// Daily cap excludes the other candidate.
	if err := st.ResetUsage(alt.ID); err != nil {
		t.Fatalf("ResetUsage: %v", err)
	}
	st.RecordAttacks(alt.ID, 50)
	if _, ok := st.Next(DefaultAccountID, now); ok {
		t.Fatal("daily-capped account was selected")
	}
}

func TestRotationPriorityOrder(t *testing.T) {
	st, _ := newTestStore(t)
	if _, err := st.Create(Input{Name: "Low", Device: Device{DeviceID: "localhost:5556"}, Enabled: true, Priority: 10}); err != nil {
		t.Fatalf("Create Low: %v", err)
	}
	if _, err := st.Create(Input{Name: "High", Device: Device{DeviceID: "localhost:5557"}, Enabled: true, Priority: 900}); err != nil {
		t.Fatalf("Create High: %v", err)
	}
	if err := st.SetRotation(Rotation{Enabled: true, Order: OrderPriority, AutoStart: true, AttacksPerAccount: 10}); err != nil {
		t.Fatalf("SetRotation: %v", err)
	}
	next, ok := st.Next(DefaultAccountID, time.Now())
	if !ok || next.Name != "High" {
		t.Fatalf("priority picked %q ok=%v, want High", next.Name, ok)
	}
}

func TestProfileIsolation(t *testing.T) {
	st, dir := newTestStore(t)
	mini, err := st.Create(Input{Name: "Mini", Device: Device{DeviceID: "localhost:5556"}, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := paths.SetProfile(mini.ID); err != nil {
		t.Fatalf("SetProfile: %v", err)
	}
	t.Cleanup(func() { _ = paths.SetProfile("") })

	if got := paths.ResolveConfig("config.json"); got != paths.ResolveProfileConfig(mini.ID, "config.json") {
		t.Fatalf("ResolveConfig = %q, want profile path", got)
	}
	cfg := config.LoadOrDefault("config.json")
	if cfg.Device.DeviceID != "localhost:5556" {
		t.Fatalf("profile config device = %q", cfg.Device.DeviceID)
	}

	// Root config must remain untouched by profile writes.
	rootData, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read root config: %v", err)
	}
	var root config.BotConfig
	if err := json.Unmarshal(rootData, &root); err != nil {
		t.Fatalf("parse root config: %v", err)
	}
	if root.Device.DeviceID != "localhost:5555" {
		t.Fatalf("root config mutated: device=%q", root.Device.DeviceID)
	}
}

func TestOpenRejectsCorruptRegistry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt registry: %v", err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt registry was accepted")
	}
}
