# Plan — Spec 0002 State Consistency and Scheduler

 - **Status**: complete
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner
- **Completed**: 2026-08-24, v1-wave9~10 实现并通过验证
- **Spec**: `specs/0002-state-consistency-and-scheduler/spec.md`（proposed）
- **Boundaries**: `docs/module-boundaries.md`（run 模块：`run/model`、`run/scheduler`、`run/plan`；
  adapter：`storage/memory`）

## 1. Package layout

```
internal/run/model/       纯状态机：status、迁移、聚合、记录类型、CR 名派生
internal/run/plan/        不可变 Plan + canonical digest
internal/run/scheduler/   consumer-side ports + Ingestor/Dispatcher/Projector/Collector/Canceler
internal/storage/memory/  语义对齐 PG 的内存 durable store 与 active store（测试与 dev 用）
```

依赖方向符合 module-boundaries：`model`、`plan` 无内部依赖；`scheduler` 只依赖 `model`；
`storage/memory` 实现 scheduler ports（结构化满足，编译期断言放在测试中）。

## 2. State machines

JobRun durable status：`queued → dispatched → running → {succeeded | failed}`，
任意非终态 → `cancelled`。终态粘滞；非法迁移返回 `ErrIllegalTransition`。

Observed phase（Kubernetes 侧观测）：`pending | running | succeeded | failed`，经单调投影映射到
durable status：pending 不降级、running 前进、succeeded/failed 直接进入对应终态。

WorkflowRun status 由纯函数聚合（spec FR-A.3）。rank：queued 0 < dispatched 1 < running 2 <
terminal 3；run 状态更新同样单调。

## 3. Ports（consumer-side，定义在 scheduler）

```go
type DurableStore interface {
    RecordDelivery(ctx, Delivery) (DeliveryRecord, created, err)   // unique(provider, deliveryID)
    CreateRun(ctx, RunSeed, now) (RunRecord, created, err)         // 原子 run+jobRuns，create-or-get by delivery
    GetRun / GetJobRun / ListJobRuns / CancelRun
    ClaimNextQueuedJob(ctx) (JobRunRecord, err)                    // 串行化领取，不改状态；ErrNoQueuedJob
    ReleaseClaim(ctx, jobRunID) err                                // dispatch 失败时释放领取（模拟行锁释放）
    AckDispatched(ctx, jobRunID, ActiveObject, now) err            // 幂等；非 queued/dispatched 拒绝
    ApplyObserved(ctx, jobRunID, ObservedPhase, now) err           // 单调投影
    ListGCReadyJobs(ctx) ([]JobRunRecord, err)                     // run 终态 + job 终态 + 未回收
    MarkCollected(ctx, jobRunID, now) err                          // 回收成功后标记，防重复扫描
}
type ActiveExecutionStore interface {
    CreateOrGet(ctx, jobRunID) (ActiveObject, err)                 // 确定性名称
    Delete(ctx, jobRunID) err                                      // 幂等
}
type Compiler interface { Compile(ctx, Event, payload []byte) ([]JobIntent, err) }  // 0006 实现
```

## 4. Failure-window protocol

- Ingest：`RecordDelivery` → 已绑定 run 则直接返回（去重在昂贵工作之前）；否则 `Compile` →
  `CreateRun`（唯一键 delivery；崩溃重放收敛）。Window 0（delivery 有、run 无）由 FR-B.3 覆盖。
- Dispatch：`ClaimNextQueuedJob`（不改状态）→ 前置检查 job 仍可派发 → `ActiveExecutionStore.CreateOrGet`
  （确定性名）→ `AckDispatched`。失败路径 `ReleaseClaim` 归还领取。W1：CR 未建 → 重放再建；
  W2：CR 已建、ack 未落 → CreateOrGet 幂等返回。取消与 ack 之间的竞态产生的孤儿对象由
  Collector 回收（cancelled 是 GC-eligible 终态）。
- Project：`ApplyObserved` 单调粘滞；随后重算 run 聚合并单调更新 run。W3：重放无副作用。
- Collect：仅当 run 与 job 均 durable 终态且未标记回收时才 `Delete` CR（幂等），成功后
  `MarkCollected` 防止重复扫描；Delete 成功而 Mark 失败时，下一轮重复幂等删除后补标记。W4：终态未确认不删除。

## 5. IDs and determinism

- RunID/JobRunID：ULID（可排序），由注入 clock+entropy 的 IDGen 生成；测试注入固定源。
- CR 名 = `jobrun-<lower(ulid)>`（纯函数，model 提供）。
- Plan digest：结构体字段顺序固定的 canonical JSON（encoding/json 对 map key 排序）→ SHA-256 hex。

## 6. Testing strategy

- model/plan：表驱动纯函数测试（迁移、聚合、digest 稳定性、无明文 secret 断言）。
- scheduler：对 ports 做故障注入装饰器（第 N 次调用返回注入错误），逐窗口断言收敛；并发领取用
  `-race` 下的多 goroutine 测试；内存 store 语义（唯一键、原子性、单调投影）单独测试。
- 覆盖率目标：`internal/run/**` ≥ 85%。

## 7. Out of this plan

- PostgreSQL adapter（pgx、事务、`FOR UPDATE SKIP LOCKED` SQL、migration）：tasks 单列，
  语义以本 plan 的内存实现为准绳。
- FR-E schedule：V1 阶段独立任务，复用 FR-B 幂等键机制。
