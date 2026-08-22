# forgelet 架构与实现蓝图

> 状态：v0.1（实现前基线）。本文是权威架构文档；变更需通过 PR 并同步更新 `specs/`。

## 1. 目标与非目标

**目标**

- 直接运行 GitHub Actions 兼容的 workflow（`.github/workflows/*.yml`，路径不变）。
- Kubernetes-native：Kubernetes 本身就是 Runner Fleet，一个 Job = 一个 Pod。
- GitHub 仅作为 Source Provider（webhook、Git、Check Runs）。未来可换 Gitea/GitLab/Forgejo。
- CI 执行结果仍可回写 GitHub PR 页面（Check Runs）。

**非目标（明确排除）**

- 不做 GitHub Runner / ARC。
- 不暴露 Docker socket，不默认 DinD。
- 不把 Tekton 放到核心执行链上（借鉴其 CRD/controller 思想）。
- V1 不追求 100% GitHub Actions 兼容。

## 2. 总体架构

```
                           GitHub (只负责 Git Repository)
                              │  push / pull_request webhook
                              ▼
                    ┌──────────────────┐
                    │ GitHub Provider  │  Webhook / GitHub App Auth / Clone / Check Runs
                    └────────┬─────────┘
                             │ Normalized Event
                             ▼
┌───────────────────────────────────────────────────────┐
│                  CI Control Plane (ci-server)          │
│  Workflow Loader → YAML Parser → Expression Engine     │
│        → Workflow Compiler (matrix/needs/if/concurrency)│
│        → Workflow Scheduler → Job Scheduler            │
└─────────┬─────────────────────────────────────────────┘
          │ JobRun CRD
          ▼
┌───────────────────── Kubernetes ──────────────────────┐
│        ci-controller ── creates ── Job Pod             │
│              Job Pod 内 ci-executor (PID 1)             │
│              run / JS Action / Composite / builtin      │
│              Docker Action、Services → 独立 Pod          │
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
- `job.container:` 通过 initContainer 拷入静态 `ci-executor`（`CGO_ENABLED=0`），主容器即用户镜像，命令为 `/ci/ci-executor`。
- `services:` 不模拟 Docker 网络：每个 service 一个 Service+Pod，DNS 形如 `redis.ci-run-xxx.svc`，Executor 注入 `REDIS_HOST` 等；后续可加 localhost TCP proxy 提升兼容性。
- Docker Action：Executor 无 K8s 凭证，通过 `POST /internal/jobs/xxx/container-step` 请求控制面，由 ci-controller 创建 Step Pod；workspace 用 PVC 共享。`image: Dockerfile` 走 BuildKit → 内部 OCI registry → Step Pod。
- Workspace：默认 emptyDir；Compiler 发现 Docker Action 自动切换 ephemeral PVC；Step Pod 固定调度到 Job Pod 同 Node 以兼容 RWO。

## 6. CRD（仅 3 个）

| CRD | 职责 |
|-----|------|
| `RunnerClass` | 基础设施 profile（镜像、resources、nodeSelector、workspace 模式、security） |
| `WorkflowRun` | 一次 workflow 执行（repo、sha、event、status） |
| `JobRun` | matrix 展开后的一次 Job（matrix、runnerClass、plan ID + digest） |

**CRD 只放 plan ID / digest / metadata / status，不放完整 plan 和 secret。**
Executor 启动后从控制面拉取真正执行计划。

`runs-on` 匹配 RunnerClass（支持 label 列表，如 `[linux, arm64, large]`），表示“要什么计算资源”，而不是“找哪台 runner”。

## 7. 状态与存储

- etcd/CRD = active execution state；PostgreSQL = long-term history。
- 完成 24–72h 后 GC CRD，PG 永久保留。
- PG 表：`repositories / workflow_runs / job_runs / step_runs / webhook_deliveries / schedules / artifacts / cache_entries / secrets / variables / environments / deployments`。
- 调度不引入消息队列：`SELECT ... WHERE status='queued' ORDER BY priority, created_at FOR UPDATE SKIP LOCKED`，配合 Kubernetes reconciliation。控制面崩溃后依据 PG + CRD 继续 reconcile。规模到 10K+ jobs/min 再考虑 NATS/Kafka。
- Artifact/Cache → MinIO/S3（repo scoped cache，防跨仓库污染）。

## 8. Provider 抽象与 GitHub 接入

```go
type SourceProvider interface {
    Repository(...) / GetFile(...) / ResolveRef(...) / CloneCredential(...) / SetCommitStatus(...)
}
```

- 第一版只实现 `GitHubProvider`，预留 Gitea/GitLab/Forgejo。
- GitHub 接入用 **GitHub App**（不用 PAT）：webhook、Git API、Check Runs、1h 有效期 installation clone token。
- Webhook 验签 `X-Hub-Signature-256`，用 `X-GitHub-Delivery` 去重。
- 通过 Check Runs 把每个 job 的结果回写 PR 页面，target URL 指向自家 UI。

## 9. ci-executor

静态 Go binary，职责单一：执行一个 GitHub Actions Job。

- 从控制面获取执行计划（run/job/repo/sha/steps/env/secrets）。
- step 类型：`Run` / `JavaScriptAction` / `CompositeAction` / `BuiltinAction` / `DockerAction`。
- 作为 PID 1：signal forwarding、timeout、cancellation、子进程清理。
- **GitHub File Commands 全套**：`GITHUB_ENV / GITHUB_OUTPUT / GITHUB_PATH / GITHUB_STATE / GITHUB_STEP_SUMMARY`，以及 `::add-mask:: ::warning:: ::error:: ::notice:: ::group:: ::endgroup::`。这是兼容性的关键。
- JS Action：Runner image 预置 node20/node24；Action Resolver 把 `owner/repo@ref` 解析到 commit SHA 并下载。
- Builtin Actions（第一版特殊处理）：`actions/checkout`、`actions/cache`、`actions/upload-artifact`、`actions/download-artifact`——它们依赖 GitHub Actions Runtime Service（`ACTIONS_RUNTIME_URL` 等），必须内置实现；后续再做 protocol 兼容。
- 日志：子进程 stdout → executor 结构化 JSON（含 run/job/step id）→ Alloy/FluentBit → Loki；UI 查询 Loki。Secret masking 在 Executor 落日志前完成（→ `***`）。

## 10. 安全

- Executor Pod：`automountServiceAccountToken: false`，无任何 K8s API 权限。
- Secrets：env > repo > org 优先级；PG 存储 ciphertext+nonce+key_version，envelope encryption（Master Key ← K8s Secret 或 Vault → DEK → AES-256-GCM）；只下发当前 Job 实际需要的 secrets。
- Fork PR：无 secrets、无 deploy 凭证、restricted network、untrusted RunnerClass；后续考虑 gVisor/Kata。
- CD 不给 CI Job cluster-admin：Job → Deployment Request → CD Controller → Kubernetes，带 namespace 级 policy；生产推荐 GitOps（CI 构建镜像 + 更新 GitOps repo → Argo CD/Flux）。
- Docker build：`ci/build-image@v1` → BuildKit Service → OCI registry，不走 Docker socket。

## 11. 模块与部署形态

逻辑模块：`ci-api / ci-webhook / ci-workflow(parser·compiler·expression·action-resolver) / ci-scheduler / ci-controller / ci-executor`。

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
  provider/ (provider.go, github/)
  webhook/
  workflow/ (parser/ ast/ compiler/ expression/ context/ matrix/)
  action/ (resolver/ javascript/ composite/ docker/ builtin/)
  scheduler/
  executor/ (runtime/ command/ filecommand/ env/ output/ mask/)
  kubernetes/
  artifact/ cache/ secret/ log/ database/
```

## 13. V1 兼容范围

| Feature | V1 |
|---------|----|
| push / pull_request / workflow_dispatch / schedule | ✅ |
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
