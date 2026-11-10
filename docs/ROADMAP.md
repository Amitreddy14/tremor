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
| 6 | Network and node faults | NetworkLatency, NetworkLoss, NodeDrain | Delivered, awaiting merge |
| 7 | Safety and scheduling | Namespace allowlist, blast radius, dry-run, abort, `ChaosSchedule` | |
| 8 | Ship it | Helm chart, kind e2e in CI, free deployment guide, README with real measured results | |

## Free deployment plan

- CI: every PR runs unit tests; from Phase 8 a kind cluster runs end-to-end experiments on GitHub Actions (free for public repos).
- Public demo (when a cloud account exists): k3s on Oracle Cloud Always Free, MongoDB on Atlas M0 free tier, images on GitHub Container Registry.
- Until then: Helm chart and guide are delivered, real deployment is done later.
