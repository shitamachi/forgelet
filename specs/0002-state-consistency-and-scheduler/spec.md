# Spec 0002 — State Consistency and Scheduler

- **Status**: accepted
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted）
- **Covers**: FR-7（全部）、FR-1.4、FR-1.5 的 durable 去重语义
- **Out of scope here**: GitHub webhook 验签/归一化（0005）、CRD schema 与 controller（0004）、
  workflow 编译为 job 实例（0006）、PG 物理部署（0011）

## 1. Problem

PostgreSQL 与 Kubernetes 之间不存在原子事务。任何“先写 PG 再建 CR”或“先建 CR 再回写 PG”的实现
在崩溃、重试或重复投递下都可能产生重复 JobRun、丢失终态或残留 CR。Spec 0001 FR-7 要求这类正确性
不依赖消息队列，而依赖稳定 ID、唯一约束和 reconciliation。

本 spec 定义 durable run 状态机、幂等键与四个故障窗口的收敛协议，使后续 0004（controller）与
0005（webhook）可以直接复用同一套语义。

## 2. Requirements

### FR-A Durable run 状态机

- FR-A.1 `[P0]` WorkflowRun、JobRun 各自有显式状态枚举与合法迁移集合；非法迁移必须被拒绝并返回
  可识别错误，而不是被静默改写。终态一旦写入即不可变更（重试产生新 attempt，不复活终态记录）。
- FR-A.2 `[P0]` 状态迁移、状态聚合（job 状态 → workflow run 状态）是纯函数：不读时钟、不产生
  随机数、不做 I/O，可独立表驱动测试。
- FR-A.3 `[P0]` WorkflowRun 状态由其 JobRun 集合聚合得出：任一 job 非终态且未开始 → queued；
  任一 job running → running；全部 job 终态时，任一 failed → failed，否则任一 cancelled →
  cancelled，否则 succeeded。

### FR-B 幂等键与 durable intent

- FR-B.1 `[P0]` 每条 provider delivery 先落 durable 记录（provider、delivery ID、raw payload、
  接收时间），再触发任何昂贵工作（如 workflow 编译）；重复 delivery 返回已有结果，不产生第二个
  WorkflowRun。
- FR-B.2 `[P0]` WorkflowRun ID、JobRun ID 全局唯一、可排序，由注入的时钟/熵源生成；Kubernetes
  资源名称由 JobRun ID 确定性派生，同一 JobRun 永远映射到同一资源名。
- FR-B.3 `[P0]` “记录 delivery”与“创建 run 及其全部 JobRun”是两个可独立重放的步骤：delivery 已
  存在但 run 未创建时，重放同一 delivery 必须恰好创建一个 run（create-or-get，唯一键为 delivery）。
- FR-B.4 `[P0]` 创建 run 及其全部 JobRun 在 durable store 内是原子的：要么全部可见，要么全不可见。
- FR-B.5 `[P0]` Job 的组成（compile 结果）对同一 delivery 重放必须产生相同 job 集合；compile 失败
  时 delivery 记录仍然保留（可审计、可重试），但不创建 run。

### FR-C 跨存储故障窗口收敛

- FR-C.1 `[P0]` Window 1（PG 已提交 JobRun intent、CR 未创建）：dispatch 重放使用确定性资源名
  create-or-get，收敛后系统中该 JobRun 仅有零或一个 CR，不得出现两个。
- FR-C.2 `[P0]` Window 2（CR 已创建、PG 未记录）：重放时读取既有 CR 并补记 UID，不得另建副本。
- FR-C.3 `[P0]` Window 3（CR observed status 变化、PG 投影未落）：observed status 投影到 durable
  store 必须幂等且单调（状态只能沿 queued → dispatched → running → terminal 前进；终态粘滞）；
  重复投递同一 observed 值是无副作用的成功。
- FR-C.4 `[P0]` Window 4（PG 终态已确认、CR 未回收）：只有 durable 侧 run 处于终态时才允许回收 CR；
  回收操作幂等（目标已不存在视为成功）。终态未确认前 CR 必须保留。
- FR-C.5 `[P0]` 上述收敛逻辑必须以“端口 + 可注入故障”的方式实现并测试：每个窗口注入一次失败，
  重放后断言无重复 JobRun、无重复 CR、终态不丢失。不要求测试接触真实 Kubernetes。

### FR-D 调度领取

- FR-D.1 `[P0]` 领取下一个待执行 JobRun 的操作在 durable store 内串行化（等价 `FOR UPDATE SKIP
  LOCKED`）：并发领取不会把同一 JobRun 交给两个 dispatcher；领取本身不改变 JobRun 状态。
- FR-D.2 `[P0]` dispatch 确认（记录 CR 名称/UID、状态 → dispatched）幂等；对已取消或已终态的
  JobRun 执行 dispatch 确认必须失败。
- FR-D.3 `[P1]` 调度优先级与排队公平性（priority、created_at 排序）字段预留，V1 实现排序语义。

### FR-E 内部 schedule（FR-1.4）

- FR-E.1 `[P1]` cron 定义来自默认分支 workflow；forgelet 内部 scheduler 按
  `(repository, workflow, cron expression, scheduled fire time)` 幂等触发，重复调度只产生一个
  WorkflowRun。
- FR-E.2 `[P1]` 必须定义并测试：cron 变更刷新、missed fire 策略、重叠抑制、时区语义。
- FR-E.3 `[P1]` 内部触发使用稳定、可重建的幂等键，不依赖 GitHub delivery ID。

### FR-F Plan 与 digest

- FR-F.1 `[P0]` Plan 是不可变值：持久化内容包含步骤、环境与 secret 引用，绝不包含解析后的明文
  secret。
- FR-F.2 `[P0]` Plan digest 是规范化序列化（canonical encoding）的稳定哈希：同构 Plan（map 顺序
  不同）digest 相同；内容变化 digest 变化；digest 输入不含明文 secret。

### FR-G 取消

- FR-G.1 `[P0]` 取消 WorkflowRun 将其所有非终态 JobRun 置为 cancelled 并将 run 置为 cancelled；
  终态 job 不被改写。已取消的 JobRun 不能再被领取或 dispatch。

## 3. Acceptance criteria

**AC-M0**（全部为 `[P0]`，以表驱动/故障注入测试自动验证）：

1. 同一 delivery 重放 N 次：恰好 1 个 WorkflowRun、每个 job 恰好 1 个 JobRun（FR-B.1/B.3）。
2. compile 在 delivery 记录之后失败：delivery 存在、run 不存在；修复后重放创建 run（FR-B.5）。
3. 四个故障窗口各注入一次失败并重放：CR 创建次数 =1、无重复 JobRun、终态与 GC 语义正确（FR-C）。
4. 并发 N 个 dispatcher 领取：同一 JobRun 恰好被领取一次（FR-D.1，go test -race 下通过）。
5. 同一 Plan 两种 map 插入顺序 digest 相同；改动一步 digest 变化；序列化结果不含明文 secret（FR-F）。
6. 取消后 dispatch 被拒绝且状态合法（FR-G）。

**AC-V1**：schedule 幂等键测试（FR-E）与 PG 真实唯一约束/事务测试通过（adapter 层）。

## 4. Design notes（非约束）

- durable store 与 active execution（Kubernetes）各自由 consumer-side port 描述；本 spec 实现提供
  语义与 PG 一致的内存适配器及故障注入测试，PostgreSQL adapter 与 0004 controller 在后续 PR 落地
  并复用同一测试语义。
