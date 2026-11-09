# ADR 0010: Sustained faults (dependency outage, CPU and memory stress)

Status: accepted (Phase 5)

## Context
PodKill is instantaneous: the fault is over the moment it is injected. A database outage or CPU stress lasts for a while and must be undone, and the recovery time that matters is how long the system takes to come back after the fault is gone.

## Decision

### A `Holding` phase
`Injecting -> Holding -> Recovering`. One-shot faults (PodKill) skip `Holding`. In `Holding` the fault is active until it has run its course (`fault.params.duration`, default 30 s, 1 s to 10 min); then the controller releases it (`ActionRevert`) and the recovery clock starts. The recovery timeout covers recovery only, never the hold. The state machine stays pure (`internal/engine`); the controller decides "has the fault run its course" and passes it in as `FaultDone`.

### Recovery is measured from the release
For sustained faults `podRecoveryMillis` and `userRecoveryMillis` count from `status.revertedAt`, not from injection (this amends ADR 0003). Otherwise a 45 s outage would show up as 45 s of "recovery" no matter how good the system is. Availability still covers the whole window from injection to recovery, so the outage itself is counted. `status.revertedAt` and the injection time give the fault duration; `tremorctl show` prints both.

### DependencyOutage
The target selector picks the dependency's pods, and Tremor resolves their owner (Deployment via its ReplicaSet, or StatefulSet) and scales it to zero. In the same update it stores the original replica count and the experiment's UID as annotations (`tremor.dev/original-replicas`, `tremor.dev/held-by`). That makes the restore independent of operator memory and status updates, makes inject and revert idempotent, and refuses a second experiment on a workload that is already held down. Because an outage is a total outage of the dependency, `safety.allowTotalOutage: true` is required. A selector that matches pods of more than one workload is rejected.

### CPUStress and MemoryStress
Stress runs in an **ephemeral container** added to the target pods, so it competes with the application inside the pod's own cgroup, which is what a noisy neighbour or a leak looks like to the app. The container runs a few lines of POSIX shell (`yes` loops, or a `dd` buffer held by a blocked pipe) on `busybox` (configurable with `--stress-image`), as user `nobody` with all capabilities dropped. Targets are chosen with the same safety rules as PodKill (default at most 50 %, always one pod spared).

Stress is "done" when its container has terminated, not after a timer, because image pulls delay the start. A container that has not started within two minutes fails the run: otherwise nothing would have been stressed and the result would be a meaningless success.

### Never leave a fault behind
A fault is released when the hold ends, when the run fails or is aborted while the fault is active, and when the experiment is deleted (finalizer). Deployments and StatefulSets are read straight from the API server rather than the cache, so a revert right after an inject cannot read a stale copy that lacks the hold marker.

## Consequences and limits
- An ephemeral container cannot be removed or stopped early. Aborting or deleting an experiment ends the *experiment* immediately, but a stress container keeps running until its duration is over (at most 10 minutes). The container also stays in the pod's spec until the pod is replaced.
- Ephemeral containers cannot set resource limits, so stress shares the pod's limits. Memory stress above the pod's memory limit triggers an OOM kill; which process dies is up to the kernel. That is the point of the experiment, but choose the size deliberately.
- Ephemeral containers need Kubernetes 1.25 or later, an image the node can pull, and a namespace whose Pod Security level admits a non-root, no-capability container.
- The operator needs `update` on `pods/ephemeralcontainers` and on `deployments` and `statefulsets` (see `config/rbac`).
- An autoscaler (HPA) or GitOps tool that fights the scale-down of an outage can bring the dependency back early; Tremor does not detect that. Pause them for the target workload.
- StatefulSet volumes survive an outage. With the demo's `emptyDir` MongoDB the data does not, which is fine for the demo.
