# Tasks — Spec 0012 JS and Composite Actions

- [x] T1 `provider/github`：`ActionFetcher`（`action.yml` 经 content API，`runs.using` 解析）
- [x] T2 `compiler`：`uses` 分流（builtin → `JSCall`/`Composite` 展开，`inputs.*` 上下文，`plan` 新增 `JS`/`Composite` 分支）
- [x] T3 `executor/js`：`goja` 运行时 + `core`/`github`/`exec` 注入（`getInput`/`setOutput`/`exec` 往返）
- [x] T4 `executor/composite`：`CompositeStep` 内联展开（继承 `if:`/`env`/`continue-on-error`，`GITHUB_STATE` 隔离）
- [x] T5 `server`：`WorkflowFetcher`+`ActionFetcher` 同源装配，`plan` digest 覆盖新分支
- [ ] T6 AC-J1/J2 全链路（`github-script` 输出透传、`composite` 内 `run`+`cache` 展开）

