# Spec 0004 — CRD API and Controller

- **Status**: accepted
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted）
- **Covers**: FR-4（P0 全部；FR-4.5 为 P2 仅登记边界）、FR-7.1/7.3 的 CRD 侧所有权、FR-12.3
  模块边界（runtime/controller 不依赖 provider SDK、不直接访问 PG）
- **Depends on**: 0002（ActiveExecutionStore port、确定性 CR 名）、0003（audience-bound
  token 的 Pod 侧投影要求）
- **Out of scope here**: Executor 容器内容与 Plan 获取（0008）、Service/Docker Action 辅助
  Pod 与 PVC（P2）、webhook 校验、部署清单与 k3s 支持矩阵（0011）

## 1. Problem

0002 定义了 durable 状态机与跨存储收敛协议，其中 Kubernetes 侧以 `ActiveExecutionStore`
port 出现。本 spec 把该 port 落到真实 API 对象：三个 CRD、一个 controller、以及 Pod 模板的
安全不变量。CRD schema 一旦发布即成兼容面，必须在实现早期冻结核心字段。

## 2. Requirements

### FR-C1 CRD 面（api/v1alpha1）

- FR-C1.1 `[P0]` 三个 CRD：`RunnerClass`（基础设施 profile）、`WorkflowRun`（durable run
  在集群内的引用）、`JobRun`（一次 JobInstance 的 active execution 对象）。
- FR-C1.2 `[P0]` CRD 绝不携带 secret 或完整 Plan：JobRun spec 只含 run 引用、job key、
  runnerClass 名、plan ID 与 digest、attempt。
- FR-C1.3 `[P0]` CR 名称确定性：由 forgelet JobRun ID 派生（`jobrun-<ulid lower>`），与 0002
  的 `ActiveExecutionStore` 协议一致；重复 create 收敛为 get。
- FR-C1.4 `[P0]` 状态字段单一所有权：CRD status 只描述 observed Kubernetes 执行
  （phase、pod 引用、起止时间、conditions）；durable 业务状态归 PG（0002），不得在 CRD 复制。
- FR-C1.5 `[P0]` WorkflowRun CR 仅作集群内引用（repo、sha、event、delivery key、durable run
  ID），不承载历史。
- FR-C1.6 `[P1]` CRD 生成物（deepcopy、manifest）随代码生成并在 CI 校验（`make generate` 幂等）。

### FR-C2 Primary Pod 不变量（FR-4.1、FR-4.4、FR-9.1）

- FR-C2.1 `[P0]` 每个 JobRun CR 至多一个 Primary Pod：名称确定性派生、ownerRef 指向 JobRun
  CR、重复 reconcile 不创建第二个。
- FR-C2.2 `[P0]` Pod 显式 `automountServiceAccountToken: false`；仅显式投影面向
  `forgelet-control-plane` audience 的短时效 ServiceAccount token（≤1h），挂载到 Executor。
  该 ServiceAccount 无任何 Kubernetes RBAC。
- FR-C2.3 `[P0]` Pod `restartPolicy: Never`；镜像、资源、nodeSelector 来自 RunnerClass；
  workspace 默认 emptyDir 挂载 `/workspace`（PVC 模式属 FR-4.5）。
- FR-C2.4 `[P0]` JobRun observed phase 终态后，Primary Pod 被删除不得重建（GC 语义，衔接
  0002 Collector）。

### FR-C3 Controller reconciliation（FR-4.2、FR-4.3）

- FR-C3.1 `[P0]` Controller watch JobRun CR：确保 Pod（create-or-get 语义）、将 Pod phase 映射
  为 observed phase（pending/running/succeeded/failed）写回 CR status，并经 control-plane port
  幂等投影到 durable store（0002 `ApplyObserved` 语义）。
- FR-C3.2 `[P0]` 投影只在 phase 变化时发生；重复 reconcile 对 durable store 无副作用。
- FR-C3.3 `[P0]` `runs-on` 解析：JobRun CR 携带 runnerClass 名，controller 解析 RunnerClass；
  缺失时设置 Ready=False condition（RunnerClassMissing），不创建 Pod、不崩溃、持续重试。
- FR-C3.4 `[P0]` Controller 对 durable store 的依赖只通过 consumer-side port（投影接口），
  不 import PostgreSQL adapter，不依赖 provider SDK。

### FR-C4 ActiveExecutionStore 落地

- FR-C4.1 `[P0]` 提供 0002 `ActiveExecutionStore` 的 Kubernetes adapter：`CreateOrGet` =
  create-or-get JobRun CR（spec 数据经 JobRunSource port 获取）；`Delete` = 删除 CR（ownerRef
  级联删除 Pod），幂等。
- FR-C4.2 `[P0]` adapter 不在 CR 中写入 0002 之外的语义；UID/Name 返回值与 CR 一致。

### FR-C5 测试与验证分层（NFR-4）

- FR-C5.1 `[P0]` M0 用 controller-runtime fake client 覆盖全部 reconciliation 行为。
- FR-C5.2 `[P1]` envtest（真实 API server）覆盖 API 行为：ownerRef 级联、CRD schema 校验、
  watch 语义；真实 k3s 冒烟属 0011。

## 3. Acceptance criteria

**AC-M0**（fake client 自动化）：

1. N 次 reconcile：恰好 1 个 Primary Pod；Pod 具备：确定性名称、JobRun ownerRef、
   `automountServiceAccountToken=false`、audience 投影 token volume、`restartPolicy=Never`、
   RunnerClass 的镜像/资源/nodeSelector、`/workspace` emptyDir。
2. Pod phase 矩阵（Pending/Running/Succeeded/Failed）正确映射到 CR status 并经 port 投影；
   同 phase 重复 reconcile 不重复投影；终态后删除 Pod 不重建。
3. RunnerClass 缺失：Ready=False（RunnerClassMissing）、无 Pod；补齐后下一次 reconcile 创建。
4. adapter：CreateOrGet 两次同名同 UID；Delete 后 Pod 级联声明（ownerRef）、再次 Delete 成功。
5. CR 名与 `model.JobRunID.CRName()` 一致（协议一致性测试）。

**AC-V1**：envtest 通过；`make generate` 产物 diff 干净；CRD manifest 无 secret/plan 载荷字段。

## 4. Design notes（非约束）

- group `ci.forgelet.dev`，version `v1alpha1`；label 域 `ci.forgelet.dev/*`。
- Executor 容器命令暂为占位（`/ci/executor`），由 0008 替换为真实镜像与启动参数；M0 仅保证
  Pod 形状与状态机正确。
