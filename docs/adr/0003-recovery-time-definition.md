# ADR 0003: Definition of recovery time

Status: accepted (clarified in Phase 2)

## Context
"Recovery time" is ambiguous: a pod can be Ready while users still see errors, and a fault may be held for a while before being released.

## Decision
Report two numbers per run:

- `recoveryTimeSeconds` (headline, user-visible): time from fault injection until the steady-state HTTP probe has passed `successThreshold` consecutive times. For faults held for a duration, the clock starts at release if the probe was failing at release, and the run also records `availabilityDuringFault`.
- `podRecoverySeconds`: time until all selected target pods are Ready again.

Probes run every `steadyState.interval` from the operator. A run fails if recovery is not reached within `steadyState.timeout`.

## Consequences
Resolution is bounded by the probe interval (default 1 s), documented in results. Operators that cannot reach the target over HTTP fall back to pod readiness only and say so in the status.

## Clarification (Phase 2)

- Recovery is the moment the probe series ends in a final unbroken run of `successThreshold` passes (the threshold-th pass of that run). A service that comes back and fails again is not recovered, so flapping cannot under-report.
- If no probe failed after injection, `userRecoveryMillis` is `0` and `userImpacted` is false: users never noticed. The experiment still waits for the pods and a stable probe run before completing.
- `podRecoveryMillis` is recorded the first time all pods are Ready again, even if the probe is still catching up.
- Availability is `probeSuccesses / (probeSuccesses + probeFailures)` over the window from injection to recovery.
- Time resolution is one probe interval (1 s default).
