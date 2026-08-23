# Deployment: k3s support matrix and image pipeline (spec 0011 T8)

## Image publishing

One Dockerfile builds every binary; the target binary is a build arg:

```bash
make images                      # ghcr.io/shitamachi/forgelet/{server,controller,executor,minttoken}:dev
IMAGE_TAG=v1.2.3 make images     # release tagging
```

- `server` / `controller` / `minttoken`: alpine + ca-certificates.
- `executor`: additionally ships bash — it is PID 1 of every job pod and runs
  user `run:` scripts (0008). RunnerClass `spec.image` must point at an image
  containing the executor at `/ci/executor`.

Publishing is a plain `docker push` of the four tags from CI; the smoke below
is the pre-push gate.

## k3s support matrix

| Component   | Version under test                          |
|-------------|---------------------------------------------|
| k3s         | v1.34.x (`rancher/k3s:v1.34.4-k3s1`)        |
| Kubernetes  | ≥ 1.29 required for TokenReview extras with the `authentication.kubernetes.io/*` prefix; older clusters emit `authentication.k8s.io/*` which forgelet also accepts |
| CRDs        | ci.forgelet.dev/v1alpha1 (generated)        |
| Storage     | PostgreSQL via `--database-url` (in-memory store is dev-only) |

## Local end-to-end smoke

`hack/k3s-smoke.sh` stands up k3s-in-Docker and drives one full run through
the production loop:

```
signed push webhook → ingest → compile → durable queue → dispatcher
→ JobRun CR → executor pod (projected token, TokenReview) → internal API
→ status projection → check completed/success
```

```bash
make image-server image-controller image-executor image-minttoken
KEEP=1 ./hack/k3s-smoke.sh     # KEEP=1 preserves the cluster for debugging
```

Requirements: docker, kubectl, openssl. The script fails loudly on any stage;
`KEEP=1` plus the printed state dump is the debugging entry point.

## Identity wiring in-cluster

- Executor pods mount an audience-bound projected ServiceAccount token
  (`forgelet-control-plane`, 1h) with `automountServiceAccountToken: false`.
  The server verifies them through TokenReview and binds each verified pod to
  exactly one JobRun via the pod's `ci.forgelet.dev/jobrun-id` label.
- The controller authenticates to the server with an `observed:write` token
  minted offline (`minttoken`) — it may project phases for any JobRun but has
  no other authority.
