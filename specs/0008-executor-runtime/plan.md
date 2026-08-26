# Plan — Spec 0008 Executor Runtime

- **Status**: complete
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner
- **Completed**: 2026-08-24, v1-wave9 实现并通过验证
- **Spec**: `specs/0008-executor-runtime/spec.md`（proposed）
- **Boundaries**: `docs/module-boundaries.md`（runtime/executor：禁止 Kubernetes client、
  provider SDK、直接访问 PG）

## 1. Package layout

```
internal/runtime/executor/
  port.go            ControlPlane port + JobResult/StepResult
  engine.go          step 执行引擎（进程组、取消、超时、file commands 接线）
  mask/              SecretMasker + slog Handler 包装（写出前替换 ***）
  filecommand/       GITHUB_ENV/OUTPUT/PATH 解析（纯函数）
  command/           `::name params::message` workflow command 解析（纯函数）
  httpclient/        ControlPlane 的 HTTP adapter（Bearer token）
cmd/executor/main.go PID 1 入口（flag/env 装配 + SIGTERM→cancel）
```

依赖：executor → run/plan、security/identity（仅类型）、run/model；mask/filecommand/command
纯标准库；httpclient → net/http。不 import k8s.io / provider / storage。

## 2. Engine

```go
type Engine struct {
    CP ControlPlane; WorkDir, Shell string; Grace time.Duration
    Logger *slog.Logger; DefaultEnv map[string]string
}
func (e *Engine) Run(ctx, id identity.Identity, p plan.Plan) (JobResult, error)
```

流程：FetchSecrets（仅 plan refs）→ masker 注册 + ENV 注入 → 基础 ENV（plan.Env、
GITHUB_SHA/REF/JOB、CI=true）→ 逐 step：脚本落盘 `.forgelet/step-<n>.sh` →
`bash -e -o pipefail <file>`（进程组）→ stdout/stderr 逐行：command.Parse →
add-mask 即刻注册 → masked logger 输出 → 处理 env/output/path 文件 → 失败即止 →
ReportJob。

取消：`SysProcAttr.Setpgid`；watcher goroutine 监听 ctx.Done → `kill(-pgid, SIGTERM)` →
grace 后 SIGKILL。timeout 由调用方 ctx 控制（main 装配 WithTimeout）。

## 3. mask

```go
type Masker struct{ vals []string }  // 长度降序替换
func (m *Masker) Add(v string); func (m *Masker) Apply(s string) string
type Handler struct{ inner slog.Handler; m *Masker }  // msg 与 string attrs 先 Apply
```

空串不注册（否则把所有日志变 ***）。Handler 是并发安全的只读快照（Add 后新值即刻生效，
采用 mutex 保护 slice）。

## 4. filecommand / command（纯函数）

- `ParseKVFile(data []byte) (kvs map[string]string, order []string, err error)`：
  `NAME=value` 与 `NAME<<DELIM\n...\nDELIM`；ENV/OUTPUT 共用。
- `ParsePathFile(data []byte) []string`。
- `Parse(line string) (Command, bool)`：`::name p1=v1,p2=v2::message`；name 小写归一；
  已知 name 集合 add-mask/group/endgroup/warning/error/notice/debug。

## 5. HTTP adapter

- `NewClient(baseURL, token string, hc *http.Client)`；错误统一 `ClientError{Op, Status}`，
  401/403/404/5xx 类型化；请求体只含 refs（scope+name），绝不含值；错误信息只含状态与路径。
- fake server 测试 AC 6。

## 6. Testing strategy

- engine：真实 bash 执行（CI ubuntu 满足）；临时 WorkDir；表驱动 + 行为测试覆盖 AC 1–5；
  取消测试用 `sleep` 子进程验证进程组清理（等待退出，断言无孤儿需 ps —— 简化为断言命令在
  grace+缓冲内退出且返回 ctx.Canceled 类错误）。
- mask/filecommand/command：纯函数矩阵。
- httpclient：httptest 矩阵（AC 6）。
- 覆盖率 ≥ 80%。
