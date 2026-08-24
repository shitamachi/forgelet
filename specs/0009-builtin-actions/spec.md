# Spec 0009 — Builtin Actions

- **Status**: implemented
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner（会话指令「提交然后继续任务」，唯一待批任务即本 spec）
- **Implemented**: 2026-08-24, project owner（v1-wave5 全量交付并通过 `make verify`）
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted；FR-6、FR-5.2）
- **Covers**: FR-6.1、FR-6.2；FR-5.2 中 Builtin Action 的 `uses:` 语义
- **Out of scope**: JavaScript Action 与 Composite Action 的运行时（后续 spec）、Docker Action
  （P2）、GitHub Marketplace 网络下载与任意第三方 action 兼容

## 1. Problem

forgelet 以 replacement mode 取代仓库的 GitHub Actions 原生执行。真实 CI 工作流几乎必然引用
`uses: actions/checkout@v4`、`actions/cache@v4`、`actions/upload-artifact@v4` 等 action。当前
引擎只支持 `run:` step，任何含 `uses:` 的工作流在编译期被拒。为兑现「典型 Go CI fixture 无需
GitHub Actions Runtime 即可完成 checkout/cache/test」的 V1 承诺（FR-6 AC），必须提供一组官方
builtin action：由 forgelet 自带实现、语义对齐 GitHub 版本，但不做网络下载、不依赖 runner。

## 2. Requirements

### FR-A1 `uses:` 解析与 builtin 注册表

- FR-A1.1 `[P0]` `uses: <owner>/<repo>@<ref>` 在编译期解析：命中 builtin 注册表 → 编译为
  builtin step；未命中 → 类型化编译错误，指明位置与最近似名称。
- FR-A1.2 `[P0]` 注册表内容固定为白名单（本 spec FR-A2/FR-A3 所列）；禁止网络下载任意
  third-party action。
- FR-A1.3 `[P1]` `with:` 输入以字符串映射传入；未知输入键产生编译警告级诊断，不阻断执行。
- FR-A1.4 `[P1]` 同一 step 不允许同时声明 `uses:` 与 `run:`（类型化错误）。

### FR-A2 actions/checkout

- FR-A2.1 `[P0]` 默认行为：在 workspace 内检出事件对应 commit（push→head SHA；
  pull_request→merge/head SHA 按 0005 事件语义；schedule→默认分支 HEAD）。
- FR-A2.2 `[P0]` 支持输入子集：`repository`、`ref`、`token`、`fetch-depth`、`persist-credentials`
  （其余输入显式报子集外）。
- FR-A2.3 `[P1]` 认证：私有仓库经 control plane 下发的短期 token 走 git credential helper，
  凭据只存在于 step 进程环境；`persist-credentials: false` 时步骤结束后不可复用。
- FR-A2.4 `[P2]` LFS 与 submodule 支持另行评估；M1 显式拒绝并给出可读诊断。

### FR-A3 cache 与 artifacts

- FR-A3.1 `[P0]` `actions/cache`：key/restore-keys 前缀回退语义对齐 GitHub；cache 条目
  repository-scoped，跨仓库不可读写；命中/未命中通过 step output `cache-hit` 暴露。
- FR-A3.2 `[P0]` 存储后端为 S3-compatible（MinIO 兼容）；后端凭据由 control plane 注入，
  不进入 Plan 明文。
- FR-A3.3 `[P1]` `actions/upload-artifact` / `actions/download-artifact`：按 name 上传/下载，
  path 支持 glob；同 run 内跨 job 可下载；retention-days 接受但仅作记录（清理策略属部署件）。
- FR-A3.4 `[P1]` cache 写入是 best-effort：保存失败不使 job 失败，仅记录告警。

### FR-A4 执行契约

- FR-A4.1 `[P0]` builtin step 在 JobRun 主容器内执行（架构不变量：无额外 Pod、无 DinD）。
- FR-A4.2 `[P0]` 输出协议复用 0008 文件命令：outputs 经 `GITHUB_OUTPUT`，env 修改经
  `GITHUB_ENV`，日志走同一 mask/结构化管道。
- FR-A4.3 `[P0]` 失败语义与 `run:` 一致：非零/错误 → outcome failure；受 `continue-on-error`
  与 `if:` 约束。
- FR-A4.4 `[P2]` setup-* 类 action（如 setup-go）不在注册表内；以 container image 承载工具链
  是既定路线（RunnerClass image）。

## 3. Acceptance criteria

**AC-V1**（全部自动化）：

1. 编译矩阵：合法 `uses:` 白名单命中、未知 action/未知输入/`uses`+`run` 并存的位置化错误
   （FR-A1）。
2. checkout fixture：push 事件检出正确 SHA、fetch-depth 生效、workspace 含仓库文件且 `.git`
   存在（FR-A2.1/2.2）。
3. cache fixture：首跑 miss→save、二跑 hit 且文件恢复；restore-keys 前缀回退命中；跨仓库
   key 隔离（FR-A3.1/3.2）。
4. artifact fixture：upload 后同 run 另一 job download 内容一致（FR-A3.3）。
5. 全链路：声明支持的 Go CI fixture（checkout → cache → test）在 k3s smoke 中成功完成，
   全程不访问 github.com 之外的 action 运行时服务（FR-6 AC）。

## 4. Design notes（非约束）

- 实现形态预计为 executor 内嵌的 builtin handler 表（编译产物携带规范化的 builtin 标识与
  inputs），具体接口由 plan 决定。
- cache/artifact 的 S3 bucket 布局与凭据下发通道由 plan 细化，须遵守「Plan 只含 secret 引用」
  的既有不变量。
