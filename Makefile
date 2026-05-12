APP := recodex
BRIDGE := rcc-bridge
RELAY := rcc-relay
BIN_DIR := bin
CACHE_DIR := .cache
GOCACHE := $(CURDIR)/$(CACHE_DIR)/go-build

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

.PHONY: all build bridge relay test fmt clean

all: test build

build:
	@mkdir -p $(BIN_DIR) $(GOCACHE)
	GOCACHE=$(GOCACHE) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BIN_DIR)/$(APP) .

bridge:
	@mkdir -p $(BIN_DIR) $(GOCACHE)
	GOCACHE=$(GOCACHE) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BIN_DIR)/$(BRIDGE) ./cmd/rcc-bridge

relay:
	@mkdir -p $(BIN_DIR) $(GOCACHE)
	GOCACHE=$(GOCACHE) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BIN_DIR)/$(RELAY) ./cmd/rcc-relay

test:
	@mkdir -p $(GOCACHE)
	GOCACHE=$(GOCACHE) go test ./...

fmt:
	gofmt -w main.go cmd internal

clean:
	rm -rf $(BIN_DIR) $(CACHE_DIR)
