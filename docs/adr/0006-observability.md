# ADR 0006: Prometheus metrics and a provisioned Grafana dashboard

Status: accepted

## Decision
The operator exposes the metrics listed in DESIGN.md. A `ServiceMonitor` and a Grafana dashboard (JSON in a labelled ConfigMap, picked up by the kube-prometheus-stack sidecar) ship with the Helm chart. Recovery time is a histogram with buckets suited to seconds-to-minutes recovery.

## Consequences
Works out of the box with kube-prometheus-stack; users without the Prometheus Operator can scrape the metrics endpoint directly.

## Phase 3 details

- Metrics register on controller-runtime's registry, so they share the operator's `/metrics` endpoint (port 8080) and its Service `tremor-metrics`.
- Recovery time carries a `kind` label (`pod` or `user`) so one histogram serves both measurements.
- Per-experiment gauges (`tremor_last_*`) outlive a run so the dashboard can show the latest result; they are deleted when the experiment resource is deleted. The live `tremor_steady_state_ok` gauge is removed when a run ends.
- Counters change only after the status update is persisted, so retried reconciles never double count.
- The ServiceMonitor scrapes every 5 s so the steady-state gauge can show a short outage. Prometheus' default of 30 s would miss most of it.
- The dashboard JSON lives in `config/monitoring/dashboards/tremor.json` and is loaded through a ConfigMap labelled `grafana_dashboard=1`.
