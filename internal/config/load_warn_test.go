package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// A file that exists but cannot be parsed must not fall back to defaults in
// silence: that is how every setting appeared to reset with no explanation.
func TestLoadOrDefaultWarnsOnCorruptConfig(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	corrupt := []byte(`{"search": {"min_loot_gold": 750000`)
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}

	got := LoadOrDefault(path)
	if !reflect.DeepEqual(got, DefaultConfig()) {
		t.Fatal("LoadOrDefault did not return the default config")
	}
	if !strings.Contains(buf.String(), "config unreadable") {
		t.Fatalf("no warning logged for a corrupt config; log = %q", buf.String())
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, corrupt) {
		t.Fatal("LoadOrDefault rewrote the corrupt file")
	}
}

// First run has no file at all; that must stay silent.
func TestLoadOrDefaultIsSilentWhenMissing(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })

	got := LoadOrDefault(filepath.Join(t.TempDir(), "config.json"))
	if !reflect.DeepEqual(got, DefaultConfig()) {
		t.Fatal("LoadOrDefault did not return the default config")
	}
	if buf.Len() != 0 {
		t.Fatalf("missing config logged a warning: %q", buf.String())
	}
}
