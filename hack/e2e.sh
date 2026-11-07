#!/usr/bin/env bash
# End-to-end check: deploy a victim workload, kill a pod, expect Completed.
set -euo pipefail

on_error() {
  echo "--- e2e failed, dumping state ---"
  kubectl -n demo get chaosexperiment -o yaml || true
  kubectl -n demo get pods -o wide || true
  kubectl -n tremor-system logs deploy/tremor-manager --tail=100 || true
}
trap on_error ERR

kubectl apply -f config/samples/victim.yaml
kubectl -n demo rollout status deploy/victim --timeout=180s

kubectl apply -f config/samples/pod-kill.yaml
kubectl -n demo wait --for=jsonpath='{.status.phase}'=Completed chaosexperiment/kill-victim --timeout=180s

kubectl -n demo get chaosexperiment kill-victim
ms=$(kubectl -n demo get chaosexperiment kill-victim -o jsonpath='{.status.podRecoveryMillis}')
echo "pod recovery: ${ms} ms"
test -n "${ms}"
