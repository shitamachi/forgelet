# Spec 0010 — Observability

- **Status**: accepted（M0 切片；metrics/tracing 为 V1 任务）
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner（M0 收尾整体授权）
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted）
- **Covers**: FR-10.1 `[P0]`（FR-10.2 为 V1 任务）

## Requirements（M0）

- FR-O1 `[P0]` Executor 与控制面输出结构化 JSON 日志（slog），至少可按 run/job/step 检索；
  已由 0008 实现并被测试固化（日志行可解析、携带标识、secret 先 mask 后输出）。
- FR-O2 `[P0]` server/controller 装配的日志同样为结构化 JSON，请求处理错误携带 request
  路径与 job 标识（0011 wiring 落实）。
- FR-O3 `[P1/V1]` Prometheus 指标（队列深度、dispatch 延迟、job 时长/成功率）与关键链路
  OpenTelemetry tracing；日志采集（Alloy/FluentBit → Loki）部署件。

## Acceptance criteria

**AC-M0**：进程内 e2e（0011）产生的日志行全部为可解析 JSON 且不含 secret；0008 日志形状测试
保持绿。**AC-V1**：指标/tracing 端点与仪表盘、Loki 链路部署件。
