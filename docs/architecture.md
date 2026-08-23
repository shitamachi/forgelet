# forgelet 架构与实现蓝图

> 状态：v0.2（Spec 0001 accepted 后的实现前基线）。本文是权威架构文档；变更需通过 PR
> 并同步更新 `specs/`。模块边界的权威说明见 `docs/module-boundaries.md`。

## 1. 目标与非目标

**目标**

- 直接运行 GitHub Actions 兼容的 workflow（`.github/workflows/*.yml`，路径不变）。
- Kubernetes-native：Kubernetes 本身就是 Runner Fleet，一个 JobRun = 一个 Primary Pod。
- GitHub 仅作为 Source Provider（webhook、Git、Check Runs）。未来可换 Gitea/GitLab/Forgejo。
- CI 执行结果仍可回写 GitHub PR 页面（Check Runs）。
- 默认采用 replacement mode：仓库禁用 GitHub Actions 原生执行，由 forgelet 解释同一路径下的 workflow。

**非目标（明确排除）**

- 不做 GitHub Runner / ARC。
- 不暴露 Docker socket，不默认 DinD。
- 不把 Tekton 放到核心执行链上（借鉴其 CRD/controller 思想）。
- V1 不追求 100% GitHub Actions 兼容。
- 不承诺在 GitHub Actions 原生执行开启时自动避免重复运行；coexistence mode 必须显式启用。

## 2. 总体架构

```
               GitHub webhooks       forgelet API/CLI       internal cron
                 push / PR          manual dispatch          scheduler
                       └───────────────┬──────────────────────┘
                                       ▼
                    ┌──────────────────┐
                    │  Event Gateway   │  verify / dedupe / normalize + raw payload
                    └────────┬─────────┘
                             │ Normalized Event
                             ▼
┌───────────────────────────────────────────────────────┐
│                  CI Control Plane (ci-server)          │
│  GitHub Provider → Workflow Loader → YAML Parser        │
│                  → Expression Engine                    │
│        → Workflow Compiler (matrix/needs/if/concurrency)│
│        → Workflow Scheduler → Job Scheduler            │
└─────────┬─────────────────────────────────────────────┘
          │ JobRun CRD
          ▼
┌───────────────────── Kubernetes ──────────────────────┐
│        ci-controller ── creates ── Job Pod             │
│              Job Pod 内 ci-executor (PID 1)             │
│              run / JS Action / Composite / builtin      │
│              Docker Action、Services → 辅助 Pod（P2）     │
└───────────────────────────────────────────────────────┘
          │                │                │
          ▼                ▼                ▼
      PostgreSQL         MinIO            Loki
      metadata        artifact/cache       logs
```

**核心设计原则：Workflow Engine 与 Kubernetes Runtime 分离。**
不是“把 act 塞进 Pod 里跑”，而是控制面把 workflow 编译成 Kubernetes 可调度的资源。

## 3. Workflow 编译管线

```
YAML → AST → Workflow IR → Compiled Workflow → Job DAG → JobRun
```

- 绝不让 YAML AST 直接参与调度。
- Compiler 展开 `strategy.matrix`（如 `test[go=1.27,os=arm64]`），真正调度的是 JobInstance。
- `needs` 构成 DAG；`if` / `concurrency` 在调度期判定。

内部 IR（示意）：

```go
type Workflow struct {
    Name        string
    Triggers    []Trigger
    Env         map[string]Expression
    Concurrency *Concurrency
    Jobs        map[string]*Job
}

type Job struct {
    ID, Needs, If, RunsOn, Strategy, Env, Container,
    Services, Steps, Timeout, ContinueOnError
}
```

## 4. Expression Engine

单一 Go package，两阶段求值：

| 阶段 | 求值方 | 例子 |
|------|--------|------|
| Scheduler-time | 控制面 | `if: github.ref == 'refs/heads/master'` |
| Runtime | Executor | `if: steps.test.outputs.xxx == '123'` |

支持 contexts：`github / env / vars / secrets / runner / matrix / strategy / needs / jobs / steps / inputs`；
函数：`success() failure() cancelled() always() contains() startsWith() endsWith() format() join() toJSON() fromJSON() hashFiles()` 等。

实现参考 act `exprparser` 的思想/部分代码，但不 `import act/pkg/runner` 形成长期依赖；语法校验参考 actionlint。

## 5. Kubernetes Runtime

- **一个 Job 内所有普通 step 共用同一个容器**（filesystem / PATH / ENV / tool cache / workspace），保证 GitHub Actions 兼容性。
- 每个 JobRun 固定拥有一个主执行 Pod。JavaScript、Composite、Builtin 和普通 `run` step 在主容器中执行；
  Service Pod 与 Docker Action Step Pod 是 P2 的显式辅助资源，不计入“一个主执行 Pod”不变量。
- `job.container:` 通过 initContainer 拷入静态 `ci-executor`（`CGO_ENABLED=0`），主容器即用户镜像，命令为 `/ci/ci-executor`。
- `services:` 不模拟 Docker 网络：每个 service 一个 Service+Pod，DNS 形如 `redis.ci-run-xxx.svc`，Executor 注入 `REDIS_HOST` 等；后续可加 localhost TCP proxy 提升兼容性。
- Docker Action：Executor 无 K8s API 权限，通过受 JobRun-scoped 身份保护的控制面接口请求创建
  Step Pod；workspace 用 PVC 共享。`image: Dockerfile` 走 BuildKit → 内部 OCI registry → Step Pod。
- Workspace：默认 emptyDir；Compiler 发现 Docker Action 自动切换 ephemeral PVC；Step Pod 固定调度到 Job Pod 同 Node 以兼容 RWO。

## 6. CRD（仅 3 个）

| CRD | 职责 |
|-----|------|
| `RunnerClass` | 基础设施 profile（镜像、resources、nodeSelector、workspace 模式、security） |
| `WorkflowRun` | 一次 workflow 执行（repo、sha、event、status） |
| `JobRun` | matrix 展开后的一次 Job（matrix、runnerClass、plan ID + digest） |

**CRD 只放 plan ID / digest / metadata / status，不放完整 plan 和 secret。** Plan 本身持久化 secret
引用而非解析后的明文值；plan digest 不包含 secret 明文。
Executor 启动后从控制面拉取真正执行计划。

`runs-on` 匹配 RunnerClass（支持 label 列表，如 `[linux, arm64, large]`），表示“要什么计算资源”，而不是“找哪台 runner”。

## 7. 状态与存储

- PostgreSQL 是 durable scheduling、幂等键和长期历史的事实来源；etcd/CRD 是 active Kubernetes
  execution 的期望/观测状态来源。两者职责不重叠为同一字段的并列真相。
- 调度事务先在 PG 写入 immutable run/job ID 与唯一执行键；reconciler 使用由 JobRun ID 派生的
  确定性 CR 名称执行 create-or-get。PG 已提交但 CR 创建失败时可重试；CR 已存在但 PG 未记录 UID
  时，下次 reconcile 读取既有对象并补记，禁止另建副本。
- ci-controller 只更新 CRD observed status，并通过 control-plane port 幂等投影 durable status；
  controller 不直接 import PostgreSQL adapter。终态被 PG 确认前不得 GC CRD。
- 完成且 durable projection 已确认后 24–72h GC CRD，PG 按 retention policy 保留历史。
- PG 表：`repositories / workflow_runs / job_runs / step_runs / webhook_deliveries / schedules / artifacts / cache_entries / secrets / variables / environments / deployments`。
- 调度不引入消息队列：PG 锁只负责领取 durable intent，所有 PG↔Kubernetes 跨存储操作都按
  at-least-once 执行并依赖唯一键、resource ownership 与 reconciliation 收敛。规模到 10K+ jobs/min
  再评估 NATS/Kafka，消息队列也不能替代幂等状态机。
- Artifact/Cache → MinIO/S3（repo scoped cache，防跨仓库污染）。

首批状态 spec 必须覆盖四个故障窗口：PG commit 后创建 CR 前、创建 CR 后回写 PG 前、CR status
变化后投影 PG 前、PG 已确认终态后 GC CR 前。

## 8. Provider 抽象与 GitHub 接入

- Provider 按 consumer-side capability 拆分：repository reader、ref resolver、clone credential issuer、
  webhook decoder 与 check reporter；不预先固定一个包含所有方法的“大接口”。
- Check Reporter 必须表达 create/update/complete/rerequest 生命周期，使用 Check Run 的
  `details_url` 与 `external_id`，不能用 Commit Status 的 `target_url` 模型代替。
- 第一版只实现 GitHub adapters，预留 Gitea/GitLab/Forgejo。
- GitHub 接入用 **GitHub App**（不用 PAT）：webhook、Git API、Check Runs、1h 有效期 installation clone token。
- Webhook 验签 `X-Hub-Signature-256`，用 `X-GitHub-Delivery` 去重。
- 通过 Check Runs 把每个 job 的结果回写 PR 页面，`details_url` 指向自家 UI。

事件进入系统时同时保留两层数据：

- `NormalizedEvent`：provider-neutral 的 repo、ref、sha、actor、event name、trigger time 等调度字段；
- `ProviderPayload`：不可变 raw JSON + provider + delivery ID。GitHub context builder 用它生成兼容的
  `github.event`，核心模块只依赖数据结构，不依赖 GitHub SDK 类型。

触发入口分三类：

- `push` / `pull_request`：GitHub App webhook；
- `workflow_dispatch`：replacement mode 由 forgelet API/CLI 创建；coexistence mode 可选接收
  GitHub App webhook，但必须标识并接受 GitHub 原生 workflow 同时运行的风险；
- `schedule`：forgelet 读取默认分支 workflow 后注册内部 cron，不存在 GitHub `schedule` webhook。

## 9. ci-executor

静态 Go binary，职责单一：执行一个 GitHub Actions Job。

- 从控制面获取执行计划（run/job/repo/sha/steps/env/secret references），通过同一 JobRun-scoped
  授权按需获取允许的 secret 值。
- step 类型：`Run` / `JavaScriptAction` / `CompositeAction` / `BuiltinAction` / `DockerAction`。
- 作为 PID 1：signal forwarding、timeout、cancellation、子进程清理。
- **GitHub File Commands 全套**：`GITHUB_ENV / GITHUB_OUTPUT / GITHUB_PATH / GITHUB_STATE / GITHUB_STEP_SUMMARY`，以及 `::add-mask:: ::warning:: ::error:: ::notice:: ::group:: ::endgroup::`。这是兼容性的关键。
- JS Action：Runner image 预置 node20/node24；Action Resolver 把 `owner/repo@ref` 解析到 commit SHA 并下载。
- Builtin Actions（第一版特殊处理）：`actions/checkout`、`actions/cache`、`actions/upload-artifact`、`actions/download-artifact`——它们依赖 GitHub Actions Runtime Service（`ACTIONS_RUNTIME_URL` 等），必须内置实现；后续再做 protocol 兼容。
- 日志：子进程 stdout → executor 结构化 JSON（含 run/job/step id）→ Alloy/FluentBit → Loki；UI 查询 Loki。Secret masking 在 Executor 落日志前完成（→ `***`）。

Executor 拉取 Plan/secret 和上报状态时使用专用 workload identity：Pod 继续设置
`automountServiceAccountToken: false`，同时显式投影短时效、`audience=forgelet-control-plane` 的
ServiceAccount token。该 ServiceAccount 不授予 Kubernetes RBAC；控制面验证 token，并把
namespace、Pod UID、JobRun、允许接口和过期时间绑定到授权上下文。这里的 token 仅是 forgelet
身份，不得被 Kubernetes API 当作有效授权使用。0003 spec 必须固化具体 TokenReview/JWKS 验证、
轮换和防重放方案。

## 10. 安全

- Executor Pod：`automountServiceAccountToken: false`，无任何 K8s API 权限；只显式投影面向
  forgelet control plane 的 audience-bound、短时效 workload identity。
- Secrets：environment > repository > organization 优先级；PG 存储 ciphertext+nonce+key_version，
  envelope encryption（Master Key ← K8s Secret 或 Vault → DEK → AES-256-GCM）。Plan 只持久化
  secret reference，控制面只向经过 JobRun-scoped 授权的 Executor 下发当前 Job 需要的值。
- Fork PR：无 secrets、无 deploy 凭证、restricted network、untrusted RunnerClass；后续考虑 gVisor/Kata。
- CD 不给 CI Job cluster-admin：Job → Deployment Request → CD Controller → Kubernetes，带 namespace 级 policy；生产推荐 GitOps（CI 构建镜像 + 更新 GitOps repo → Argo CD/Flux）。
- Docker build：`ci/build-image@v1` → BuildKit Service → OCI registry，不走 Docker socket。

## 11. 模块与部署形态

仓库采用 modular monorepo：spec、API、三个 Go binary、部署资产和未来 Web UI 同仓演进，初期共享
一个根 `go.mod`。模块化由 package/port/依赖方向保证，不通过提前拆多个 Go module 保证。

逻辑模块包括 `workflow`、`run`、`provider`、`report`、`runtime-controller`、`runtime-executor`、
`storage`、`security`、`observability`。完整 ownership 与允许依赖见 `docs/module-boundaries.md`。

第一版收敛为 **3 个 binary**：

```
cmd/server      = ci-api + ci-webhook + ci-scheduler
cmd/controller  = ci-controller
cmd/executor    = ci-executor
```

## 12. 目录结构

```
cmd/{server,controller,executor}/main.go
api/v1alpha1/{workflowrun,jobrun,runnerclass}_types.go
internal/
  workflow/{syntax,expression,compiler}/
  run/{model,scheduler,plan}/
  provider/github/
  report/
  runtime/{controller,executor}/
  storage/{postgres,object}/
  security/
  observability/
proto/   # ConnectRPC schemas，按 spec 创建
deploy/  # Helm/Kustomize/manifests，按 spec 创建
web/     # Web UI，进入对应里程碑后创建
```

默认不创建 `common/shared/helpers/utils`。新增多个 `go.mod` 需要独立 accepted spec/ADR；确需多
module 时使用根 `go.work`，并补齐各 module 的独立测试、版本和发布策略。

## 13. V1 兼容范围

| Feature | V1 |
|---------|----|
| push / pull_request webhook | ✅ |
| workflow_dispatch | ✅ forgelet API/CLI；GitHub webhook 仅 coexistence mode |
| schedule | ✅ forgelet internal scheduler |
| jobs / needs / if / matrix / env / secrets / run | ✅ |
| JS Action / Composite Action | ✅ |
| actions/checkout、cache、artifacts | ✅ Builtin |
| continue-on-error / timeout / outputs / concurrency | ✅ |
| job.container / services / Docker Action | 第二阶段 |
| reusable workflow / environment approval / OIDC | 第三阶段 |
| permissions | 渐进实现 |

## 14. 技术选型

Go · net/http+chi · ConnectRPC · PostgreSQL · controller-runtime+client-go · yaml.v3 ·
actionlint（校验思想）· 自研 expression engine（参考 act exprparser）· GitHub App ·
Loki · Prometheus · OpenTelemetry · MinIO/S3 · BuildKit · registry/Harbor。
明确拒绝：Docker socket ❌、默认 DinD ❌、GitHub Runner ❌、ARC ❌、Tekton 作为核心依赖 ❌。

## 15. 里程碑（实现顺序）

1. **M0 闭环**：GitHub push → webhook → 读 workflow → parse → compile DAG → JobRun → Pod → executor → run step → 日志入 Loki → Check Run → success。
2. M1：needs / if / matrix。
3. M2：JS Action / Composite Action。
4. M3：GITHUB_ENV / GITHUB_OUTPUT / pre/post。
5. M4：cache / artifact。
6. M5：job.container / services / Docker Action / BuildKit。
7. M6：Web UI / RBAC / multi-tenant / environment / deployment / OIDC / GitLab·Gitea。

每个里程碑对应一个或多个 spec（见 `specs/`）。

M0 只承诺 `push + runs-on + 单 job/多 run step` 的最小纵向闭环。总纲中的 P1/V1 能力不得被
解释为 M0 的完成条件；每个子 spec 必须分别列出 M0 slice 与完整 V1 slice。

## 16. 设计依据

- [GitHub webhook events and payloads](https://docs.github.com/en/webhooks/webhook-events-and-payloads)
- [GitHub workflow trigger semantics](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows)
- [GitHub contexts reference](https://docs.github.com/en/actions/reference/workflows-and-actions/contexts)
- [GitHub Checks API guide](https://docs.github.com/en/rest/guides/using-the-rest-api-to-interact-with-checks)
- [Kubernetes Service Accounts](https://kubernetes.io/docs/concepts/security/service-accounts/)
- [Go: managing module source](https://go.dev/doc/modules/managing-source)
- [Go multi-module workspaces](https://go.dev/doc/tutorial/workspaces)
