FROM node:24.14.0-bookworm-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web-build /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -o /out/forgelet ./cmd/forgelet && \
    CGO_ENABLED=0 go build -trimpath -o /out/forgelet-executor ./cmd/executor

FROM alpine:3.22 AS server
RUN apk add --no-cache ca-certificates
COPY --from=build /out/forgelet /usr/local/bin/forgelet
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/forgelet"]

FROM node:20.20.0-bookworm-slim AS node20
FROM moby/buildkit:v0.20.2 AS buildkit
FROM node:24.14.0-bookworm-slim AS executor
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates bash git tar xz-utils unzip && rm -rf /var/lib/apt/lists/*
COPY --from=node20 /usr/local/bin/node /opt/forgelet/node20/bin/node
RUN mkdir -p /opt/forgelet/node24/bin && cp /usr/local/bin/node /opt/forgelet/node24/bin/node
COPY --from=build /out/forgelet-executor /usr/local/bin/forgelet-executor
COPY --from=buildkit /usr/bin/buildctl /usr/local/bin/buildctl
USER 1000:1000
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/forgelet-executor"]

# Testcontainers uses the shipped executor environment and both actual binaries.
FROM executor AS integration
COPY --from=build /out/forgelet /usr/local/bin/forgelet
COPY test/fixtures/actions /opt/forgelet/test-actions
