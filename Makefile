# forgelet development commands
BIN_DIR := ./bin

.PHONY: build test lint generate verify kind-up clean

build:
	@packages="$$(go list ./cmd/...)"; status=$$?; \
	if [ $$status -ne 0 ]; then \
		exit $$status; \
	elif [ -z "$$packages" ]; then \
		echo "build: no command packages yet (docs-only phase)"; \
	else \
		mkdir -p "$(BIN_DIR)"; \
		go build -o "$(BIN_DIR)/" $$packages; \
	fi

test:
	@packages="$$(go list ./...)"; status=$$?; \
	if [ $$status -ne 0 ]; then \
		exit $$status; \
	elif [ -z "$$packages" ]; then \
		echo "test: no Go packages yet (docs-only phase)"; \
	else \
		go test $$packages; \
	fi

lint:
	@packages="$$(go list ./...)"; status=$$?; \
	if [ $$status -ne 0 ]; then \
		exit $$status; \
	elif [ -z "$$packages" ]; then \
		echo "lint: no Go packages yet (docs-only phase)"; \
	elif ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "lint: golangci-lint is required (CI uses v2.13.1)" >&2; \
		exit 1; \
	else \
		golangci-lint run; \
	fi

generate:
	@packages="$$(go list ./...)"; status=$$?; \
	if [ $$status -ne 0 ]; then \
		exit $$status; \
	elif [ -z "$$packages" ]; then \
		echo "generate: no Go packages yet (docs-only phase)"; \
	else \
		go generate $$packages; \
	fi

verify: build test lint generate

# Local k3s cluster with PG / MinIO / Loki. Intentionally unavailable until
# specs 0010 and 0011 define the dependency and support matrix.
kind-up:
	./hack/kind-up.sh

clean:
	rm -rf "$(BIN_DIR)"
