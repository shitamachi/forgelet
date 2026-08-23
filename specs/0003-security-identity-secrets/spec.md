# Spec 0003 — Security: Identity and Secrets

- **Status**: proposed
- **Date**: 2026-08-23
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted）
- **Covers**: FR-5.1、FR-9.1、FR-9.2、FR-9.3；FR-9.4 的 deny 语义（fork 分类输入属 0005）
- **Out of scope here**: Pod 内 token 的投影与 TokenReview/JWKS 的 Kubernetes adapter（0004/0011
  联调）、fork PR 信任判定的事件来源（0005）、CD/GitOps 权限路径（FR-9.5，P2）、secrets 管理
  API/CLI（V1）

## 1. Problem

Executor 运行不可信的用户代码，却必须从控制面获取 Plan、解析后的 secret 值并上报状态。这要求：

1. 一个不能被其他 Pod、其他 JobRun、过期或错误 audience 的凭据冒用的 workload identity；
2. secret 的静态加密（envelope encryption）与按需最小化下发；
3. 明文 secret 永不出现在 CRD、持久化 Plan、digest、日志或错误信息中。

本 spec 定义这三个安全内核的契约与纯逻辑实现；Kubernetes 侧的 token 投影与 TokenReview
adapter 在 0004/0011 以 envtest/真实 k3s 验证。

## 2. Requirements

### FR-S1 Executor workload identity（FR-9.1、FR-5.1）

- FR-S1.1 `[P0]` Identity 以短时效 token 呈现，绑定以下全部字段：audience
  （`forgelet-control-plane`）、namespace、Pod UID、JobRun ID、允许接口（scopes）、过期时间
  （≤ 1h）与签发时间。
- FR-S1.2 `[P0]` 验证必须检查签名、audience、有效期与签发时间；验证产物（Identity）携带全部
  绑定字段供授权与审计使用。时钟注入，容差可配置。
- FR-S1.3 `[P0]` 授权阶段必须核对 Identity 的 JobRun 绑定与请求的 JobRun 一致，且 scope 覆盖
  所调用的接口；不匹配即拒绝（可识别错误，不含 secret 值）。
- FR-S1.4 `[P0]` token 在有效期内的重放只能触发幂等操作（Plan 获取、状态上报本身幂等）；
  防重放 nonce 缓存定义为 port，M0 提供内存实现并接入协议测试。
- FR-S1.5 `[P1]` 生产 verifier 是 TokenReview/JWKS adapter（projected ServiceAccount token）；
  本 spec 的本地签名 token issuer/verifier 仅用于开发与测试，并以此显式标注。

### FR-S2 Secret 静态加密（FR-9.3）

- FR-S2.1 `[P0]` envelope encryption：每条 secret 使用随机 DEK，载荷以 AES-256-GCM 加密，
  DEK 以版本化 master key 包裹。持久化字段仅含：nonce、ciphertext、wrapped DEK、master key
  version——不含明文与未包裹的 DEK。
- FR-S2.2 `[P0]` AES-GCM 的 AAD 绑定 secret 身份（scope + name）：密文在不同 secret 间互换
  必须解密失败。
- FR-S2.3 `[P0]` key rotation：任意保留版本可解密；加密/重包裹始终使用 current 版本；rotation
  是显式操作（rewrap），旧版本在退役前保持可读；退役后 encrypt 失败、已封数据仍可读直至数据
  迁移。
- FR-S2.4 `[P0]` master key provider 是 port：M0 提供 static provider（配置注入），K8s
  Secret/Vault adapter 后续接入。key material 不得出现在日志与错误信息中。

### FR-S3 下发授权（FR-9.3、FR-9.4、FR-9.2）

- FR-S3.1 `[P0]` 只有当前 Job Plan 实际引用的 secret 才允许解析下发；请求集合与 Plan 引用
  集合取交集，交集外的请求必须拒绝并给出原因（仅含 secret 名，不含值）。
- FR-S3.2 `[P0]` 授权输入包含信任级别（trusted / same-repo / fork）；`fork` 级别 deny-all
  （无 secret、无 deploy credential）。
- FR-S3.3 `[P0]` 判定是纯函数：不读时钟、不做 I/O；结果可表驱动测试。
- FR-S3.4 `[P0]` 明文 secret 值只允许出现在解析下发通道与内存中：不得进入 CRD、持久化结构、
  Plan digest、日志、错误字符串或 trace（以测试断言错误与结构序列化结果）。

## 3. Acceptance criteria

**AC-M0**（全部自动化测试）：

1. 错误 audience、过期、未来签发、错误签名、其他 JobRun、其他 Pod UID 的 token 在验证或授权
   层被拒绝，且错误信息不含 secret 值（FR-S1）。
2. 载荷篡改、wrapped DEK 篡改、AAD 互换（同名不同 scope / 不同名）均解密失败；rotation 后
   旧密文可读、新封数据用新版本（FR-S2）。
3. 请求 ⊄ Plan 引用 → 拒绝交集外项；fork → 全拒；scope 缺失 → 全拒（FR-S3）。
4. 防重放 nonce 缓存：同一 nonce 二次出现被拒（FR-S1.4）。

**AC-V1**：TokenReview/JWKS adapter 在 envtest/真实 k3s 通过；secrets 管理 API；K8s Secret /
Vault keyring adapter。

## 4. Design notes（非约束）

- `identity`、`secret`、`policy` 三个 package 保持纯逻辑（仅标准库 crypto），port 由 server
  wiring 在 0005/0008 接线；本地 HMAC token 明确标注 dev/test-only。
