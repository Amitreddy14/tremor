# ADR 0004: MongoDB as run history, resource status as source of truth

Status: accepted

## Context
Custom resource status is fine for the current run but is not a good long-term history store, and the stack calls for MongoDB.

## Decision
Finished runs are written to a `tremor.runs` collection (status, spec snapshot, operator version). The operator never depends on MongoDB to run an experiment: write failures are logged, retried with backoff, and surfaced as a metric. `tremorctl` reads history from MongoDB.

## Consequences
Experiments work without a database; history and reports need one. The demo app also uses MongoDB, but through a separate database and credentials so the dependency-outage experiment is meaningful.

## Implementation (Phase 4)

- One document per run in `tremor.runs`, `_id` = the experiment's UID. Saving is an upsert, so repeating it is harmless.
- `status.runRecorded` is set only after a successful save. If the operator crashes in between, the run is saved again and replaced, never duplicated. Experiments that finished before history was enabled are recorded the first time they are reconciled.
- A failed save is logged, counted in `tremor_run_store_writes_total{result="error"}` and retried every 15 seconds (a fixed interval rather than exponential backoff, because the only cost is one small write attempt).
- The history database is separate from the demo's MongoDB, so a dependency-outage experiment against the demo cannot touch it.
- A run deleted before it was recorded is not recorded. The finalizer does not wait for the store, because deletion must never hang on a database outage.
- Selector keys such as `app.kubernetes.io/name` contain dots, which MongoDB 5.0 and later accept in field names (the manifests use MongoDB 7).
- `tremorctl` talks to MongoDB directly (`--mongo-uri` or `TREMOR_MONGO_URI`); there is no API server in between. Percentiles use the nearest-rank method and cover completed runs only.
