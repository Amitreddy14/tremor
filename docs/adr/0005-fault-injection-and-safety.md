# ADR 0005: Fault injection mechanisms and safety rules

Status: accepted

## Decision
Mechanisms: PodKill via API delete; DependencyOutage via scale to zero then restore; CPU, memory and network faults via ephemeral containers (Kubernetes 1.25+, `NET_ADMIN` only for netem); NodeDrain via cordon plus eviction API.

Safety rules, enforced by the controller:

1. Experiments only run in namespaces on an operator-level allowlist.
2. `maxAffectedPercent` caps how many matching pods a fault may touch; at least one pod is always spared unless explicitly overridden.
3. `dryRun: true` resolves targets and reports what would happen without injecting.
4. An abort annotation stops a run and reverts immediately.
5. Every injection is reverted through a finalizer; injected helpers carry a TTL so they clean themselves up if the operator dies.
6. Baseline must be healthy before any fault is injected.

## Consequences
Safe by default, at the cost of some friction when testing single-replica workloads (needs an explicit override).
