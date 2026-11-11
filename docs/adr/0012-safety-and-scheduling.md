# ADR 0012: Safety guards, scheduling and exact release time

Status: accepted (Phase 7)

## Context

Through Phase 6 anyone who can create a `ChaosExperiment` can aim it at any namespace, make the operator request any URL, and start several experiments on the same pods. Experiments also had to be started by hand, and recovery after a network or stress fault read 0-11 ms because the release time was the moment the controller noticed the container had finished, about a second late.

## Decisions

1. **Guards are checked by the operator, not by admission.** A webhook adds certificates and a second moving part for a small project. The controller fails the experiment before the baseline check, with a message that names the rule. The rules live in `internal/engine` as pure functions with table tests.
2. **Namespace allowlist** (`--allowed-namespaces`, empty means all). The local overlay sets `demo`.
3. **Probe URL allowlist.** The probe is an outbound request from the operator pod, so a user with only experiment rights could reach cluster-internal or metadata endpoints. By default only `name.<ns>.svc[.cluster.local]` of the experiment's own namespace is allowed. `--allowed-probe-hosts` adds exact hosts or `*.suffix` (matched on a dot boundary). IP literals need an exact entry. The HTTP client no longer follows redirects, since the target of a redirect was never validated.
4. **Overlap blocking.** Two selectors overlap unless a shared key demands different values. An experiment waits in `BaselineCheck` while another overlapping one is `Injecting`, `Holding` or `Recovering`; two that are still waiting are ordered by creation time then name, so exactly one goes first. After `baselineTimeout` the waiting experiment fails with the name of the blocker. Only same-namespace selectors are compared; a node drain can still touch other workloads, which a PodDisruptionBudget covers.
5. **`--force-dry-run`** makes every experiment a dry run, for rehearsing on a cluster where nothing may be disturbed.
6. **`ChaosSchedule`** is a separate CRD (`cxs`) with its own reconciler. Cron is five fields in UTC (plus `@hourly`, `@daily`, ...), parsed by a small stdlib package. Day-of-month and day-of-week combine with OR when both are restricted, as in classic cron. The experiment is named `<schedule>-<unix run time>`, so a repeated attempt for the same run hits AlreadyExists. Every due run advances `lastScheduleTime`, created or skipped, so nothing is judged twice. `Forbid` (default) skips a run while one is active; runs older than `startingDeadlineSeconds` (default 300) are skipped, and only the latest missed run is considered, never a burst. Experiments are owned by the schedule (garbage collected with it) and pruned by history limits. The template's spec schema is a copy of the experiment's, so mistakes are caught when the schedule is applied.
7. **Exact release time.** Faults that run in an ephemeral container implement `fault.EndTimer`; the latest `finishedAt` of their containers becomes `status.revertedAt` when it lies between injection and now. Otherwise the current time is used, as before. Recovery of stress and network faults therefore reflects what users saw, not the controller's polling delay.
8. **Milder latency sample** (`shop-network-latency-mild`: 300 ms, 50 ms jitter). A probe crosses two or three egress legs, so it stays under the one-second probe timeout and shows a slowdown instead of readiness failures.

## Consequences

- Experiments created in a non-allowed namespace or with a rejected probe URL fail fast and inject nothing.
- Schedules need create and delete rights on experiments in the operator's role.
- Recovery times of network and stress faults can now be small but honest; anything under the probe interval is limited by probe resolution.
- Overlap detection is by label selector only.
