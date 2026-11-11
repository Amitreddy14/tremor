#!/usr/bin/env bash
# Runs each fault sample against the demo shop and prints a markdown table of
# the measured recovery times (median over RUNS runs). Run `make demo-deploy`
# and `make kind-deploy` first, and `make netem-load` for the network faults.
#
#   RUNS=3 bash hack/results.sh > results.md
set -euo pipefail

RUNS="${RUNS:-3}"
TIMEOUT="${TIMEOUT:-360}"
SAMPLES=(
  shop-pod-kill
  mongo-outage
  shop-cpu-stress
  shop-memory-stress
  shop-network-latency-mild
  shop-packet-loss
  shop-node-drain
)

kubectl -n demo rollout status deploy/shop --timeout=180s >&2

median() { sort -n | awk '{a[NR]=$1} END{ if (NR==0) {print "-"} else if (NR%2) {print a[(NR+1)/2]} else {print int((a[NR/2]+a[NR/2+1])/2)} }'; }

echo "| Fault (sample) | Runs | Pod recovery (median ms) | User recovery (median ms) | Runs with user impact | Probes failed (median) |"
echo "| --- | --- | --- | --- | --- | --- |"

for sample in "${SAMPLES[@]}"; do
  file="config/samples/${sample}.yaml"
  pod=(); user=(); fails=(); impacted=0; ok=0
  for _ in $(seq 1 "${RUNS}"); do
    kubectl delete -f "${file}" --ignore-not-found --wait=true >/dev/null 2>&1 || true
    obj=$(kubectl apply -f "${file}" -o name | head -1)
    phase=""
    end=$((SECONDS + TIMEOUT))
    while [ "${SECONDS}" -lt "${end}" ]; do
      phase=$(kubectl -n demo get "${obj}" -o jsonpath='{.status.phase}' 2>/dev/null || true)
      case "${phase}" in Completed | Failed | Aborted) break ;; esac
      sleep 3
    done
    if [ "${phase}" = "Completed" ]; then
      ok=$((ok + 1))
      get() { kubectl -n demo get "${obj}" -o jsonpath="{.status.$1}"; }
      pod+=("$(get podRecoveryMillis)")
      user+=("$(get userRecoveryMillis)")
      fails+=("$(get probeFailures)")
      [ "$(get userImpacted)" = "true" ] && impacted=$((impacted + 1))
    else
      echo "  ${sample}: run ended as '${phase:-timeout}'" >&2
    fi
    kubectl -n demo rollout status deploy/shop --timeout=180s >&2
    kubectl -n demo rollout status deploy/mongo --timeout=180s >&2
    sleep 5 # let the probe series settle before the next run
  done
  printf '| %s | %d/%d | %s | %s | %d | %s |\n' "${sample}" "${ok}" "${RUNS}" \
    "$(printf '%s\n' "${pod[@]:-}" | grep -E '^[0-9]+$' | median)" \
    "$(printf '%s\n' "${user[@]:-}" | grep -E '^[0-9]+$' | median)" \
    "${impacted}" \
    "$(printf '%s\n' "${fails[@]:-}" | grep -E '^[0-9]+$' | median)"
  kubectl delete -f "${file}" --ignore-not-found >/dev/null 2>&1 || true
done
