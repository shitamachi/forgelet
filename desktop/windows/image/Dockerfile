# Linux job environment for the Windows desktop runner.
#
# The Windows app starts one container per job attempt with wslc.exe and passes
# the attempt's configuration on stdin. Build the binary first:
#
#   make runner-linux-executor
#   wslc build -t forgelet-runner-wslc:dev desktop/windows/image
#
# Push the image to the registry the Windows device can pull from, pin its
# digest, and put the pinned reference in connection.json. Jobs run as a
# non-root user in /workspace; add the toolchains your repositories need.
FROM node:24-bookworm

ENV LANG=C.UTF-8 \
    GIT_TERMINAL_PROMPT=0

COPY forgelet-runner /usr/local/bin/forgelet-runner
RUN chmod 0755 /usr/local/bin/forgelet-runner \
    && mkdir -p /workspace \
    && chown node:node /workspace

USER node
WORKDIR /workspace
CMD ["/usr/local/bin/forgelet-runner", "--exec"]
