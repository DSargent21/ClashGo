package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
)

// TestUI_WizardSurfacesSaveErrors is the guard for a defect the browser stub
// hid: the packaged app has no visible console, and a rejected wizard save
// (invalid loot floor, missing strategy file, refused start) was only a
// console.error behind the modal, so clicking Start changed nothing on screen.
//
// The wizard must therefore carry an error prop rendered as an alert, the App
// must fill it from the failed attempt, and the floor inputs must clamp to the
// same ceiling config.ValidateLootFloors enforces — otherwise the wizard lets
// the user build a value its own save rejects.
func TestUI_WizardSurfacesSaveErrors(t *testing.T) {
	wizard := readUISource(t, "web/src/components/LaunchSetup.tsx")
	app := readUISource(t, "web/src/App.tsx")

	for _, want := range []string{
		"error?: string | null",
		"role=\"alert\"",
		"Could not apply this setup",
		"Math.min(MAX_LOOT_FLOOR, Math.max(0, parseInt(event.target.value, 10) || 0))",
	} {
		if !strings.Contains(wizard, want) {
			t.Errorf("LaunchSetup.tsx is missing %q; a refused save would be invisible again", want)
		}
	}
	if got := strings.Count(wizard, "max={MAX_LOOT_FLOOR}"); got != 3 {
		t.Errorf("loot floor inputs carry max={MAX_LOOT_FLOOR} %d times, want 3", got)
	}
	match := regexp.MustCompile(`MAX_LOOT_FLOOR = ([0-9_]+)`).FindStringSubmatch(wizard)
	if match == nil {
		t.Fatal("LaunchSetup.tsx does not define MAX_LOOT_FLOOR")
	}
	limit, err := strconv.Atoi(strings.ReplaceAll(match[1], "_", ""))
	if err != nil {
		t.Fatalf("parse MAX_LOOT_FLOOR %q: %v", match[1], err)
	}
	if limit != config.MaxLootFloor {
		t.Errorf("wizard clamps floors at %d, but config.ValidateLootFloors rejects above %d", limit, config.MaxLootFloor)
	}

	for _, want := range []string{
		"const [launchError, setLaunchError] = useState<string | null>(null)",
		"setLaunchError(null)",
		"setLaunchError(err instanceof Error ? err.message : String(err))",
		"error={launchError}",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("App.tsx is missing %q; the wizard would not receive the refusal reason", want)
		}
	}
}

// readUISource reads one frontend file relative to the repo root.
func readUISource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
