# Spec 0001 — Platform Overview

- **Status**: accepted
- **Date**: 2026-08-22
- **Accepted**: 2026-08-23, project owner
- **Scope**: forgelet 全平台总纲。定义产品边界、术语、顶层需求与验收标准。
  细粒度特性必须在后续 spec 中细化，并能追溯到本 spec。

## 1. Problem

现有自建 CI/CD 方案通常需要在「GitHub Actions workflow 兼容」和「Kubernetes-native」之间取舍：

- GitHub-hosted/self-hosted Actions 以 runner 为执行抽象；
- 将 Docker runner/act 放入 Pod 仍依赖 Docker API 执行模型；
- 其他 Kubernetes pipeline API 与 GitHub Actions workflow 语义差异较大。

forgelet 要解决的问题是：在不引入 GitHub Runner、Docker socket 或 DinD 的前提下，让受支持的
GitHub Actions workflow 语义直接映射为 Kubernetes 调度与执行对象。

## 2. Product statement

forgelet 是一个 CI/CD 平台：

1. 读取 `.github/workflows/*.yml` 并执行明确声明的 GitHub Actions 兼容子集；
2. 以 Kubernetes 为唯一执行运行时；
3. Source Provider 可插拔，第一版为 GitHub App；
4. 默认以 replacement mode 工作：仓库禁用 GitHub Actions 原生执行，forgelet 负责触发和执行；
5. 可选 coexistence mode，但必须显式启用，且不承诺自动消除 GitHub 与 forgelet 的重复运行。

“路径不变”不等于“无需配置”：replacement mode 需要安装 GitHub App、配置 webhook，并在仓库或
组织层关闭 GitHub Actions 原生执行。V1 也不承诺 100% workflow 语法兼容。

## 3. Terms

| 术语 | 定义 |
|------|------|
| Workflow | `.github/workflows/*.yml` 中的一个 YAML 文档 |
| WorkflowRun | 一次事件触发的 workflow 执行 |
| Job / JobInstance | workflow 中的 job / matrix 展开后的实例 |
| JobRun | JobInstance 的 durable 记录与 active Kubernetes execution 的关联 |
| Primary Pod | 每个 JobRun 唯一的主执行 Pod；普通、JS、Composite、Builtin step 在其主容器执行 |
| Auxiliary Pod | P2 中 Service 或 Docker Action 使用的辅助 Pod，不替代 Primary Pod |
| Plan | Executor 执行 JobRun 所需的不可变计划；持久化内容含 secret reference，不含解析后的明文 secret |
| RunnerClass | 基础设施 profile：镜像、资源、调度约束、安全属性 |
| NormalizedEvent | provider-neutral 的调度字段 |
| ProviderPayload | 与事件一同保存的不可变 provider 原始 payload，用于兼容 context 与审计 |
| Provider | Source Provider capability 的 adapter，GitHub adapters 为第一版实现 |
| Control Plane | `server` 与 `controller` 的合称 |

## 4. Priorities and release slices

- **P0 / M0**：最小纵向闭环；只要求 `push + runs-on + 单 job/多 run step` 从 webhook 跑到 Check Run。
- **P1 / V1**：首个可用版本必须完成，但不是 M0 完成条件。
- **P2**：第二、三阶段能力。

每个子 spec 必须分别列出适用的 M0 slice 和 V1 slice。标题上的优先级不覆盖子条目；以每条需求
自己的 `[P0]`、`[P1]`、`[P2]` 为准。

## 5. Requirements

### FR-1 事件接入

- FR-1.1 `[P0]` 通过 GitHub App webhook 接收 `push`。
- FR-1.2 `[P1]` 通过 GitHub App webhook 接收 `pull_request`。
- FR-1.3 `[P1]` replacement mode 由 forgelet API/CLI 产生 `workflow_dispatch` 兼容事件；
  coexistence mode 可选接收 GitHub App `workflow_dispatch` webhook。
- FR-1.4 `[P1]` `schedule` 由 forgelet 内部 scheduler 读取默认分支 workflow 并触发，不依赖
  不存在的 GitHub `schedule` webhook。必须定义 cron 变更刷新、missed fire、重叠和时区语义。
- FR-1.5 `[P0]` GitHub webhook 必须验证 `X-Hub-Signature-256`，并以 delivery ID 做 durable 去重。
  内部触发使用稳定、可重建的幂等键。
- FR-1.6 `[P0]` 事件同时保留 NormalizedEvent 和 ProviderPayload；后续模块不得依赖 GitHub SDK
  类型，但不得丢弃构造 `github.event` 所需的原始数据。

**AC-M0**：重放同一个 `push` delivery，只产生一个 WorkflowRun；workflow 能读取与原始 payload
一致的 `github.event` 字段。

**AC-V1**：同一 `(repository, workflow, cron expression, scheduled fire time)` 重复调度只产生一个
WorkflowRun；schedule 在没有 GitHub webhook 的情况下运行；replacement manual dispatch 不创建
GitHub Actions 原生 run。

### FR-2 Workflow 解析与编译

- FR-2.1 `[P0]` 使用 YAML → syntax tree → IR → compiled workflow → Job DAG 管线；source syntax
  node 不得直接进入调度状态。
- FR-2.2 `[P0]` 支持 M0 子集：`on.push`、`jobs`、`runs-on`、`steps.run`、job/step `name` 与 `env`。
- FR-2.3 `[P1]` 支持 V1 子集：`pull_request/workflow_dispatch/schedule`、`needs/if/matrix/secrets/uses`、
  `continue-on-error/timeout/outputs/concurrency`。
- FR-2.4 `[P0]` 对不在当前声明兼容子集内的字段显式报错，包含文件、行和列，不得静默忽略。
- FR-2.5 `[P1]` `strategy.matrix` 展开为具有稳定 ID 的独立 JobInstance；显示名称与 ID 分离。

**AC-M0**：M0 fixture 编译为预期 DAG；未知字段返回带 source location 的诊断。

**AC-V1**：官方文档中属于声明兼容范围的 fixture 全部有快照和行为测试；matrix 展开顺序及 ID
在重试后保持稳定。

### FR-3 表达式引擎

- FR-3.1 `[P0]` 提供 M0 所需的字面量、布尔/比较运算和 `github/env` context 读取能力。
- FR-3.2 `[P1]` 控制面与 Executor 共用同一语法和求值语义，并支持 V1 contexts：
  `github/env/vars/secrets/runner/matrix/strategy/needs/jobs/steps/inputs`。
- FR-3.3 `[P1]` 支持 V1 函数：`success`、`failure`、`cancelled`、`always`、`contains`、
  `startsWith`、`endsWith`、`format`、`join`、`toJSON`、`fromJSON`、`hashFiles`。
- FR-3.4 `[P0]` 引擎不得依赖 Kubernetes、数据库、provider SDK 或网络；需要 workspace 的函数通过
  明确 capability 求值，不能隐藏访问全局文件系统。

**AC**：逐运算符、context 和函数的 table-driven tests 通过；可在两个阶段求值的同一表达式结果一致；
不允许当前阶段访问的 context 返回类型化诊断。

### FR-4 Kubernetes 执行运行时

- FR-4.1 `[P0]` 一个 JobRun 始终只有一个 Primary Pod；M0 的所有 `run` step 共享主容器的
  filesystem、PATH、ENV、tool cache 和 workspace。
- FR-4.2 `[P0]` Controller watch JobRun，幂等创建/回收 Primary Pod 并更新 observed status。
- FR-4.3 `[P0]` `runs-on` 解析为 RunnerClass capability，不表示长期存活的 runner machine。
- FR-4.4 `[P0]` Executor 不得获得 Kubernetes API 权限，Pod 禁止自动挂载默认 ServiceAccount token。
- FR-4.5 `[P2]` `job.container`、Service Pod、Docker Action Step Pod、ephemeral PVC 和同节点约束
  由后续 spec 定义，属于 Primary Pod 不变量的显式辅助资源例外。

**AC-M0**：单 job 两个 `run` step 共享文件；Pod spec 无自动 SA token，Executor 身份不能调用
Kubernetes API；重复 reconcile 不创建第二个 Primary Pod。

### FR-5 Executor

- FR-5.1 `[P0]` Executor 作为 Primary Pod PID 1，使用 JobRun-scoped、短时效 workload identity
  从控制面取得 Plan、所需 secret 值并上报状态。
- FR-5.2 `[P0]` M0 支持 `run` step；`[P1]` 增加 JavaScript、Composite 和 Builtin Action；
  `[P2]` 增加 Docker Action。
- FR-5.3 `[P1]` 实现 `GITHUB_ENV/GITHUB_OUTPUT/GITHUB_PATH/GITHUB_STATE/GITHUB_STEP_SUMMARY`
  及声明支持的 workflow commands。
- FR-5.4 `[P0]` 负责 signal forwarding、timeout、cancellation 和子进程清理。
- FR-5.5 `[P0]` 日志结构化并携带 run/job/step ID；secret masking 必须发生在任何日志输出前。

**AC-M0**：取消 JobRun 后子进程在 grace period 内退出；重复获取 Plan 或上报状态不会扩大权限或
创建重复 step；日志中的已下发 secret 显示为 `***`。

### FR-6 Builtin Actions

- FR-6.1 `[P1]` 提供 `actions/checkout` builtin，并明确支持的认证、shallow、submodule、LFS 子集。
- FR-6.2 `[P1]` 提供 `actions/cache`、`actions/upload-artifact`、`actions/download-artifact` builtin，
  对接 S3-compatible storage；cache 必须 repository-scoped。

**AC-V1**：声明支持的典型 Go CI fixture 在不依赖 GitHub Actions Runtime Service 的情况下完成
checkout、setup-go、cache 和 test。

### FR-7 状态、幂等与历史

- FR-7.1 `[P0]` PostgreSQL 是 durable scheduling、幂等键和历史的事实来源；CRD 是 active
  Kubernetes execution 的期望/观测状态来源。每个状态字段只有一个 owner。
- FR-7.2 `[P0]` PG 与 Kubernetes 之间不存在原子事务；所有跨存储操作必须使用稳定 ID、唯一约束、
  create-or-get 和 reconciliation，在 at-least-once 执行下收敛。
- FR-7.3 `[P0]` CRD 终态只有在 durable history 确认持久化后才能进入 GC；GC retention 可配置。
- FR-7.4 `[P0]` V1 不依赖消息队列来获得正确性。

**AC-M0**：分别在 PG commit/CR create、CR create/PG acknowledgement、CR status/PG projection、
PG terminal acknowledgement/CR GC 四个边界注入故障，恢复后均无重复 JobRun、无丢失终态。

### FR-8 Provider 与 Check Reporting

- FR-8.1 `[P0]` Provider 能力按消费方拆分；CI domain 不依赖 provider SDK 类型。
- FR-8.2 `[P0]` GitHub App 使用短时效 installation token 读取仓库和 clone。
- FR-8.3 `[P0]` Check reporting 支持 create、update、complete 生命周期，保存 forgelet external ID，
  并用 Check Run `details_url` 指向 forgelet UI/API 页面。
- FR-8.4 `[P1]` 支持 GitHub 发起的 rerequest，并保证新的 attempt 与原 JobRun 可追踪且不覆盖历史。

**AC-M0**：同一 job retry 不创建无法关联的重复 Check Run；PR/commit 页面显示最终结果和详情链接。

### FR-9 安全

- FR-9.1 `[P0]` Executor workload identity 必须绑定 audience、namespace、Pod UID、JobRun、允许接口
  和过期时间；对应 ServiceAccount 不拥有 Kubernetes RBAC。
- FR-9.2 `[P0]` CRD 与持久化 Plan 不得包含解析后的 secret 明文；Plan digest 不包含 secret 明文。
- FR-9.3 `[P0]` secret 使用 envelope encryption，支持 key version 与轮换；只向已授权 Job 下发
  其实际引用的值。
- FR-9.4 `[P1]` fork PR 无 secret、deploy credential，并使用 untrusted RunnerClass 与可验证的
  network policy。
- FR-9.5 `[P2]` deployment 通过受策略约束的独立 CD/GitOps 路径，不向 CI Job 授予 cluster-admin。

**AC-M0**：其他 Pod、其他 JobRun、过期 token 和错误 audience 均无法读取 Plan/secret 或上报状态；
Executor token 无法成功调用 Kubernetes API。

### FR-10 可观测性

- FR-10.1 `[P0]` Executor 输出可采集的结构化日志，至少可按 run/job/step 查询。
- FR-10.2 `[P1]` 提供队列深度、调度延迟、job 时长/成功率指标与关键链路 tracing。

### FR-11 第二/三阶段能力

以下能力均为 `[P2]`：`job.container`、services、Docker Action、BuildKit、reusable workflow、
environment approval、OIDC、完整 permissions、Web UI、multi-tenant、Gitea/GitLab provider。

### FR-12 模块化 Monorepo

- FR-12.1 `[P0]` spec、公共 API、多个 binary、部署资产和测试保存在同一仓库，使跨组件变更可原子评审。
- FR-12.2 `[P0]` 初期使用一个 Go module，通过业务模块、consumer-side ports 和单向依赖实现模块化。
- FR-12.3 `[P0]` 禁止无所有权的 `common/shared/helpers/utils` 模块，禁止核心 workflow/run 模块
  依赖 Kubernetes、provider SDK 或具体 storage adapter。
- FR-12.4 `[P2]` 只有出现独立版本、发布或依赖策略需求时才拆分多个 Go module；该变更需要
  accepted spec/ADR，并使用 `go.work` 支持同仓开发。

**AC**：CI 能检测禁止的反向依赖；每个 binary 只在 composition root 组装 adapter；一次 PR 可以
同时更新受影响的 spec、API、实现、部署清单和端到端测试。

## 6. Non-functional requirements

- NFR-1 每个 release 发布明确的 Kubernetes/k3s 支持矩阵；V1 至少在一个 pinned、默认配置的 k3s
  版本上完成安装和 M0 smoke test，并验证默认 StorageClass、DNS 与 NetworkPolicy 前提。
- NFR-2 控制面崩溃恢复不依赖进程内状态。
- NFR-3 镜像已预热的测试环境中，webhook → Primary Pod Running 的 P95 目标为 30 秒以内；测试环境、
  样本量和测量窗口必须随结果记录。
- NFR-4 所有外部交互均通过可替换 port 测试；Kubernetes 行为使用 fake/envtest 与真实 k3s 分层验证。
- NFR-5 根目录 `make verify` 在 docs-only 和任意 implementation commit 上都必须成功。

## 7. Out of scope（V1）

- GitHub Actions Runtime protocol 的完整兼容；
- 100% workflow 语法兼容或静默忽略未知字段；
- runner pool、Docker socket、DinD；
- coexistence mode 下自动阻止 GitHub Actions 原生 workflow 执行；
- 未在 V1 support matrix 中声明的 Kubernetes/k3s 版本。

## 8. Traceability and child-spec order

先解决跨模块正确性和安全边界，再冻结 API，最后实现纵向闭环。编号表示计划创建顺序：

| 后续 spec | 覆盖 |
|-----------|------|
| 0002-state-consistency-and-scheduler | FR-1.4, FR-7 |
| 0003-security-identity-secrets | FR-5.1, FR-9 |
| 0004-crd-api-and-controller | FR-4 |
| 0005-github-events-and-checks | FR-1, FR-8 |
| 0006-workflow-syntax-and-compiler | FR-2 |
| 0007-expression-engine | FR-3 |
| 0008-executor-runtime | FR-5 |
| 0009-builtin-actions | FR-6 |
| 0010-observability | FR-10 |
| 0011-deployment-and-k3s-support | NFR-1 |

FR-12 是所有子 spec 的横切约束，由 `docs/module-boundaries.md` 和 CI 依赖检查共同执行。
