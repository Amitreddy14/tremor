# ADR 0013: Helm chart, release images and result reporting

Status: accepted (Phase 8)

## Context

Through Phase 7 the operator was installed with kustomize overlays that assume locally built images. A cluster you do not own (Oracle k3s, a colleague's kind) needs published images, one install command and a way to point history at an external MongoDB.

## Decisions

1. **Helm chart in `deploy/helm/tremor`**, kustomize kept for development. Values cover images, allowlists, history mode (`none`, `builtin`, `external`), the ServiceMonitor and resources. External history takes either a URI or an existing Secret; the operator already reads `TREMOR_HISTORY_MONGO_URI`, so the secret never appears in the pod spec as an argument.
2. **CRDs live in `crds/`.** Helm installs them first and never changes them afterwards, which avoids the surprise of a chart upgrade deleting custom resources. The deployment guide says to apply CRD changes by hand.
3. **No drift between kustomize and chart.** CI compares the CRD files byte for byte and the RBAC rules structurally, and renders the chart with several value sets (including one that must fail).
4. **Images on GHCR, built on a version tag**, for amd64 and arm64 (Oracle's free tier is Ampere). The Dockerfile cross-compiles with `--platform=$BUILDPLATFORM` instead of emulating, so the arm64 build takes about as long as amd64. The same workflow packages the chart and attaches it to the release, so no chart repository is needed.
5. **Measured results come from `hack/results.sh`**, which runs each sample N times against the demo shop and prints a median table. The README links to numbers that were measured, with the cluster and date, rather than claims.
6. **The public demo stays optional.** Everything runs on kind for free; the Oracle, Atlas and GHCR path is documented in `docs/DEPLOYMENT.md` and costs nothing but was not run for this phase.

## Consequences

- Cutting a release is `git tag vX.Y.Z && git push origin vX.Y.Z`.
- Chart values must be kept in step with operator flags.
- The Oracle path is documented, not continuously tested; only the chart rendering and the kind e2e are in CI.
