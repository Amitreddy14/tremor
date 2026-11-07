# ADR 0006: Prometheus metrics and a provisioned Grafana dashboard

Status: accepted

## Decision
The operator exposes the metrics listed in DESIGN.md. A `ServiceMonitor` and a Grafana dashboard (JSON in a labelled ConfigMap, picked up by the kube-prometheus-stack sidecar) ship with the Helm chart. Recovery time is a histogram with buckets suited to seconds-to-minutes recovery.

## Consequences
Works out of the box with kube-prometheus-stack; users without the Prometheus Operator can scrape the metrics endpoint directly.
