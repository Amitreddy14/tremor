# ADR 0009: Operator-side probing and the demo shop

Status: accepted

## Context
Pod readiness is not what users experience. Phase 2 needs a user-visible recovery time and an application worth breaking.

## Decision
1. The operator probes the experiment's HTTP endpoint itself, from a background goroutine per experiment (`internal/prober`), starting when the experiment starts. The baseline must pass the probe before any fault, and every probe between injection and recovery is captured at the configured interval.
2. Probe results live in memory, trimmed to about an hour. Final numbers (user recovery, impact flag, probe counts) are written to the resource status when the run completes. If the operator restarts mid-run, probe history restarts, so a run interrupted this way may under-report impact. This is accepted for now.
3. Each probe opens a fresh connection (no keep-alive) so it exercises Service routing the way a new user request would.
4. The demo shop (`cmd/shop`) uses MongoDB through a small `Store` interface with an in-memory implementation for tests. Readiness depends on the database; liveness does not, so an outage removes pods from the Service without restarting them.
5. One Dockerfile builds both binaries with `--build-arg TARGET`.

## Consequences
The probe URL is user-supplied and fetched from inside the cluster, which is a server-side request risk. Phase 7 adds an allowlist for namespaces and probe targets; until then only trusted users should be allowed to create experiments (RBAC on the CRD).
