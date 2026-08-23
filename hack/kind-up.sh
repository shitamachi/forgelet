#!/usr/bin/env bash
# Creates a local dev cluster with kind and installs the forgelet M0
# manifests (CRDs, namespaces, RBAC, control plane).
#
# Experimental (spec 0011 T5): images must be built and loaded manually
# until the release pipeline (T8) exists.
set -euo pipefail

CLUSTER="${CLUSTER:-forgelet}"

command -v kind >/dev/null 2>&1 || { echo "kind is required (brew install kind)" >&2; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "kubectl is required" >&2; exit 1; }

if ! kind get clusters 2>/dev/null | grep -q "^${CLUSTER}$"; then
  kind create cluster --name "$CLUSTER"
else
  echo "cluster ${CLUSTER} already exists"
fi

kubectl config use-context "kind-${CLUSTER}"
kubectl apply --server-side -f deploy/crds
kubectl apply -f deploy/manifests

kubectl -n forgelet-system rollout status deploy/forgelet-server --timeout=120s || {
  echo "server not ready — did you build and load the images?" >&2
  exit 1
}
echo "forgelet M0 installed. Next: create a RunnerClass in forgelet-jobs."
