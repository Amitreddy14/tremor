# ADR 0002: One `ChaosExperiment` CRD with a typed fault union

Status: accepted

## Context
Seven fault types share the same lifecycle (baseline, inject, observe, recover) but differ in parameters.

## Decision
A single `ChaosExperiment` kind with `spec.fault.type` as discriminator and per-type `params`. Each type implements a Go `Injector` interface (`Validate`, `Inject`, `Revert`). A separate `ChaosSchedule` kind is added in Phase 7 for recurring runs.

## Consequences
One state machine and one status shape for every fault, which keeps metrics and reports uniform. Validation of `params` happens in the controller (and later a webhook) rather than purely in the CRD schema.
