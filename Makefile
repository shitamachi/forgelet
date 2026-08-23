# forgelet development commands
BIN_DIR := ./bin
IMAGE_REGISTRY ?= ghcr.io/shitamachi/forgelet
IMAGE_TAG ?= dev

.PHONY: build test lint generate verify kind-up images clean

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

# Release-pipeline images (spec 0011 T8). The executor image ships bash:
# job pods run user scripts in it.
images: image-server image-controller image-executor image-minttoken

image-server:
	docker build --build-arg BIN=server \
		-t "$(IMAGE_REGISTRY)/server:$(IMAGE_TAG)" .

image-controller:
	docker build --build-arg BIN=controller \
		-t "$(IMAGE_REGISTRY)/controller:$(IMAGE_TAG)" .

image-executor:
	docker build --build-arg BIN=executor --build-arg PKGS="ca-certificates bash" \
		-t "$(IMAGE_REGISTRY)/executor:$(IMAGE_TAG)" .

image-minttoken:
	docker build --build-arg BIN=minttoken \
		-t "$(IMAGE_REGISTRY)/minttoken:$(IMAGE_TAG)" .

clean:
	rm -rf "$(BIN_DIR)"
