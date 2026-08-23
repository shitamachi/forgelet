# Tasks — Spec 0005 GitHub Events and Checks

- [x] T1 `internal/report`：Check 类型、Reporter port、状态映射纯函数（FR-G3.1）
- [x] T2 `internal/provider/github` webhook：签名验证 + push 解码 + 删除 push 语义（FR-G1.1/1.3/1.4）
- [x] T3 webhook handler：验签 → Ingest 去重 → ProviderPayload 保留（FR-G1.2）
- [x] T4 GitHub App auth：RS256 JWT + installation token + 缓存 TokenSource（FR-G2）
- [x] T5 Check Run adapter：按 external_id 幂等 upsert、details_url、生命周期（FR-G3.2/3.3）
- [x] T6 AC-M0 测试矩阵（签名/重放/payload 一致/映射/生命周期）
- [ ] T7 `pull_request` 解码 + fork 判定字段（P1）
- [ ] T8 rerequest → 新 attempt 追踪（FR-8.4，P1）
- [ ] T9 替换 chi 路由接入 cmd/server 装配（随 0008/0011 wiring）
