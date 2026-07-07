package main

// LDFLAGS-overridable build metadata.
//
// Set by the Makefile via `-ldflags "-X main.version=v0.2.0-beta ..."` for
// both the CLI (`build-cli`) and the Wails GUI (`build-gui`) build paths.
// Keeping these in a tag-free file guarantees the same vars are visible in
// the Wails build (//go:build !cli) AND the CLI build (//go:build cli) —
// putting them in cli.go would otherwise hide them from the GUI.
var (
	version = "0.1.0-beta"
	commit  = "none"
	date    = "unknown"
)
