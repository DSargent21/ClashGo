# Makefile for ClashGO

BINARY_NAME=bot_cli
BUILD_DIR=build/bin
MACOS_VERSION=$(shell sw_vers -productVersion | cut -d . -f 1-2)

# Build metadata — shared between the CLI build (-tags cli) and the
# Wails GUI build (build/darwin). Override VERSION on the command line
# before running `make release`:
#
#     make release VERSION=0.2.0-beta
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
BUILD_DATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_ZIP := ClashGO-v$(VERSION)-macOS.zip

# ldflags injected into both the CLI binary and the Wails GUI binary.
# Variables must match the package-level var names in version.go
# (main.version / main.commit / main.date).
LDFLAGS := -X main.version=$(VERSION) \
           -X main.commit=$(GIT_COMMIT) \
           -X main.date=$(BUILD_DATE)

.PHONY: all build build-cli build-gui clean release manifest

all: build-cli build-gui

build: build-cli build-gui

build-cli:
	@echo "Building CLI (version=$(VERSION), commit=$(GIT_COMMIT))..."
	@mkdir -p $(BUILD_DIR)
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_VERSION) go build -tags cli -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) .

# one-shot attack: capture screen, design placements per unit, deploy via
# formula. No game restart, no bot search loop. Single command. Always
# rebuilds the CLI first so the cached binary used by
# ./run_designed_attack.sh is fresh.
attack-once: build-cli
	@./run_designed_attack.sh --clashgo build/bin/$(BINARY_NAME)

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

build-gui:
	@echo "Building GUI (version=$(VERSION), commit=$(GIT_COMMIT))..."
	@mkdir -p $(BUILD_DIR)
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_VERSION) wails build -o ClashGO -ldflags "$(LDFLAGS)"

# manifest target — produces build/bin/latest.json once the zip is on
# disk. Standalone so it can be invoked from CI without a full GUI
# build. Run after `build-cli` (which produces the zip via `release`).
manifest:
	@echo "Building release_manifest helper..."
	@mkdir -p $(BUILD_DIR)
	@go build -o $(BUILD_DIR)/release_manifest ./cmd/release_manifest
	@echo "Emitting build/bin/latest.json for $(VERSION)..."
	@./build/bin/release_manifest \
		-version $(VERSION) \
		-zip $(BUILD_DIR)/$(RELEASE_ZIP) \
		-min-supported $(shell echo "$(VERSION)" | sed -E 's/-.*//; s/^([0-9]+\.[0-9]+)\.[0-9]+.*/\1.0/') \
		-out $(BUILD_DIR)/latest.json \
		-repo Ducky705/ClashGO \
		-os darwin

package: build-gui
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

# build-zip turns the packaged .app into the zip artifact.
build-zip:
	@echo "Building release zip..."
	@mkdir -p $(BUILD_DIR)/release
	@cp -R $(BUILD_DIR)/ClashGO.app $(BUILD_DIR)/release/
	@cd $(BUILD_DIR)/release && zip -r ../$(RELEASE_ZIP) ClashGO.app
	@rm -rf $(BUILD_DIR)/release

clean:
	rm -rf $(BUILD_DIR)/*
	rm -f $(BINARY_NAME)
