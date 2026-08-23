# Plan — Spec 0001 Platform Overview

- **Status**: accepted
- **Accepted**: 2026-08-23, project owner
- **Spec**: `specs/0001-platform-overview/spec.md` (`accepted`)
- **Architecture**: `docs/architecture.md`
- **Module boundaries**: `docs/module-boundaries.md`

本 plan 只定义子 spec 的落地顺序和跨模块门禁，不替代各子 spec 自己的技术 plan。

## 1. Delivery strategy

采用 risk-first + vertical-slice：先冻结无法通过局部重构补救的状态、安全和 API 边界，再实现 M0
纵向闭环。编号表示创建顺序，不要求一个 spec 完整实现后才能起草下一个，但任何实现必须等待其
直接依赖的 spec 与 plan 都 accepted。

1. `0002-state-consistency-and-scheduler`：状态所有权、幂等键、PG/CRD 故障窗口、internal schedule。
2. `0003-security-identity-secrets`：Executor workload identity、Plan/secret 边界、授权和 key rotation。
3. `0004-crd-api-and-controller`：在前两项约束下冻结 CRD schema、状态机和 controller ownership。
4. `0005-github-events-and-checks`：webhook、raw payload、replacement mode、Check Run lifecycle。
5. `0006`–`0010`：workflow、expression、executor、builtin、observability，并逐步完成 M0/V1 slice。
6. `0011-deployment-and-k3s-support`：固定 release support matrix、安装方式与真实 k3s smoke test。

## 2. M0 integration order

```text
push webhook
  → durable delivery/event
  → M0 workflow parse/compile
  → durable WorkflowRun/JobRun intent
  → deterministic JobRun CR
  → Primary Pod
  → authenticated Plan fetch
  → multiple run steps
  → terminal status projection
  → Check Run completion
```

M0 fixture 只使用 `runs-on`、一个 job 和多个 `run` step。JS/Composite/Builtin、matrix、schedule、
Docker Action、Service 和 Web UI 不得成为 M0 合并门槛。

## 3. Cross-cutting gates

每个 implementation PR 必须满足：

- requirement 与 acceptance criterion 可追溯到 accepted spec；
- `make verify` 通过，新增行为有测试；
- 新增 import 符合 `docs/module-boundaries.md`；
- 跨 PG/Kubernetes 操作有稳定 ID、唯一约束和故障窗口测试；
- secret 不进入 CRD、持久化 Plan、日志、错误或 trace；
- API/CRD 变化同步更新 spec、生成物、兼容性说明和 migration plan；
- M0 之后每个里程碑保留上一里程碑的端到端 fixture。

## 4. Decisions locked by Spec 0001

- modular monorepo，初期一个 Go module、多 binary；
- replacement mode 是默认运行模式；
- schedule 是内部调度能力，不是 GitHub webhook；
- PostgreSQL 和 CRD 各自拥有不同状态，跨存储通过 reconciliation 收敛；
- 每个 JobRun 一个 Primary Pod，P2 auxiliary Pods 是显式例外；
- Executor 无 Kubernetes API 权限，通过专用短时效身份访问 forgelet control plane；
- Provider capability 与 Check Reporter 分离，不用 Commit Status 接口冒充 Check Run。

这些决策若被后续现实推翻，必须先修改 Spec 0001 和本 plan，再修改实现。
