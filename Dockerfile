# Multi-stage image for all forgelet binaries (spec 0011 T8).
# Build one binary per image:
#
#   docker build --build-arg BIN=server   -t ghcr.io/shitamachi/forgelet/server:dev .
#   docker build --build-arg BIN=executor -t ghcr.io/shitamachi/forgelet/executor:dev .
#
# The executor runtime installs bash: it is PID 1 of every job pod and runs
# user `run:` scripts. Server/controller images stay minimal.
ARG GO_VERSION=1.27
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG BIN=server
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app "./cmd/${BIN}"

FROM alpine:3.21 AS runtime
ARG PKGS="ca-certificates"
RUN apk add --no-cache ${PKGS}
COPY --from=build /out/app /usr/local/bin/app
# Job pods launch the executor at /ci/executor (0004 §4 pod template); the
# duplicate copy keeps one Dockerfile for every binary.
COPY --from=build /out/app /ci/executor
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/app"]
