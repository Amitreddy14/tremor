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

# Metrics: the operator must expose Tremor metrics for the finished run.
kubectl -n tremor-system port-forward deploy/tremor-manager 18080:8080 >/dev/null 2>&1 &
pf_pid=$!
trap 'kill "${pf_pid}" 2>/dev/null || true' EXIT

metrics=""
for _ in $(seq 1 15); do
  if metrics=$(curl -sf http://127.0.0.1:18080/metrics); then break; fi
  sleep 1
done
test -n "${metrics}"

echo "--- tremor metrics ---"
echo "${metrics}" | grep -E '^tremor_' | grep -v '_bucket' || true

echo "${metrics}" | grep -E 'tremor_experiments_total\{[^}]*result="completed"[^}]*\} [1-9]'
echo "${metrics}" | grep -E 'tremor_recovery_seconds_count\{[^}]*kind="pod"[^}]*\} [1-9]'
echo "${metrics}" | grep -E 'tremor_recovery_seconds_count\{[^}]*kind="user"[^}]*\} [1-9]'
echo "${metrics}" | grep -E 'tremor_last_availability_ratio\{[^}]*\} '
echo "metrics check passed"

# History: the finished run must be saved to MongoDB and readable by tremorctl.
kubectl -n demo wait --for=jsonpath='{.status.runRecorded}'=true chaosexperiment/kill-shop --timeout=60s

kubectl -n tremor-system port-forward svc/tremor-history 27117:27017 >/dev/null 2>&1 &
hist_pid=$!
trap 'kill "${pf_pid}" "${hist_pid}" 2>/dev/null || true' EXIT
sleep 3

ctl() { go run ./cmd/tremorctl "$@" --mongo-uri mongodb://127.0.0.1:27117; }

echo "--- tremorctl history ---"
history_json=""
for _ in $(seq 1 10); do
  if history_json=$(ctl history --output json 2>/dev/null) && echo "${history_json}" | grep -q '"name": "kill-shop"'; then break; fi
  sleep 2
done
echo "${history_json}" | grep -q '"name": "kill-shop"'
echo "${history_json}" | grep -q '"result": "completed"'
ctl history
echo "--- tremorctl report ---"
ctl report --since all
ctl report --since all | grep -q 'PodKill'
echo "history check passed"
