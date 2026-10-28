ARG RUNNER_IMAGE=forgelet-executor-act:dev
FROM ${RUNNER_IMAGE}
# Go Testcontainers shares one Ryuk across package processes. Keep the reaper
# available between serial packages; explicit fixture and Pod cleanup still run.
ENV RYUK_RECONNECTION_TIMEOUT=2m
