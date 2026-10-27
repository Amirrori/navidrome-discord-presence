SHELL := /usr/bin/env bash
.PHONY: test build package clean

PLUGIN_NAME := discord-status
WASM_FILE := plugin.wasm
BUILD_DIR := build
TINYGO := $(shell command -v tinygo 2> /dev/null)

# Optional: restrict the plugin to your bridge instead of all private address
# ranges, e.g. make package BRIDGE_HOSTS=presence.example.com,192.0.2.16
BRIDGE_HOSTS ?=

test:
	go test -race ./...

build:
ifdef TINYGO
	tinygo build -opt=2 -scheduler=none -no-debug -o $(WASM_FILE) -target wasip1 -buildmode=c-shared .
else
	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o $(WASM_FILE) .
endif

package: build
	mkdir -p $(BUILD_DIR)
ifneq ($(strip $(BRIDGE_HOSTS)),)
	go run ./tools/manifest -in manifest.json -out $(BUILD_DIR)/manifest.json -hosts "$(BRIDGE_HOSTS)"
else
	cp manifest.json $(BUILD_DIR)/manifest.json
endif
	rm -f $(PLUGIN_NAME).ndp
	zip -j -X $(PLUGIN_NAME).ndp $(WASM_FILE) $(BUILD_DIR)/manifest.json

clean:
	rm -rf $(WASM_FILE) $(PLUGIN_NAME).ndp $(BUILD_DIR)
