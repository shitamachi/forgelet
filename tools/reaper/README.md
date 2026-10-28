# Ryuk reconnection grace for the Ubuntu act tool image

This optional image recipe adds `RYUK_RECONNECTION_TIMEOUT=2m` to the existing
Ubuntu tool image. It changes image configuration only, adds no filesystem
layers and does not change Forgelet's native or Rust profiles. Pin the parent
image and deploy the resulting image by digest through `act.runner_image`.

Go Testcontainers v0.44.0 shares a Ryuk session between package test processes
under one `go test` parent. Its default ten-second reconnection grace can expire
between packages. A new process can then discover the exiting container and
spend a minute waiting for its port. The larger grace keeps the same reaper
available across short gaps. It is a mitigation for that lifecycle race, not a
replacement for investigating other Docker or service failures.

Ryuk stays enabled. Fixtures still explicitly terminate their containers, and
Forgelet still removes the entire Pod and its private Docker engine on completion
or cancellation. After an unexpectedly disconnected client, Ryuk's automatic
cleanup may now wait up to two minutes instead of ten seconds. Nothing runs
outside the job Pod or remains resident between jobs. Workflows can override
the image environment when they need a different policy.

```sh
docker build -f tools/reaper/Dockerfile \
  --build-arg RUNNER_IMAGE=your-registry/tools@sha256:parent-digest \
  -t your-registry/tools:reaper-grace .

FORGELET_TEST_REAPER_IMAGE=your-registry/tools:reaper-grace \
FORGELET_TEST_REAPER_ALPINE=your-zot-mirror/alpine:3.21 \
  go test -tags=integration ./test/integration \
  -run '^TestToolsReaperReconnectGrace$' -count=1 -v -timeout=5m
```

The opt-in regression reads the real image environment and runs actual
Testcontainers processes against the development Docker daemon. Two processes
share a parent and are separated by fifteen seconds. The default-policy control
must create a different Ryuk; the image policy must reuse the same Ryuk. The
fixture explicitly removes service containers and tracks exact reaper IDs for
cleanup. This isolated test does not reproduce every timing condition in the
original business workflow. That workflow must separately pass against the
on-demand Pod engine before production acceptance is recorded.
