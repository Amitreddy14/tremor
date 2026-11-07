# ADR 0008: Controller design for safe, idempotent injection

Status: accepted

## Context
A controller can crash or hit a write conflict between choosing targets and acting on them. A naive loop could then pick and kill a second set of pods.

## Decision
1. Decisions are made by a pure function (`internal/engine.Decide`) with no Kubernetes dependency, so every transition is unit tested.
2. Targets are chosen once, in the `BaselineCheck` to `Injecting` step, and persisted in `status.affectedPods` (name plus UID) before anything is injected. Injection then acts only on that stored list, so retries are idempotent.
3. PodKill deletes with a UID precondition. A pod that is gone or has been replaced under the same name counts as already handled.
4. Recovery requires all affected UIDs to be gone, no terminating pods, every pod Ready and at least the baseline Ready count. This avoids false instant recovery caused by a stale cache.
5. The controller watches pods, so recovery is detected on the readiness event; a 1 s requeue is the backstop. Injection time uses microsecond timestamps.
6. A finalizer calls `Revert` before deletion so later faults that need undoing (Phases 5 and 6) share the same path.

## Consequences
Phase 1 measures pod-level recovery only; user-visible recovery through HTTP probes is Phase 2. Resolution is bounded by watch latency (typically well under a second) and the 1 s poll.
