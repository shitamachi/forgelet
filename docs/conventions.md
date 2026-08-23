# 工程约定（Go）

本文是 `AGENTS.md` 中编码规则的展开，评审时按此执行。

## 项目布局

- 仓库采用 modular monorepo，初期使用单一 Go module；完整边界见 `docs/module-boundaries.md`。
- 遵循标准 Go 布局：`cmd/`（composition roots）、`api/`（CRD 公共类型）、`internal/`（非公开模块）。
- `internal/workflow/**` 是纯逻辑（parser/compiler/expression），**不得 import** k8s.io、数据库、HTTP client——保持可独立单测。
- 依赖方向：`cmd → adapter/application → domain/pure logic`。禁止反向 import、内部包循环依赖，
  也禁止用 `common/shared/helpers/utils` 绕过模块归属。
- 每个 `cmd/*` 只负责配置、依赖装配、启动和优雅退出，不承载业务逻辑。
- 新增第二个 `go.mod` 前必须有 accepted spec/ADR；多 module 本地开发使用根 `go.work`。

## Go 风格

- Go >= 1.27；坚持 `gofmt` + `golangci-lint`（配置见仓库根 `.golangci.yml`）。
- 错误：`fmt.Errorf("read workflow %s: %w", path, err)`；哨兵错误用 `errors.Is/As`。
- 禁止 `panic` 于非 `main`/测试代码；禁止 `init()` 副作用；依赖在 `main` 显式 wire。
- Context 显式传递；库代码内不得出现 `context.Background()`。
- 并发：优先串串的顺序代码；需要并发时用 `errgroup`；所有阻塞操作可被 cancel。
- 接口定义在使用方（consumer-side），不在实现方。
- 时间、随机数、ID 和外部 I/O 在需要确定性测试的模块中显式注入；禁止隐藏的全局 clock/client。

## 测试

- Table-driven；`testdata/` 放 fixture（workflow yml、webhook payload 等）。
- `internal/workflow/**` 与 `internal/run/scheduler/**` 覆盖率 ≥ 85%，其余 ≥ 60%。
- 表达式引擎必须有 GitHub 官方文档逐函数的用例集（参考 `expression/testdata/`）。
- Bug fix 必须附回归测试。
- 跨 PG/Kubernetes 的流程必须覆盖每个非原子故障窗口、重试和重复事件。
- Controller 使用 envtest/fake client 做 API 行为测试，并用真实 k3s 版本做最小集成测试。

## 兼容性基线

- Workflow 语法以 GitHub Actions 官方文档为准；不支持的字段要**显式报错**而非静默忽略（parser 层）。
- Executor 的 file commands 语义以 GitHub runner 源码行为为准，不确定的行为写成测试用例固化。

## 提交与分支

- Conventional Commits：`feat|fix|docs|spec|refactor|test|chore:`。
- 分支 `spec/NNNN-desc` / `fix/...` / `chore/...`；一个 spec 的实现可拆多个 PR，但每个 PR 必须独立可构建、测试通过。
