# ADR 0007: Free-tier deployment strategy

Status: accepted

## Decision
Ship a Helm chart. CI proves it on kind. The public demo targets k3s on Oracle Cloud Always Free with MongoDB Atlas M0 and images on GitHub Container Registry. Until a cloud account exists, the deliverable is the chart plus a step-by-step guide.

## Consequences
Zero hosting cost. Always Free capacity and Atlas M0 limits constrain the demo size, which is fine for a single small cluster.
