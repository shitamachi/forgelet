# Spec 0001 — Platform Overview

- **Status**: draft
- **Date**: 2026-08-22
- **Scope**: forgelet 全平台总纲。定义产品边界、术语、顶层需求与验收标准。
  细粒度特性（expression engine、JobRun controller、artifact……）在后续编号 spec 中细化，
  且必须能在本 spec 中找到对应条目（traceability）。

## 1. Problem

现有自建 CI/CD 方案在「GitHub Actions workflow 兼容」与「Kubernetes-native」之间二选一：

- GitHub Actions + ARC：被 runner 生命周期、配额、Docker 依赖拖累；
- act 塞进 Pod：本质仍是 Docker API 执行模型，不是 Kubernetes-native；
- Tekton：与 GitHub Actions 语义差异大，需要庞大翻译层。

## 2. Product statement

forgelet 是一个 CI/CD 平台：

1. 直接执行 GitHub Actions 兼容 workflow（路径保留 `.github/workflows/`）；
2. 以 Kubernetes 为唯一执行运行时（无 runner、无 Docker socket、无 DinD）；
3. Source Provider 可插拔，第一版为 GitHub（GitHub App），后续 Gitea/GitLab/Forgejo。

## 3. Terms

| 术语 | 定义 |
|------|------|
| Workflow | `.github/workflows/*.yml` 中的一个 YAML 文档（GitHub Actions 语法） |
| WorkflowRun | 一次事件触发的 workflow 执行 |
| Job / JobInstance | workflow 中的 job / matrix 展开后的实例 |
| JobRun | JobInstance 的执行记录（CRD + PG 行） |
| Plan | Executor 执行一个 JobRun 所需的完整计划（steps、env、secrets），存 PG，CRD 仅存 plan ID + digest |
| RunnerClass | 基础设施 profile CRD：镜像、资源、调度约束、安全属性 |
| Provider | Source Provider 接口实现（GitHubProvider 第一版） |
| Control Plane | `ci-server`（API + webhook + scheduler）与 `ci-controller` 的合称 |

## 4. Requirements

需求编号 `FR-x`；每条含验收标准（AC）。优先级 P0=第一闭环必须，P1=V1 必须，P2=第二/三阶段。

### FR-1 事件接入（P0）

- FR-1.1 通过 GitHub App webhook 接收 `push`、`pull_request`、`workflow_dispatch`、`schedule`。
- FR-1.2 验签 `X-Hub-Signature-256`；以 `X-GitHub-Delivery` 去重（重复投递不得产生重复 run）。
- FR-1.3 事件归一化为内部 NormalizedEvent，后续管线不得出现 GitHub 专有类型。
- **AC**: 重放同一 delivery 的 webhook，系统中有且仅有一个 WorkflowRun。

### FR-2 Workflow 解析与编译（P0）

- FR-2.1 从触发 commit 读取 `.github/workflows/*.yml`，经 YAML → AST → IR → Compiled Workflow → Job DAG 管线；YAML AST 不得进入调度。
- FR-2.2 V1 支持：`on/push/pull_request/workflow_dispatch/schedule`、`jobs/needs/if/matrix/env/secrets/run/uses`、`continue-on-error/timeout/outputs/concurrency`。
- FR-2.3 不支持的字段必须显式报错（含文件、行号），不得静默忽略。
- FR-2.4 `strategy.matrix` 展开为独立 JobInstance，名称形如 `test[go=1.27,os=arm64]`。
- **AC**: 对官方文档示例 workflow 集合（`internal/workflow/testdata/`）parse+compile 结果与预期 DAG 快照一致；未知字段产生错误。

### FR-3 表达式引擎（P0/P1）

- FR-3.1 单一 Go package，控制面（scheduler-time）与 Executor（runtime）两阶段共用。
- FR-3.2 支持 contexts：`github/env/vars/secrets/runner/matrix/strategy/needs/jobs/steps/inputs`；函数：`success/failure/cancelled/always/contains/startsWith/endsWith/format/join/toJSON/fromJSON/hashFiles`。
- FR-3.3 引擎不得依赖 Kubernetes、数据库或网络。
- **AC**: 逐 context、逐函数的表驱动用例通过；同一表达式在两阶段求值结果一致（对可静态求值的表达式）。

### FR-4 Kubernetes 执行运行时（P0）

- FR-4.1 一个 JobRun = 一个主 Pod；job 内所有普通 step 共用同一容器（filesystem/PATH/ENV/toolcache/workspace）。
- FR-4.2 `ci-controller` watch JobRun CR，创建/回收 Pod，回写状态。
- FR-4.3 `runs-on` 解析为 RunnerClass（单个名字或 label 列表），不表示任何长期存活的 runner。
- FR-4.4 Pod 模板：initContainer 注入静态 `ci-executor`；`automountServiceAccountToken: false`；Executor 无任何 K8s API 权限。
- FR-4.5 workspace 默认 emptyDir；存在 Docker Action 时自动切换 ephemeral PVC，且 Step Pod 与 Job Pod 同 Node。
- **AC**: 单 job 双 step（第二个 step 读取第一个 step 写入的文件/env）通过；Pod spec 检查无 SA token 挂载。

### FR-5 Executor（P0）

- FR-5.1 `ci-executor` 为静态 Go binary（CGO_ENABLED=0），作为 Pod PID 1，启动后凭 JobRun 身份从控制面拉取 Plan。
- FR-5.2 step 类型：`run`（shell）、JavaScript Action、Composite Action、Builtin Action；Docker Action 第二阶段。
- FR-5.3 实现 GitHub File Commands：`GITHUB_ENV/GITHUB_OUTPUT/GITHUB_PATH/GITHUB_STATE/GITHUB_STEP_SUMMARY`，以及 workflow command `::add-mask::/::warning::/::error::/::notice::/::group::/::endgroup::`。
- FR-5.4 负责 signal forwarding、timeout、cancellation、子进程清理。
- FR-5.5 日志为结构化 JSON（run/job/step id），先做 secret masking 再输出。
- **AC**: `echo "FOO=bar" >> $GITHUB_ENV` 后下一步 `echo $FOO` 输出 `bar`；日志中 secret 呈现为 `***`。

### FR-6 Builtin Actions（P0/P1）

- FR-6.1 `actions/checkout` → `builtin://checkout`（自控认证/shallow/submodule/LFS）。
- FR-6.2 `actions/cache`、`actions/upload-artifact`、`actions/download-artifact` → builtin，对接 MinIO/S3；cache 必须 repo-scoped。
- **AC**: 典型 Go CI（checkout + setup-go + cache + test）在无 GitHub Actions backend 的前提下完整跑通。

### FR-7 状态与历史（P0）

- FR-7.1 CRD 仅保存 active state；完成后 24–72h TTL GC。
- FR-7.2 PostgreSQL 保存全量历史：`repositories/workflow_runs/job_runs/step_runs/webhook_deliveries/schedules/artifacts/cache_entries/secrets/variables/environments/deployments`。
- FR-7.3 调度基于 PG `FOR UPDATE SKIP LOCKED` + Kubernetes reconciliation，第一版不引入消息队列。
- **AC**: 杀死 ci-server 后重启，进行中的 run 依据 PG + CRD 恢复调度，无重复 JobRun。

### FR-8 Provider 抽象（P0）

- FR-8.1 定义 `SourceProvider` 接口（Repository/GetFile/ResolveRef/CloneCredential/SetCommitStatus），CI Engine 不得感知代码来源。
- FR-8.2 GitHub App：短时效 installation token 用于 clone；Check Runs 回写每个 job 结果，target URL 指向 forgelet UI。
- **AC**: PR 页面可见每个 job 的 ✓/✗ 与跳转链接。

### FR-9 安全（P0）

- FR-9.1 Secrets：environment > repository > organization 优先级；PG 中 envelope encryption 存储（AES-256-GCM，Master Key 来自 K8s Secret 或 Vault）；只下发当前 Job 需要的 secrets。
- FR-9.2 Fork PR：无 secrets、无 deploy 凭证、restricted network、untrusted RunnerClass。
- FR-9.3 CD 不给 CI Job cluster-admin：deploy 走 Deployment Request → CD Controller（namespace 级 policy）；生产推荐 GitOps 路径。
- **AC**: fork PR 的执行环境中注入的 secret 集合为空（有测试断言）。

### FR-10 可观测性（P1）

- FR-10.1 日志链路：executor JSON → Alloy/FluentBit → Loki；UI 按 run/job/step 查询。
- FR-10.2 Prometheus 指标（队列深度、调度延迟、job 时长/成功率）；OpenTelemetry tracing。

### FR-11 第二/三阶段（P2，仅登记）

`job.container`、`services`（Service+Pod + DNS + HOST 注入）、Docker Action（Step Pod + PVC + BuildKit）、reusable workflow、environment approval、OIDC、permissions 渐进、Web UI、RBAC、multi-tenant、Gitea/GitLab provider。

## 5. Non-functional requirements

- NFR-1 单 k3s 集群即可部署（forgelet 自身也是 K8s workload）。
- NFR-2 控制面无状态化：崩溃恢复不依赖内存状态（依据 PG + CRD）。
- NFR-3 冷启动（webhook → Pod Running）P95 ≤ 30s（同集群镜像预热后）。
- NFR-4 所有外部交互（GitHub、PG、S3、Loki）可注入 fake 以测试。

## 6. Out of scope（V1）

- 不实现 GitHub Actions Runtime protocol 完整兼容（builtin 替代）。
- 不做 runner pool 管理、不做 Docker socket/DinD 支持。
- 不承诺 100% workflow 语法兼容；不支持字段显式报错。

## 7. Traceability

后续 spec 编号规划（落地时逐个建目录）：

| 后续 spec | 覆盖 |
|-----------|------|
| 0002-expression-engine | FR-3 |
| 0003-workflow-parser-compiler | FR-2 |
| 0004-crds-and-controller | FR-4 |
| 0005-executor-runtime | FR-5 |
| 0006-builtin-actions | FR-6 |
| 0007-github-provider | FR-1, FR-8 |
| 0008-state-and-scheduler | FR-7 |
| 0009-security-secrets | FR-9 |
| 0010-observability | FR-10 |
