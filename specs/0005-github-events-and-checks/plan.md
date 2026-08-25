# Plan — Spec 0005 GitHub Events and Checks

- **Status**: complete
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner
- **Completed**: 2026-08-24, v1-wave8 实现并通过验证
- **Spec**: `specs/0005-github-events-and-checks/spec.md`（proposed）
- **Boundaries**: `docs/module-boundaries.md`（provider/github 是 adapter，可依赖 application
  port；report 定义 port；核心模块无 SDK 依赖）

## 1. Package layout

```
internal/report/report.go        Check 值、Reporter port、状态映射纯函数
internal/provider/github/
  webhook.go                     VerifySignature + DecodePush（纯函数）
  webhook_handler.go             http.Handler：验签 → 去重入口 → Ingest
  auth.go                        App JWT（RS256）+ installation token + TokenSource port
  check.go                       report.CheckReporter 的 GitHub REST adapter
```

依赖：provider/github → run/model、run/scheduler（Ingest 端口）、report；report → run/model。
不引入 go-github SDK；REST 调用 net/http + json。

## 2. Webhook 协议

- Header：`X-Hub-Signature-256: sha256=<hex>`、`X-GitHub-Delivery`、`X-GitHub-Event`。
- Handler 流程：读 body → 验签（constant-time）→ event=push 且非删除 → `DecodePush` →
  `model.Delivery{Key, Event, Payload: rawBody}` → IngestPort.Ingest → 200
  `{"runId":..,"created":..}`。签名失败 403；畸形 400；删除 push / 未知事件 200 ignored
  （删除 push 不落 delivery；未知事件落 delivery 回执但不 compile）。
- IngestPort 在 provider/github 内定义（consumer-side）：`Ingest(ctx, model.Delivery) (RunID, bool, error)`。

## 3. App auth

```go
type TokenSource interface { Token(ctx) (string, error) }   // Authorization: Bearer <token>
type AppAuth struct{ AppID, InstallationID int64; Key *rsa.PrivateKey;
                     BaseURL string; HTTP *http.Client; Now func() time.Time }
InstallationToken(ctx) (token, expiresAt, error)
```

JWT：header{alg:RS256,typ:JWT}，claims{iss:AppID, iat:now-60s, exp:now+5min}；SHA-256 +
PKCS1v15 签名。POST `{BaseURL}/app/installations/{id}/access_tokens`（Accept:
application/vnd.github+json）。缓存 token 至 expiresAt-60s（内存缓存，并发安全）。

## 4. Check reporting

```go
type Check struct { RunID, JobRunID, Name, ExternalID, DetailsURL string;
                    Status CheckStatus; Conclusion CheckConclusion;
                    StartedAt, CompletedAt *time.Time }
type CheckReporter interface { Report(ctx, Check) error }
MapJobRun(job model.JobRunRecord, detailsBase string) Check
```

映射：queued→status=queued；dispatched/running→in_progress；succeeded→success；failed→failure；
cancelled→cancelled。`ExternalID = jobrun ID`；`DetailsURL = base + /runs/{run}/jobs/{jobkey}`。

GitHub adapter upsert：GET `/repos/{o}/{r}/commits/{sha}/check-runs`（按 external_id 过滤）→
命中 PATCH `/repos/{o}/{r}/check-runs/{id}`，未命中 POST `/repos/{o}/{r}/check-runs`。
请求体含 name/head_sha/external_id/details_url/status[/conclusion][started_at/completed_at]。
repo/sha 取自 RunRecord —— Reporter 接口扩展 `Report(ctx, run model.RunRecord, check Check)`，
避免在 Check 中复制 github 上下文。

## 5. Testing strategy

- 纯函数表驱动：签名（含大小写/前缀畸形）、push 解码 fixture、删除 push、状态映射。
- handler：httptest + memory durable store + fake compiler；重放去重、payload 字节一致、
  各类 4xx/2xx-ignored。
- auth：httptest fake API 校验 JWT（解码 header/claims、过期拒签）、token 缓存刷新、401 错误。
- check：httptest fake API 计数 create/patch，断言 external_id、details_url、幂等 upsert。
- 覆盖率目标 ≥ 80%。
