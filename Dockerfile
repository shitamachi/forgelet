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
RUN apt-get update && apt-get install -y --no-install-recommends g++ gnupg ripgrep && rm -rf /var/lib/apt/lists/*
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
FROM alpine:3.22 AS go-toolcache
RUN apk add --no-cache curl ca-certificates
ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) arch=x64; sha0=aac1b08a0fb0c4e0a7c1555beb7b59180b05dfc5a3d62e40e9de90cd42f88235; sha4=1153d3d50e0ac764b447adfe05c2bcf08e889d42a02e0fe0259bd47f6733ad7f;; \
      arm64) arch=arm64; sha0=bd03b743eb6eb4193ea3c3fd3956546bf0e3ca5b7076c8226334afe6b75704cd; sha4=ef758ae7c6cf9267c9c0ef080b8965f453d89ab2d25d9eb22de4405925238768;; \
      *) exit 1;; esac && \
    for patch in 0 4; do \
      if [ "$patch" = 0 ]; then checksum="$sha0"; else checksum="$sha4"; fi; \
      curl -fL --retry 3 --max-time 600 "https://go.dev/dl/go1.26.$patch.linux-$TARGETARCH.tar.gz" -o /tmp/go.tar.gz && \
      echo "$checksum  /tmp/go.tar.gz" | sha256sum -c - && \
      mkdir -p "/opt/hostedtoolcache/go/1.26.$patch/$arch" && \
      tar -xzf /tmp/go.tar.gz --strip-components=1 -C "/opt/hostedtoolcache/go/1.26.$patch/$arch" && \
      touch "/opt/hostedtoolcache/go/1.26.$patch/$arch.complete" || exit 1; \
    done && rm /tmp/go.tar.gz
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
COPY --from=go-toolcache /opt/hostedtoolcache/go /opt/hostedtoolcache/go
USER 1000:1000

# Opt-in Rust/PowerShell profile; no extra daemon is started while idle.
FROM rust:1.97.0-slim-bookworm@sha256:6d220bf85c74e842a79da63997af8d2e74455c0b8847d8bb3a5888572334991d AS rust197
RUN rustup component add rustfmt clippy
ARG CARGO_REGISTRY_MIRROR
RUN if [ -n "$CARGO_REGISTRY_MIRROR" ]; then \
      case "$CARGO_REGISTRY_MIRROR" in *[[:space:]]*) exit 1;; esac; \
      printf '%s' "$CARGO_REGISTRY_MIRROR" | grep -Eq '^sparse\+https://[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._~/-]+)?/$' && \
      printf '[source.crates-io]\nreplace-with = "forgelet-mirror"\n[source.forgelet-mirror]\nregistry = "%s"\n' \
        "$CARGO_REGISTRY_MIRROR" > "$CARGO_HOME/config.toml" && \
      chown 1000:1000 "$CARGO_HOME/config.toml"; \
    fi
FROM rust197 AS executor-rust-build
ENV CARGO_BUILD_JOBS=2
FROM ubuntu:24.04 AS executor-act-rust-tools
COPY --from=golang:1.27.3-alpine /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ARG UBUNTU_MIRROR
RUN if [ -n "$UBUNTU_MIRROR" ]; then \
      sed -i -E "s|https?://[^ /]+|$UBUNTU_MIRROR|g" /etc/apt/sources.list.d/ubuntu.sources; \
    else sed -i 's|http://|https://|g' /etc/apt/sources.list.d/ubuntu.sources; fi && \
    apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates bash git tar xz-utils unzip curl zstd gcc g++ make \
      pkg-config libssl-dev python3 ripgrep libicu74 libunwind8 libuuid1 \
      libgssapi-krb5-2 zlib1g && rm -rf /var/lib/apt/lists/*
ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) psarch=x64; pssha=1da6d6d6035d651f20e70ddee7c4c15ac6ab8b9fb40ee92240d91263f28a884b;; \
      arm64) psarch=arm64; pssha=02eb9991af447c7e7fe881acde195961afd397c40b8eb1c2b3856b84ec2d85f2;; \
      *) exit 1;; esac && \
    curl -fL --retry 3 "https://github.com/PowerShell/PowerShell/releases/download/v7.6.9/powershell-7.6.9-linux-$psarch.tar.gz" -o /tmp/pwsh.tar.gz && \
    echo "$pssha  /tmp/pwsh.tar.gz" | sha256sum -c - && \
    mkdir -p /opt/powershell && tar -xzf /tmp/pwsh.tar.gz -C /opt/powershell && \
    chmod +x /opt/powershell/pwsh && ln -s /opt/powershell/pwsh /usr/local/bin/pwsh && rm /tmp/pwsh.tar.gz
COPY --from=rust197 /usr/local/rustup /opt/forgelet/rustup
COPY --from=rust197 /usr/local/cargo /opt/forgelet/cargo
ENV RUSTUP_HOME=/opt/forgelet/rustup CARGO_HOME=/opt/forgelet/cargo
ENV PATH=/opt/forgelet/cargo/bin:$PATH
RUN chown -R 1000:1000 /opt/forgelet/rustup /opt/forgelet/cargo && \
    git config --system --add safe.directory /workspace/repository

FROM executor-act-rust-tools AS executor-act-rust
COPY --from=node20 /usr/local/bin/node /opt/forgelet/node20/bin/node
COPY --from=node24-toolchain /usr/local/bin/node /opt/forgelet/node24/bin/node
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
COPY docs/licenses /usr/share/forgelet/licenses
COPY --from=build /out/forgelet-executor /usr/local/bin/forgelet-executor
COPY --from=build /out/forgelet-act /usr/local/bin/forgelet-act
COPY --from=build /out/forgelet-node /usr/local/bin/node
USER 1000:1000
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/forgelet-executor"]
