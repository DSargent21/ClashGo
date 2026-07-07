# Makefile for ClashGO

BINARY_NAME=bot_cli
BUILD_DIR=build/bin
MACOS_VERSION=$(shell sw_vers -productVersion | cut -d . -f 1-2)

.PHONY: all build build-cli build-gui clean

all: build-cli build-gui

build: build-cli build-gui

build-cli:
	@echo "Building CLI..."
	@mkdir -p $(BUILD_DIR)
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_VERSION) go build -tags cli -o $(BUILD_DIR)/$(BINARY_NAME) .

# one-shot attack: capture screen, design placements per unit, deploy via
# formula. No game restart, no bot search loop. Single command. Always
# rebuilds the CLI first so the cached binary used by
# ./run_designed_attack.sh is fresh.
attack-once: build-cli
	@./run_designed_attack.sh --clashgo build/bin/$(BINARY_NAME)

.PHONY: attack-once attack-once-cli auto-attack auto-attack-right auto-attack-left auto-attack-top auto-attack-bottom attack-record attack-replay

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
	@echo "Building GUI..."
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_VERSION) wails build -o ClashGO

package: build-gui
	@echo "Packaging DMG..."
	@mkdir -p $(BUILD_DIR)/dmg
	@cp -R $(BUILD_DIR)/ClashGO.app $(BUILD_DIR)/dmg/
	@mkdir -p $(BUILD_DIR)/dmg/ClashGO.app/Contents/Resources/assets
	@cp -R assets/* $(BUILD_DIR)/dmg/ClashGO.app/Contents/Resources/assets/
	@ln -s /Applications $(BUILD_DIR)/dmg/Applications
	@rm -f $(BUILD_DIR)/ClashGO.dmg
	hdiutil create -volname "ClashGO" -srcfolder $(BUILD_DIR)/dmg -ov -format UDZO $(BUILD_DIR)/ClashGO.dmg
	@rm -rf $(BUILD_DIR)/dmg
	@echo "DMG created at $(BUILD_DIR)/ClashGO.dmg"

VERSION=$(shell grep '"productVersion":' wails.json | cut -d '"' -f 4)

release: build-cli package
	@echo "Creating versioned release ZIP for v$(VERSION)..."
	@mkdir -p $(BUILD_DIR)/release
	@cp $(BUILD_DIR)/ClashGO.dmg $(BUILD_DIR)/release/
	@cp $(BUILD_DIR)/bot_cli $(BUILD_DIR)/release/
	@cp -R assets $(BUILD_DIR)/release/
	@cd $(BUILD_DIR)/release && zip -r ../../ClashGO-v$(VERSION)-macOS.zip .
	@rm -rf $(BUILD_DIR)/release
	@echo "Release ZIP created: ClashGO-v$(VERSION)-macOS.zip"

clean:
	rm -rf $(BUILD_DIR)/*
	rm -f $(BINARY_NAME)
