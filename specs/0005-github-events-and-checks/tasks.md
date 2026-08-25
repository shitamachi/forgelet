# Tasks — Spec 0005 GitHub Events and Checks

- [x] T1 `internal/report`：Check 类型、Reporter port、状态映射纯函数（FR-G3.1）
- [x] T2 `internal/provider/github` webhook：签名验证 + push 解码 + 删除 push 语义（FR-G1.1/1.3/1.4）
- [x] T3 webhook handler：验签 → Ingest 去重 → ProviderPayload 保留（FR-G1.2）
- [x] T4 GitHub App auth：RS256 JWT + installation token + 缓存 TokenSource（FR-G2）
- [x] T5 Check Run adapter：按 external_id 幂等 upsert、details_url、生命周期（FR-G3.2/3.3）
- [x] T6 AC-M0 测试矩阵（签名/重放/payload 一致/映射/生命周期）
- [x] T7 `pull_request` 解码 + fork 判定字段（P1）；server 信任分级与 e2e
- [x] T8 rerequest → 新 attempt 追踪（FR-8.4，P1）—— `check_run:rerequested` → `RerequestJob`（`attempt+1`，run 重开，`external_id` 幂等）+ `plan`/`trust` 复制
- [x] T9 chi 路由已接入 `cmd/server`
