# ADR 0004: MongoDB as run history, resource status as source of truth

Status: accepted

## Context
Custom resource status is fine for the current run but is not a good long-term history store, and the stack calls for MongoDB.

## Decision
Finished runs are written to a `tremor.runs` collection (status, spec snapshot, operator version). The operator never depends on MongoDB to run an experiment: write failures are logged, retried with backoff, and surfaced as a metric. `tremorctl` reads history from MongoDB.

## Consequences
Experiments work without a database; history and reports need one. The demo app also uses MongoDB, but through a separate database and credentials so the dependency-outage experiment is meaningful.
