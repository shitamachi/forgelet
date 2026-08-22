# 工程约定（Go）

本文是 `AGENTS.md` 中编码规则的展开，评审时按此执行。

## 项目布局

- 遵循标准 Go 布局：`cmd/`（入口）、`api/`（CRD 公共类型）、`internal/`（其余一切）。
- `internal/workflow/**` 是纯逻辑（parser/compiler/expression），**不得 import** k8s.io、数据库、HTTP client——保持可独立单测。
- 依赖方向：`cmd → internal/scheduler|controller|executor → internal/workflow → （无）`。
  禁止反向 import、禁止内部包循环依赖。

## Go 风格

- Go >= 1.27；坚持 `gofmt` + `golangci-lint`（配置见仓库根 `.golangci.yml`，随首批代码一起落地）。
- 错误：`fmt.Errorf("read workflow %s: %w", path, err)`；哨兵错误用 `errors.Is/As`。
- 禁止 `panic` 于非 `main`/测试代码；禁止 `init()` 副作用；依赖在 `main` 显式 wire。
- Context 显式传递；库代码内不得出现 `context.Background()`。
- 并发：优先串串的顺序代码；需要并发时用 `errgroup`；所有阻塞操作可被 cancel。
- 接口定义在使用方（consumer-side），不在实现方。

## 测试

- Table-driven；`testdata/` 放 fixture（workflow yml、webhook payload 等）。
- `internal/workflow/**` 与 `internal/scheduler/**` 覆盖率 ≥ 85%，其余 ≥ 60%。
- 表达式引擎必须有 GitHub 官方文档逐函数的用例集（参考 `expression/testdata/`）。
- Bug fix 必须附回归测试。

## 兼容性基线

- Workflow 语法以 GitHub Actions 官方文档为准；不支持的字段要**显式报错**而非静默忽略（parser 层）。
- Executor 的 file commands 语义以 GitHub runner 源码行为为准，不确定的行为写成测试用例固化。

## 提交与分支

- Conventional Commits：`feat|fix|docs|spec|refactor|test|chore:`。
- 分支 `spec/NNNN-desc` / `fix/...` / `chore/...`；一个 spec 的实现可拆多个 PR，但每个 PR 必须独立可构建、测试通过。
