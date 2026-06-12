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
