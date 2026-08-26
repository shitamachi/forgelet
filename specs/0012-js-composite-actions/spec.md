# Spec 0012 — JS and Composite Actions

- **Status**: draft
- **Date**: 2026-08-24
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted；FR-5.2 P1）
- **Covers**: FR-5.2 中 JS Action 与 Composite Action 的 P1 切片（0008 T9 剩余、0009 之后）
- **Depends on**: 0008（Engine file commands 已就绪）、0009（Builtin 注册表形态）

## 1. Problem

`run` 与 `builtin` 已覆盖典型 Go CI，但生态中大量复用 `actions/github-script`（JS）与
组织内 `composite` action（steps 组合）。当前 `uses:` 仅识别 builtin 白名单，其余一律编译
错误，导致存量工作流无法在 replacement 模式下运行。

## 2. Requirements

### FR-J1 JS Action

- FR-J1.1 `[P1]` `uses: owner/repo@ref` 且 `action.yml` 声明 `runs.using: node*` 时，在
  `goja`（纯 Go）中执行 `main` 入口，注入 `core`（`getInput`/`setOutput`/`setFailed`/`addPath`
  等）、`github`（`context`/`getOctokit` 桩）与 `exec` 能力；`GITHUB_ENV` 等文件命令透传。
- FR-J1.2 `[P1]` `with:` 输入经表达式插值后作为 `core.getInput` 可见；`outputs` 经
  `core.setOutput` 回注 `steps.*.outputs`。
- FR-J1.3 `[P1]` 超时与取消透传至 JS 执行（`context` 取消即 `setFailed`）。

### FR-J2 Composite Action

- FR-J2.1 `[P1]` `action.yml` 声明 `runs.using: composite` 时，将其 `steps` 内联展开为
  调用方的子 steps（继承 `shell`/`env`/`if`/`continue-on-error` 语义），`inputs` 映射为
  `inputs.*` 上下文。
- FR-J2.2 `[P1]` composite 内的 `run` 与 `uses`（含嵌套 composite）均受 `if:` 与
  `continue-on-error` 约束，`GITHUB_STATE` 仅在 composite 边界内传递。

## 3. Acceptance criteria

**AC-J1**：`actions/github-script` 的 `core.getInput`/`setOutput` 往返；`exec.exec` 能调 `git`。

**AC-J2**：一个复合 action（`action.yml` 内含 `run` + `actions/cache`）在 forgelet 任务内
展开并成功执行，其 `inputs` 与 `outputs` 正确透传。

## 4. Design notes（非约束）

- JS 选 `goja` 而非 `otto`（维护更活跃、与 `es5` 兼容足够）；`github` 上下文复用 `0007`
  的 `github` 对象。
- Composite 展开在 `compiler` 完成，复用 `0009` 的 `BuiltinStep` 形态；`plan` 不感知 JS
  与 Composite 的区别，仅executor 的 handler 表区分。
