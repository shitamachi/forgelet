# Development-only amd64 environment for homelab Actions experiments.
FROM docker:28.5.1-cli AS docker
FROM alpine:3.22 AS certificates
FROM node:20.20.0-bookworm-slim
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources && apt-get update && apt-get install -y --no-install-recommends git bash ca-certificates curl tar xz-utils unzip zstd && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y --no-install-recommends gcc make && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y --no-install-recommends g++ && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y --no-install-recommends gnupg && rm -rf /var/lib/apt/lists/*
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
COPY worker /usr/local/bin/act-kernel
COPY action-sources /opt/probe/action-sources
ENV GOPROXY=https://goproxy.cn,direct
