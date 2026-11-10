# ADR 0011: Network and node faults

Status: accepted (Phase 6)

## Context
Pod, dependency and resource faults leave the network and the cluster's nodes alone. Two failures are common in practice and teach different things: a slow or lossy network (the app is up but users wait), and a node going away (many pods vanish at once and must land somewhere else).

## Decision

### NetworkLatency and NetworkLoss
Both are sustained faults (ADR 0010: `Holding` phase, `duration`, recovery measured from the release). They run in an **ephemeral container** in the target pod, so they share the pod's network namespace and touch nothing on the node. The container runs `tc qdisc add dev <iface> root netem delay ...` or `... loss ...`, where `<iface>` is the interface of the default route (falling back to `eth0`), then watches the rule, and removes it when the duration is over or when it is terminated.

- **Own image.** BusyBox's `tc` has no netem support, so the fault uses `tremor-netem` (Alpine plus `iproute2`, `Dockerfile.netem`, `--netem-image`).
- **Least privilege.** The container runs as root, because `NET_ADMIN` is useless to a non-root user in a container, with every capability dropped except `NET_ADMIN`, no privilege escalation and the runtime default seccomp profile.
- **Egress only.** netem shapes what the pod *sends*: responses to callers and its own outgoing calls. That is what users and dependencies feel. Traffic to the pod is not delayed, and traffic on the loopback is untouched.
- **Failing loudly.** If `tc` cannot add the rule (most often the node kernel lacks `sch_netem`), the container exits with code 42 and the experiment fails with a message naming the module, rather than reporting a successful run in which nothing was degraded.
- **Early end.** An ephemeral container cannot be stopped, but the rule can be removed: on abort, failure or delete, Tremor adds a second, tiny cleanup container that deletes the qdisc. The first container notices within a second and exits. When the fault ended by itself, no cleanup container is added.
- **Readiness is affected too.** The kubelet's readiness probe talks to the pod over the same interface. A latency larger than the probe timeout (1 s in the demo) makes the pod unready, which is a realistic outcome and part of what the experiment measures.
- Targets are chosen with the PodKill safety rules (default at most 50 %, one pod always spared). `count` applies; `latency` defaults to 200 ms (1 ms to 10 s), `jitter` to none (at most the latency), `lossPercent` to 20 (1 to 100).

### NodeDrain
Drain means: cordon the node, evict the target pods on it. Tremor does what `kubectl drain` does for the workload under test, and only that: it does not touch other workloads' pods.

- **Node choice.** The controller counts the target workload's pods per node and `engine.PickDrainNode` picks the node with the most pods whose drain stays within the safety limits (the same limits as for killing that many pods: `maxAffectedPercent`, always spare one pod unless `allowTotalOutage`). If no node qualifies, for example all replicas on one node, the experiment fails before changing anything. The choice is made once and stored in `status.node` and `status.affectedPods`.
- **Cordon until the run ends.** Evicted pods are recreated by their controller; if the node stayed schedulable they might come right back. The node is therefore cordoned for the whole run and uncordoned when it ends, **whatever the outcome**: completed, failed, aborted or deleted. The node's original `unschedulable` value is stored in an annotation (`tremor.dev/was-unschedulable`) next to the experiment's UID (`tremor.dev/held-by`), written in the same patch, so a node that was already cordoned stays cordoned and a second experiment cannot take over a held node.
- **Recovery starts at the eviction**, not at the end of the run: the clock is not reset by the uncordon (`revertedAt` is recorded for the node drain but does not move the clock, and is not stored in run history as a release).
- **Eviction API, not delete.** Evictions honour PodDisruptionBudgets. If one refuses (HTTP 429), the run fails with `eviction blocked` (each eviction is bounded to 5 s, because client-go would otherwise keep retrying a 429 for minutes and stall the controller), the node is uncordoned, and that is a result: the workload's own budget forbids this maintenance. Pods are evicted with a zero grace period, like PodKill, so the measured recovery is not hidden behind a termination delay.
- Nodes are read straight from the API server (not cached) and need only `get` and `patch`; pods need `create` on `pods/eviction`.

## Consequences and limits
- Network faults need `NET_ADMIN` in the target namespace (the `restricted` Pod Security level forbids it), an image the nodes can pull, and the `sch_netem` module. GitHub Actions runners may lack the module, so the CI network check is informational (`continue-on-error`) until it is known to be stable; the pure parts (scripts with a fake `tc`, validation, node choice) and the controller logic (envtest) are tested on every run.
- netem rules persist in the pod's network namespace until removed; if a pod's cleanup container cannot start (for example the image cannot be pulled), the rule stays until the pod is replaced.
- NodeDrain on the single-worker CI cluster cannot run; the e2e script skips it there and runs it on the two-worker cluster (`make kind-up`).
- Cluster-level rights (`nodes`) are new in this phase. Phase 7 adds allowlists to bound where Tremor may act.
