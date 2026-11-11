# Roadmap

Built in phases. Each phase is a branch, a PR, green CI, and a tag.

| Phase | Name | Deliverable | Status |
| --- | --- | --- | --- |
| 0 | Design and scaffold | Design doc, ADRs, Go skeleton, Dockerfile, Makefile, kind configs, CI | Done |
| 1 | Operator core | Kubebuilder/controller-runtime setup, `ChaosExperiment` CRD, state machine, finalizer, PodKill, envtest tests | Done |
| 2 | Demo app and recovery measurement | Go shop API + MongoDB manifests, HTTP steady-state prober, recovery-time calculation | Done |
| 3 | Observability | Prometheus metrics, ServiceMonitor, Grafana dashboard | Done |
| 4 | Result store and CLI | MongoDB run history, `tremorctl` history, show and report | Done |
| 5 | Dependency and resource faults | DependencyOutage, CPUStress, MemoryStress, `Holding` phase | Done |
| 6 | Network and node faults | NetworkLatency, NetworkLoss, NodeDrain | Done |
| 7 | Safety and scheduling | Namespace and probe-URL allowlists, overlap blocking, forced dry-run, `ChaosSchedule`, exact release time | Done |
| 8 | Ship it | Helm chart, GHCR release workflow, kind e2e in CI, free deployment guide, README with measured results | Delivered, awaiting merge |

## Free deployment plan

- CI: every PR runs unit tests, lints and renders the Helm chart, and runs end-to-end experiments on a kind cluster (free for public repos).
- Public demo (when a cloud account exists): k3s on Oracle Cloud Always Free, MongoDB on Atlas M0 free tier, images on GitHub Container Registry.
- Until then: Helm chart and guide are delivered, real deployment is done later.
