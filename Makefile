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

clean:
	rm -rf $(BUILD_DIR)/*
	rm -f $(BINARY_NAME)
