#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

if [[ ! -x "./bin/kustomize" ]]; then
  echo "missing ./bin/kustomize; run 'make kustomize' first" >&2
  exit 1
fi

./bin/kustomize build config/samples >/dev/null

if ! rg -q "<PLACEHOLDER_KUBECONFIG>" config/samples/bf-destination-kubeconfig-secret.yaml; then
  echo "expected placeholder kubeconfig in sample secret; do not commit real kubeconfig" >&2
  exit 1
fi

echo "preflight ok"
