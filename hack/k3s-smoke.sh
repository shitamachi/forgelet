#!/usr/bin/env bash
# Real-cluster M0 smoke on k3s-in-Docker (spec 0011 T8).
#
# Stands up a single-node k3s, imports locally built forgelet images and
# drives one full run through the production loop:
#
#   signed push webhook → ingest → compile → durable queue → dispatcher
#   → JobRun CR → executor pod (projected token) → internal API →
#   status projection → WorkflowRun succeeded.
#
# Requirements: docker, kubectl, make images (built first). Optional env:
#   K3S_IMAGE  (default rancher/k3s:v1.34.4-k3s1)
#   KEEP=1     leave the cluster running after the smoke
set -euo pipefail

CLUSTER=forgelet-k3s
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.34.4-k3s1}"
REGISTRY=ghcr.io/shitamachi/forgelet
TAG=dev
JOBS_NS=forgelet-jobs
SYS_NS=forgelet-system
WORKDIR="$(mktemp -d)"
PORT=18080

cleanup() {
  if [[ "${KEEP:-0}" != "1" ]]; then
    docker rm -f "$CLUSTER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl is required" >&2; exit 1; }
for img in server controller executor minttoken; do
  docker image inspect "$REGISTRY/$img:$TAG" >/dev/null 2>&1 || {
    echo "image $REGISTRY/$img:$TAG missing — run 'make image-$img' first" >&2
    exit 1
  }
done

echo "== starting k3s ($K3S_IMAGE)"
docker rm -f "$CLUSTER" >/dev/null 2>&1 || true
docker run -d --privileged --name "$CLUSTER" \
  -p 6443:6443 -p "$PORT":80 \
  -e K3S_KUBECONFIG_OUTPUT=/output/kubeconfig.yaml \
  -e K3S_KUBECONFIG_MODE=666 \
  -v "$WORKDIR":/output \
  "$K3S_IMAGE" server --disable traefik --disable servicelb >/dev/null

echo "== waiting for the admin kubeconfig"
for _ in $(seq 1 60); do
  [[ -s "$WORKDIR/kubeconfig.yaml" ]] && break
  sleep 2
done
[[ -s "$WORKDIR/kubeconfig.yaml" ]] || { echo "kubeconfig never appeared" >&2; exit 1; }
sed "s|127.0.0.1|localhost|" "$WORKDIR/kubeconfig.yaml" > "$WORKDIR/kubeconfig"
export KUBECONFIG="$WORKDIR/kubeconfig"
kubectl get --raw /readyz >/dev/null 2>&1 || {
  for _ in $(seq 1 60); do
    kubectl get --raw /readyz >/dev/null 2>&1 && break
    sleep 2
  done
}
kubectl get --raw /readyz >/dev/null || { echo "k3s API never became ready" >&2; exit 1; }

echo "== importing forgelet images"
docker save "$REGISTRY/server:$TAG" "$REGISTRY/controller:$TAG" \
  "$REGISTRY/executor:$TAG" "$REGISTRY/minttoken:$TAG" \
  | docker exec -i "$CLUSTER" ctr -n k8s.io images import -

echo "== installing CRDs and control plane"
kubectl apply --server-side -f deploy/crds
kubectl apply -f deploy/manifests/00-namespace.yaml

TOKEN_KEY="$(openssl rand -hex 32)"
WEBHOOK_SECRET="$(openssl rand -hex 16)"
CONTROLLER_TOKEN="$(
  docker run --rm "$REGISTRY/minttoken:$TAG" -key "$TOKEN_KEY" -scopes observed:write -ttl 55m |
  tail -1)"
kubectl -n "$SYS_NS" create secret generic forgelet-server \
  --from-literal="TOKEN_KEY=$TOKEN_KEY" \
  --from-literal="WEBHOOK_SECRET=$WEBHOOK_SECRET" \
  --from-literal="CONTROL_PLANE_TOKEN=$CONTROLLER_TOKEN"

cat <<'YAML' | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: forgelet-workflows
  namespace: forgelet-system
data:
  ci.yml: |
    name: Smoke
    on:
      push:
        branches:
          - main
    jobs:
      smoke:
        runs-on: k3s-small
        steps:
          - name: hello
            run: echo smoke-ok > hello.txt && test "$(cat hello.txt)" = smoke-ok
          - name: outputs
            run: echo color=green >> $GITHUB_OUTPUT
          - name: gated
            if: steps.outputs.outputs.color == 'green'
            run: test -n "$GITHUB_SHA"
YAML

# Dependencies exist before the deployments reference them.
kubectl apply -f deploy/manifests

cat <<EOF | kubectl apply -f -
apiVersion: ci.forgelet.dev/v1alpha1
kind: RunnerClass
metadata:
  name: k3s-small
  namespace: $JOBS_NS
spec:
  image: $REGISTRY/executor:$TAG
EOF

echo "== waiting for control plane rollouts"
kubectl -n kube-system rollout status deploy/coredns --timeout=120s
kubectl -n "$SYS_NS" rollout status deploy/forgelet-server --timeout=180s
kubectl -n "$SYS_NS" rollout status deploy/forgelet-controller --timeout=180s

echo "== driving a signed push webhook"
# traefik/servicelb are disabled; expose the API with a port-forward.
kubectl -n "$SYS_NS" port-forward svc/forgelet-server "$PORT":80 \
  >"$WORKDIR/port-forward.log" 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true; cleanup' EXIT
ready=0
for _ in $(seq 1 15); do
  if curl -sf "http://localhost:$PORT/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [[ "$ready" != "1" ]]; then
  echo "port-forward failed:" >&2
  cat "$WORKDIR/port-forward.log" >&2 || true
  exit 1
fi
BODY='{"ref":"refs/heads/main","after":"5m0k35h425h425h425h425h425h425h425h425ha","repository":{"name":"forgelet","owner":{"login":"smoke"}},"pusher":{"name":"smoke"}}'
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" -hex | sed 's/^.* //')"
curl -sf -X POST "http://localhost:$PORT/webhooks/github" \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: push" \
  -H "X-GitHub-Delivery: smoke-$(date +%s)" \
  -H "X-Hub-Signature-256: $SIG" \
  -d "$BODY" | tee "$WORKDIR/ingest.json"
grep -q '"created":true' "$WORKDIR/ingest.json" || { echo "webhook did not create a run" >&2; exit 1; }

echo "== polling for the executor pod to succeed"
deadline=$((SECONDS + 240))
while true; do
  # The full loop: dispatch → JobRun CR → executor pod → internal API →
  # status projection → check report. Pod phase Succeeded proves the job
  # ran; the server's check log proves the durable state converged.
  phase="$(kubectl -n "$JOBS_NS" get pods -l ci.forgelet.dev/app=executor \
    -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' 2>/dev/null || true)"
  if echo "$phase" | grep -q '^Succeeded$'; then
    break
  fi
  if echo "$phase" | grep -q '^Failed$\|^Error$\|^CrashLoopBackOff$'; then
    echo "an executor pod failed:" >&2
    kubectl -n "$JOBS_NS" get pods -o wide >&2 || true
    kubectl -n "$JOBS_NS" logs -l ci.forgelet.dev/app=executor --tail=50 >&2 || true
    exit 1
  fi
  if (( SECONDS > deadline )); then
    echo "timed out waiting; state dump:" >&2
    kubectl -n "$JOBS_NS" get pods,jobruns -o wide >&2 || true
    kubectl -n "$JOBS_NS" describe pod -l ci.forgelet.dev/app=executor >&2 || true
    kubectl -n "$SYS_NS" logs deploy/forgelet-server --tail=50 >&2 || true
    kubectl -n "$SYS_NS" logs deploy/forgelet-controller --tail=50 >&2 || true
    exit 1
  fi
  sleep 5
done

echo "== verifying durable projection via server checks"
server_logs="$(kubectl -n "$SYS_NS" logs deploy/forgelet-server)"
echo "$server_logs" | grep -q '"conclusion":"success"' || {
  echo "no successful check reported by the control plane" >&2
  echo "$server_logs" | tail -30 >&2
  exit 1
}

echo "SMOKE OK"
