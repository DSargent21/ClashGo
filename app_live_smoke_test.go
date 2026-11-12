package main

import (
	"os"
	"testing"
)

func TestLiveSmokeRequested(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	t.Setenv("CLASHGO_LIVE_SMOKE", "")

	os.Args = []string{"ClashGO"}
	if liveSmokeRequested() {
		t.Fatal("normal launch requested live smoke")
	}

	os.Args = []string{"ClashGO", "--live-smoke"}
	if !liveSmokeRequested() {
		t.Fatal("--live-smoke was not detected")
	}

	os.Args = []string{"ClashGO"}
	t.Setenv("CLASHGO_LIVE_SMOKE", "1")
	if !liveSmokeRequested() {
		t.Fatal("CLASHGO_LIVE_SMOKE=1 was not detected")
	}
}
