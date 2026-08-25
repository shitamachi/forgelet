# Spec 0005 — GitHub Events and Checks

- **Status**: implemented
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner
- **Implemented**: 2026-08-24, v1-wave8（T8 rerequest 全量交付并通过验证）
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted）
- **Covers**: FR-1.1、FR-1.2、FR-1.3（replacement 部分）、FR-1.5、FR-1.6；FR-8.1、FR-8.2、FR-8.3
  （FR-8.4 rerequest 为 P1 任务）
- **Depends on**: 0002（Ingestor/delivery 去重/ProviderPayload 持久化）
- **Out of scope here**: 内部 schedule（FR-1.4，0002 T9）、coexistence mode 的
  `workflow_dispatch` webhook、fork 信任判定（FR-9.4 输入侧，随 pull_request P1 落地）、
  workflow 文件读取（0006）、Check Run 的 UI 页面本身

## 1. Problem

M0 闭环的控制面入口与出口都在 GitHub：入口是 webhook（验签、去重、归一化、原始 payload 保留），
出口是 Check Run（create/update/complete 生命周期、external ID 关联、details_url）。同时 GitHub
App 的短时效 installation token 是 clone/API 的唯一凭据来源。核心模块不得依赖 GitHub SDK 类型。

## 2. Requirements

### FR-G1 Webhook 接入（FR-1.1、FR-1.5、FR-1.6）

- FR-G1.1 `[P0]` Webhook handler 必须先验证 `X-Hub-Signature-256`（HMAC-SHA256，constant-time），
  再读取 body 语义；签名不符返回 4xx，不产生任何 durable 写入。
- FR-G1.2 `[P0]` 以 `X-GitHub-Delivery` + provider 构成 delivery key，经 0002 Ingestor 做 durable
  去重：同一 delivery 重放只产生一个 WorkflowRun。
- FR-G1.3 `[P0]` `push` 事件解码为 provider-neutral `model.Event`（repo/ref/SHA/actor），原始
  payload 字节原样持久化（ProviderPayload，构造 `github.event` 的数据源）；解码器不引入
  GitHub SDK 类型。
- FR-G1.4 `[P0]` 分支删除 push（`after` 全零）不触发 run；无法识别的事件类型返回 2xx-ignored
  并保留 delivery 回执，不得让 GitHub 重试风暴。
- FR-G1.5 `[P1]` `pull_request` 事件解码（含 fork 判定字段，供 0003/0009 的 trust level）。

### FR-G2 GitHub App 凭据（FR-8.2）

- FR-G2.1 `[P0]` App 私钥签发 RS256 JWT（`iss=AppID`，exp ≤ 10min），换取 installation token；
  token 短时效（GitHub 默认 1h），到期自动刷新；时钟注入。
- FR-G2.2 `[P0]` TokenSource 是 consumer-side port；GitHub adapter 之外的模块不接触 JWT/私钥。
- FR-G2.3 `[P0]` API base URL 可配置（默认 api.github.com），测试与自托管（GitHub Enterprise）
  使用注入 base URL 与 http.Client。

### FR-G3 Check Reporting（FR-8.1、FR-8.3）

- FR-G3.1 `[P0]` Check 状态 port（`internal/report`）定义 Check 值与 Reporter 能力；状态映射是
  纯函数（durable job status → GitHub check status/conclusion）。
- FR-G3.2 `[P0]` Reporter 以 forgelet JobRun ID 作为 Check Run `external_id` 实现幂等 upsert：
  先按 head SHA + external_id 查找既有 Check Run，存在则 update，否则 create；同一 job 的多次
  上报不产生无法关联的重复 Check Run。
- FR-G3.3 `[P0]` `details_url` 指向 forgelet UI/API 页面（base URL 注入）；status 使用
  queued/in_progress + conclusion（success/failure/cancelled）生命周期。
- FR-G3.4 `[P0]` Reporter 不解析 secret、不在错误中携带 token；API 失败返回可重试错误。

## 3. Acceptance criteria

**AC-M0**（自动化，fake GitHub API / httptest）：

1. 签名验证：正确签名通过；篡改 body、错误 secret、缺失/畸形 header 拒绝且无 durable 写入。
2. 同一 delivery 重放两次：一个 run、第二次 created=false；payload 与发送字节完全一致。
3. 分支删除 push：2xx、无 run；未知事件类型：2xx ignored、delivery 回执保留。
4. AppAuth：JWT 头格式正确（RS256）、token 端点调用成功返回短时效 token；401 时返回错误。
5. CheckReporter：queued→in_progress→success 序列恰好产生 1 个 Check Run（create 后 update），
   `external_id`/`details_url` 正确；重复 Report 同状态幂等。
6. 状态映射矩阵（queued/dispatched/running/succeeded/failed/cancelled → check status/conclusion）。

**AC-V1**：pull_request + fork 字段、rerequest attempt 追踪、真实 GitHub App 联调。

## 4. Design notes（非约束）

- handler 为纯 `http.Handler`（net/http），chi 路由在 server 装配时引入；GitHub REST 调用用
  net/http + encoding/json，不引入 SDK。
