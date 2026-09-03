.PHONY: all build build-web test test-web vet check clean

GO ?= go
BIN_DIR ?= bin

all: check build

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BIN_DIR)/homelab-server ./cmd/server
	$(GO) build -trimpath -o $(BIN_DIR)/homelab-agent ./cmd/agent

build-web:
	cd web && npm run build

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

test-web:
	cd web && npm run lint
	cd web && npm test

check: test vet test-web

clean:
	$(GO) clean -testcache
	rm -f -- bin/homelab-server bin/homelab-agent
	rmdir --ignore-fail-on-non-empty bin
