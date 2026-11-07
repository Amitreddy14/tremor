# ADR 0003: Definition of recovery time

Status: accepted

## Context
"Recovery time" is ambiguous: a pod can be Ready while users still see errors, and a fault may be held for a while before being released.

## Decision
Report two numbers per run:

- `recoveryTimeSeconds` (headline, user-visible): time from fault injection until the steady-state HTTP probe has passed `successThreshold` consecutive times. For faults held for a duration, the clock starts at release if the probe was failing at release, and the run also records `availabilityDuringFault`.
- `podRecoverySeconds`: time until all selected target pods are Ready again.

Probes run every `steadyState.interval` from the operator. A run fails if recovery is not reached within `steadyState.timeout`.

## Consequences
Resolution is bounded by the probe interval (default 1 s), documented in results. Operators that cannot reach the target over HTTP fall back to pod readiness only and say so in the status.
