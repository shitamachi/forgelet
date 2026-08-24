# Plan — Spec 0009 Builtin Actions

- **Status**: complete
- **Date**: 2026-08-23
- **Approved**: 2026-08-23, project owner（与 spec 同批授权）
- **Completed**: 2026-08-24, v1-wave5 实现并通过验证

## 1. 总体形态

builtin 是**编译期展开、运行期内嵌执行**的 action：编译器把 `uses:` 解析为规范化的
`BuiltinCall`，Plan 携带其标识与 inputs；executor 内置 handler 表直接执行。无网络下载、
无额外 Pod、无 runner。

```
syntax(uses/with) → compiler(registry resolve) → plan.Step.Builtin → executor handler
                                                                        ↓ presigned URL
                                                          S3/MinIO ← (cache/artifact)
```

## 2. 接口与数据

### 2.1 syntax / compiler

- `syntax.Step` 增加 `Uses string`、`With map[string]string`；`uses`+`run` 并存、未知字段照常
  白名单报错。
- `compiler` 内注册表：

```go
type Builtin struct {
    Name        string            // "actions/checkout" 等，不带 ref
    Inputs      map[string]InputSpec // 类型 + 是否支持
}
var Registry = map[string]Builtin{ ... }   // checkout / cache / upload-artifact / download-artifact
```

- 编译产物 `Step.Builtin *BuiltinCall{Name, Version, Inputs map[string]string}`；
  未注册 action → `UnknownActionError`（含位置）；输入键不在 InputSpec → 警告诊断。
- `with:` 值中整段 `${{ secrets.NAME }}` 与 env 同规则转成 plan SecretRef
  （scope=repository），其余表达式保留到运行时插值。

### 2.2 plan

```go
type BuiltinStep struct {
    Action  string            // 规范名 "actions/checkout"
    Version string            // "v4"
    Inputs  map[string]string
}
// plan.Step 增加 Builtin *BuiltinStep；Builtin != nil 时 Run 为零值
```

Digest 自动覆盖新字段（canonical JSON）。

### 2.3 executor handler 表

```go
type BuiltinContext struct {   // handler 视角的世界
    Ctx       context.Context
    Workspace string                     // /workspace
    Event     EventInfo                  // repo owner/name、sha、ref、event_name
    Inputs    map[string]string          // 已插值的 with 值（secrets 已注入 env）
    Env       func(string) string        // 读当前环境
    SetOutput func(k, v string) error    // GITHUB_OUTPUT
    Logger    *slog.Logger               // 已接 mask
    CP        ControlPlane               // cache/artifact 需经控制面取 presigned URL
}
handler := func(BuiltinContext) error
engine.RegisterBuiltin(name, handler)     // 默认表内置四个；测试可覆盖
```

引擎 step 循环：`step.Builtin != nil` → 走 handler，不落脚本文件；outcome/conclusion、
`continue-on-error`、`if:` 与 run step 完全一致（FR-A4.3）。

### 2.4 cache/artifact 的存储通道：presigned URL

executor **永不持有** S3 凭据。控制面新增内部接口（复用 jobrun 绑定鉴权）：

```
POST /internal/jobruns/{id}/cache/resolve   {key, restoreKeys[]} →
      {hit bool, get string, put string}    // presigned GET/PUT，10 分钟有效
POST /internal/jobruns/{id}/artifacts/{name}  → {upload string}      （创建+上传 URL）
GET  /internal/jobruns/{id}/artifacts/{name}  → {download string}
```

对象 key 由服务端派生并强制 repository scope：
`{provider}/{owner}/{name}/cache/<sha256(key)>`、`.../artifacts/<runID>/<name>.tar.gz`。
客户端只见到 URL。server 由 `-s3-endpoint/-s3-bucket/-s3-access-key/-s3-secret-key`
配置 MinIO/S3 客户端；凭据同样可来自 SecretValues。

### 2.5 checkout

git CLI（主容器内已具备）：

1. `init` + `remote add origin https://<host>/<owner>/<repo>`（host 来自 event provider，
   V1 固定 github.com，GHE 后续）。
2. token 存在时注入 `http.extraheader=AUTHORIZATION: basic <b64(x-access-token:)>`
   的 credential 环境变量（仅子进程 env，不落盘）。
3. `fetch --depth=N origin <ref>` + `checkout FETCH_HEAD`；N=0 全量。
4. persist-credentials=false → 不写入任何 credential helper 文件。

## 3. 失败与安全

- cache save 失败仅告警（FR-A3.4）；restore 网络失败 = step 失败（可重试语义由调用方决定）。
- presigned URL 有效期 10 分钟且一次性 scope 到单对象；日志不得输出完整 URL（含签名），
  只输出对象 key。
- 所有 builtin 输入在编译期做白名单校验，运行期不再接受未声明输入。

## 4. 测试策略

- compiler：注册表命中/未命中/输入白名单/uses+run 冲突，表驱动。
- executor handler：本地 git fixture（真实 clone 本地 bare repo）验证 checkout 各分支语义；
  fakeCP 提供 presigned URL 指向 httptest server 验证 cache/artifact 上传下载与 restore-keys。
- server：cache/resolve 的 repo-scope 隔离与签名 URL 有效性（MinIO 集成测试，Docker）。
- e2e：Go CI fixture（checkout→cache→test）进 k3s-smoke 可选阶段。
