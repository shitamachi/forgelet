# Observability runbook (specs 0010 / 0011)

## Components

| Concern  | Choice                                        | Where                          |
|----------|-----------------------------------------------|--------------------------------|
| Logs     | structured JSON via slog → Alloy → Loki       | `deploy/observability/`        |
| Metrics  | Prometheus text format at `/metrics`          | server + controller (`:8080`)  |
| Tracing  | OTLP HTTP export, W3C traceparent propagation | `--otlp-endpoint` on binaries  |

Install the log pipeline after the control plane:

```bash
kubectl apply -f deploy/observability
```

## Log conventions (FR-O1)

Every forgelet process logs one JSON object per line. Stable identifiers:

| Field     | Meaning                  | Emitted by        |
|-----------|--------------------------|-------------------|
| `jobRun`  | JobRun id                | executor, server  |
| `step`    | step display id          | executor          |
| `msg`     | human-readable message   | all               |

Secret values are masked by the executor before any output (0008); the
control plane never logs resolved secret values.

## LogQL queries

```logql
# All lines of one job run
{job_run_id="01HXYZ"}

# One step's output within a run
{job_run_id="01HXYZ", step=~"build.+"}

# Control-plane dispatch errors
{app="forgelet-server"} |= "dispatch loop" |= "err"

# Step failure rate over the last day: parse outcome from result reports
sum by (step) (rate({app~"forgelet.*"} | json | outcome="failure" [1d]))
```

## Metrics that matter

- `forgelet_jobrun_queue_depth` — backlog; sustained growth means dispatch or
  capacity starvation.
- `forgelet_dispatch_duration_seconds` — webhook-to-pod latency budget.
- `forgelet_job_duration_seconds{status,runner_class}` and
  `forgelet_jobruns_completed_total{status}` — success rate =
  `succeeded / ignoring(status) completed_total`.

## Tracing

Binaries are no-op tracers until `--otlp-endpoint host:port` is set. Spans:
`server.ingest` → `server.dispatch_drain`/`server.dispatch` →
`server.project`; executor jobs continue the same trace across the internal
API calls (W3C traceparent).
