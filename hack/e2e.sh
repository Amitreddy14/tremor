#!/usr/bin/env bash
# End-to-end check: kill a shop pod, take MongoDB down, stress the shop's CPU,
# and check recovery times, metrics and run history. Run `make demo-deploy` first.
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

# --- DependencyOutage: take the shop's MongoDB down and bring it back ---------
echo "--- DependencyOutage: MongoDB down for 30s ---"
kubectl apply -f config/samples/mongo-outage.yaml
kubectl -n demo wait --for=jsonpath='{.status.phase}'=Completed chaosexperiment/mongo-outage --timeout=300s

kubectl -n demo get chaosexperiment mongo-outage
getx() { kubectl -n demo get chaosexperiment "$1" -o jsonpath="{.status.$2}"; }
echo "pod recovery (from release):  $(getx mongo-outage podRecoveryMillis) ms"
echo "user recovery (from release): $(getx mongo-outage userRecoveryMillis) ms (impacted: $(getx mongo-outage userImpacted))"
echo "probes: $(getx mongo-outage probeSuccesses) ok, $(getx mongo-outage probeFailures) failed"

# Taking the database down must be visible to users (the shop goes unready).
test "$(getx mongo-outage userImpacted)" = "true"
test -n "$(getx mongo-outage revertedAt)"
test -n "$(getx mongo-outage podRecoveryMillis)"
# ... and the dependency must be fully restored, markers removed.
test "$(kubectl -n demo get deploy mongo -o jsonpath='{.spec.replicas}')" = "1"
test -z "$(kubectl -n demo get deploy mongo -o jsonpath='{.metadata.annotations.tremor\.dev/held-by}')"
kubectl -n demo rollout status deploy/mongo --timeout=120s
kubectl -n demo rollout status deploy/shop --timeout=120s

# --- CPUStress: burn CPU inside one shop pod ----------------------------------
echo "--- CPUStress: 30s on one shop pod ---"
kubectl apply -f config/samples/shop-cpu-stress.yaml
kubectl -n demo wait --for=jsonpath='{.status.phase}'=Completed chaosexperiment/shop-cpu-stress --timeout=300s

kubectl -n demo get chaosexperiment shop-cpu-stress
echo "pod recovery:  $(getx shop-cpu-stress podRecoveryMillis) ms"
echo "user recovery: $(getx shop-cpu-stress userRecoveryMillis) ms (impacted: $(getx shop-cpu-stress userImpacted))"
test -n "$(getx shop-cpu-stress revertedAt)"
test -n "$(getx shop-cpu-stress userRecoveryMillis)"
# Stress does not remove pods: all three shop pods are still there.
test "$(kubectl -n demo get pods -l app=shop --no-headers | wc -l | tr -d ' ')" = "3"

# --- History: every finished run must be saved and readable by tremorctl -------
for exp in kill-shop mongo-outage shop-cpu-stress; do
  kubectl -n demo wait --for=jsonpath='{.status.runRecorded}'=true "chaosexperiment/${exp}" --timeout=60s
done

kubectl -n tremor-system port-forward svc/tremor-history 27117:27017 >/dev/null 2>&1 &
hist_pid=$!
trap 'kill "${pf_pid}" "${hist_pid}" 2>/dev/null || true' EXIT
sleep 3

ctl() { go run ./cmd/tremorctl "$@" --mongo-uri mongodb://127.0.0.1:27117; }

echo "--- tremorctl history ---"
history_json=""
for _ in $(seq 1 10); do
  if history_json=$(ctl history --output json 2>/dev/null) \
    && echo "${history_json}" | grep -q '"name": "kill-shop"' \
    && echo "${history_json}" | grep -q '"name": "mongo-outage"' \
    && echo "${history_json}" | grep -q '"name": "shop-cpu-stress"'; then break; fi
  sleep 2
done
echo "${history_json}" | grep -q '"name": "kill-shop"'
echo "${history_json}" | grep -q '"name": "mongo-outage"'
echo "${history_json}" | grep -q '"name": "shop-cpu-stress"'
echo "${history_json}" | grep -q '"result": "completed"'
ctl history
echo "--- tremorctl show (outage) ---"
outage_id=$(echo "${history_json}" | python3 -c 'import json,sys; print(next(r["id"] for r in json.load(sys.stdin) if r["name"]=="mongo-outage"))')
shown=$(ctl show "${outage_id}")
echo "${shown}"
echo "${shown}" | grep -q 'from release'
echo "--- tremorctl report ---"
report=$(ctl report --since all)
echo "${report}"
for f in PodKill DependencyOutage CPUStress; do echo "${report}" | grep -q "${f}"; done
echo "history check passed"
