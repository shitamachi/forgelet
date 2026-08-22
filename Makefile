# forgelet — dev commands (contract; implementations land with the code)
BIN_DIR := ./bin

.PHONY: build test lint generate kind-up clean

build:
	go build -o $(BIN_DIR)/ ./cmd/...

test:
	go test ./...

lint:
	golangci-lint run

generate:
	go generate ./...

# local dev k3s/kind cluster with deps (PG, MinIO, Loki)
kind-up:
	./hack/kind-up.sh

clean:
	rm -rf $(BIN_DIR)
