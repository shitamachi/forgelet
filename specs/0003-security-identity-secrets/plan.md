# Plan — Spec 0003 Security: Identity and Secrets

- **Status**: complete
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner
- **Completed**: 2026-08-24, v1-wave7 实现并通过验证
- **Spec**: `specs/0003-security-identity-secrets/spec.md`（proposed）
- **Boundaries**: `docs/module-boundaries.md`（security 模块；禁止依赖 workflow parser 具体实现）

## 1. Package layout

```
internal/security/identity/   Identity/claims、scopes、Issuer/Verifier port、
                              LocalIssuer/LocalVerifier（dev/test-only，HMAC-SHA256）、
                              NonceCache port + 内存实现
internal/security/secret/     Keyring port + static keyring、Sealed 记录、
                              Cipher（envelope AES-256-GCM + AAD 绑定 + rewrap）
internal/security/policy/     secret 下发授权纯函数（trust level、Plan 引用交集、绑定核对）
```

依赖：三个 package 仅依赖标准库与 `internal/run/model` 的 ID 类型；不 import k8s.io、数据库、
HTTP。Server 侧接线（TokenReview adapter、PG secret 表、控制面接口）在 0005/0008。

## 2. Identity contract

```go
type Identity struct {
    Audience  string          // 固定 "forgelet-control-plane"
    Namespace string
    PodUID    string
    JobRunID  model.JobRunID
    Scopes    []string        // plan:read | secrets:read | status:write
    TokenID   string          // 防重放 nonce
    IssuedAt, ExpiresAt time.Time
}
type Verifier interface { Verify(ctx, raw string) (Identity, error) }   // 签名+aud+exp+iat
type Issuer  interface { Issue(ctx, id Identity) (string, error) }
```

Local token = `base64url(canonical JSON claims) + "." + base64url(HMAC-SHA256)`；密钥注入；
`Verify` 校验签名、`exp > now`、`iat ≤ now+skew`、audience 等于配置值。授权层另行核对
JobRun/PodUID/scope（`policy`），使验证与授权职责分离。

NonceCache：`Claim(tokenID) (first bool)`，内存实现带 TTL 清理；同一 TokenID 二次 Claim 拒绝。
M0 仅对状态变更类调用启用（接线时决定）。

## 3. Envelope encryption

```go
type Key struct { Version uint32; Material []byte /* 32B */ }
type Keyring interface { Current() (Key, error); Key(v uint32) (Key, bool) }
type Sealed struct { Nonce, Ciphertext, WrappedDEK []byte; MasterKeyVersion uint32 }
type Cipher struct { ring Keyring; rng io.Reader }
Seal(plaintext, aad) / Open(sealed, aad) / Rewrap(sealed, aad)
AAD = "forgelet-secret\x00" + scope + "\x00" + name（由 SecretAAD 构造）
```

Seal：随机 DEK(32B) → AES-GCM(载荷) → master key AES-GCM 包裹 DEK（独立 nonce 并入 WrappedDEK
前缀）。Open：按 MasterKeyVersion 取 key，解包裹 DEK，再解开载荷；AAD 不符即失败。Rewrap：Open
后以 current 重新 Seal（新 nonce、新 DEK）。StaticKeyring 从配置构造，禁止重复版本。

## 4. Delivery authorization（纯函数）

```go
type TrustLevel string // trusted | same-repo | fork
type Ref struct{ Scope, Name string }
func AuthorizeExecution(id identity.Identity, jobRun model.JobRunID) error
    // JobRun 绑定核对
func DecideSecrets(id identity.Identity, requested, planRefs []Ref, trust TrustLevel) Decision
    // scope 检查 → fork deny-all → 交集；Decision{Allowed, Denied[]{Ref, Reason}}
```

拒绝原因只含 scope/name，不含值。`AuthorizeExecution` 与 `DecideSecrets` 由控制面在
Plan/secret 接口处串行调用。

## 5. Testing strategy

- identity：表驱动（audience/exp/iat/签名/解析错误）、nonce 缓存 TTL 与并发。
- secret：roundtrip、篡改（载荷/wrapped DEK/AAD 互换/版本缺失）、rotation（rewrap 后旧密文可读、
  新密文新版本、退役 key 不可 encrypt）、keyring 语义。
- policy：交集/fork/scope/绑定核对矩阵；错误信息不含值（构造值再断言子串）。
- 覆盖率目标 ≥ 85%（对齐 run 模块基线）。

## 6. Out of this plan

TokenReview/JWKS adapter（0004/0011 envtest）、K8s Secret/Vault keyring、PG secrets 表与控制面
HTTP/Connect 接口（0005/0008 接线时落地，复用本 plan 契约）。
