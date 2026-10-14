#!/usr/bin/env bash
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

# Runs the Kubernetes executor's integration tests in a throwaway k3d
# cluster: builds opensnack and the runtime images, deploys the example
# manifests with deploy/k8s/lambda-it on top, and runs the tests tagged
# k8sintegration against it.
#
#   deploy/k8s/lambda-it/run.sh          create, test, delete the cluster
#   KEEP_CLUSTER=1 deploy/k8s/lambda-it/run.sh   leave it running after
#
# The cluster gets its own kubeconfig (never your default one), and every
# kubectl call names it.

set -euo pipefail

CLUSTER=${CLUSTER:-opensnack-lambda-it}
API_PORT=${API_PORT:-14566}
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
WORK=${WORK:-$(mktemp -d)}
KUBECONFIG_FILE="$WORK/kubeconfig"
export KUBECONFIG="$KUBECONFIG_FILE"
kc() { kubectl --kubeconfig "$KUBECONFIG_FILE" "$@"; }

cleanup() {
  status=$?
  if [ "$status" -ne 0 ] && [ -f "$KUBECONFIG_FILE" ]; then
    echo "--- opensnack logs"
    kc -n opensnack logs deploy/opensnack --tail=200 || true
    echo "--- sandbox pods"
    kc -n opensnack-lambda get pods -o wide || true
  fi
  if [ -z "${KEEP_CLUSTER:-}" ]; then
    k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  else
    echo "cluster $CLUSTER kept; kubeconfig: $KUBECONFIG_FILE"
  fi
  exit "$status"
}
trap cleanup EXIT

echo "--- building images"
docker build -q -t opensnack:it "$ROOT"
docker build -q -f "$ROOT/deploy/lambda-runtime/Dockerfile" --target python -t opensnack-lambda-python:it "$ROOT"
docker build -q -f "$ROOT/deploy/lambda-runtime/Dockerfile" --target nodejs -t opensnack-lambda-nodejs:it "$ROOT"

echo "--- creating cluster $CLUSTER"
if ! k3d cluster get "$CLUSTER" >/dev/null 2>&1; then
  k3d cluster create "$CLUSTER" \
    --kubeconfig-update-default=false --kubeconfig-switch-context=false \
    --no-lb --wait --timeout 180s \
    -p "$API_PORT:30566@server:0:direct" \
    --k3s-arg "--disable=traefik@server:0"
fi
k3d kubeconfig get "$CLUSTER" >"$KUBECONFIG_FILE"
chmod 600 "$KUBECONFIG_FILE"
k3d image import -c "$CLUSTER" opensnack:it opensnack-lambda-python:it opensnack-lambda-nodejs:it

echo "--- deploying"
kc apply -k "$ROOT/deploy/k8s/lambda-it"
kc -n opensnack rollout status deploy/postgres --timeout=180s
kc -n opensnack rollout restart deploy/opensnack
kc -n opensnack rollout status deploy/opensnack --timeout=180s
for _ in $(seq 60); do
  curl -fs "http://localhost:$API_PORT/_opensnack/health" >/dev/null && break
  sleep 1
done

echo "--- testing"
cd "$ROOT"
OPENSNACK_IT_ENDPOINT="http://localhost:$API_PORT" \
OPENSNACK_IT_KUBECONFIG="$KUBECONFIG_FILE" \
  go test -tags k8sintegration -count=1 -v -timeout 20m ./internal/api/lambda/k8sit/ "$@"
