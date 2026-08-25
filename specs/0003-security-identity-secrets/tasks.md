# Tasks — Spec 0003 Security: Identity and Secrets

- [x] T1 `internal/security/identity`：Identity/scope 常量、Issuer/Verifier port、
      LocalIssuer/LocalVerifier（HMAC，dev/test-only）、NonceCache + 内存实现（FR-S1）
- [x] T2 `internal/security/secret`：Keyring port + StaticKeyring、Sealed、Cipher
      （Seal/Open/Rewrap、AAD 绑定）（FR-S2）
- [x] T3 `internal/security/policy`：AuthorizeExecution、DecideSecrets 纯函数（FR-S3）
- [x] T4 AC-M0 测试矩阵：token 拒绝面、篡改面、rotation、交集/fork/scope、nonce 重放
- [x] T5 TokenReview/JWKS verifier adapter（envtest/真实 k3s，与 0004/0011 联动）（V1）
      —— TokenReview adapter（Pod label 绑定源）+ `--executor-auth=tokenreview` 接线 +
      协议/授权链测试已落地；envtest/真实 k3s 验证随 0011 T8 已在烟雾中通过
- [x] T6 K8s Secret/Vault keyring adapter；PG `secrets` 表与迁移（V1）—— `FileKeyring`（K8s Secret 挂载文件，hex/raw 32B）+ `postgres.SecretStore`（`Seal`/`Open` 封存，`ON CONFLICT` upsert，AAD 绑定）+ `server.SecretStore` 回退链与 `--secret-key-file` 接线
- [x] T7 secrets 管理 API/CLI（set/list/delete、key rotation 运维流程）（V1）—— `POST/GET/DELETE /api/secrets` + `cmd/secretctl`（`set`/`list`/`delete`，`--value-file`/stdin）+ `memStore` 测试
