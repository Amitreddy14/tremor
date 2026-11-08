#!/usr/bin/env bash
# End-to-end check: kill a shop pod and expect pod and user-visible recovery
# times. Run `make demo-deploy` first.
set -euo pipefail

on_error() {
  echo "--- e2e failed, dumping state ---"
  kubectl -n demo get chaosexperiment -o yaml || true
  kubectl -n demo get pods -o wide || true
  kubectl -n tremor-system logs deploy/tremor-manager --tail=100 || true
  kubectl -n demo logs deploy/shop --tail=50 || true
}
trap on_error ERR

kubectl -n demo rollout status deploy/mongo --timeout=180s
kubectl -n demo rollout status deploy/shop --timeout=180s

# Seed an order so the probed endpoint returns real data.
kubectl -n demo run seed --rm -i --restart=Never --image=curlimages/curl:8.10.1 --quiet -- \
  curl -sf -X POST -H 'Content-Type: application/json' \
  -d '{"item":"keyboard","quantity":1}' http://shop.demo.svc.cluster.local:8080/orders

kubectl apply -f config/samples/shop-pod-kill.yaml
kubectl -n demo wait --for=jsonpath='{.status.phase}'=Completed chaosexperiment/kill-shop --timeout=240s

kubectl -n demo get chaosexperiment kill-shop
get() { kubectl -n demo get chaosexperiment kill-shop -o jsonpath="{.status.$1}"; }
pod_ms=$(get podRecoveryMillis)
user_ms=$(get userRecoveryMillis)
echo "pod recovery:  ${pod_ms} ms"
echo "user recovery: ${user_ms} ms (impacted: $(get userImpacted))"
echo "probes: $(get probeSuccesses) ok, $(get probeFailures) failed"
test -n "${pod_ms}"
test -n "${user_ms}"
