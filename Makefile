# Makefile for ClashGO

BINARY_NAME=bot_cli
BUILD_DIR=build/bin
MACOS_VERSION=$(shell sw_vers -productVersion | cut -d . -f 1-2)

# Build metadata — shared between the CLI build (-tags cli) and the
# Wails GUI build (build/darwin). Override VERSION on the command line
# before running `make release`:
#
#     make release VERSION=0.3.0-beta
#
# The same value goes into:
#   - Go ldflags (binary --version)
#   - Wails .app Info.plist (productVersion)
#   - The release zip filename (-v<VERSION>-macOS)
#   - The latest.json manifest emitted alongside the zip
ifeq ($(origin VERSION),undefined)
VERSION := $(shell grep '"productVersion":' wails.json | cut -d '"' -f 4)
endif
VERSION_TAG := v$(VERSION)
GIT_COMMIT  := $(shell git rev-parse HEAD 2>/dev/null || echo "none")
RELEASE_ZIP := ClashGO-v$(VERSION)-macOS.zip

# ldflags injected into both the CLI binary and the Wails GUI binary.
# Variables must match the package-level var names in version.go
# (main.version / main.commit / main.date).
LDFLAGS := -X main.version=$(VERSION) \
           -X main.commit=$(GIT_COMMIT)

# min_supported in latest.json: the lowest version that can update in
# place. For stable releases that's the version being shipped; for a
# prerelease (e.g. 0.3.0-beta) it's the previous minor (0.2.0) so users
# of the last stable/beta still get the update banner. Override with
# MIN_SUPPORTED= on the command line when the default is wrong.
MIN_SUPPORTED ?= $(shell echo "$(VERSION)" | awk -F'[.-]' '{ if (NF > 3) { m = $$2 - 1; if (m < 0) m = 0; print $$1 "." m ".0" } else { print $$1 "." $$2 ".0" } }')

# OpenCV test ldflags: the local Homebrew/sdk OpenCV build ships dylibs
# with @rpath install names, so test binaries need the lib dir baked into
# LC_RPATH or they abort at spawn ("Library not loaded: libopencv_gapi").
# Resolved via pkg-config; empty when unavailable (CI runners use a
# keg-only install where the default linking already works).
OPENCV_LIBDIR := $(shell pkg-config --variable=libdir opencv4 2>/dev/null)
ifneq ($(OPENCV_LIBDIR),)
    GOTEST_LDFLAGS := -ldflags='-extldflags=-Wl,-rpath,$(OPENCV_LIBDIR)'
endif

# Every binary that links OpenCV needs the same LC_RPATH — without it it aborts
# at spawn with "Library not loaded: @rpath/libopencv_gapi.410.dylib" and every
# probe/live run in this repo is dead on arrival. Empty on CI, where the keg-only
# install's absolute install names resolve without help.
#
# The GUI used to be excluded "because the bundle is packaged for other Macs",
# which was backwards: excluding it produced a DMG whose app died in dyld on
# every machine, including this one. The bundle is made portable by
# `make bundle-dylibs` (tools/bundle_dylibs.sh), which copies the closure into
# Contents/Frameworks and rewrites these references away — so baking the build
# machine's lib dir into the binary is fine, and the bundler is what removes it
# from the shipped artifact.
#
# -headerpad_max_install_names is not optional: install_name_tool refuses to
# grow a load command in a binary linked without it ("larger updated load
# commands do not fit ... you may need to use -headerpad or
# -headerpad_max_install_names"), and the bundler has to rewrite every OpenCV
# reference plus add an rpath.
# `-Wl,` forwards a COMMA-separated list to the linker, so both options ride in
# one -extldflags token. `-extldflags` takes a single argument: a space-separated
# second -Wl, flag is parsed by the Go linker as a flag of its own and the build
# dies printing its usage (`-extldflags='a b'` does not survive either, because
# the wails CLI splits the -ldflags string on spaces before handing it to Go).
ifneq ($(OPENCV_LIBDIR),)
    CLI_OPENCV_LDFLAGS := -extldflags=-Wl,-rpath,$(OPENCV_LIBDIR),-headerpad_max_install_names
endif

.PHONY: all build build-cli build-gui clean release manifest test test-go test-py

all: build-cli build-gui

build: build-cli build-gui

# test: full verification suite — vet, Go tests, Python strategy contract
# tests. go vet before tests so a type error fails fast without paying
# for the (slow) first OpenCV link.
test: test-go test-py
	@echo "ALL TESTS PASSED"

test-go:
	@echo "Running go vet ./..."
	go vet ./...
	@echo "Running go test ./..."
	go test $(GOTEST_LDFLAGS) ./...

# Locate a python3 interpreter that actually has pytest + pyyaml. The
# macOS system python3 (CLT 3.9) lacks both, while framework/homebrew
# installs carry them — probe candidates instead of assuming.
PYTEST_PY := $(shell for p in python3 /usr/local/bin/python3.12 /opt/homebrew/bin/python3 $(HOME)/.local/bin/python3.11; do command -v $$p >/dev/null 2>&1 || continue; $$p -c "import pytest, yaml" >/dev/null 2>&1 && { command -v $$p; break; }; done)

test-py:
	@echo "Running strategy contract tests (pytest)"
ifdef PYTEST_PY
	$(PYTEST_PY) -m pytest tests/
else
	@echo "SKIP: no python3 with pytest+pyyaml found — strategy contract tests not run"
endif

build-cli:
	@echo "Building CLI (version=$(VERSION), commit=$(GIT_COMMIT))..."
	@mkdir -p $(BUILD_DIR)
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_VERSION) go build -tags cli -ldflags "$(LDFLAGS) $(CLI_OPENCV_LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) .

# one-shot attack: capture screen, design placements per unit, deploy via
# formula. No game restart, no bot search loop. Single command. Always
# rebuilds the CLI first so the cached binary used by
# ./run_designed_attack.sh is fresh.
attack-once: build-cli
	@./run_designed_attack.sh --clashgo build/bin/$(BINARY_NAME)

# 720p attack review: render the full plan (formula taps, spell lines,
# army-bar slots, red zone) onto the live emulator frame WITHOUT tapping.
# Run it BEFORE every real attack — it catches spells-in-corner, missing
# bar units and broken count OCR before a battle is spent.
.PHONY: attack-verify
attack-verify: build-cli
	@go build -ldflags "$(CLI_OPENCV_LDFLAGS)" -o $(BUILD_DIR)/attack_verify ./cmd/attack_verify
	@$(BUILD_DIR)/attack_verify -save tmp/attack_verify_overlay.png

# pick-coords: click the exact deploy points for each of the four sides on the
# LIVE emulator frame, then save them to assets/precision_config.json. Serves a
# picker on 127.0.0.1:8791 — open it in the Preview tab (or your browser) and
# click the frame: no coordinates to type, no geometry to reason about.
#
# Saved pins are AUTHORITATIVE: for a pinned side the bot stops substituting the
# strategy formula, and the strategy `offset` no longer pushes the line toward
# the base. Place points in the outer ring (the shaded band) and they are
# deployable on every base.
#   make pick-coords
#   make pick-coords ARGS="-addr 127.0.0.1:9000"
.PHONY: pick-coords
pick-coords:
	@mkdir -p $(BUILD_DIR)
	@go build -ldflags "$(CLI_OPENCV_LDFLAGS)" -o $(BUILD_DIR)/pick_coords ./cmd/pick_coords
	@$(BUILD_DIR)/pick_coords $(ARGS)

# pick-army-slots: select and calibrate the clickable boxes for each of the
# 4 saved army recipe slots and the dropdown arrow on live game frames.
#   make pick-army-slots
.PHONY: pick-army-slots
pick-army-slots:
	@mkdir -p $(BUILD_DIR)
	@go build -ldflags "$(CLI_OPENCV_LDFLAGS)" -o $(BUILD_DIR)/pick_army_slots ./cmd/pick_army_slots
	@$(BUILD_DIR)/pick_army_slots $(ARGS)

# see: the debugging agent's eyes on the emulator. Renders a frame as text
# with a pixel-coordinate ruler plus everything the bot's own vision layer
# perceives (state, action button, red zone, recognised text with boxes), and
# keeps a rolling store of frames so a run that already happened can still be
# reviewed. Never taps.
#   make see                     # report on the live screen
#   make see ARGS="look -img tmp/x.png -rect 580,585,130,50"
#   make see ARGS="diff tmp/a.png tmp/b.png"
#   make see ARGS="timeline"
# refmap: turn physically-dragged rects into asset-shaped JSON. Every picking
# tool routes its drags through this so the HUD law is inverted in exactly one
# place (internal/game.Calibration.HudRectReferenceSnap); dividing the drag by a
# scale factor, which the old wall-button picker did, is NOT the inverse and was
# why the recorded boxes moved on every run.
#   make refmap ARGS="-describe"
#   printf 'gold 591 503 692 601\n' | make refmap ARGS="-w 1280 -h 720 -k 1.325 -schema buttons"
.PHONY: refmap
refmap:
	@mkdir -p $(BUILD_DIR)
	@go build -ldflags "$(CLI_OPENCV_LDFLAGS)" -o $(BUILD_DIR)/refmap ./cmd/refmap
	@if [ -n "$(ARGS)" ]; then $(BUILD_DIR)/refmap $(ARGS); fi

.PHONY: see
see:
	@mkdir -p $(BUILD_DIR)
	@go build -ldflags "$(CLI_OPENCV_LDFLAGS)" -o $(BUILD_DIR)/see ./cmd/see
	@$(BUILD_DIR)/see $(if $(ARGS),$(ARGS),look)

# attack-report: read a finished run's log and say, per unit, what the deploy
# phase actually established. The geometry harnesses answer "were the taps in
# the band"; this answers "did the army deploy and did the spells land somewhere
# useful", which is the question a bad attack really raises. Exits non-zero when
# the log contradicts a success.
#   make attack-report ARGS="-log tmp/live_attack/run.log"
.PHONY: attack-report
attack-report:
	@mkdir -p $(BUILD_DIR)
	@go build -ldflags "$(CLI_OPENCV_LDFLAGS)" -o $(BUILD_DIR)/attack_report ./cmd/attack_report
	@$(BUILD_DIR)/attack_report $(ARGS)

.PHONY: attack-once attack-once-cli auto-attack auto-attack-right auto-attack-left auto-attack-top auto-attack-bottom attack-record attack-replay attack-classify

attack-once-cli: build-cli
	@./run_designed_attack.sh \
		--strategy assets/strategies/auto_edrag_rush.yaml \
		--device localhost:5555 \
		--out tmp/last_designed_attack \
		--clashgo build/bin/$(BINARY_NAME)

# No-click variants: capture screen → auto-pick every unit on the chosen
# side → deploy via formula. Use when the manual click-by-click picker
# is overkill (e.g. forcing a single-side attack for repeat runs).
auto-attack: build-cli
	@./run_designed_attack.sh --clashgo build/bin/$(BINARY_NAME) --auto --target-edge right

auto-attack-right: auto-attack
auto-attack-left: build-cli
	@./run_designed_attack.sh --clashgo build/bin/$(BINARY_NAME) --auto --target-edge left
auto-attack-top: build-cli
	@./run_designed_attack.sh --clashgo build/bin/$(BINARY_NAME) --auto --target-edge top
auto-attack-bottom: build-cli
	@./run_designed_attack.sh --clashgo build/bin/$(BINARY_NAME) --auto --target-edge bottom

auto-attack-bluestacks: build-cli
	@./run_designed_attack.sh --clashgo build/bin/$(BINARY_NAME) --auto --target-edge right --device 127.0.0.1:5555

# Macro recorder/replayer — teach by demonstration. Record mode opens a
# window over the device screen; clicks get forwarded + saved to JSON.
# Replay mode replays that JSON on the device at the recorded cadence.
#
# These targets compile cmd/attack_record to a SEPARATE binary
# (build/bin/attack_record) so it doesn't share flags with bot_cli's
# main CLI. They do NOT depend on build-cli because the recorder has no
# `-tags cli` build tag — the gocv window + tap hooks are pure main.
OUT ?= tmp/my_attack.json
IN ?= tmp/my_attack.json
DEVICE ?= 127.0.0.1:5555
# Replay-only: how many extra fires per non-hero drop (troop/spell). Heroes
# always stay at 0 (one tap is correct — hero ability does the work).
# Set to 1 or 2 if BlueStacks is dropping single taps. Pairs with EXTRA_DELAY.
EXTRA_TAPS ?= 1
EXTRA_DELAY ?= 50

build-attack-record:
	@echo "Building attack_record..."
	@mkdir -p $(BUILD_DIR)
	@go build -o $(BUILD_DIR)/attack_record ./cmd/attack_record

attack-record: build-attack-record
	@./build/bin/attack_record --mode record --out $(OUT) --device $(DEVICE)

attack-replay: build-attack-record
	@./build/bin/attack_record --mode replay --in $(IN) --device $(DEVICE) \
		--extra-tap-count $(EXTRA_TAPS) --extra-tap-delay $(EXTRA_DELAY)

# Same as attack-replay but lets you preview what classification+extras a
# recorded macro gets without actually firing taps. Useful for sanity-check.
# Uses the binary's --dry-run flag so no clicks reach the device.
attack-classify: build-attack-record
	@echo "Classifying taps in $(IN) (dry-run, no device taps fired)"
	@./build/bin/attack_record --mode replay --in $(IN) --device $(DEVICE) \
		--extra-tap-count $(EXTRA_TAPS) --extra-tap-delay $(EXTRA_DELAY) \
		--dry-run 2>&1 | grep -E 'slot|deploy|dry-run'

# build-gui MUST carry $(CLI_OPENCV_LDFLAGS) like every CLI target does: the
# GUI links the same gocv/OpenCV libraries, and without the rpath dyld aborts
# the app before main() — "Library not loaded: @rpath/libopencv_gapi.410.dylib,
# no LC_RPATH's found" — which is a DMG that mounts, verifies and then will not
# launch. CI passes the same flag, where it resolves to the runner's Homebrew
# prefix; those absolute install names are why a released DMG still only
# launches on a machine with the matching OpenCV at the same path. Bundling the
# dylibs into Contents/Frameworks is the fix for that, and is not what this
# flag does.
build-gui:
	@echo "Building GUI (version=$(VERSION), commit=$(GIT_COMMIT))..."
	@mkdir -p $(BUILD_DIR)
	# -skipbindings: bindings are committed (web/wailsjs/go), and generating
	# them runs wails' throwaway helper binary, which links OpenCV with an
	# @rpath it can never satisfy without the build machine's exact DYLD
	# environment (CI run #11 failed here on a fresh checkout; locally the
	# same way). Skipping it works because the committed bindings are what
	# generation would emit.
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_VERSION) wails build -o ClashGO -skipbindings -ldflags "$(LDFLAGS) $(CLI_OPENCV_LDFLAGS)"

# manifest target — produces build/bin/latest.json once the zip is on
# disk. Standalone so it can be invoked from CI without a full GUI
# build. Run after `build-cli` (which produces the zip via `release`).
# Optional release-notes file for latest.json ("notes" field). The CI
# workflow passes the CHANGELOG section for the tagged version so the
# in-app update window shows what's new instead of "No release notes"
# and the GitHub release body carries the same text.
NOTES_FILE ?=

manifest:
	@echo "Building release_manifest helper..."
	@mkdir -p $(BUILD_DIR)
	@go build -o $(BUILD_DIR)/release_manifest ./cmd/release_manifest
	@echo "Emitting build/bin/latest.json for $(VERSION)..."
	@./build/bin/release_manifest \
		-version $(VERSION) \
		-zip $(BUILD_DIR)/$(RELEASE_ZIP) \
		-min-supported $(MIN_SUPPORTED) \
		-out $(BUILD_DIR)/latest.json \
		-repo DSargent21/ClashGo \
		-os darwin \
		$(if $(NOTES_FILE),-notes-file $(NOTES_FILE))

# stage-app injects the read-only assets (templates, strategies) and the
# in-app update helper into the built .app bundle itself, then re-signs
# (ad-hoc). This is required BEFORE both packaging paths so every
# artifact the user can install — the DMG and the zip — ships with a
# working assets dir and a valid bundle signature:
#
#   - Without this, build-zip would zip the raw wails .app, whose
#     Contents/Resources holds only the icon. Installed from the zip,
#     GetAssetsDir() would resolve to an empty dir: the Config page's
#     strategy list would be empty and the bot's OCR templates would
#     silently fail to load.
#   - build_dmg.sh does its own injection into a staged copy for
#     standalone use, but the zip path never saw it.
#
# wails ad-hoc signs the bundle during `wails build`; touching
# Contents/Resources afterwards invalidates the sealed resources, so we
# re-sign here (codesign --verify --deep --strict must pass on the
# shipped app — see tools/build_dmg.sh Stage 2.5 for the full rationale).
.PHONY: stage-app
stage-app: build-gui
	@echo "Staging assets + install helper into $(BUILD_DIR)/ClashGO.app..."
	@mkdir -p $(BUILD_DIR)/ClashGO.app/Contents/Resources/assets
	@if [ -d assets ]; then cp -R assets/. $(BUILD_DIR)/ClashGO.app/Contents/Resources/assets/; fi
	@if [ -f build/darwin/install_update.sh ]; then cp build/darwin/install_update.sh $(BUILD_DIR)/ClashGO.app/Contents/Resources/install_update.sh && chmod +x $(BUILD_DIR)/ClashGO.app/Contents/Resources/install_update.sh; fi
	@codesign --force --deep -s - $(BUILD_DIR)/ClashGO.app >/dev/null 2>&1 && echo "  re-signed (ad-hoc)" || echo "  WARNING: codesign failed"
	@codesign --verify --deep --strict $(BUILD_DIR)/ClashGO.app >/dev/null 2>&1 && echo "  bundle signature verified" || echo "  WARNING: signature verify failed"

# bundle-dylibs makes the .app self-contained: it copies the OpenCV closure
# into Contents/Frameworks and rewrites the references, so the DMG launches on a
# Mac with no OpenCV installed. It runs after stage-app (assets in, first
# signature) and must run BEFORE package and build-zip, because the DMG is
# assembled from this bundle and the zip is made from it too — and because
# build_dmg.sh copies whatever bundle it is handed.
.PHONY: bundle-dylibs
bundle-dylibs: stage-app
	@echo "Bundling OpenCV dylibs into $(BUILD_DIR)/ClashGO.app..."
	# CLASHGO_BUNDLE_SMOKE=1: after the rewrite, launch the binary once so a
	# bundler regression is a build failure here, not a dead app for a user.
	@CLASHGO_BUNDLE_SMOKE=1 bash tools/bundle_dylibs.sh $(BUILD_DIR)/ClashGO.app

# package depends on bundle-dylibs (not build-gui directly) so a `make
# release` run never rebuilds the app between staging and DMG assembly —
# a fresh `wails build` would wipe the staged Contents/Resources/assets
# and the zip (built afterwards) would silently ship without them. make
# dedupes the stage-app prerequisite within one invocation, so `release`
# still only builds the GUI once.
package: bundle-dylibs
	@echo "Packaging DMG via tools/build_dmg.sh..."
	@bash tools/build_dmg.sh $(BUILD_DIR)/ClashGO.app $(BUILD_DIR)/ClashGO.dmg "ClashGO Installer"

# note: tools/build_dmg.sh runs standalone \u2014 no Make dependency on the
# caller. Callers (CI / `make release`) still trigger `package` to keep
# the legacy make-graph intact.

release: build-cli package build-zip manifest
	@echo "Release v$(VERSION) emitted:"
	@echo "  - $(BUILD_DIR)/ClashGO-v$(VERSION)-macOS.zip"
	@echo "  - $(BUILD_DIR)/ClashGO.dmg"
	@echo "  - $(BUILD_DIR)/latest.json (publish alongside the zip on GitHub)"

# build-zip turns the packaged .app into the zip artifact. Depends on
# bundle-dylibs so the zip carries Contents/Frameworks too — the zip is the
# artifact the in-app updater installs, so it must be as self-contained as the DMG.
build-zip: bundle-dylibs
	@echo "Building release zip..."
	@mkdir -p $(BUILD_DIR)/release
	@cp -R $(BUILD_DIR)/ClashGO.app $(BUILD_DIR)/release/
	@cd $(BUILD_DIR)/release && zip -r ../$(RELEASE_ZIP) ClashGO.app
	@rm -rf $(BUILD_DIR)/release

clean:
	rm -rf $(BUILD_DIR)/*
	rm -f $(BINARY_NAME)
