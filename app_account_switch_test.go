package main

// app_account_switch_test.go covers the app half of the in-game switch: the
// game is driven BEFORE the registry changes, and an identity mismatch leaves
// the previous account active so the bot can never farm under the wrong one.
// The ADB/template driver is replaced through a.switchGameAccounts; the real
// driver's tap sequence is covered in internal/game/accountswitch_test.go.

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/accounts"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
)

// newSwitchTestApp boots a config dir whose default account is slot 1 on
// localhost:5555 — the shared-emulator layout the switcher exists for.
func newSwitchTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	if err := paths.SetProfile(""); err != nil {
		t.Fatalf("reset profile: %v", err)
	}
	t.Cleanup(func() { _ = paths.SetProfile("") })

	a := NewApp()
	// Switching queues an async stats write; flush it before TempDir cleanup
	// removes the tree (last registered cleanup runs first).
	t.Cleanup(a.saveStatsSync)
	if _, err := a.GetAccounts(); err != nil {
		t.Fatalf("GetAccounts: %v", err)
	}
	if _, err := a.UpdateAccount(accounts.DefaultAccountID, accounts.Input{
		Name:     "Main",
		Device:   accounts.Device{DeviceID: "localhost:5555"},
		Slot:     1,
		Enabled:  true,
		Priority: 100,
	}); err != nil {
		t.Fatalf("set slot on the default account: %v", err)
	}
	return a
}

func TestApp_SwitchDrivesGameBeforeChangingActive(t *testing.T) {
	a := newSwitchTestApp(t)

	created, err := a.CreateAccount(accounts.Input{
		Name:      "Fleet Two",
		PlayerTag: "#2P0Y8LQGR",
		Device:    accounts.Device{DeviceID: "localhost:5555"},
		Slot:      2,
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	var drove []string
	a.switchGameAccounts = func(acct accounts.Account) error {
		// The registry must not have moved yet: the game is switched first so
		// a failed tap sequence can leave everything as it was.
		if got := a.loadedAccounts().ActiveID(); got != accounts.DefaultAccountID {
			t.Errorf("active account = %q while the game was still switching, want %q", got, accounts.DefaultAccountID)
		}
		drove = append(drove, acct.ID)
		return nil
	}

	res, err := a.SwitchAccount(created.ID, false)
	if err != nil {
		t.Fatalf("SwitchAccount: %v", err)
	}
	if !res.Account.Active || res.Started {
		t.Fatalf("switch result = %+v", res)
	}
	if paths.Profile() != created.ID {
		t.Fatalf("profile = %q, want %q", paths.Profile(), created.ID)
	}
	if want := []string{created.ID}; !reflect.DeepEqual(drove, want) {
		t.Fatalf("game switched for %v, want %v", drove, want)
	}
	cfg := config.LoadOrDefault("config.json")
	if cfg.Device.DeviceID != "localhost:5555" {
		t.Fatalf("profile device = %q, want localhost:5555", cfg.Device.DeviceID)
	}
}

func TestApp_SwitchIdentityMismatchKeepsPriorAccountAndStartsNothing(t *testing.T) {
	a := newSwitchTestApp(t)

	created, err := a.CreateAccount(accounts.Input{
		Name:      "Fleet Two",
		PlayerTag: "#2P0Y8LQGR",
		Device:    accounts.Device{DeviceID: "localhost:5555"},
		Slot:      2,
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	var drove []string
	a.switchGameAccounts = func(acct accounts.Account) error {
		drove = append(drove, acct.ID)
		if acct.ID == created.ID {
			return &game.IdentityMismatchError{
				Slot:     acct.Slot,
				Expected: game.Identity{Name: acct.Name, Tag: acct.PlayerTag},
				Observed: game.Identity{Name: "Somebody Else", Tag: "#9XYZ0000"},
			}
		}
		return nil
	}

	_, err = a.SwitchAccount(created.ID, false)
	if err == nil {
		t.Fatal("mismatch switch reported success")
	}
	var mismatch *game.IdentityMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %T %v, want IdentityMismatchError", err, err)
	}
	for _, needle := range []string{"#9XYZ0000", "#2P0Y8LQGR"} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error %q is missing %q", err, needle)
		}
	}

	state, err := a.GetAccounts()
	if err != nil {
		t.Fatalf("GetAccounts: %v", err)
	}
	if state.ActiveID != accounts.DefaultAccountID {
		t.Fatalf("active account = %q, want the previous %q", state.ActiveID, accounts.DefaultAccountID)
	}
	if paths.Profile() != "" {
		t.Fatalf("profile = %q, want none selected", paths.Profile())
	}
	// The emulator is showing the wrong village, so the switch must try to put
	// the previous account back on it — and still report the failure.
	if want := []string{created.ID, accounts.DefaultAccountID}; !reflect.DeepEqual(drove, want) {
		t.Fatalf("game drove %v, want %v (target, then restore)", drove, want)
	}
	a.mu.Lock()
	botRunning := a.bot != nil || a.cancel != nil
	a.mu.Unlock()
	if botRunning {
		t.Fatal("a bot was started despite the mismatch")
	}
}

func TestApp_SwitchRefusesAccountWithoutSlot(t *testing.T) {
	a := newSwitchTestApp(t)
	legacy, err := a.CreateAccount(accounts.Input{
		Name:    "No Slot",
		Device:  accounts.Device{DeviceID: "localhost:5556"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	drove := false
	a.switchGameAccounts = func(accounts.Account) error {
		drove = true
		return nil
	}
	_, err = a.SwitchAccount(legacy.ID, false)
	if err == nil {
		t.Fatal("switch accepted an account without a slot")
	}
	if !strings.Contains(err.Error(), "Supercell ID slot") || !strings.Contains(err.Error(), "1-5") {
		t.Fatalf("error %q does not tell the operator to set the slot", err)
	}
	if drove {
		t.Fatal("the game was driven for an account with no slot")
	}
	if paths.Profile() != "" {
		t.Fatalf("profile changed to %q", paths.Profile())
	}
}
