# Spec 0008 — Executor Runtime

- **Status**: proposed
- **Date**: 2026-08-23
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted）
- **Covers**: FR-5.1（Plan/secret 获取与状态上报的 Executor 侧契约）、FR-5.2（M0 `run` step）、
  FR-5.4、FR-5.5；FR-5.3 file commands 属本 spec V1 切片（M0 实现 GITHUB_ENV/OUTPUT/PATH 的
  核心，GITHUB_STATE/SUMMARY 后续）
- **Depends on**: 0002（Plan 类型）、0003（identity/TokenSource 语义）
- **Out of scope here**: 控制面 server 的内部 API 实现与装配（0011 wiring）、JS/Composite/
  Builtin action（P1，builtin 与 0009 合并考虑）、Docker Action（P2）、日志采集链路（0010）、
  Pod 镜像与 initContainer（0011）

## 1. Problem

Executor 是唯一运行不可信用户代码的组件。M0 需要：顺序执行 `run` step（共享 filesystem/PATH/ENV）、
GitHub file commands 的核心语义、secret masking 先于一切日志、信号转发/取消/子进程组清理、
以及与控制面的认证契约（Plan 获取、按需 secret、状态上报）。

## 2. Requirements

### FR-X1 Step 执行引擎

- FR-X1.1 `[P0]` 一个 Job 的所有 `run` step 顺序执行，共享同一 workspace（filesystem）、
  运行期 ENV 与 PATH；`GITHUB_PATH` 追加的路径对后续 step 生效（前置）。
- FR-X1.2 `[P0]` 默认 shell 为 `bash -e -o pipefail`（无 profile/rc），step 脚本落盘为临时
  文件执行；step 可声明 shell 覆盖（仅命令名，参数固定）。
- FR-X1.3 `[P0]` step 失败（非零退出）终止后续 step，job 结果为 failure；`continue-on-error`
  为 V1。
- FR-X1.4 `[P0]` 每个 step 的 stdout/stderr 逐行作为结构化 JSON 日志输出，携带
  run/job/step 标识与 stream；**secret 值在任何日志写出前被替换为 `***`**。

### FR-X2 取消、超时与子进程清理（FR-5.4）

- FR-X2.1 `[P0]` 子进程在独立进程组中运行；取消（ctx cancel / SIGTERM）先向进程组发
  SIGTERM，超过 grace period 后 SIGKILL；无孤儿进程。
- FR-X2.2 `[P0]` job 级 timeout 到期视同取消。
- FR-X2.3 `[P0]` 被取消的 job 报告 cancelled/failure 终态，不悬挂。

### FR-X3 File commands 与 workflow commands（FR-5.3 核心）

- FR-X3.1 `[P0]` `GITHUB_ENV`：`NAME=value` 与 heredoc（`NAME<<EOF ... EOF`）两种形式；
  step 结束后合并进运行期 ENV。
- FR-X3.2 `[P0]` `GITHUB_OUTPUT`：同语法，产出 step outputs（M0 记录为结构化日志）。
- FR-X3.3 `[P0]` `GITHUB_PATH`：每行一个路径，step 结束后前置进 PATH。
- FR-X3.4 `[P0]` stdout 中 `::add-mask::value` 立即注册 mask；`::group::/::endgroup::/
  ::warning::/::error::/::notice::` 解析为结构化事件（M0 不要求渲染）。
- FR-X3.5 `[P1]` `GITHUB_STATE`、`GITHUB_STEP_SUMMARY`、`::debug::`、带 properties 的
  命令参数（`file=...,line=...`）。

### FR-X4 控制面契约（FR-5.1 Executor 侧）

- FR-X4.1 `[P0]` ControlPlane port：`FetchPlan(identity)`、`FetchSecrets(identity, refs)`、
  `ReportJob(identity, result)`；identity 为 0003 的 audience-bound token。
- FR-X4.2 `[P0]` HTTP adapter：Bearer token；`GET /internal/jobruns/{id}/plan`、
  `POST /internal/jobruns/{id}/secrets`（body=请求的 refs，响应=仅授权值）、
  `POST /internal/jobruns/{id}/status`。服务端实现属 0011 装配；本 spec 用 fake server 验证
  客户端协议。
- FR-X4.3 `[P0]` 只请求 Plan 声明的 secret refs；响应值仅进入进程 ENV 与 masker，不进入
  日志、错误、上报体。
- FR-X4.4 `[P0]` 重复 FetchPlan/ReportJob 幂等（服务端语义），Executor 不因重试产生重复 step
  ——step 执行与状态上报分离，重试只重报状态。

### FR-X5 二进制

- FR-X5.1 `[P0]` `cmd/executor`：读 token 文件与配置 → FetchPlan → 执行 → ReportJob →
  以 job 结果设置进程退出码。作为未来 Primary Pod 的 PID 1 入口（镜像装配属 0011）。

## 3. Acceptance criteria

**AC-M0**（自动化）：

1. 双 step 共享文件：step1 写文件并 `GITHUB_ENV` 导出变量，step2 读取成功。
2. secret（下发值）出现在 step 输出时，日志中为 `***`；`::add-mask::` 动态注册后同样生效。
3. 失败 step 终止序列且 job 报告 failure；成功路径报告 success（含每 step 退出码）。
4. 取消：ctx cancel 后子进程组在 grace 内终止、无孤儿（sleep 子进程被清理），job 报告取消。
5. `GITHUB_PATH` 追加目录后，后续 step 能执行该目录下新脚本。
6. HTTP 客户端协议矩阵：正确的 bearer/路径/JSON；401/5xx 为类型化可重试错误；secrets 响应
   值不进入任何客户端错误信息。

**AC-V1**：STATE/SUMMARY、JS/Composite/Builtin action、日志流式上报、真实集群 PID 1 行为。

## 4. Design notes（非约束）

- Engine 为库（可注入 WorkDir/Shell/Grace/Clock/Logger），`cmd/executor` 仅装配；
  in-cluster 信号处理由 PID 1 的 main 完成（SIGTERM → cancel）。
- 命令解析与 file command 处理独立子包，纯函数化。
