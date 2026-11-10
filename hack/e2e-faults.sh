#!/usr/bin/env bash
# Network and node fault checks on the demo shop. Run first:
#   make demo-deploy kind-deploy netem-load
# Network faults need the sch_netem kernel module on the kind node's host.
# NodeDrain needs at least two worker nodes and is skipped otherwise.
set -euo pipefail

on_error() {
  echo "--- e2e-faults failed, dumping state ---"
  kubectl -n demo get chaosexperiment -o yaml || true
  kubectl -n demo get pods -o wide || true
  kubectl get nodes || true
  kubectl -n tremor-system logs deploy/tremor-manager --tail=100 || true
}
trap on_error ERR

getx() { kubectl -n demo get chaosexperiment "$1" -o jsonpath="{.status.$2}"; }

# Wait for Completed, and stop early with the message when the run fails.
wait_done() {
  local phase=""
  for _ in $(seq 1 150); do
    phase=$(kubectl -n demo get chaosexperiment "$1" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    case "${phase}" in
      Completed) return 0 ;;
      Failed | Aborted)
        echo "$1 ended ${phase}: $(getx "$1" message)"
        return 1
        ;;
    esac
    sleep 2
  done
  echo "$1 did not finish (phase: ${phase})"
  return 1
}

kubectl -n demo rollout status deploy/mongo --timeout=180s
kubectl -n demo rollout status deploy/shop --timeout=180s

for sample in shop-network-latency shop-packet-loss; do
  echo "--- ${sample}: 30s on one shop pod ---"
  kubectl apply -f "config/samples/${sample}.yaml"
  wait_done "${sample}"
  kubectl -n demo get chaosexperiment "${sample}"
  echo "pod recovery (from release):  $(getx "${sample}" podRecoveryMillis) ms"
  echo "user recovery (from release): $(getx "${sample}" userRecoveryMillis) ms (impacted: $(getx "${sample}" userImpacted))"
  echo "probes: $(getx "${sample}" probeSuccesses) ok, $(getx "${sample}" probeFailures) failed"
  test -n "$(getx "${sample}" revertedAt)"
  test -n "$(getx "${sample}" podRecoveryMillis)"
  # Network faults do not remove pods: all three shop pods are still there.
  test "$(kubectl -n demo get pods -l app=shop --no-headers | wc -l | tr -d ' ')" = "3"
  kubectl -n demo rollout status deploy/shop --timeout=120s
done

workers=$(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' --no-headers | wc -l | tr -d ' ')
if [ "${workers}" -ge 2 ]; then
  echo "--- shop-node-drain: ${workers} workers ---"
  kubectl apply -f config/samples/shop-node-drain.yaml
  wait_done shop-node-drain
  kubectl -n demo get chaosexperiment shop-node-drain
  echo "drained node: $(getx shop-node-drain node)"
  echo "pod recovery: $(getx shop-node-drain podRecoveryMillis) ms"
  echo "user recovery: $(getx shop-node-drain userRecoveryMillis) ms (impacted: $(getx shop-node-drain userImpacted))"
  test -n "$(getx shop-node-drain node)"
  test -n "$(getx shop-node-drain podRecoveryMillis)"
  # The drained node must be open again, markers removed.
  if kubectl get nodes -o jsonpath='{.items[*].spec.unschedulable}' | grep -q true; then
    echo "a node is still cordoned after the drain"
    exit 1
  fi
  test -z "$(kubectl get nodes -o jsonpath='{.items[*].metadata.annotations.tremor\.dev/held-by}')"
else
  echo "--- shop-node-drain skipped: needs two workers, found ${workers} (use make kind-up) ---"
fi
echo "network and node fault checks passed"
