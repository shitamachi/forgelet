# syntax=docker/dockerfile:1
# Opt-in amd64 compatibility tools, independent of the controller/executor build.
# RUNNER_IMAGE must be an existing Go1.26.4-capable, Debian bookworm tools image.
ARG RUNNER_IMAGE=forgelet-executor-act-web:dev
ARG RUNNER_PLATFORM=linux/amd64
FROM --platform=$BUILDPLATFORM node:24.14.0-bookworm-slim@sha256:d8e448a56fc63242f70026718378bd4b00f8c82e78d20eefb199224a4d8e33d8 AS duckdb-build
ARG TARGETARCH
RUN test "$TARGETARCH" = amd64
RUN apt-get update && apt-get install -y --no-install-recommends \
    g++-x86-64-linux-gnu cmake ninja-build python3 ca-certificates && \
    rm -rf /var/lib/apt/lists/*
ADD --checksum=sha256:e2b89ce6058186436cc076f2e94e3bf7f3742cd6f3748a4a585ad0635af94b8a \
    https://api.github.com/repos/duckdb/duckdb/tarball/d54cbbe3df17e8e5ce68312f483b4155836112aa /tmp/duckdb.tar.gz
RUN mkdir /src && tar -xzf /tmp/duckdb.tar.gz --strip-components=1 -C /src && rm /tmp/duckdb.tar.gz
COPY tools/duckdb/build.sh tools/duckdb/probe.c /duckdb-tools/
ARG BUILD_JOBS=2
RUN DUCKDB_SOURCE=/src DUCKDB_BUILD=/build DUCKDB_RESULT=/result sh /duckdb-tools/build.sh "$BUILD_JOBS"
FROM scratch AS duckdb-library
COPY --from=duckdb-build /result/ /
FROM --platform=$RUNNER_PLATFORM ${RUNNER_IMAGE} AS portable-tools
USER root
COPY --from=duckdb-build /result/duckdb-probe /opt/forgelet/duckdb/bin/duckdb-probe
COPY --from=duckdb-build /result/libduckdb.so /opt/forgelet/duckdb/lib/libduckdb.so
COPY --from=duckdb-build /result/provenance.json /opt/forgelet/duckdb/provenance.json
COPY --from=duckdb-build /result/LICENSE /usr/share/forgelet/licenses/duckdb-LICENSE
COPY --from=duckdb-build /result/licenses/ /usr/share/forgelet/licenses/duckdb-third-party/
# This explicitly changes Go's DuckDB linking policy for this tools image.
# An administrator must opt in; the default/native/Rust images are unchanged.
ENV GOFLAGS=-tags=duckdb_use_lib \
    CGO_LDFLAGS=-L/opt/forgelet/duckdb/lib
RUN echo /opt/forgelet/duckdb/lib > /etc/ld.so.conf.d/forgelet-duckdb.conf && chmod 0644 /opt/forgelet/duckdb/lib/libduckdb.so /opt/forgelet/duckdb/provenance.json && chmod 0555 /opt/forgelet/duckdb/bin/duckdb-probe && ldconfig
COPY tools/duckdb/warm-std.sh /opt/forgelet/duckdb/bin/warm-std.sh
RUN sh /opt/forgelet/duckdb/bin/warm-std.sh
USER 1000:1000
