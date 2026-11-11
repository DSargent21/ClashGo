package main

import (
	"encoding/json"
	"testing"

	"github.com/Ducky705/ClashGO/internal/accounts"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
)

// TestApp_AccountLifecycle exercises the switcher end to end without a live
// bot: bootstrap, create, switch (profile + device apply), refusal to delete
// the active account, then switch back and delete.
func TestApp_AccountLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	if err := paths.SetProfile(""); err != nil {
		t.Fatalf("reset profile: %v", err)
	}
	t.Cleanup(func() { _ = paths.SetProfile("") })

	a := NewApp()

	state, err := a.GetAccounts()
	if err != nil {
		t.Fatalf("GetAccounts: %v", err)
	}
	if len(state.Accounts) != 1 || state.ActiveID != accounts.DefaultAccountID {
		t.Fatalf("unexpected bootstrap state: %+v", state)
	}
	// The React client reads flattened account fields plus active/running/
	// stats. Assert the wire shape instead of trusting struct embedding.
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	var wire struct {
		Accounts []map[string]json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	for _, key := range []string{"id", "name", "device", "active", "running", "stats", "status"} {
		if _, ok := wire.Accounts[0][key]; !ok {
			t.Fatalf("account JSON is missing %q: %s", key, raw)
		}
	}

	created, err := a.CreateAccount(accounts.Input{
		Name:      "Mini",
		PlayerTag: "#2P0Y8LQGR",
		Device:    accounts.Device{DeviceID: "localhost:5556"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	res, err := a.SwitchAccount(created.ID, false)
	if err != nil {
		t.Fatalf("SwitchAccount: %v", err)
	}
	if !res.Account.Active || res.Started {
		t.Fatalf("unexpected switch result: %+v", res)
	}
	if paths.Profile() != created.ID {
		t.Fatalf("profile = %q, want %q", paths.Profile(), created.ID)
	}
	cfg := config.LoadOrDefault("config.json")
	if cfg.Device.DeviceID != "localhost:5556" {
		t.Fatalf("switched config device = %q, want localhost:5556", cfg.Device.DeviceID)
	}

	if err := a.DeleteAccount(created.ID); err == nil {
		t.Fatal("deleted the active account")
	}

	if _, err := a.SwitchAccount(accounts.DefaultAccountID, false); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if err := a.DeleteAccount(created.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	state, err = a.GetAccounts()
	if err != nil {
		t.Fatalf("GetAccounts after delete: %v", err)
	}
	if len(state.Accounts) != 1 || state.ActiveID != accounts.DefaultAccountID {
		t.Fatalf("unexpected state after delete: %+v", state)
	}
}

func TestApp_RotationPersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	if err := paths.SetProfile(""); err != nil {
		t.Fatalf("reset profile: %v", err)
	}
	t.Cleanup(func() { _ = paths.SetProfile("") })

	a := NewApp()
	if _, err := a.GetAccounts(); err != nil {
		t.Fatalf("GetAccounts: %v", err)
	}
	want := accounts.Rotation{Enabled: true, Order: accounts.OrderPriority, AutoStart: false, AttacksPerAccount: 7}
	if err := a.SetRotation(want); err != nil {
		t.Fatalf("SetRotation: %v", err)
	}
	state, err := a.GetAccounts()
	if err != nil {
		t.Fatalf("GetAccounts: %v", err)
	}
	if state.Rotation != want {
		t.Fatalf("rotation = %+v, want %+v", state.Rotation, want)
	}
}
