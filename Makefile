BIN_DIR := ./bin
IMAGE_REGISTRY ?= forgelet
IMAGE_TAG ?= dev

.PHONY: build test test-integration test-kernels lint generate verify images clean web-install web-build web-test web-e2e web-browsers

build: web-build
	@mkdir -p "$(BIN_DIR)"
	go build -o "$(BIN_DIR)/forgelet" ./cmd/forgelet
	go build -o "$(BIN_DIR)/forgelet-executor" ./cmd/executor
	go build -o "$(BIN_DIR)/forgelet-act" ./cmd/act-worker
	go build -o "$(BIN_DIR)/forgelet-node" ./cmd/node-dispatch

# Includes real SQLite and httptest integrations; no container daemon required.
test:
	go test -race ./...

# A disposable k3s cluster. Docker is used only by the development test harness.
test-integration: web-install web-browsers
	go test -tags=integration ./test/integration -count=1 -v -timeout 95m

# Development-only comparison of pinned act libraries in disposable k3s Pods.
# The private steam-tasks probe is opt-in; see test/actkernel/README.md.
test-kernels:
	go test -race ./test/actkernel/...
	go test -tags=integration ./test/actkernel -count=1 -v -timeout 50m

lint:
	golangci-lint run --build-tags integration

generate:
	go generate ./...

web/node_modules/.installed: web/package.json web/package-lock.json
	npm --prefix web ci --no-audit --no-fund
	@touch $@

web-install: web/node_modules/.installed

web-build: web-install
	npm --prefix web run build

web-test: web-install
	npm --prefix web run typecheck
	npm --prefix web run format:check
	npm --prefix web test

web-browsers: web-install
	cd web && npx playwright install chromium

# Browser tests use deterministic fixture APIs; k3s tests exercise the real API.
web-e2e: web-build web-browsers
	npm --prefix web run test:e2e

verify: build test lint generate web-test web-e2e

images:
	docker build --target server -t "$(IMAGE_REGISTRY)-server:$(IMAGE_TAG)" .
	docker build --target executor -t "$(IMAGE_REGISTRY)-executor:$(IMAGE_TAG)" .

clean:
	rm -rf "$(BIN_DIR)"
