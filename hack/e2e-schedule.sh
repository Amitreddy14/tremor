#!/usr/bin/env bash
# End-to-end check of safety guards and scheduling. Run `make demo-deploy` and
# `make kind-deploy` first (the operator must run with --allowed-namespaces=demo).
set -euo pipefail

on_error() {
  echo "--- e2e-schedule failed, dumping state ---"
  kubectl -n demo get chaosschedule,chaosexperiment -o yaml || true
  kubectl -n tremor-system logs deploy/tremor-manager --tail=100 || true
}
trap on_error ERR

kubectl -n demo rollout status deploy/shop --timeout=180s

phase() { kubectl "$@" -o jsonpath='{.status.phase}' 2>/dev/null || true; }
wait_phase() { # kind name namespace want timeout
  local end=$((SECONDS + $5))
  while [ "${SECONDS}" -lt "${end}" ]; do
    [ "$(phase -n "$3" get "$1" "$2")" = "$4" ] && return 0
    sleep 2
  done
  echo "timed out waiting for $1/$2 to be $4"
  return 1
}

echo "--- guard: namespace outside the allowlist is refused ---"
kubectl apply -f - <<'YAML'
apiVersion: tremor.dev/v1alpha1
kind: ChaosExperiment
metadata:
  name: not-allowed
  namespace: default
spec:
  target:
    selector:
      app: anything
  fault:
    type: PodKill
YAML
wait_phase chaosexperiment not-allowed default Failed 60
kubectl -n default get chaosexperiment not-allowed -o jsonpath='{.status.message}{"\n"}' | grep -q 'not allowed'
kubectl -n default delete chaosexperiment not-allowed

echo "--- guard: probe url outside the allowlist is refused ---"
kubectl apply -f - <<'YAML'
apiVersion: tremor.dev/v1alpha1
kind: ChaosExperiment
metadata:
  name: bad-probe
  namespace: demo
spec:
  target:
    selector:
      app: shop
  fault:
    type: PodKill
  steadyState:
    http:
      url: http://169.254.169.254/latest/meta-data
YAML
wait_phase chaosexperiment bad-probe demo Failed 60
kubectl -n demo get chaosexperiment bad-probe -o jsonpath='{.status.message}{"\n"}' | grep -q 'probe host'
kubectl -n demo delete chaosexperiment bad-probe

echo "--- schedule: one shop pod kill every minute ---"
sed 's#\*/5 \* \* \* \*#* * * * *#' config/samples/shop-pod-kill-schedule.yaml | kubectl apply -f -
end=$((SECONDS + 150))
exp=""
while [ "${SECONDS}" -lt "${end}" ]; do
  exp=$(kubectl -n demo get chaosschedule shop-pod-kill-5m -o jsonpath='{.status.lastExperiment}' 2>/dev/null || true)
  [ -n "${exp}" ] && break
  sleep 3
done
test -n "${exp}"
echo "schedule created ${exp}"
wait_phase chaosexperiment "${exp}" demo Completed 240
kubectl -n demo get chaosexperiment "${exp}"
kubectl -n demo get chaosexperiment "${exp}" -o jsonpath='{.metadata.ownerReferences[0].kind}' | grep -q ChaosSchedule

echo "--- schedule: suspend, then delete; owned experiments are collected ---"
kubectl -n demo patch chaosschedule shop-pod-kill-5m --type merge -p '{"spec":{"suspend":true}}'
kubectl -n demo delete chaosschedule shop-pod-kill-5m
for _ in $(seq 1 30); do
  [ "$(kubectl -n demo get chaosexperiment -l tremor.dev/schedule=shop-pod-kill-5m --no-headers 2>/dev/null | wc -l | tr -d ' ')" = "0" ] && break
  sleep 2
done
test "$(kubectl -n demo get chaosexperiment -l tremor.dev/schedule=shop-pod-kill-5m --no-headers 2>/dev/null | wc -l | tr -d ' ')" = "0"
kubectl -n demo rollout status deploy/shop --timeout=120s
echo "e2e-schedule passed"
