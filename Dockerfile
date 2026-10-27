FROM node:24.14.0-bookworm-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="$GOPROXY" go mod download
# Keep dependency compilation cached across application-only source changes.
RUN CGO_ENABLED=0 go build github.com/nektos/act/pkg/runner github.com/rhysd/actionlint modernc.org/sqlite k8s.io/client-go/kubernetes
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web-build /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -o /out/forgelet ./cmd/forgelet && \
    CGO_ENABLED=0 go build -trimpath -o /out/forgelet-executor ./cmd/executor && \
    CGO_ENABLED=0 go build -trimpath -o /out/forgelet-act ./cmd/act-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/forgelet-node ./cmd/node-dispatch

FROM alpine:3.22 AS server
RUN apk add --no-cache ca-certificates
COPY --from=build /out/forgelet /usr/local/bin/forgelet
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/forgelet"]

FROM node:20.20.0-bookworm-slim AS node20
FROM moby/buildkit:v0.20.2 AS buildkit
FROM node:24.14.0-bookworm-slim AS executor-base
COPY --from=golang:1.27.3-alpine /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ARG DEBIAN_MIRROR=https://deb.debian.org
RUN sed -i "s|http://deb.debian.org|$DEBIAN_MIRROR|g" /etc/apt/sources.list.d/debian.sources && apt-get update && apt-get install -y --no-install-recommends ca-certificates bash git tar xz-utils unzip && rm -rf /var/lib/apt/lists/*
COPY --from=node20 /usr/local/bin/node /opt/forgelet/node20/bin/node
RUN mkdir -p /opt/forgelet/node24/bin && cp /usr/local/bin/node /opt/forgelet/node24/bin/node
COPY --from=buildkit /usr/bin/buildctl /usr/local/bin/buildctl
USER 1000:1000
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/forgelet-executor"]

# Keep tools independent of changing Go binaries so source edits reuse the
# distribution/toolchain layers in both native and act images.
FROM executor-base AS executor
COPY --from=build /out/forgelet-executor /usr/local/bin/forgelet-executor

# Testcontainers uses the shipped executor environment and both actual binaries.
FROM executor AS integration
COPY --from=build /out/forgelet /usr/local/bin/forgelet
COPY test/fixtures/actions /opt/forgelet/test-actions

# Explicit opt-in profile for trusted Actions jobs. The engine is a Pod-owned
# native sidecar, never a permanently running service or a host socket.
FROM docker:28.5.1-cli AS docker-cli
FROM executor-base AS executor-act
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
COPY docs/licenses /usr/share/forgelet/licenses
USER root
RUN apt-get update && apt-get install -y --no-install-recommends curl zstd gcc make && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y --no-install-recommends g++ gnupg && rm -rf /var/lib/apt/lists/*
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
# The root job shares a workspace created by the unprivileged supervisor.
RUN git config --system --add safe.directory /workspace/repository
COPY --from=build /out/forgelet-executor /usr/local/bin/forgelet-executor
COPY --from=build /out/forgelet-act /usr/local/bin/forgelet-act
COPY --from=build /out/forgelet-node /usr/local/bin/node
USER 1000:1000

FROM rancher/mirrored-library-busybox:1.37.0 AS integration-busybox
FROM executor-act AS integration-act
COPY --from=build /out/forgelet /usr/local/bin/forgelet
COPY test/fixtures/actions /opt/forgelet/test-actions

USER root
COPY --from=integration-busybox / /opt/forgelet/test-rootfs/
RUN chmod -R a+rX /opt/forgelet/test-rootfs
USER 1000:1000

# Optional trusted web profile. Application caches remain in Forgelet, while
# immutable tool downloads are part of the execution image.
FROM node:24.14.0-bookworm-slim AS node24-toolchain
FROM executor-act AS executor-act-web
USER root
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/forgelet/browsers
COPY --from=node24-toolchain /usr/local /opt/hostedtoolcache/node/24.14.0/toolchain
RUN arch="$(/opt/forgelet/node24/bin/node -p process.arch)" && \
    mv /opt/hostedtoolcache/node/24.14.0/toolchain "/opt/hostedtoolcache/node/24.14.0/$arch" && \
    touch "/opt/hostedtoolcache/node/24.14.0/$arch.complete"
ARG PLAYWRIGHT_DOWNLOAD_HOST
ARG DEBIAN_BUILD_MIRROR
RUN cp /etc/apt/sources.list.d/debian.sources /tmp/forgelet-debian.sources && \
    if [ -n "$DEBIAN_BUILD_MIRROR" ]; then sed -i -E "s|https?://[^ /]+|$DEBIAN_BUILD_MIRROR|g" /etc/apt/sources.list.d/debian.sources; fi && \
    PLAYWRIGHT_DOWNLOAD_HOST="$PLAYWRIGHT_DOWNLOAD_HOST" npm exec --yes --package=playwright-core@1.63.0 -- playwright-core install --with-deps chromium && \
    chmod -R a+rX /opt/forgelet/browsers && \
    mv /tmp/forgelet-debian.sources /etc/apt/sources.list.d/debian.sources && \
    rm -rf /root/.npm /var/lib/apt/lists/*
USER 1000:1000
