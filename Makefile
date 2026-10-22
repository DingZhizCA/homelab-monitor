.PHONY: all build build-web generate-plugin-rpc test test-web test-storage vet vet-storage check clean

GO ?= go
BIN_DIR ?= bin

all: check build build-web

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BIN_DIR)/homelab-server ./cmd/server
	$(GO) build -trimpath -o $(BIN_DIR)/homelab-agent ./cmd/agent
	$(GO) build -trimpath -o $(BIN_DIR)/homelab-plugin-pack ./cmd/plugin-pack

build-web:
	cd web && npm run build

# Requires protoc v30.2, protoc-gen-go v1.36.9, and protoc-gen-go-grpc v1.5.1 on PATH.
generate-plugin-rpc:
	protoc -I=. --go_out=. --go_opt=module=github.com/DingZhizCA/homelab-monitor \
		--go-grpc_out=. --go-grpc_opt=module=github.com/DingZhizCA/homelab-monitor \
		internal/plugins/rpc/v1/plugin.proto

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

vet-storage:
	cd tools/storage-eval && $(GO) vet ./...

test-web:
	cd web && npm run lint
	cd web && npm test

test-storage:
	cd tools/storage-eval && $(GO) test -race ./...

check: test vet test-web test-storage vet-storage

clean:
	$(GO) clean -testcache
	rm -f -- bin/homelab-server bin/homelab-agent bin/homelab-plugin-pack
	rmdir --ignore-fail-on-non-empty bin
