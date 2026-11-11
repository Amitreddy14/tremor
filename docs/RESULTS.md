# Measured results

Numbers below were measured on a local kind cluster (1 control plane, 2 workers, Docker Desktop on Windows) running the demo shop with 3 replicas and one MongoDB. They show what Tremor reports, not how your application will behave. Rerun them on your own cluster:

```bash
RUNS=5 make results > results.md     # median of 5 runs per fault, one markdown table
```

Pod recovery is injection (or, for sustained faults, release) until every target pod is Ready again. User recovery is the same moment for the HTTP steady-state probe (`/orders`, one probe a second); `0` means no probe ever failed.

## Recorded runs (11 Nov 2026, kind, Phase 6 build)

| Fault | Setup | Pod recovery | User recovery | Probes (ok / failed) | What was observed |
| --- | --- | --- | --- | --- | --- |
| NodeDrain | worker `tremor-worker` (2 of 3 shop pods) cordoned and evicted | 2,221 ms | 0 ms | no failures | Evicted pods rescheduled; node returned to schedulable |
| NetworkLatency | 1.5 s on one shop pod, 30 s | 5-9 ms (see note) | 0 ms | 5 / 1 | Probes through the slowed pod timed out, so its readiness probe failed and the kubelet restarted the shop container (restart counts 1 and 3) |
| NetworkLoss | packet loss on one shop pod, 30 s | 11 ms (see note) | 0 ms | no user impact | Completed without readiness or restart effects |

**Note on the 5-11 ms values.** Phase 6 timed recovery from the moment the controller noticed the fault container had ended, about a second after the real end. For network and stress faults the true pod recovery is therefore close to zero, and these values only show that the clock started late. From Phase 7 the clock starts at the container's `finishedAt`. Treat sub-second values as "no measurable recovery time" rather than as a precise figure.

**Note on 1.5 s latency.** It exceeds the shop's one-second probe timeouts, so the fault disturbs Kubernetes health checks more than users. `shop-network-latency-mild` (300 ms, 50 ms jitter) stays under the timeouts and shows a slowdown instead.

## What a result tells you

- A three-replica service behind a Service usually shows **0 ms user recovery** for a single pod kill or node drain. That is the point of replicas, and it is what a regression would break.
- A `DependencyOutage` of the database is different: users are affected for the whole outage, and user recovery measures how long the shop takes to serve again after MongoDB returns.
- A run that ends `Failed` with "not recovered after ..." is as valuable as a green one: it is the failure you wanted to find.

## Full table

Not yet recorded for the final build. Run `make results` and paste the table here with the date and cluster description.
