# Tasks — Spec 0003 Security: Identity and Secrets

- [x] T1 `internal/security/identity`：Identity/scope 常量、Issuer/Verifier port、
      LocalIssuer/LocalVerifier（HMAC，dev/test-only）、NonceCache + 内存实现（FR-S1）
- [x] T2 `internal/security/secret`：Keyring port + StaticKeyring、Sealed、Cipher
      （Seal/Open/Rewrap、AAD 绑定）（FR-S2）
- [x] T3 `internal/security/policy`：AuthorizeExecution、DecideSecrets 纯函数（FR-S3）
- [x] T4 AC-M0 测试矩阵：token 拒绝面、篡改面、rotation、交集/fork/scope、nonce 重放
- [ ] T5 TokenReview/JWKS verifier adapter（envtest/真实 k3s，与 0004/0011 联动）（V1）
- [ ] T6 K8s Secret / Vault keyring adapter；PG secrets 表与迁移（V1）
- [ ] T7 secrets 管理 API/CLI（set/list/delete、key rotation 运维流程）（V1）
