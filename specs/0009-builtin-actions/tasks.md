# Tasks — Spec 0009 Builtin Actions

- [x] T1 syntax：`uses:`/`with:` 解析、uses+run 冲突类型化错误（FR-A1.1/1.4）
- [x] T2 compiler：Registry 白名单（checkout/cache/upload-artifact/download-artifact）、
      输入校验与警告诊断、UnknownActionError；plan SecretRef 转换（FR-A1）
- [x] T3 plan：`BuiltinStep` 字段与 digest 兼容（FR-A4）
- [x] T4 executor：BuiltinContext/handler 表接入 step 循环；checkout handler（本地 git
       fixture 测试）（FR-A2）
- [ ] T5 server：cache/resolve 与 artifact URL 端点 + S3/MinIO presign 适配器 + repo-scope
      隔离集成测试（FR-A3）
- [ ] T6 executor：cache（restore-keys 回退、best-effort save）与 artifact 上传下载
      handler（FR-A3）
- [ ] T7 AC-V1 全链路 fixture：Go CI（checkout→cache→test）进 k3s smoke 可选阶段
