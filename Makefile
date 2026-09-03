.PHONY: all build test vet check clean

GO ?= go
BIN_DIR ?= bin

all: check build

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BIN_DIR)/homelab-server ./cmd/server
	$(GO) build -trimpath -o $(BIN_DIR)/homelab-agent ./cmd/agent

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: test vet

clean:
	$(GO) clean -testcache
	rm -f -- bin/homelab-server bin/homelab-agent
	rmdir --ignore-fail-on-non-empty bin
