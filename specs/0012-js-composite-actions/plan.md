# Plan — Spec 0012 JS and Composite Actions

- **Status**: complete
- **Date**: 2026-08-24
- **Approved**: 2026-08-24, project owner（与 spec 同批授权）
- **Completed**: 2026-08-24, v1-wave11 实现并通过验证

## 1. 总体形态

`uses:` 的解析保持 `syntax → compiler` 白名单，但白名单的“未命中”不再直接
报错，而是尝试按 `action.yml` 判定 `runs.using`:

```
syntax(uses/with) → compiler(Registry? → builtin : action.yml? → js/composite : UnknownActionError)
                → plan.Step.{Builtin|JS|Composite} → executor handler
```

*   **JS**：`goja` 运行时内嵌于 executor 主容器，`core`/`github`/`exec` 以 Go 对象注入；
    超时/取消通过 `context` 透传，`GITHUB_ENV` 等文件命令保持原管道。
*   **Composite**：编译期展开为调用方 job 的子 steps 序列（保持 `if:`/`env`/`continue-on-error`
    语义），`inputs.*` 上下文在展开时插值。

## 2. 接口与数据

### 2.1 Action 元数据获取

`ActionFetcher`（新 port，`provider/github` 侧实现经 content API 读取
`<owner>/<repo>/<ref>/action.yml`）：

```go
type ActionFetcher interface {
    FetchAction(ctx context.Context, repo model.RepositoryRef, ref, path string) (*ActionMeta, error)
}
type ActionMeta struct {
    RunsUsing string            // "node12" | "node16" | "composite" | ...
    Main      string            // "dist/index.js" 等
    Inputs    map[string]InputMeta
    Outputs   map[string]OutputMeta
    RunsSteps []syntax.Step     // 仅 composite 时填充（已解析的 steps）
}
```

`WorkflowFetcher` 已有 `FetchWorkflows`；`ActionFetcher` 复用同一 `ContentClient`，
`server` 装配时二者共享 `TokenSource`。

### 2.2 compiler

*   `compileBuiltin` 更名为 `compileUses`：先查 `Registry` → 命中即 `BuiltinCall`；
    未命中则经 `ActionFetcher` 拉取 `action.yml` → 依 `runs.using` 分流：
    *   `node*` → `JSCall{Repo, Ref, Main, Inputs}`
    *   `composite` → 递归编译其 `steps`（复用 `parseSteps`），并将外层 `with:` 映射为内层
        `inputs.*`（`expression` 上下文新增 `inputs`）。
*   未命中且 `action.yml` 不存在 → `UnknownActionError`（含 `external_id` 提示）。
*   `Warnings` 复用：`action.yml` 中未知 `inputs` 仍记警告。

### 2.3 plan

```go
type JSStep struct {
    Repo    string            // "actions/github-script"
    Ref     string            // "v6"
    Main    string            // "dist/index.js"（相对 action 根）
    Inputs  map[string]string // 已插值（除 secrets 外）
    Script  string            // 内联脚本（github-script 的 with.script）
}
type CompositeStep struct {
    Steps []Step // 展开后的子 steps（已继承外层属性）
}
// plan.Step 新增互斥分支：
type Step struct {
    ID              string
    If              string
    Run             RunStep
    Builtin         *BuiltinStep
    JS              *JSStep
    Composite       *CompositeStep
    ContinueOnError bool
}
```

`Digest` 覆盖新增字段（canonical JSON）。

### 2.4 executor

*   **JS**：`jsHandler` 持 `goja.Runtime`，预注入：
    ```js
    core: { getInput(k), setOutput(k,v), setFailed(msg), addPath(p), exportVariable(k,v) }
    github: { context: { eventName, sha, ref, repository, actor, ... } }
    exec: { exec(cmd, args, opts) } // 薄封装 `os/exec` 经 process-group
    ```
    `with.script`（`actions/github-script` 特有）直接作为 `goja` 源码执行；
    普通 JS action 的 `main` 需先经 `ActionFetcher` 取回（`dist/index.js` 内容随 `plan` 或
    运行时拉取——V1 随 `plan` 携带 `Main` 源码的 hash，执行时再拉取）。

*   **Composite**：`compositeHandler` 将 `CompositeStep.Steps` 视为子计划，复用同一
    `Engine` 的 step 循环（`if:`/`env`/`continue-on-error`/`GITHUB_STATE` 均透传），
    `inputs.*` 通过 `expression` 上下文 `inputs` 提供。

## 3. 失败与安全

*   JS 抛异常或 `core.setFailed` → `outcome failure`，受 `continue-on-error` 约束；
    `context` 取消时中断 `goja`（`Interrupt`）。
*   Composite 内 `run` 失败同样受外层 `continue-on-error` 约束；`GITHUB_STATE` 仅在
    composite 边界内可见（`STATE_` 前缀按 composite 隔离）。
*   `action.yml` 拉取失败视为编译错误（typed），不在运行时重试。

## 4. 测试策略

*   `compiler`：`uses: actions/github-script@v6` 解析为 `JSCall`，`composite` 的 `steps` 展开与
    输入警告，表驱动。
*   `executor/js`：`goja` 单元（`core.getInput`/`setOutput` 往返、`exec` 调 `git`）；
    `composite` 的 `inputs` 透传与嵌套 `if:`。
*   `server`：`ActionFetcher` 经 `httptest` 的 content API；`workflowSource` 集成（`uses` 触发
    二次拉取）。
*   `e2e`：`actions/github-script` 的 `core.setOutput` 经 `steps.*.outputs` 被后续 `run` 消费
    的全链路（进程内）。
