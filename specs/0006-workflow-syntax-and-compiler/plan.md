# Plan — Spec 0006 Workflow Syntax and Compiler

- **Status**: draft
- **Date**: 2026-08-23
- **Spec**: `specs/0006-workflow-syntax-and-compiler/spec.md`（proposed）
- **Boundaries**: `docs/module-boundaries.md`（workflow：纯逻辑，禁止 k8s/DB/HTTP/provider SDK）

## 1. Package layout

```
internal/workflow/syntax/     Diagnostic、Workflow AST 类型、Parse（yaml.Node 白名单遍历）
internal/workflow/compiler/   Compiled（IR）、语义校验、触发匹配、model.JobIntent 桥接
```

依赖：syntax → gopkg.in/yaml.v3；compiler → syntax、run/model。下游（scheduler）只见
run/model；syntax 类型不出 workflow 模块（import 纪律测试固化）。

## 2. 语法遍历

手工遍历 yaml.Node（Content 对）逐层白名单：

```
doc → mapping{ name?, on, jobs }
on → "push" | mapping{ push: mapping{ branches?/branches-ignore? } }
jobs → mapping{ <job-id>: mapping{ name?, runs-on, env?, steps } }
steps → seq[ mapping{ name?, run, env? } ]
env → mapping{ string: string }
```

- 每个 mapping key 查白名单；未命中 → `Diagnostic{File, Line, Column(key node), Path, Msg}`。
- 值类型不符 → 同样带位置。
- 多个 Diagnostic 一次返回（`*syntax.Error`），解析失败时无部分 AST。
- `${{ }}` 原文保留在字符串值中。

## 3. 编译

```go
type Compiled struct {
    Name string
    Jobs []JobInstance            // 文档顺序
}
type JobInstance struct {
    Key, DisplayName, RunnerClass string
    Env  map[string]string
    Steps []Step                  // IR step：Name/Run/Env
}
func Compile(wf *syntax.Workflow) (*Compiled, error)          // FR-W2.2 校验
func (c *Compiled) MatchesPush(ref string) bool               // FR-W2.3
func (c *Compiled) JobIntents() []model.JobIntent             // FR-W2.4 桥接
```

分支匹配：`ref` 去掉 `refs/heads/` 前缀 → glob `path.Match`；`!` 前缀模式先于包含模式判断。

## 4. V1 扩展点（本切片不实现）

- `needs` DAG + 环检测：Compiled 增加 edges，展开仍在本层。
- matrix：`JobInstance.Key = "test[go=1.27]"`，`DisplayName` 分离（FR-2.5）。
- 表达式求值钩子：IR 保留 raw `${{ }}`，0007 注入 evaluator。

## 5. Testing strategy

- fixture 内联（字符串常量），覆盖 AC 1–6；断言 Diagnostic 的 Line/Column 精确值（fixture
  固定缩进）。
- import 纪律：测试断言 run/scheduler、runtime/controller 的依赖闭包不含 workflow/syntax
  （遍历 `go list -deps` 不便，改为编译期固定断言包不存在 import —— 用 `depsTest` 读
  `go.mod`/静态 grep 的轻量脚本在 CI 中执行；本切片用单测固化 compiler 只依赖 syntax+model）。
- 覆盖率 ≥ 85%（对齐 workflow 模块约定）。
