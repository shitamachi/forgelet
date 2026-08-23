# Tasks — Spec 0010 Observability

- [x] T1 M0：executor/控制面结构化 JSON 日志 + secret 先 mask 后输出（0008 已交付）
- [x] T2 M0：server wiring 日志结构化（0011 一并落地）
- [x] T3 V1：Prometheus 指标（队列深度/dispatch 延迟/job 时长与成功率）；`internal/observability/metrics`
      私有 registry，`/metrics` 端点，`CountQueuedJobs` port（memory+PG）
- [x] T4 V1：OpenTelemetry tracing 关键链路（webhook→ingest、dispatch、投影；W3C 传播
      进出 server 与 executor 控制面调用）；`internal/observability/tracing`，OTLP HTTP 导出，
      空 endpoint 即 no-op
- [ ] T5 V1：日志采集部署件（Alloy/FluentBit → Loki）与查询约定
