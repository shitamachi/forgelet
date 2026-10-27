# 项目状态

更新：2026-10-27（Asia/Shanghai）。当前：**阶段 10 执行内核与缓存修复已提交/推送并部署 homelab；steam-platform-web 两次真实 push 的原始 workflow、冷/热缓存与镜像发布全部通过，GitHub Checks 成功。**
按阶段快速实现，暂停 spec 审批，不设独立基线或性能审核；计划、代码、自动测试与实际环境接入分别记录。

## 已交付

- 单 Go 控制面、SQLite + 一个数据 PVC、按需原生 Pod；默认并发 4，可配置，多任务超额排队。
- GitHub App adapter、HMAC/幂等、固定 SHA 文件与 checkout、Checks 回写、日志/历史、管理 API 和 Web 工作台。
- needs DAG、条件、失败/跳过传播、job/step outputs、超时/取消、重启恢复、历史/日志/事件/备份保留清理。
- 数据磁盘低水位暂停新调度、恢复后自动继续；可配置双阈值、探测失败保护、状态 API 与首页提示。
- 正式 server/executor Dockerfile、最小 k3s 安装清单、race 单测、进程内集成与 Testcontainers 临时 k3s。
- artifact/cache PVC 存储、按需 rootless BuildKit、GitOps direct/PR、Flux 健康观测、限定 image digest 回滚。
- 旧实现已清理并保留仓库外完整快照；恢复位置见本页末尾。
- 2026-10-25 阶段 8 server/executor 已部署 homelab；review #1/#2 已修复并验证。
- 2026-10-25 阶段 9 可靠性修正（review #3–#10 及 UI 仓库列表）已提交并部署；schema v6，详见本轮记录。

## 本轮：提交、homelab 部署与真实 push 验收（完成）

- Forgelet 的阶段 10 依赖实现与缓存修复已提交/推送：2504478；Web 工具镜像构建选项为 2970d8b，分支 chore/homelab-reset-20261023。业务修复 aff95ca 与验证说明 c6ac5b5 已推送 main，包含此前本机的价格修复 1b5dfad。两次 push 的 workflow、Dockerfile、pnpm lockfile 与原 75ed696 字节一致。
- 经 owner 确认，GitHub App 仅新增 steam-platform-web，保留原三个仓库；原 check.yml 已启用，发布凭据经管理 API 加密保存。实际接收 GitHub push，读取对应提交源码，执行原始公开 Actions；每轮 14 个主步骤全部成功、48 单测与 28 Playwright，通过生产构建及 SSR smoke。

| 运行 | 实际源码 | Forgelet | pnpm cache-hit | 单测 / Playwright |
|---|---|---|---|---|
| 冷 | [aff95ca](https://github.com/shitamachi/steam-platform-web/commit/aff95caaa8a75ad149eab293128e8a06a47d01a1) | [47c54f11](https://forgelet.k.mygo.rocks/runs/47c54f11751abcb1ff782a7d) | false | 48 / 28 |
| 热 | [c6ac5b5](https://github.com/shitamachi/steam-platform-web/commit/c6ac5b56ac14759f6813df7d6bc07dbc3d46b7e4) | [c5313da9](https://forgelet.k.mygo.rocks/runs/c5313da9aff6f2592720df87) | true | 48 / 28 |

- 冷发布 latest / sha-aff95caaa8a75ad149eab293128e8a06a47d01a1 / Action 输出均为 `sha256:72f7bd00a5ca2c9c9057ff506f8de99127110bc6cb6ff5d2b519b2f926ab8ce8`。
- 热发布 latest / sha-c6ac5b56ac14759f6813df7d6bc07dbc3d46b7e4 / Action 输出均为 `sha256:3dd0eedeeb7b395d18fdebe366a1cd447d6be385a8b68f0c07583bb5cecdfcba`。新 image Pod UID 为 `6c8e053e-abaa-4f54-9cf8-6f657166a598`；冷轮为 `306004e6-d9da-479e-9012-22803ce7a6a6`。热轮实际导入 GHA manifest，原 RUN pnpm install --frozen-lockfile 层 CACHED。两轮共 4 个执行 Pod、28 个主步骤、96 单测与 56 Playwright。
- 两次提交各有 2 个 GitHub App Checks，全部 completed/success。验证期间临时暂停该仓库的 GitHub 托管 Actions，避免并发覆盖相同标签；验证结束恢复原 enabled=true / allowed_actions=all。后续 push 会按原配置触发 GitHub 托管 Actions。
- 最终 server：`zot.k.mygo.rocks/forgelet/server@sha256:b0285b7105c3d51b45f8d5a7c2c25b538cc737f7c9cebe943037190cda272521`；native：`registry.shitamchi.com/forgelet/executor@sha256:9b407caad70711c8112fa0e704640a2bdc76ec3455dcd779da8f7bf5f4ee723e`；ubuntu-latest Web act：`zot.k.mygo.rocks/forgelet/executor-act-web@sha256:e6bbad13b83f13e3cf8ba34b72516a11dec2138777cc6a46eb8d4fd3a2e7a9df`。BuildKit 固定源为 Zot 的 98cc6a3fc462；引擎继续使用既有 Pod 自有 Docker28.5.1，不挂宿主 socket 或 Kubernetes 身份。Web 工具 target 增加镜像体积，不增加空闲常驻服务。
- server 最初从外部 registry 拉取，rollout 成功后的首个会话探针遇到短暂 503，随后独立 health/session 检查通过。已将同一 amd64 manifest 从原构建 index 49dd15d469a4 同步到 Zot b0285b7105c3，并在冷/热之间切换；无代码或 schema 变化，冷轮历史与应用缓存跨控制面重启保留。升级前保存原子 SQLite、PVC、Deployment 和 ConfigMap，最终服务 Ready。
- Web 镜像提供官方 Node24.14.0 完整 toolcache 与 Playwright1.63.0 / Chromium153。原 setup/浏览器安装命令仍执行；冷/热指 pnpm 和 BuildKit 应用缓存，预装工具不计作在线下载验收。官方浏览器归档经官方 MD5 及 ZIP CRC 校验，并记录完整 SHA256，构建转发保持官方 HTTPS 上游及 APT 签名校验，软件源安装后恢复；node1 的 Node20/24/Chromium 实际启动检查通过。临时镜像站与端口转发已关闭，真实任务使用集群中的镜像与服务。
- 自动验证：本轮复跑 make verify（Go race/HTTP/SQLite、实际构建、lint/generate、35 Vitest、14 Chromium）与业务 48 单测、tsc、28 Playwright 均通过。同一 Go 修复的默认 129 MiB 和原 workflow Testcontainers 专项见下方前批记录；本轮没有重跑全量 26 场景。实际 push 验收与 Testcontainers 证据分别保存；原 workflow 的终点为镜像发布，业务 Flux CD 接续仍属于 10.6e。
- 备份、两轮分页日志/结果、Checks、registry 对照与构建来源位于 `/Users/guo/.config/forgelet/homelab-web-20261027`。真实运行没有注入 fixture 源码补丁，任务启动与缓存恢复不依赖本机代理。

## 前批：steam-platform-web 比较竞态与完整 workflow（临时 k3s 专项验收记录）

- **归因已确认**：原提交 `75ed69647af3c5aa4a44c416b5b60d1c491def48` 的失败用例在 Forgelet 外、相同 Node24/pnpm9.15.9/Chromium 下，普通单 worker 两次通过；仅增加 CDP 6 倍 CPU slowdown、桌面/移动端各重复三次后 6/6 失败，均等待“开始比较”，页面只剩 1 款。应用修复后同一条件 6/6 通过（24.5 秒）。这是业务 hydration 竞态，环境时序使它暴露。
- **业务修复**：本机 `steam-platform-web` HEAD `1b5dfadd791a205fdec2a8a74768fcc32c4ef77c` 的相关逻辑与原提交一致；修改 `src/shared/state.tsx`、`src/shared/components.tsx`，增加 `src/shared/state.test.tsx`。状态恢复前禁用比较按钮，操作同时检查 hydration/ID，保留选择/移除/三款上限。新回归在修复前 2 failed/1 passed，修复后 3/3 通过；工作区 48 单测、tsc、28 Playwright（14.2 秒）通过。
- **真实构建发现并修复缓存协议边界**：实际 BuildKit 客户端使用 1 MiB blocks，旧 128 blocks 限制导致合法大层失败；现为最多 256 blocks，保留原 32 MiB/block、256 MiB/对象、512 MiB/attempt 及对象数量限制，分别诊断块数/对象/暂存超限。热运行导出还会重复查询大层；现对同 attempt 已恢复的精确不可变 key/version 复用 URL/文件，fallback 仍查后端，避免遮蔽新 primary。无新依赖、常驻服务或 schema 迁移。
- **单测/集成**：256 blocks/257 拒绝、字节限额分类、重复查询不增加文件/字节、fallback 后新 primary 优先均有回归。最终 `make verify` 通过：Go race/真实 HTTP/SQLite、lint/generate、35 Vitest、14 Chromium（31.1 秒，`verify-cache-reuse.log`）；独立缓存回归 `cache-reuse-unit.log` 通过。
- **默认大层 Testcontainers 完整命令通过**：`buildcache-large-reuse.log` PASS（720.983 秒，场景 519.35 秒）。真实 129 MiB 不可压缩层覆盖 SDK 超过旧 128 块上限的行为，继续验证冷/热、重启保留 Pod、失败不覆盖发布、运行中取消清理。冷/热 `9c3e071d891578cba795c200` / `27e7289eba301b13d3d3704f`，失败 `f504723f0511770257680169`、取消 `35cbfdba70e6fec10a4eac38`。tmpfs 仅该场景临时扩到 512 MiB 后恢复，磁盘压力用例仍为原 256 MiB；不设性能门禁，不依赖私有源码。
- **原始 workflow 验证来源**：`hydration.patch` SHA256 `f75a19ea428ea581d88af7c7de1730513266136315399dc43a7cec1bbb796917` 显式应用到临时 clone，生成固定测试对象 `17ea226bfde9179845fbfeb745bcb6ed5670a8ed`。原 YAML/Dockerfile/E2E 断言字节不变；源码带修复，不把它宣称为 GitHub 原 SHA 的全部原字节。未更新输入快照或 owner 的 HEAD/index，业务修复留为未提交改动。
- **最终业务专项**：`platform-workflow-reuse-final.log`。冷运行 `0072294bf20fda672b4e5e61` 的 14 个原主步骤全部成功（47 单测、28 Playwright、build、smoke、原 setup-buildx/login/build-push 与 cache export）；pnpm cache-hit=false。latest/SHA 标签与 Action digest 核对一致：`sha256:da3a3e05ba99777c3d87dffe4fdeb77746477ce06fef8b0b81c66d975677a7b5`。热运行 `779b52250521d030a504343b` 也完整通过：pnpm cache-hit=true，47 单测、28 Playwright（17.0 秒），新 builder UID `52732faa-6464-4394-9968-2cef3f10afa0` 的 `RUN pnpm install --frozen-lockfile` 实际 CACHED；latest/SHA 标签与 Action digest 均为 `sha256:dd003f342fd89bbeaf5c3325873ce176d8ed6c0af7ad6b632ea139e16e57ec42`。完整专项命令 PASS（1550.930 秒，业务场景 1348.72 秒）；共 4 个执行 Pod、28 个原主步骤、94 项单测与 56 项 Playwright，原 source workflow/YAML 不变。
- **执行环境边界**：显式 `FORGELET_TEST_TOOL_CACHE_IMAGE` 提供官方 Node24.14.0 toolcache 与 Playwright1.63.0 / Chromium153；浏览器归档经 HTTPS、官方 MD5/ZIP CRC 校验，Node 来源和 digest 记录于证据目录。setup Actions、浏览器安装命令仍执行；pnpm/GHA 数据缓存从空开始。原始在线工具安装曾在前述排查运行成功，最终预装工具 profile 不算新的在线下载验收，也不改变 formal executor 默认镜像。
- **保留失败证据**：`platform-workflow.log` / `platform-workflow-retry.log` 的 413 最初误判为总额度；实测约 195 MiB、128 个 1 MiB blocks 后确认块数限制，已撤回非必要额度配置。`platform-workflow-tools-final.log` 冷运行完整通过，热运行虽然依赖 RUN 层 CACHED 仍因重复恢复超限而整体 FAIL（1642.832 秒），促成查询复用修正。其他 TLS/下载失败、主动取消的环境排查与最终通过分开，不吞错或跳过测试。
- **范围与证据**：`/Users/guo/.config/forgelet/platform-web-fix-20261027/` 保存本批前快照、失败 trace/截图、固定来源、完整运行与日志。本批运行 make verify、单测/HTTP/SQLite、默认大层与原业务 Testcontainers 专项；未重跑上批全量 26 场景，不冒充本批全量。仅临时 k3s/隔离 registry，不写真实 Checks/业务 registry，不提交/推送业务仓库，不部署 homelab。

## 上批：10.6c 镜像构建、缓存与包发布凭据（历史记录，platform-web 阻塞现已解除）

- 复用原控制面/PVC 与按需引擎，在 supervisor 中接入 CacheService v2 JSON、Azure 风格 block/single PUT、block list、finalize、HEAD/range 下载；旧 v1 路径保留。新 runtime token 为每 attempt 的短时 JWT 兼容能力标识，服务端仍按精确随机能力校验，不信任客户端 claims。
- 缓存监听 Pod IP，供同 Pod 引擎内的 bridge BuildKit 访问；native 不新增 Docker 依赖。临时服务随 attempt 退出，持久隔离/不可变 key/配额/保留继续由原 blob 服务负责。
- 管理员可用 act.buildkit_config 设置 builder 默认 registry/TLS 配置；现有镜像目录中的 moby/buildkit:buildx-stable-1 固定源作为 setup-buildx 的默认 image。显式 workflow 输入优先，不把引擎预加载扩称独立 BuildKit 自动继承镜像目录。
- packages:write 需要 act profile、push/manual 事件与管理员 package_tokens → 加密 Secret 名绑定；计划只存引用。运行时 package credential 与 source clone token 分开；隐式 checkout 继续只读源 token。禁止 PR 获取包发布凭据，缺绑定/缺 Secret/撤销时明确失败。
- 原始 workflow 暴露的 github.ref_type 与显式 GITHUB_TOKEN Secret 环境绑定已补齐，保留其他 runner 身份变量和 file-command 写保护。原始样本位于 test/fixtures/steam-build；源码固定提交的仓库外快照通过 gh 只读获取。
- 公共 Action 归档读取补上 HTTP/2 早关流回归：只为读取 Git PAX commit 标识的归档请求使用独立连接，避免下一次请求卡在已取消的大流；metadata 完整读取继续复用连接，不携带用户凭据。真实临时 Pod 连续请求两次成功，HTTP/2 回归单测通过。
- **单测/进程内集成**：最终 `make verify` 通过（`verify-last.log`）：构建、Go race、真实 HTTP/SQLite、lint/generate、35 Vitest、14 Chromium（32.0 秒）；workflow 覆盖率 88.1%。覆盖 v2 分块/整对象/重试/限额/版本隔离、Put Blob 替换未提交 blocks 后的磁盘清理与计数、凭据引用/加密/重启/轮换/撤销、PR 拒绝、源 token 分离、默认 builder 与显式输入优先。
- **独立 BuildKit Testcontainers 已通过**：使用真实 BuildKit v0.20.2、原版 setup-buildx@v3/login@v3/build-push@v6；冷运行 `efc2df4d289d3ef3c7324d24`、热运行 `a78857db45c2f5f3e18deef0`，实际 RUN secret mount 层恢复后显示 CACHED。冷运行期间重启控制面保留 Pod UID `24e238f0-f2f3-44dd-8b24-3f1e58187e80`；失败运行 `2a925c58b954e307180f76c4` 不改变镜像 digest，取消运行 `3acf9d66f9f53df392911cdf` 清理按需引擎/Pod。
- **原始 steam-admin image 已通过**：固定提交 `2d6d2f5eb0f7236ff68bec49b3db1aa09a38e07d`，运行 `ee279133c662c5c86e9d39a6`，隔离 registry digest `sha256:98c203cfaf30ccc5b4a4ac1c012950ecaf9462fb9f4c26c16cb559b152f57a4b`。原 YAML、源码、Dockerfile 不改字节。
- **原始 steam-login docker 已通过**：固定提交 `0a5ce9753cf4b6f429cf273eea000353a9a19106`，专项命令 PASS（335.048 秒），运行 `97d46b224f59e593edc53f7d`。原 checkout/buildx/login/build-push@v5 和 packages:write 六个主步骤成功；自有 registry digest `sha256:692bbf076be8de170978ddac847c80e1094d018e72d0f6595ad97b16fc43e8f6`，隔离 GHCR namespace digest `sha256:10aff2802ccaf9718a47aa81ac9dba10e9d77fc275b2e63b6e53a962aea7c6e6`。两处均核对 Action 输出与实际 manifest；GHCR 代理单独校验测试 package token，不连接真实 GHCR（`login-original.log`）。
- **原始 steam-platform-web 仍受阻**：固定提交 `75ed69647af3c5aa4a44c416b5b60d1c491def48`，运行 `384e5eb6db2dafdd19234d37`。Node24、pnpm9.15.9、安装、单测与 Chromium 安装成功；真实 Playwright 26 passed、1 flaky、1 failed。桌面 `tests/e2e/catalog-search.spec.ts:52` 切换搜索页后找不到“开始比较”链接，重试也失败；镜像 job 按 needs 正确 skipped。源码中 CompareButton 未等 hydration，toggleCompare 可能覆盖 sessionStorage 恢复值，是待独立复现的线索，不能直接认定根因。未改业务代码、删测试或把跳过发布记为成功；冷热两次完整 workflow 尚未完成。
- **最终缓存修正复验**：Put Blob 替换旧 blocks 的磁盘清理/计数回归通过（`cache-replacement.log`）；最终产品代码的独立 BuildKit 专项 PASS（702.191 秒，`buildcache-last.log`），包括冷热、重启、失败和取消。该次发现取消触发所依赖的 Buildx 日志可能延迟刷新，测试现改为观察本 job 独立 builder 中的真实 RUN 进程，避免日志时序影响。最终专项 PASS（339.990 秒，场景 216.29 秒；`buildcache-process-signal.log`），冷/热运行 `f48636c67969f3a87b48c780` / `824845023363eeaa7a177b3e`，失败 `63559e864603e4e4cea159c4`、取消 `c6c7b4c1d5714a3e2081e37e`；日志/ACK 本身继续由独立日志场景验证。最终 integration 编译与 lint（0 issues）通过。
- **组合全量结果**：`testcontainers-combined-final.log` 用时 2138.808 秒，26 个必选产品叶子场景均通过，但命令整体 FAIL（上述可选 platform-web 业务 E2E）。包括实际 BuildKit/GitOps/Flux、低水位、取消/恢复、日志、原生 Go/Node、Web 与认证；不能把这个命令报告成全量 PASS。默认 `make test-integration` 独立完整命令 PASS（1763.748 秒，`testcontainers-product-final.log`）：26 个必选叶子场景通过，两项需私有源码目录的可选业务测试显式 skip。真实 Go/Node 工具链 71.61 秒，集群 Web 4 项 27.1 秒、认证浏览器 1 项 8.3 秒，保留清理通过。该全量命令在最后的 Put Blob 旧分块清理修正前已启动；修正后的最终代码另经上述 make verify、缓存回归和 BuildKit 专项验证，最终取消测试改进也经专项验证，不将其称为重新执行全部场景。
- 本批证据和工作区修改前快照：`/Users/guo/.config/forgelet/stage106c-20261026/`；未提交/推送任何仓库，未部署 homelab 或写真实 GHCR/业务 registry。

## 上批：10.6b 公共 Action 引用与原始业务 workflow

- **实现**：纯 workflow 编译器接受公共 tag/branch/full SHA 的合法语法；控制面在新 act 运行保存前解析精确引用与远程 Composite 依赖，保存 Action.SHA / ActionPins。默认从 GitHub Git 归档的 global PAX commit 标识固定 SHA，再读取该 SHA 的 metadata；精确 tag 优先于 branch，避免匿名 REST core 配额。可选受控 REST 源保留 annotated tag 解引用。重跑、重复 delivery 与恢复沿用已保存值；旧原生版本限制和无锁旧计划恢复保留。不新增依赖、常驻服务或数据库迁移。
- **运行上下文修正**：原 metadata-action 的 repoData 调用暴露了 provider 地址未传入 job 的问题；新计划保存既有 github_api_url，并传入 act/native 的 github.api_url 与 GITHUB_API_URL。重跑沿用原地址；公共 Action 源解析仍无账号 token，不与 job API 凭据混用。
- **单测与进程内集成**：最终 `verify-accepted.log` 的 make verify 通过：构建、Go race/HTTP/SQLite、lint/generate、35 Vitest、14 Chromium（30.7 秒）。覆盖 mutable tag 更新、新运行刷新而重跑/重启/重复请求不漂移、远程 Composite/循环/子目录、上下文取消、非法 metadata/源地址、临时限流返回 503 与事件退避、不保存半份计划和首次运行只解析一次。追加的归档 404/限流/中断回归另经 action 包 race 验证通过；workflow 包覆盖率 88.1%。
- **原始业务 Testcontainers 验收已通过**：一次完整专项命令 PASS（728.470 秒；业务场景 615.60 秒）。steam-platform-server 固定提交 `e37944b787fc89cc4e96c327e377dab09a09a940` 的 check.yml 两次、steam-admin-web 固定提交 `40140fc9e3281467d4199586f2f5f988fad0731f` 的 ci.yml 两次和 image.yml 一次，共 5 个运行、28 个原始主步骤全部成功。Go/npm 均验证 cache-hit=false → true；3 份 YAML、源码与 Dockerfile 保留原字节。
- **原始运行记录**：Go 冷/热为 `c3db1faed139aaec5e677a7b` / `0edb0235ce88c995e30df60f`；Node 冷/热为 `4fb0647e58c360c8f5c07025` / `5536a4c237a004f2692bf1ee`；image 为 `c2bffa8dcc2166d7cc715c4e`。实际执行原版 checkout、setup-go、setup-node、login、metadata、build-push；其中 setup-node@v4 固定到 `49933ea5288caeca8642d1e84afbd3f7d6820020`。临时 registry 的 manifest 与 Action 输出相同：`sha256:b102ce41a284135eb66c4d45b4c569ff772d169ed4ac35c8ad37262ab26abdda`。
- **全量回归**：最终 `make test-integration` 一次完整命令 PASS（1344.762 秒，`testcontainers-accepted.log`），25 个既有叶子场景全部通过。包括真实 BuildKit/GitOps/Flux、重启恢复/取消/身份隔离、低水位、Unicode 日志 ACK、act 缓存/运行时（217.45 秒）、matrix/并发组、真实 Go/Node 和保留清理；集群 Web 4 项（27.7 秒）及认证 1 项（8.0 秒）通过。默认命令未设置业务目录，显式 skip 可选原业务场景；该场景已由上述单独的最终 5 次运行验证。追加测试后的最终 lint 也通过（lint-last.log）。
- **实验中发现的问题**：修正测试引擎 ctr 入口、重复 insecure-registry 配置、内层 registry 域名路由/TLS 与 API fixture 的 token scheme；随后真实源解析遇到 REST 匿名限流，改用 Git 归档 commit 标识并加回归。初轮失败、仅 image 专项和最终 5 次运行分别保存，只有最终命令计入完整业务验收。
- **范围**：这是临时 k3s 中的真实执行；源码 checkout 经同 SHA Git 对象镜像，API/发布使用隔离测试替身，公共 Actions 与 Go/npm 依赖真实下载。不携带宿主 gh 凭据，不写真实 GitHub Checks 或业务 registry，不等于账号 push 接入或 homelab 部署。build-push 的 build record 上传仍有 artifact 协议警告，不记为 artifact v4 兼容；现有原始 ci/image 独立触发，没有隐含的跨 workflow 发布门禁或 Flux CD。
- 证据与工作区变更前快照：`/Users/guo/.config/forgelet/stage106b-20261026/`，最终业务证据 `business-final.log`，最终全量 `testcontainers-accepted.log`，最终单测 `verify-accepted.log` / `archive-final-unit.log`。未提交、推送本项目或业务仓库，未部署 homelab。

## 上批：10.6a 业务仓库盘点与后续实施顺序（仅检查和文档）

- **读取范围**：使用当前 gh 登录的 shitamachi 账号，扫描全部 15 个 steam-* 仓库的默认分支固定 SHA；9 个包含 Actions，获取 13 份原始 workflow，以及关联 Dockerfile、go.mod/package.json、Makefile 和 Buf 配置。未执行仓库脚本、触发业务 workflow、提交或推送业务改动。
- **当前代码检查**：临时 Go 探针直接调用当前 `workflow.ValidateFiles`，加载同 SHA 的全部辅助 workflow。13 份中 6 份规划通过、7 份拒绝；另逐条检查 Action 引用。主要拒绝点是 6 个未支持的 tag 别名与 pwsh；源文件、完整 SHA、诊断 JSON 和 GitHub 记录保存在 `/Users/guo/.config/forgelet/steam-inventory-20261026/`。探针已移除。此结果不包含 profile admission、真实步骤或外部发布验证。
- **实际仓库差异**：Go/race/Buf/rg、Node24/npm/pnpm/真实 Playwright、Rust1.97/pwsh/Python3，以及 Buildx/type=gha/双 registry 发布均有真实需求。当前源码未引用远程 reusable、动态 matrix、artifact v4 或 OIDC，暂不优先扩展这些能力。
- **业务问题已重新核对**：steam-tasks 默认分支仍是 `8265ad9772c5`；GitHub run `39965363438` 失败于 `steam-proto-go/priceregion` 缺包，v0.6.0 的树也没有该目录。Dockerfile 必需的 steam_proto_go context 仍未由 image workflow 提供。steam-api 当前 run `39959378343` 失败于 `TestRedisAdmissionAndParentSnapshotIntegration`，尚未进一步诊断该业务测试的原因，不能归类为 Forgelet 执行失败。SDK steam-proto-go 当前公开，不能把它列为已确认的私有授权阻塞。
- **优先顺序**：先 steam-platform-server（当前 GitHub 同提交 CI 成功）与 steam-admin-web（当前 GitHub 同提交 CI/image 成功），交付通用 Action ref 固定、真实 Go/Node 与镜像发布；再覆盖 Buf、pnpm/Chromium、独立 BuildKit 缓存、GHCR 与 Rust profile；随后逐库接续所需 GitOps/Flux、健康检查与回滚。全部 13 份原 workflow 均无 Flux 部署步骤，镜像发布不等于 CD。详情见 [compatibility.md](compatibility.md) 业务仓库表和 [plan.md](plan.md) 10.6b–10.6e。
- **验证与完成范围**：本批只有 gh 只读盘点、当前编译器探针和文档修改，未修改产品行为，未重跑 make verify、单测/集成或 Testcontainers，未部署 homelab。下文 10.5d 的最终 25 场景 PASS 仍是该实现批次的历史结果，不冒充本批业务验收。

## 上批：10.5d（代码、自动验证与 homelab 全链路完成）

- **公共镜像目录**：管理员将 Docker Hub tag 映射到 runner 同 registry 的固定 SHA256，最多 16 项，拒绝重复别名、未固定的源及其他 credential host。supervisor 自动拉取、登记原名并核对完整 config identity，再启动 worker；失败阻断步骤，无公网回退。Secret 仍只供 supervisor/Kubelet，不进入 engine/内层任务。Docker 引擎也使用镜像 digest，startup probe 检查实际 28.5.1；不增加常驻缓存或共享 Docker data。
- **Zot 实际目录**：按 amd64 原 manifest/blob 同步 PostgreSQL 18-alpine、Ryuk 0.14.0、Go 1.26.4-bookworm、Alpine 3.22、Dockerfile frontend 1.7 与 Docker 28.5.1-dind；在 `zot.k.mygo.rocks/forgelet/mirrors/` 保存固定源。上传采用本机 OCI 暂存后经临时 SSH 隧道写入现有 Zot Service，避免慢速上游流式上传中断；执行仍用正常 HTTPS。没有改动 Zot 服务配置，原 workflow/Dockerfile 镜像名均未修改。
- **日志**：同 step 小写入合并为最多 8 KiB、每 200ms 刷新的块，保留原 seq/SQLite/ACK 协议、1 MiB 待确认上限、大写入背压。首次尝试后内容不可再合并；ACK 丢失以相同字节重试。网络 I/O 不持队列锁，上传串行、等待可取消，避免上传挂起阻塞 job/step 超时；终态前以独立有界 context 排空并上报已确认序号。Unicode 与动态秘密跨块边界保持完整。控制面独立复制 profile 的目录 map，避免调用者修改运行配置。
- **构建**：Dockerfile 分离不随 Go 代码变动的系统工具层，native/act 继续各自交付，单 module 和四个真实二进制不变。`distribution/reference` 仅将已有依赖转为直接使用，负责镜像引用校验；没有新增 registry 客户端 Go 运行时依赖。

| 自动验证 | 最终结果 |
|---|---|
| `make verify` | 通过：真实构建、Go race/进程内集成、lint/generate、35 Vitest、14 Chromium（31.7 秒）；最后格式检查通过 |
| 日志/配置单测 | Unicode/动态脱敏、同 step 合并、ACK 丢失且尾部继续到达、永久上传失败停止代码、上传挂起时本地 job 超时、源/tag identity、目录/版本/credential host 校验与 profile 深拷贝通过 |
| HTTP/SQLite | 真实 executor → HTTP → SQLite 验证连续 ACK、精确文本、脱敏、步骤归属和最终 LastLogSeq，通过 |
| 最终 `make test-integration` | **25 个叶子场景全部通过，1746.332 秒**；一次完整命令 PASS，不用专项结果替代 |
| 新核心 k3s 场景 | 持续 Unicode/脱敏输出时重启控制面，原 Pod UID、完整行计数及顺序、唯一 seq 均保持（最终场景 3.82 秒）；act 场景 207.05 秒，包含私有 registry 自动准备、原名 Docker Action FROM、不可用 mirror 阻断、cache/调用/超时/取消/引擎退出 |
| 既有链路回归 | 真实 BuildKit/GitOps/Flux、故障/回滚、身份隔离、磁盘保护、多任务/并发组、Go/Node、浏览器/认证、保留清理通过 |

首轮 24 场景 23 个通过（1674.686 秒），唯一失败为浏览器点击保存后立即 GET，未等待异步 PUT 完成。
测试改为等待真实 200 响应再检查持久化结果，未放松断言；最终全量已包含修正。
上传锁问题另以挂起请求/秘密尾部/本地超时回归复现和修正，修正后的执行器用于最终全量和部署。

### 10.5d 实际部署与原 workflow

- 最终 server `registry.shitamchi.com/forgelet/server@sha256:99c01b9ace81809850a9f2e418b3869390299fc499c5995b14073da1d3cc2981`；native executor `registry.shitamchi.com/forgelet/executor@sha256:c0783df3e13e4219c79fad964705cfb28edbb8c7acac83463f4885ac917d3e5d`；act executor `zot.k.mygo.rocks/forgelet/executor-act@sha256:68189ab98e304637ca70532c05bdf98618c0b06b3ad44ddc61720b593497fe9e`。升级前已保存 ConfigMap/Deployment/原子 SQLite；会话保留，无 schema 变化。
- 仅提交专用测试应用 README，提交 `7f31a49360bbe55f42d2eff83f107b14775b804a`；workflow、Dockerfile、应用逻辑和 SDK tag 保持原样。run `e73fb60d516157d9c6df5a8f` 的 reliability/call gate/build-push/native deploy 全成功。原版公开 Actions、私有 SDK、Atlas、race、必跑 PostgreSQL 验收、cache-hit=true、BuildKit secret/命名 context 和真实 registry push 通过；Codecov 沿用非阻断策略，不记为外部覆盖率上传成功。
- 两个 act job 各自动准备全部 5 个公共别名，使用新的空引擎，无手工预热。控制面运行中重启，reliability 以原 Pod UID `3a595bff-7bd5-4309-87fe-9942741623bc` 完成，没有重放；Pod 完成后已 GC，以持久记录和重启前快照核对 UID。build-push 保存 109 个已 ACK 块、56,535 bytes；10.5b 同结构流程为 1624 块、51,477 bytes，这是两次实际运行的观察值，不作为性能基线或普遍速度保证。
- 三个本次 push 的 [GitHub Checks](https://github.com/shitamachi/forgelet-e2e-app/commit/7f31a49360bbe55f42d2eff83f107b14775b804a/checks) 均 completed/success。release digest `sha256:8ed01d4b12c9e8235e5e8dd90328c5599c801acf2b8b6405642fb38db60627d0`；delivery `21a38fd52ae5436aec42e4d0e8dbc643`，GitOps `385e2e04ec347150633cec9e963b119097e9203c`，实际 deployed / Flux Ready / Deployment image / HTTP 新 SHA 均相符。
- 回滚记录 `f3cf3c14149ec19cd5a25e09e1482a05`、GitOps `68cddb6cc66b207ffec874bdea05205cdf86c965` 恢复上一 digest `sha256:303ec3408f323cab25ddc01456f95887766829dded75c0707c63517677eccf42`，应用健康；随后记录 `d9cdb2c92af96ce52f91c80daf679c13`、GitOps `dc3a148ddbc3043d62aab4bbd95e125fc2b0244c` 恢复新镜像，最终 deployed / Flux Ready / HTTP SHA=7f31a493。三次观测中任务 id=1 均为 completed、worker=homelab-worker、version=3，内容完全相同；历史被替代的记录转为 superseded。
- 实际取消探针使用旧测试 SHA，避免影响本次三个成功 Checks。`b0e9ae87a6b2a426163eb0da` 在准备/执行交界发出取消，日志显示 step 已开始 sleep，按运行中取消记录，后续命令未执行；`86b879c5ca4a55129d2978d3` 在 runner 拉取阶段取消，尚未准备目录/启动用户 step。两者均 cancelled，原 Pod 与 engine 移除。取消是轮询后的停止，不把请求前快照冒充“用户代码从未启动”。

证据与升级前备份保存在私有目录 `/Users/guo/.config/forgelet/live-20261023/stage10d/`：
`verify-complete.log`、`testcontainers-final.log`、`public-images.json`、`images-release.json`、各 run/分页日志、
GitHub Checks、重启快照、三次 Flux/HTTP/Deployment 快照及取消记录。未修改或运行业务仓库，本项目没有自动提交或推送。

**范围**：这是管理员镜像目录，不是所有 Docker Hub tag 的透明代理；强制拉取/独立 BuildKit 引擎仍需配置源。
归档与版本维护方式见 deployment/compatibility；执行器仍只适用于显式 opt-in 的可信仓库/profile。

收尾复核：forgelet namespace 仅保留一个健康控制面 Pod，执行 Pod 数为 0，forgelet-lab 无 Pod。
停止本轮临时 SSH 隧道，移除本轮 builder、Testcontainers 镜像 tag 和可重建的 OCI 暂存；
用户的 lg-multiarch builder、Zot 镜像/Secret、控制面数据、应用与数据库 PVC 均保留。
最终三个成功 Checks、三次发布/回滚/恢复的 digest/revision/HTTP/任务数据、两次取消及日志秘密扫描均已复核。

## 本批：10.5b 正式单 job worker（已部署，真实链路通过）

- 已接入独立 act worker 子进程、显式 act profile/Pod 自有按需 Docker、Node20/24 分派；控制面继续 SQLite 图调度，原 executor 保留 claim/日志 ACK/control/result 协议。取消由父进程终止 worker 并停止自有引擎容器，避免 stock 的取消后 post 行为；不走有已知 PTY race 的 host backend。
- 已实现 job 本地缓存协议 facade，归档仍进入原控制面 blob 服务，保持仓库/ref/version、配额和保留语义；短期依赖仓库 token 引用由管理员显式配置，按领取身份临时签发，不持久化 token。
- `make verify` 最终一轮已通过：真实构建、Go race/进程内集成、lint/generate、35 Vitest、14 Chromium（31.0 秒）。workflow 单测覆盖率 88.1%；reserveGraph 89.6%、dependencyContext 100%、调用状态处理 88.6%。
- 实际 k3s 的 act 专项最新通过（829.803 秒，act 场景 679.02 秒）：Node20/24/post、Docker Action workspace/outputs、typed workflow_call/inherit/outputs、冷/热缓存、同 Pod UID 重启恢复、真实 exit 7/失败与 always 下游、job 超时、取消后全部引擎清理；新增真实 Git workspace 信任回归。测试日志按游标拼接，检查点输出越过脱敏尾部缓冲，避免匹配命令回显或漏读 post。
- 最终全量回归实际结果：24 个叶子场景中 23 个通过（整轮 1355.210 秒），包含 act（680.45 秒）、工具链（98.21 秒）、真实浏览器、认证和保留清理。唯一未通过项为 GitHub 下载固定 Flux v2.6.4 清单时连接重置，CD 场景中止；为测试夹具添加三次有界重试、4 MiB 限制，永久 HTTP 错误不重试。race 回归通过（2.228 秒），真实 BuildKit/GitOps/Flux 发布、故障、回滚和构建取消专项补跑通过（492.600 秒）。24 个行为场景已跨全量与补跑验证，不能将全量命令写成整体 PASS。
- 较早整轮曾因真实 Node 下载超过 6 分钟 fixture 窗口失败；仅将该下载 fixture 的 job/测试等待改为 12/13 分钟，未改变产品默认 deadline 或测试断言。工具链专项通过（514.473 秒），最终全量中的该项也通过。
- 新增不可变镜像预加载快路：校验 registry manifest SHA 和本地完整 config digest 后复用已加载镜像；任何校验失败回退标准 pull。单测/race/make verify 与真实 Docker Testcontainers 通过（4.111 秒）。不写 Docker 内部元数据，不信任 tag 或未经验证的缓存。
- 权限回归额外发现空 permissions map 被 omitempty 省略，恢复成默认能力；HTTP/SQLite 已复现并修复，保留 null/{} 区别。最终镜像专项和全量中的 act 场景包含无权限 job 不获得 token。
- act v0.2.89 只链接独立 worker；单产品 Go module 保留。go-archive 固定兼容 v0.1.0（v0.2.0 移除 act 所用弃用别名，上游 diff 已核对）；完整产品和 Testcontainers 一起验证，待上游迁移后移除此兼容 pin。

### 10.5b 实际 homelab 验证

- 正式镜像固定为 server `sha256:c362fd59b22ed212c28ee67f72fdb42c8fbdc0a7151c101c773e0c88549db312`、native executor `sha256:3d204029bf622689f4e190c29a69c929370c5e1e451b05c84ed65019d8767434`、act executor `sha256:480bc4a1aa5ff07662b83884290f3957ded3cc35dd1336bcc522f981f96db33a`，对应 `registry.shitamchi.com/forgelet/{server,executor,executor-act}`。单控制面位于 node3，act 任务在 node1 按需运行。
- 只在三个专用测试仓库操作，未修改/运行业务仓库。启用测试应用原 `image.yml` 的 push 规则，调用 reliability，再通过 needs.digest 接续 native `forgelet/deploy@v1`；GitHub 原生 Actions 仍关闭。registry secrets 由原加密设置服务保存，依赖 SDK token 按管理员的单仓库引用临时签发。
- 成功源码 `6777baadefac3574ed1899a68a586f556877fc8a`，run `77f821bf3dcf0c8514ad5d19`。原版 checkout/setup-go/Codecov/login/metadata/build-push 均实际执行；私有模块校验、vet/build、Atlas、race、必跑 PostgreSQL Testcontainers（3.45 秒）、私有 SDK v0.1.0 checkout、BuildKit secret 和命名 context 通过。Codecov 有网络/无上传 token 警告，非阻断，未计为上传成功。
- 运行中实际重启控制面，reliability Pod UID `01070288-51b1-4bba-9b85-b675d67f9832` 保持不变，executor 重启数为 0；原任务继续完成。三个实际 [GitHub Checks](https://github.com/shitamachi/forgelet-e2e-app/commit/6777baadefac3574ed1899a68a586f556877fc8a/checks) 均 completed/success。
- 构建 digest `sha256:303ec3408f323cab25ddc01456f95887766829dded75c0707c63517677eccf42`；delivery `6426676552d79ab2ded88852b57acbb0` 为 deployed；产品提交 GitOps `d17859cf10454486557072ae0d2517ae1a7b5c2f`，Flux Ready/Healthy=True、实际 Deployment digest 相符。HTTP 返回 healthy=true / production-like / 上述源码 SHA；原任务 id=1 仍为 completed、worker=homelab-worker、version=3。没有 operator 传递构建 digest 或代替产品提交 GitOps。
- 实际调用原 rollback API：记录 `45854256cbc6688f73381f6d83d1a539`、GitOps `2f75f7993fbe61ce9cf2e633b9ee8d3f910b62ce`，恢复前一个 v2 digest，deployed / Flux Ready / HTTP healthy。回滚 commit 只修改 `apps/demo/kustomization.yaml` 的一行 digest。随后回滚该回滚记录以恢复新版：记录 `c4f4bd6795a671fff96325651b770af4`、GitOps `3df5af133477d61efb886ccbb88a1a50927f81c2`，最终 deployed、应用为本次源码 SHA。三次发布/回滚/恢复均核对实际 Deployment digest，任务 id=1 内容完全相同；原发布记录随后为 superseded，历史快照保留。
- 环境准备边界：节点到 Docker Hub 曾返回 EOF，私有 runner 镜像冷拉取也较慢。本轮 operator 向相应 job 自有引擎预加载了相同版本的 runner/PostgreSQL/Ryuk/Go/Alpine/Dockerfile 镜像，保留了日志；没有共享 Docker data、跳过步骤或以预建应用镜像替代构建。细粒度日志逐条 ACK 也使终态可见性延迟；镜像分发和日志批量效率留待后续优化，本批不宣称无预热冷启动已优化。
- 失败记录保留：`7763c989c7a653be86c55a75` 在冷拉取中 interrupted；`45002450931a28b84b98bfcc` timed_out；`e6a53245d04200cd208672e5` 在 Go VCS 状态检查失败，修复精确 workspace Git 信任；`d3bb4a8e78c71e1f4cff3707` 在 Docker Hub 拉取 PostgreSQL 时 EOF。各次构建/发布均被依赖门禁阻断，没有错误交付。

证据保存于本机私有目录 `/Users/guo/.config/forgelet/live-20261023/stage10b/`：最终 `verify-release-final.log`、`testcontainers-complete-final.log`、`testcontainers-flux-retry-final.log`、`testcontainers-git-workspace-act-final.log`、`testcontainers-preloaded-image-final.log`，以及 run/日志/Checks/Flux/HTTP/重启快照。升级前的 patch、未跟踪文件、原子 SQLite、PVC/config/Deployment 备份均保留；本项目未自动提交或推送。

收尾：forgelet namespace 仅一个控制面 Pod，执行 Pod 数为 0，forgelet-lab 也无 Pod；临时 Testcontainers 容器全部清理。删除本轮 18 个本机 integration 镜像 tag、私有 builder 和可重建的镜像归档/节点临时脚本，停止预热 watcher 与 Zot 端口转发，保留原 `lg-multiarch` builder。未改变 Zot 配置、用户业务部署或数据库 PVC。最终作业日志未发现实际 registry 密码或 GitHub token 模式泄露。

### 后续配置：Zot 内网执行镜像（2026-10-26）

- owner 提供现有 `forgelet/zot-registry`（kubernetes.io/dockerconfigjson）；实际认证 `https://zot.k.mygo.rocks/v2/` 返回 200。未读取 README 中的密码，也未回显凭据。
- 将已验证 act 镜像原样同步到 `zot.k.mygo.rocks/forgelet/executor-act`，manifest digest 仍为 `sha256:480bc4a1aa5ff07662b83884290f3957ded3cc35dd1336bcc522f981f96db33a`。只修改 `ubuntu-latest` profile 的 image 和 act.registry_secret 为该镜像及 `zot-registry`，重启原控制面；server/native 镜像、应用构建 registry 和 GitOps 目标保持原配置。配置与数据库先备份。
- HTTPS 入口的大层上传曾返回 404 upload unknown / 503 reset；最终通过临时 Kubernetes 端口转发直接同步到 Zot Service，完成后关闭转发。执行器仍使用正常 HTTPS/证书验证，无 insecure registry、Zot 服务配置变更或额外常驻服务。
- 实际手动探针 run `ffb52f11c898aa4a4426afa4` 成功：新 Pod、自有空 Docker data，无手工镜像预热，自动从 Zot 拉取 runner，运行 Docker 28.5.1、真实 Node20.20.0/24.14.0。内层任务确认不可读取 registry config、workload token 和 Kubernetes token，空 permissions 下无 GITHUB_TOKEN；日志包含 ZOT_COLD_PULL_OK，未发现 Zot 密码或 auth 值泄露。节点 containerd 已缓存相同 digest 的层，主 Pod 拉取引用为 261ms；这不代表节点全冷启动耗时，内层空引擎拉取确实执行。
- 本次只有部署配置/相同镜像分发和实际探针，没有修改产品代码，也未重复 make verify 或 Testcontainers。此前 CI/CD 发布与回滚记录仍有效；workflow 自行拉取的 Docker Hub 镜像未切换到 Zot，日志逐条 ACK 的效率也尚未改变，10.5d 保持未完成。

本批证据与配置备份位于私有目录 `/Users/guo/.config/forgelet/live-20261023/zot-20261026/`。后续升级 act 镜像时须先同步新 digest 到 Zot，再更新 profile；原 registry 镜像保留可用于配置回退。

## 上批：10.5c 生产形态测试应用与实际 homelab 实验

按 owner 最新要求，不再用现有业务仓库试验。本轮只修改、提交并推送三个已有专用测试仓库；forgelet 工作区保留未提交变更，没有提交或推送本项目，也没有升级线上控制面。

- **应用与私有依赖**：`forgelet-e2e-app` 当前固定实验提交 `1941631595363a2e2c67e9baf1d1c3ee09008168`。Go/PostgreSQL 任务 API 包含幂等创建、请求冲突、乐观锁领取、归属校验、完成和持久化；迁移有事务、版本记录和 advisory lock。`forgelet-onboarding-e2e` 新增 contracts SDK，提交 `67ce93e`、发布专用 `v0.1.0`；20 个并发领取者只能有一个成功，迁移重入、重新连接和严格 HTTP 输入均有测试。未修改业务 SDK 或业务 tag。
- **接近实际的 workflow**：ci/image → reliability，typed inputs、secrets inherit、step/job/workflow outputs；原版 checkout@v4、setup-go@v5、Codecov@v4、login@v3、metadata@v5、build-push@v6。实际执行私有模块下载/校验、vet/build/race、Atlas、PostgreSQL Testcontainers、私有 SDK checkout、BuildKit secret/命名 context、真实 GitHub API 与 registry push。公开 Actions 固定 SHA，版本见 `test/actkernel/actions.json`。Codecov GPG 签名验证在最终 profile 通过；未配置该私有仓库上传 token，不能记为覆盖率上传成功。
- **先部署再实验**：`forgelet-e2e-gitops` 提交 `d8dc972` 增加应用迁移 initContainer、PostgreSQL Deployment/Service；数据库 Secret 和 1 GiB PVC 独立创建。先以基线 digest `sha256:da919d55470f4d87c72a1b710d1641442ed27b0dced1b73da7a864d5d911e5d9` 部署健康任务 API，再开始新内核 workflow。数据库属于被测应用，控制面仍是 SQLite。真实创建/重复请求/领取/完成任务 id=1；数据库 Pod 重启后仍为 completed、version=3。
- **真实运行与缓存**：修正事件输入后，`clean-ci-v2` 的 setup-go cache-hit=false、必跑 PostgreSQL 步骤通过；`verified-image` 在新 job 目录实际恢复 cache-hit=true，`TestPostgresAcceptance` 通过（47.03 秒，整步 51.29 秒），两个 workflow job 成功、SDK output=v0.1.0。源码和 SDK 分别使用只读单仓库 installation token，operator session/App 私钥/Kube 凭据不进入 CI。
- **失败与取消**：`ci-failure` 确实执行注入失败点，reliability=failure，构建未执行、无 digest，实际调用发布工具被拒绝；不把 stock 引擎留下的空 job 结果冒充 skipped。取消探针记录运行中 Pod/engine，foreground 删除后 Pod 数为 0，同名凭据 Secret 删除。所有最终 CI Pod 均确认 engine 已退出，采集结果后清理 Job/Secret。
- **发现并修复的实验问题**：vfs 超过 12 GiB emptyDir 被驱逐，改 overlay2；初始 dispatch 把 boolean 直接放进 event 导致条件步骤跳过，改为事件字符串并增加必跑步骤验收和回归测试。初始 `cold-overlay` 的镜像成功不计为完整 PostgreSQL 验收；上述 v2 运行才计入。补齐 GPG；执行镜像分开 apt 层，采用 gzip 通过 registry 入口，最终验证 profile 为 `sha256:39211b7e2e1946e5afe06eb35c7b92b03bfa3ccc5645f3711065a11887df4e18`，保留 `production-like-20261025-verified` tag。失败、取消和准备日志保留，不混称全部尝试都成功。

| 验证 | 本轮结果 |
|---|---|
| 产品 `make verify` | 最终通过：真实构建、Go race/进程内集成、lint/generate、35 Vitest、14 Chromium（31.3 秒） |
| `make test-kernels` | 最终通过，319.571 秒；16 个 k3s 叶子探针（含既有 3 项缺口观测）+ 新 Docker backend race Testcontainers（29.62 秒），两个候选各 9 项行为测试；私有业务探针明确 SKIP |
| 应用单测/集成 | SDK race、应用 HTTP/领域 race、真实 PostgreSQL Testcontainers 通过；本机应用最终整包 9.624 秒；线上另实际运行必跑数据库验收 |
| operator 工具 | 8 个行为测试通过：事件类型、条件误跳过、发布门禁、被驱逐记录、凭据范围、私有文件权限、取消归属 |
| 全量产品 k3s 23 场景 | 本轮未重复；上批最终 1072.166 秒记录保留，不能混称本轮再次通过 |

实际发布记录：

| 阶段 | 结果与证据 |
|---|---|
| 正常发布 v2 | CI digest `sha256:3c5780c83251125b2f453ada865d38ed510b50d274c1f81b0e2fe0c2dd70b188`；CD run `23caa78a929660316af4b2f6`，delivery `20ac3c2247f6d31c442c9b3994473983`，GitOps `d69c4c6834a009980c2c5319a378c10c596757fd`；deployed、HTTP healthy/version/SHA/digest 相符，任务 id=1 保留 |
| 故障发布 | digest `sha256:44e974af332ef29ac65321b8411db705796fcaa383b7cbedc8b7bd40f9136321`；CD run `f1f7c1b19ac0b3e2a33e89cf`；CI 成功，应用 readiness 故意失败；Flux Ready=False/HealthCheckFailed，delivery 保持 delivered，坏 Pod 不进入 Ready endpoints，旧 v2 继续服务，任务数据不变 |
| 回滚 | 原 rollback API record `580da273b9d01a4af3c0c9fb76dc3465` 恢复前一个 v2 digest；GitOps `475d734e933795760094e92e9ef1c549cf5dd4bb`；deployed、Flux Ready=True、HTTP healthy/version/SHA/digest 相符，坏 Pod 消失，原任务仍为 completed/version=3 |

证据保存在本机私有目录 `/Users/guo/.config/forgelet/live-20261023/production-like/`：`forgelet-verify-final.log`、`kernels-final.log`、`app-tests-final.log`、`operator-tests-final.log`、各轮脱敏日志/结果，以及 `verified-image-live.json`、`database-restart-http.json`、`unhealthy-image-live.json`、`rollback-live.json`、`cancel-probe-cancel.json`。顶层证据/凭据文件权限 0600、私有根目录 0700，不提交凭据或原始请求 Secret。

收尾确认：`forgelet-lab` 无 Pod、无实验 Job，仅保留 registry Secret 与单个 5 GiB local-path cache PVC；所有短时请求 Secret 已删除。本机只移除本次 `forgelet-lab-20261025` builder 和测试镜像，原 `lg-multiarch` builder 保留；临时端口转发停止。被测应用/PostgreSQL 保持健康，数据库 PVC/Secret 保留供后续使用，没有空闲 Docker 引擎。

**边界**：这是独立 homelab sandbox 的完整 workflow 实验，再由 operator 把固定 SHA/digest 送入现有手动 homelab workflow → GitOps → Flux。产品 allowlist 仅保留该手动 CD 路径，GitHub 原生 Actions 继续关闭；产品自动 push 调度、claim/result/日志协议与新内核整合仍属 10.5b，不能称为已接管。执行器是 privileged/rootful Docker Exec 实验配置，无宿主 socket/hostPath/Kube token；没有宣称 stock host PTY race 修复或生产隔离完成。工具与重现方法见 [test/productionlike](../test/productionlike/README.md)。

## 上批：10.5a 执行内核对比（历史，未生产接入）

- **代码与选型**：新增 `test/actkernel` / `make test-kernels`，分别构建固定 nektos/act v0.2.89、Gitea act v0.262.0；依赖 mod/sum 隔离在临时模块，产品 go.mod 未引入这两个执行库。用导出的单 RunContext、stdin JSON/结果、job logger 和 ActionCache 做窄接入，验证单 job 选择、外部 needs 快照、outputs、skipped 与秘密边界。**优先上游 act**：Gitea 的平台特化导致额外 GitHub API/结果适配，详细取舍见 architecture。
- **真实公开 Actions / 按需形态**：两者都验证 Node/Composite/post、失败清理、取消、workflow_call/typed inputs/inherit/outputs、实际缓存冷→热命中和 Docker Action。上游 act 跑完真实 login@v3 → metadata@v5 → build-push@v6 → BuildKit secret/命名 context → Pod 内临时 Registry push → pull/run。四个公开 Action 固定 SHA，源码归档通过库现有 ActionCache 接口提供，未改脚本。metadata 通过带 token 校验的只读 GitHub HTTP fixture；这不是实际 GitHub token/CD 成功。
- **明确复现的兼容缺口**：Gitea withGithubEnv 强制把 GITHUB_API_URL 写成 instance + `/api/v1`，使真实 metadata-action 调错端点失败；已改为确定性的 `build-push_github_api_gap` 负向回归，不能称 Gitea 构建通过。两者完整 workflow 模式遇子 workflow 失败都会提前结束，后续 always job 未执行；保留 forgelet 的 SQLite 图/调度，不委托完整 workflow。Gitea disabled/父调用还可能留下空结果，probe 显式转换，禁止发布未求值 outputs。
- **单测与竞争检查**：两套候选各 **9 个行为叶子用例**通过；输入拒绝与日志边界另跑 race 通过，覆盖并发/分片脱敏、固定 Action 归档路径/前缀、needs 输出和失败跳过、不重跑依赖、取消。**两套 stock host 的完整 race 均失败**，定位 PTY AutoStop 读写竞争；原失败日志保留，未打补丁或关闭检查后宣称 stock host 无竞争。生产接入前还须修复/替换该路径、保持严格 Node runtime 与现有可靠取消/日志协议。
- **新 Testcontainers**：临时 k3s 的 **16 个叶子探针完成，TestKernelPods 184.98 秒**；连同候选单测整个命令 **196.192 秒通过**。其中两项完整 workflow always 与一项 Gitea GitHub API 是明确的缺口观测，其余为行为回归，不能将 16 项写成两套库全面兼容。Docker 只对相关 Pod 添加 native sidecar，数据/socket/cache/workspace 都是本 Pod 的 emptyDir，无宿主 socket、hostPath 或 Kube token；测试确认主任务完成时 engine 已退出。PoC 是 privileged/rootful/vfs，**不是生产/rootless 隔离完成**，普通 job 无引擎，未增加常驻服务。
- **真实业务运行（失败，不记为通过）**：使用 steam-tasks 提交 `8265ad9772c5c92d3724a0372f17c31a4a8e498d` 的干净 archive，并校验 ci/image/reliability 三份原文件一致，运行原 ci → reliability；Linux amd64 匹配 Atlas 下载架构。两者实际完成 setup-go@v5（版本文件 → Go 1.26.4 / cache restore）、私有依赖下载与 go mod verify，随后报 `no required module provides package github.com/shitamachi/steam-proto-go/priceregion`。本地只读核对也证实 SDK v0.6.0 无该目录、当前 SDK 提交有。没有用本机 SDK 替换固定依赖，没有删步骤；Atlas、业务 race/Codecov 与 image push 尚未执行成功。另确认原 image workflow 未传 Dockerfile 必需的 steam_proto_go 命名 context，后续需业务仓库明确修正。
- **产品回归**：本轮 `make verify` 通过（构建、Go race/进程内集成、lint/generate、35 Vitest、14 Chromium）；新增工具最终另跑 race/lint 0 issues 和候选单测。现有全量临时 k3s **23 个叶子场景通过，1072.166 秒**，包括调用、取消/重启/磁盘、Node/工具链、artifact/cache、BuildKit → GitOps → Flux 发布/回滚、真实浏览器和登录授权；与上面的内核探针分别统计。
- **操作与资源**：没有修改 GitHub 仓库、SDK tag、生产镜像或实际 homelab；没有提交/推送。开发业务探针临时使用本机已登录账号读取私有依赖，不代表产品最小权限 job token 已接入；临时凭据文件已删除，容器/临时集群全部清理，公开测试镜像在收尾清理。在线产品兼容范围不变。

证据：`/tmp/forgelet-kernel-comparison-final.log`、`/tmp/forgelet-kernel-candidate-unit-final.log`、
`/tmp/forgelet-kernel-product-testcontainers.log`、`/tmp/forgelet-kernel-verify-final.log`；
真实业务失败为 `/tmp/forgelet-kernel-evidence/{upstream,gitea}-steam-ci.log` 与 `/tmp/forgelet-kernel-business.log`；
stock host race 为 `/tmp/forgelet-kernel-{upstream,gitea}-native-final.log`。
早期镜像准备、默认 Git 克隆超时、API 地址及测试 fixture 修正前的失败保留在其他 kernel 日志，不计为最终通过。

## 上批：阶段 10 第一批（代码与自动验证完成，未部署 homelab）

- **解析与表达式**：固定 actionlint v1.7.12 和其 YAML v4.0.0-rc.3，workflow 使用标准语法前端与有界 lowering；系统配置仍用 YAML v3。删除旧手写表达式 parser，以固定 act v0.2.89 的 MIT evaluator 源码作局部适配，保留 LICENSE/NOTICE。移除 act runner/model/Git/Docker 依赖与隐式文件访问，控制面仍为 Go；修正递归字符串转数字、null 比较、比较优先级、JSON 数组索引、format 大小/非法占位符和状态函数语义。增加事件对象、索引、对象过滤与格式化 JSON；仍保留上下文和大小限制。
- **可复用 workflow**：从一次固定提交快照读取同仓库 `./.github/workflows/*.yml/.yaml`，即使辅助 workflow 未被选为顶层触发入口也可被调用。持久化 call_gate → 子 job → call_result；支持嵌套、循环/深度限制、typed inputs/default/required、普通字符串模板、needs 完成后的参数求值、workflow outputs → needs。实际 job 继续使用独立 Pod 和既有 matrix/全局并发；控制节点不占执行额度、不创建 Pod。gate 保留原始触发 inputs，解析出的调用参数单独保存，避免重跑丢失原始参数。
- **权限与秘密**：支持 contents read/none 的受限声明，调用不能提权；不自动生成 GITHUB_TOKEN。支持显式 `secrets.NAME` 引用映射及 inherit，未传递时为空作用域，嵌套不能恢复已隐藏的 root secret。required 在 admission/rerun 检查配置名称，值仍按执行身份解密领取并脱敏，不持久化明文。
- **实际业务样本**：收录 steam-tasks 固定提交 8265ad9772c5c92d3724a0372f17c31a4a8e498d 的三份原始 workflow。标准解析通过，产品检查会给出 setup-go@v5/docker Actions 等真实执行能力阻断。没有删减业务步骤，也没有宣称整个应用 CI/image workflow 已成功运行。
- **本批边界**：远程 reusable ref、调用 job 的 matrix/concurrency、被调用 workflow 的 concurrency、依赖运行期 inputs 的提前执行设置、dynamic matrix、隐式 token/cache 与更多 Actions 后续实现，当前明确诊断。调用内部静态 matrix 已接入逻辑 job 聚合；新 API `workflow_files` 仅用于管理员提供的源码快照，GitHub 正常接入自动加载辅助文件。
- **修正真实回归发现的问题**：领取后、获取凭据前收到取消时，凭据接口继续拒绝发放，执行器通过原 UID 的 control 状态确认取消并正确上报 cancelled；不能把未确认的权限错误伪装成取消。补齐确定性单测、真实 HTTP/SQLite/执行器集成与真实 Pod 回归。参数求值用 AST 判断 needs/inputs 依赖（包含括号、整对象和动态索引），不再猜测表达式文本；禁止占位值决定早期 profile/name。没有 job output 时 workflow output 返回空值。
- **自动测试**：最终 `make verify` 通过（真实构建、race 单测/进程内集成、lint/generate、35 个 Vitest、14 组 Chromium）。workflow 覆盖率 **88.2%**；调度核心 reserveGraph **89.6%**、dependencyContext **100%**、调用状态处理 **88.6%**；storage 全包 **77.6%**，不将其混称为核心覆盖率。
- **Testcontainers**：全量临时 k3s **23 个叶子场景通过，1117.652 秒**，含两个新增调用场景、既有 needs/matrix/COE/fail-fast、70 秒停机恢复、磁盘保护、真实 Node/Composite/setup 工具链、artifact/cache、BuildKit → GitOps → Flux 发布/回滚、4 组真实工作台浏览器与登录授权回归。全量启动后补充的整对象早期设置诊断，另用最新代码/镜像专项 **2 场景通过，217.507 秒**，包含真实 API 拒绝该输入、嵌套调用、secret 脱敏、outputs、同 UID 重启恢复、rerun 参数保留、取消、失败/跳过与无 Pod 控制节点。
- **测试准备与重现**：早期冷构建/外网镜像拉取导致的准备失败没有计为通过；曾暴露取消竞态和未完成 builder 占用并发的连带失败，均在最终回归重验。提供可选 FORGELET_TEST_GOPROXY / FORGELET_TEST_DEBIAN_MIRROR；本轮 Go 构建用 https://goproxy.cn，Debian 使用官方 HTTPS 源，未关闭 go.sum、校验数据库、CA 或 Debian 签名。FORGELET_TEST_IMAGES 可导入本机缓存的官方 k3s/Registry/BuildKit/Flux 镜像，FORGELET_TEST_KEEP_IMAGE=1 用于调试重试；普通任务仍不使用 Docker。临时集群总 context 40 分钟、Go test 45 分钟，只有集群启动等待放宽至 10 分钟，功能等待/断言保持原值；失败时记录节点/系统 Pod/事件。临时公共依赖缓存代理已停止并清理。

验证日志：`/tmp/forgelet-stage10-verify.log`、`/tmp/forgelet-stage10-testcontainers.log`、`/tmp/forgelet-stage10-calls-testcontainers.log`；覆盖率为 `/tmp/forgelet-stage10-final-coverage.out`。初次全量失败记录保留在 `/tmp/forgelet-stage10-testcontainers-before-fixes.log`，不将旧失败版本的结果当作最终验证。

- **部署与仓库操作**：本批没有修改实际集群、GitHub 仓库、授权或远端提交。沿用 schema v6；新增调用数据在持久化 Plan JSON 内，不需要迁移。

## 阶段 10 前置调研记录（历史）

以下记录为第一批实现前的调研和本机 PoC；本轮产品变更与验证以本页上节为准。目标为完整 GitHub workflow 可运行，尚未运行真实 steam-tasks CI。

- **源码与选型**：首选 actionlint v1.7.12 的 Go Workflow/表达式 AST；表达式优先薄适配 act v0.2.89 pkg/exprparser，执行生命周期借鉴 act host；官方 @actions/workflow-parser/@actions/expressions 0.3.61 作为语义与测试对照。完整 act runner 不接管 SQLite/全局调度，官方 runner 不部署。候选均为 MIT；固定版本与上游限制见 architecture。
- **实际 PoC**：actionlint、act model、官方 parser 都接受 steam-tasks 三份原始 YAML；官方读取同仓库被调用 workflow 成功。act 与官方的 7 个共享表达式例子一致，另验证 act 对事件对象过滤的求值。act 的表达式包与 actionlint v1.7.12 组合通过这些试验；另运行 act pkg/exprparser 上游测试通过（1.418 秒）。不把小样例当作完整兼容证明。
- **执行试验**：act host 在 Docker 地址不存在的情况下跑完独立 fixture 的 workflow_call → Composite → 真实 Node 24/main/post → workflow output → needs 下游；另一个用例失败后跳过普通下游、执行 always 清理。首次 fixture 未 checkout 导致本地 Action 不存在，补齐正常 checkout 后通过；不是修改 steam-tasks 来绕过不支持能力。
- **边界与进度**：生产仍有 permissions/workflow_call/setup-go 等已知阻断；cache/凭据、构建 secret/命名上下文、工具链和 Docker 相关 Actions 运行环境仍需实作。未运行本批 make verify/Testcontainers，未修改 go.mod/产品代码、未部署、未提交或推送；阶段 10 实施顺序已写入 plan，不设审批或基线门禁。

临时试验位于 /tmp/forgelet-workflow-research.TgaxYz，包含各候选的固定依赖、程序与 result.txt，以及 host-runtime.log/host-failure.log。完整设计和来源见 [architecture.md](architecture.md) 阶段 10。

## 本轮：评估并修正外部 Review（阶段 9）

附件评审基于更早的重写版本。开始本轮时工作区干净，#1 COE 错误与 #2 序列化大小限制已经由 089f3d8 / 93d5d42 修复；保留已有实现，同时补上调度时合入 needs 输出后的大小复查：超限在创建 Pod 前明确失败，不能被 COE 吞掉，也不阻塞后续任务。当前保留单 Plan 900 KiB、创建时 run 合计 16 MiB 的限制，并增加真实 executor → HTTP → SQLite 的基础设施失败回归。

- **已实现 #3/#7**：空 public_url 不生成相对详情链接；已存 Check ID 的 GET 404 后按本 App/SHA/external_id 重新查找/创建并 CAS 替换。事件/Checks 持久化重试计数与到期时间，2 秒起指数退避至 5 分钟，连续 12 次后暂停并保留诊断，后续正常项可推进；新增受保护的管理员重试 API 与详情页 Checks 重试入口，不重跑 job。
- **已实现 #4/#5/#6**：运行时永久错误立即 interrupted，临时错误有持久化退避和 12 次预算，释放并发；取消删除失败保留独立预算。Pod grace 改 45 秒、最终 flush 保持 20 秒；修正终止 Pod 的身份校验，允许原 UID 收尾，禁止再 claim/取凭据。控制面中断容忍默认 5 分钟，可按 profile 配置，最大 1 小时。
- **已实现 #8/#9/#10**：rerun 检查仓库启用和当前 profile/BuildKit 能力；同名新 repository ID 返回冲突；Flux spec.path 必须匹配目标目录。
- **其他采纳**：旧零 timeout 计划 deadline 1320 秒、身份比较 JobID、普通步骤过滤内部传输环境变量、缺失/错误加密主密钥阻止启动、运行列表在 SQL 移除执行大字段、token/open/session 请求规则文档对齐。
- **明确暂缓/保留**：run 级共享 event/vars 存储改造、GHE clone 配置、可信代理与登录限速模型后续按实际需要实现；blob 目录未知文件继续 fail-closed。没有新增产品依赖、服务或审批流程。
- **提交与远端**：`32e3916`（添加仓库弹窗默认展示全部仓库、滚动加载）、`4ac1ca4`（阶段 9 可靠性批次，含 schema v6）、`4f626da`（阶段 9 文档）。分支 `chore/homelab-reset-20261023` 已推送 `origin`，`main` 尚未合并。
- **自动验证**：批次记录中的 make verify 已通过：Go 构建/race、HTTP/SQLite、lint 0 issues、generate、TypeScript/格式检查、35 个 Vitest、14 组 Chromium（最终 32.6 秒）；另补的 YAML 时长配置 race 回归通过。全量 Testcontainers **20 个叶子场景通过，1046.235 秒**；真实工作台 Chromium 4 组 48.4 秒、认证 Chromium 1 组 8.0 秒。部署前复核了 `go test -race ./...`、lint 0 issues、`tsc -b`、35 个 Vitest 和 14 组 Chromium；本机 `make verify` 在 Playwright 浏览器安装阶段因沙箱无法写 `~/Library/Caches/ms-playwright` 失败，浏览器测试已单独通过，不把该安装失败计为代码失败。全量测试启动后追加的 needs 大小 guard 使用最新镜像另跑专项，不混称该次全量已包含此新增场景；临时 k3s 新增 70 秒控制面离线后原 UID 恢复、真实 Pod 标签漂移不接管/删除、取消结果收尾断言。schema v5 → v6 迁移与待处理工作保留有独立回归。
- **实际部署**：2026-10-25 部署 server `sha256:fbc91710a659b85b17650f39115472b4ac992de578e01d62f005eaefadaaf885`、executor `sha256:cc230abba6a212797beccf88cb792b2e8c9faaf7ecba6d945961059af786d59c`（linux/amd64）；ConfigMap 2 处 executor digest 已更新。rollout 1/1 Ready、0 restarts。升级前保存 Deployment/ConfigMap 与 v5 SQLite 快照；本地副本上先执行 `PrepareRestore` + `Open` 迁移验证（`user_version=6`、`integrity_check=ok`、16 runs/34 jobs/1 admin），再在生产库启动迁移。部署后 v6 快照为 16 runs/34 jobs/1 admin/8 events、`integrity_check=ok`。旧 stage 8 server/executor digest 分别为 `sha256:fbbd4fd1...`、`sha256:751bd3c6...`；旧程序不能直接打开 v6，回退需使用部署前 v5 快照和原密钥。
- **实际验证**：从集群内访问公网入口，`/healthz` 200；`/api/auth/session` 返回 `mode=session`、`github_enabled=true`；首页为当前嵌入 bundle；匿名 `/api/runs` 401。未用管理员密码登录，未触发新的 GitHub push/manual CI 运行。仓库添加弹窗默认展示全部仓库，首屏 20 条、滚动/按钮加载更多；14 组 Chromium 覆盖默认列表、50 仓库增量加载、搜索过滤和无权限项。

验证日志：`/tmp/forgelet-review-verify-latest.log`、`/tmp/forgelet-review-integration-final.log`、`/tmp/forgelet-review-integration-size.log`；升级/重试行为见 architecture 和 deployment。

## 上批：阶段 8 部署与 review #1/#2 修复（2026-10-25）

- **提交与远端**：`2d808da`（单进程实现）、`9a79c4d`（文档重写）、`089f3d8`（continue-on-error 不再隐藏执行器错误）、`93d5d42`（Plan 大小上限）。分支 `chore/homelab-reset-20261023` 已推送 `origin`；`main` 尚未合并。
- **review 修复**：所有 `Execute` 错误路径现在都携带非空 `Result.Error`，job `continue-on-error` 不再把执行器/协议错误转换为成功；创建和重跑 run 时校验 Plan 序列化大小（单 Plan 900 KiB、单 run 16 MiB），避免创建无法被 executor claim 的任务和 SQLite 无界增长。新增 executor、storage 和 server admission 回归。
- **自动验证**：`go test -race ./...` 通过；`golangci-lint run --build-tags integration` 0 issues；当前前端 `tsc -b` 与 35 个 Vitest 通过。本批未重跑 `make verify`、Playwright 或 Testcontainers；本页第 8 阶段此前的 `make verify`/19 场景结果仍是其记录范围，不混称为本次运行。
- **部署**：server `registry.shitamchi.com/forgelet/server@sha256:fbbd4fd1d3d5579eac306f807f7127829ca6fbc22a57323b37239801939893bf`，executor `registry.shitamchi.com/forgelet/executor@sha256:751bd3c6e346e4e58a4eba1123b57fb336af6fb4885d1da52f1901026ed2c373`，均构建为 linux/amd64。ConfigMap 中 2 处 executor digest 已更新；Deployment server digest 已更新；rollout 1/1 Ready、0 restarts，无遗留执行 Pod。旧 server digest 为 `sha256:27d3bf56eff5aa1320269fd2f5ef91daf49f9c12b6de13a3c3ff0161fc77b75d`，旧 executor 为 `sha256:e571477be78896227359587d6e218452eb5d66e9ea61ffb83b314a274114114f`，部署前 Deployment/ConfigMap 已保存。
- **备份**：部署前保存 Deployment/ConfigMap，并生成带 `restore_required` 标记、`integrity_check=ok` 的 SQLite 快照（640 KiB；16 runs、34 jobs、1 admin）。PVC 上为 `/data/backup-manual-20261024T180905Z.db`，部署后另生成 `/data/backup-manual-post-20261024T183231Z.db`（同样 16/34/1）；本地证据目录 `/tmp/forgelet-deploy-20261024T180612Z/`。没有复制或输出 Secret 明文。
- **实际验证**：从集群内访问公网入口，`/healthz` 返回 200，`/api/auth/session` 返回 `mode=session`、`github_enabled=true`，首页标题与嵌入 bundle 为当前构建；匿名 `/api/runs` 仍返回 401。未用管理员密码登录，未触发新的 GitHub push/manual CI 运行。
- **当时剩余边界（本轮修正见阶段 9）**：review #3–#10（Checks 空 public_url/404、永久 reconciliation 卡住、cancel grace、executor 1 分钟容忍、outbox 重试、rerun 禁用检查、同名新 ID 重绑、Flux path 校验等）尚未修复；GitHub App 授权范围未扩大。

## 上批：修复仓库启用 403 与重写接入页（阶段 8 代码与自动验证）

- **根因**：截图中的仓库管理员权限并未缺失。使用已有加密凭据做只读诊断：`GET /user` 为 200、scopes=`read:user, repo`；`GET /repos/shitamachi/yuki-project` 为 200 且 permissions.admin=true；`GET /user/installations` 明确拒绝独立 OAuth App token。旧实现把 OAuth token 用于 GitHub App 安装授权管理，fixture 却错误返回成功，导致真实新仓库启用时 403。此处是实现与测试假设错误，不能归因于用户权限。
- **代码修复**：移除 OAuth 的安装授权调用；未授权时返回 HTTP 202 `authorization_required`，区分仓库待授权、未安装、暂停和权限更新，附服务端确定的操作链接；用户在 GitHub 确认后返回复核，验证通过再启用。待授权/失败/版本冲突不改变可信配置，不创建运行，不扩大 OAuth/App 范围。
- **页面重写**：主视图只保留已接入仓库，CI 开关、授权和最近运行分别显示；搜索与状态筛选、停用/恢复、检查运行及管理菜单。新增仓库在独立弹窗搜索、选择、确认，每页最多 6 个结果，支持账号筛选和无权限/无结果/请求失败状态；打开弹窗才请求账号目录，不再在主页铺开全部仓库。刷新恢复待授权选择，错误就近显示。
- **当前验证**：make verify 通过，Go race/HTTP/SQLite、35 个 Vitest、13 组 Chromium（最终 29.6 秒），包含旧工作台回归和新增授权等待/复核、刷新、50 仓库分页、无权限与移动布局。发现并修复新弹窗与旧 CSS 同名导致搜索框不可见的问题。全量 Testcontainers **19 个叶子场景通过，1020.446 秒**；工作台真实 Chromium 4 组 26.0 秒，认证真实 Chromium 1 组 8.0 秒，认证/重启专项 86.19 秒。授权前两次复核仍保持待接入；测试中单独模拟用户在 GitHub 确认后，真实 Go → SQLite 保存绑定 → 执行 Pod 成功；改密、session/OAuth 凭据与配置重启、原 Actions/BuildKit/GitOps/Flux/磁盘/清理回归全部通过。测试 OAuth 安装修改接口明确返回 403，外部确认通过仅存在于测试进程的本机桥接完成，产品未增加绕过授权的 API。临时容器和本机预览进程已清理。该代码验证批次当时真实集群尚未更新；实际部署见本轮，不将本机预览或受控 GitHub fixture 计为线上新仓库授权成功。

验证日志：`/tmp/forgelet-repositories-verify-current.log`、`/tmp/forgelet-repositories-k3s.log`；本机预览截图：`/tmp/forgelet-repositories-overview.png`、`/tmp/forgelet-repositories-picker.png`、`/tmp/forgelet-repositories-mobile.png`。本批不新增功能依赖，不自动提交或推送项目 Git。

历史阶段 7 文档中的“OAuth 自动加入 App installation”和其 fixture 成功结论在此撤回；实际阶段 7 上线只验证了已有授权的三个仓库，本批修正 architecture/deployment/plan 的相应承诺。没有添加 PAT、自动授权所有仓库、直接向执行 Pod 下发 OAuth token 或额外常驻服务。

## 上批：管理员账号与 GitHub 登录

- 新安装清单显式 `admin_access: session`。初始化单个 admin，Secret 指定或生成随机密码，bcrypt 哈希、SQLite 会话摘要、Cookie、退出与改密；重启不从 bootstrap 文件重置密码。旧 open/token 显式配置保留兼容，新会话模式不允许旧 admin token 绕过登录。
- 首次关联 GitHub 需要本地管理员会话，后续可以 GitHub 登录；不开放匿名 GitHub 自助注册。OAuth state/PKCE/浏览器和管理员会话绑定，一次性消费；GitHub 不变 ID 防止账号错绑。token 与 refresh token 使用 AES-GCM 加密，浏览器只收到 session Cookie。
- GitHub OAuth 请求 `read:user repo`，列出账号可访问的仓库，按实际管理权限决定可接入项；当时尝试用 OAuth token 将仓库加入 App installation；此调用不受支持，已在阶段 8 移除并改为用户在 GitHub 确认后复核。已有 App 的 webhook、clone 和 Checks 链路继续使用；没有重复创建每仓库 webhook。新账号/组织首次仍需安装 App，组织策略拒绝时给出诊断，不假报接入成功。
- Go 专项 race、构建/lint/generate、21 个 Vitest 和 11 组 Chromium 已通过；首轮浏览器发现登录后 Query 缓存清理导致页面停留在登录页，已修复并加入回归。首次容器回归暴露旧测试对同名仓库两个表格的定位不唯一，已改为明确选择登记表格行；重跑 19 个场景全部通过（951.913 秒）。

### 阶段 7 验证结果与边界

| 层次 | 结果 |
|---|---|
| Go / HTTP / SQLite / race | 初始化幂等、密码哈希/文件权限、session 摘要/数量/到期、跨源拒绝、退出/改密撤销、重启、备份恢复撤销、OAuth state/PKCE/浏览器/管理员会话绑定、回调重放、GitHub ID 防错绑、授权范围不足/重定向拒绝、其余认证验证通过；当时的 OAuth 自动授权/HTTP 304 fixture 不能证明真实 GitHub 支持，该错误假设由阶段 8 撤回 |
| 浏览器 | 21 个 Vitest、11 组 Chromium；首次登录、失败后重试、立即切换、账号/改密/退出、GitHub 用户仓库启用、清理私有查询与不使用 localStorage 保存凭据，原工作台回归通过 |
| 全量 Testcontainers | 19 个场景全部通过，951.913 秒；原工作台 4 组真实 Chromium 27.6 秒，认证 Chromium 1 组 7.8 秒；App 授权后新仓库实际创建执行 Pod，原 Actions/BuildKit/GitOps/Flux/磁盘/保留回归通过 |
| 认证真实 k3s 专项 | 104.19 秒；正确 Cookie 属性、session/加密 GitHub token/绑定重启保留、退出拒绝旧 Cookie、改密撤销另一个会话、再次重启后新密码仍有效而 bootstrap 旧密码被拒、无浏览器 Cookie 的回调与重放被拒，webhook/Pod 身份校验不被登录替代 |
| 实际 GitHub / homelab | 已配置 OAuth App/Secret 并部署 session 模式；真实浏览器关联与 GitHub 再登录、50 个仓库发现、重启保留、真实 push 的两级 CI/Checks/日志/产物通过。详细证据见下文 7.7；不将 fixture 测试计为实际账号验证 |

认证继续内置于一个 Go 控制面与 SQLite/PVC，复用已有 x/crypto；没有新增常驻服务或依赖。新安装清单默认 session，已有显式 open/token 不会升级后被隐式改变。首次 admin 密码为随机值或 Secret 指定，不提供固定默认密码；GitHub Client Secret 和真实授权 token 未写入项目。
部署步骤、凭据/密钥备份和 schema v5 回退边界已更新 deployment/architecture；本批未自动提交或推送 Git。

## 7.7 真实 OAuth App 与 homelab 部署（2026-10-24）

owner 明确授权配置和上线，并确认 `read:user repo` 授权；已在 shitamachi 创建 [Forgelet Homelab OAuth App](https://github.com/settings/applications/3960356)，回调为 `https://forgelet.k.mygo.rocks/api/auth/github/callback`，不启用通配回调或 Device Flow。生成 Secret 的过程中，GitHub 确认页最终返回 404，设置页显示五个 Never used 的 Secret；经 owner 逐项范围确认后全部撤销，重新生成并保留唯一一个。Client Secret 仅保存在本机私有文件及集群 `forgelet-oauth` Secret；GitHub 登录 token 由服务端加密保存，没有写入项目或输出明文。

- **备份和升级**：升级前保存 ConfigMap/Deployment、完整 management version 2，以及 SQLite v4 一致性快照 `backup-ce8e827c9db32251e5b2d3e0008eb3d7.db`；本机副本完整性及 schema=4 检查通过。升级后 v5 快照为 `backup-18c8edbd08cb91c1c6de77eeb2e59e61.db`，完整性、bcrypt、GitHub ID、token 密文与 session 摘要验证通过；旧 server 回退须还原 v4 快照。
- **正式部署**：amd64 server 为 `registry.shitamchi.com/forgelet/server@sha256:27d3bf56eff5aa1320269fd2f5ef91daf49f9c12b6de13a3c3ff0161fc77b75d`，镜像上传重试后成功，远端 digest 与本机 archive 一致。`admin_access: session`，OAuth Secret 只读挂载 server 的 `/auth`；继续使用原加密主密钥，executor digest、并发和磁盘阈值保持原配置。最终 Deployment 1/1 Ready，私有安装清单已同步。
- **部署修复**：首次将 OAuth Secret 命名为现有 `forgelet-auth`，覆盖了该对象声明中的旧键，导致新 Pod `CreateContainerConfigError`。已从原本机私有凭据恢复 `admin-token` / `webhook-secret`，OAuth 改为独立 `forgelet-oauth`；随后启动、重启及真实签名 push 均通过。最终原 Secret 仅保留原两个键，新 Secret 仅保留 OAuth Secret，避免后续配置再次相互覆盖。
- **真实登录**：Edge 使用初始化 admin 登录后关联 GitHub ID `46468521 / shitamachi`；退出立即返回登录页，再点击 GitHub 登录无需管理员密码即可返回 Connections。新匿名 Chromium 只显示登录页；管理 API 匿名和旧 Bearer token 均 401。实际 Set-Cookie 检查 Secure、HttpOnly、SameSite=Lax 和 7 天到期通过；未修改管理员初始密码。
- **仓库与页面运行**：OAuth `/user/repos` 返回 **50 个仓库**，App 仍仅 selected 3 个；页面可搜索账号仓库。用现有 `shitamachi/forgelet-onboarding-e2e` 预检固定 `bde29de9266dfc9812e20efd82a15b7cbf316101`，从已登录页面创建 [e9adf54c82be6880ff446f96](https://forgelet.k.mygo.rocks/runs/e9adf54c82be6880ff446f96)，verify → follow、两个本 App Checks、完整日志和 `onboarding-report` 产物均成功。
- **重启与真实 push**：真实重启控制面后，原 Cookie、GitHub 绑定、加密凭据读取及 50 仓库发现继续有效，完整 management/version 与升级前相同。向专用测试仓库 README 写入 [真实 push 0f50f962](https://github.com/shitamachi/forgelet-onboarding-e2e/commit/0f50f962e64a9ceeef0617b7f99cdaba291a60b2)，签名 webhook 自动创建 [9c9d935f8aa67558297cfdca](https://forgelet.k.mygo.rocks/runs/9c9d935f8aa67558297cfdca)，两级 job、两个 Checks、日志 SHA/标记、下载产物内容均通过。
- **现有 CD 与范围**：既有发布 `5087c75cb5493271b25e301a1e5b0501` 仍 deployed，demo HTTP 返回健康 `v1.10-disk-guard`，磁盘未暂停，最终仅保留控制面 Pod；公网 `/api/runs`、`/api/auth/session` 均 403。本批没有新建发布/回滚或授权其他组织，也没有向 App 新增第四个仓库；当时 fixture 曾错误允许 OAuth token 扩展安装授权；此结论已由阶段 8 诊断撤回，不能当作真实新仓库接入成功。

本批只做正式构建、凭据配置与线上验证，没有修改功能代码；沿用阶段 7 已通过的 make verify / 19 场景 Testcontainers，不重复记为新测试运行。本机私有证据位于 `~/.config/forgelet/live-20261023/auth/`，包含前后数据库、配置、登录与重启状态、运行/Checks/日志/产物和 `final-status.json`。Edge 保留已登录账号页，用户现在可直接使用 GitHub 登录；初始化密码保存在数据 PVC 和本机私有文件，不粘贴到对话。项目未自动提交或推送。

## 上批：GitHub 自助接入第一批

- App JWT 验证本 App、分页读取安装与仓库，metadata-only token；只有完整快照成功才返回，不因失败改变已登记配置。
- `/connections` 提供 GitHub 安装/仓库授权入口、授权后刷新、账号/组织/暂停/权限提示、仓库搜索与逐仓库启用。绑定由后端按 repository ID 权威解析，不依赖用户填写 installation ID 或回调参数。
- 启用后从默认分支解析完整 SHA，逐 workflow 检查兼容语法、profile、触发条件；支持 push、workflow_dispatch 与 inputs。运行重新读取该固定 SHA，创建原有 run/Pod/Checks，网络重试保持幂等。
- 不增加 auth、OAuth、用户、常驻服务或依赖。批量启用、自动 rename 迁移、生命周期 webhook/定期校正及撤权后的排队/凭证清理属于后续批次。
- `make verify` 已通过：Go 构建/race、lint 0 issues、generate、21 个 Vitest、10 组 Chromium；workflow 覆盖率仍为 90.3%。新增 Go 测试覆盖分页、metadata scope、错误 App/仓库/重复列表、部分失败、权限/撤权、固定 SHA、幂等和 SQLite 重启。
- 已用新增 adapter 实际读取本 App，权威目录包含原有两仓库和新建私有 `shitamachi/forgelet-onboarding-e2e`（ID 1431908513）；保持 Selected repositories，仅新增该仓库。真实 App 权限未增加，测试仓库的原生 GitHub Actions 已关闭。
- homelab 部署前已保存 ConfigMap/Deployment 和 SQLite `backup-e0b934494134169cca1243fa6246de66.db`；原应用 `v1.10-disk-guard / healthy=true`。全量 Testcontainers 已通过 18 个场景，1061.235 秒；4 组真实 Chromium 用例 26.6 秒，新向导通过真实 Go/SQLite 创建执行 Pod。镜像上传后按 digest 部署，实际页面与真实 push 结果如下。

### homelab 实际部署与第三仓库验证

- **部署**：正式 amd64 server 固定为 `registry.shitamchi.com/forgelet/server@sha256:e1b79f1d75c361ec2daf8c40ba95c26526c671c485f547160d23c9e6ebd0752f`；Deployment 1/1 Ready，私有安装清单已同步。executor 保持原 digest，本批没有改变执行器。Recreate 切换后首次入口探测短暂 503，等待 ingress 更新后恢复 200。
- **接入页面**：[仓库接入](https://forgelet.k.mygo.rocks/connections) 使用匿名新浏览器验证；实际显示 App 安装和三个 selected 授权仓库。通过页面启用 `shitamachi/forgelet-onboarding-e2e`，后端绑定 repository ID `1431908513` / installation `174462839`；没有手填 installation。随后为原 `forgelet-e2e-app` 补齐稳定 ID 绑定，management 当前 version 2；`forgelet-e2e-gitops` 没有启用 CI，原 target 保留。
- **真实首跑**：[commit 9c52d616](https://github.com/shitamachi/forgelet-onboarding-e2e/commit/9c52d616e160a98a55a589bf6fb310504c273790) 经默认分支预检，点击页面运行生成 [ea9ca03ecafcc71dc6d1d82d](https://forgelet.k.mygo.rocks/runs/ea9ca03ecafcc71dc6d1d82d)。实际 checkout 私有仓库，verify → follow 两级 needs 全 success，两个本 App GitHub Checks 均 completed/success；完整日志包含固定 SHA 与两级标记，下载 `onboarding-report` 后核对 report.txt 中的 SHA。
- **重启与自动事件**：首跑完成后真实重启 Deployment，完整 management 文档/version/仓库 ID 绑定保持一致，匿名入口恢复。随后通过 GitHub 写入测试 README 产生 [真实 push bde29de9](https://github.com/shitamachi/forgelet-onboarding-e2e/commit/bde29de9266dfc9812e20efd82a15b7cbf316101)，签名 webhook 自动创建 [c5189386a3b6085ac95a7baf](https://forgelet.k.mygo.rocks/runs/c5189386a3b6085ac95a7baf)，两级任务、两个 GitHub Checks、完整日志与产物再次成功；未伪造 GitHub 事件或用手动运行替代 push。
- **现有环境**：原 demo HTTP 返回 `healthy=true / v1.10-disk-guard`，磁盘默认保护正常且未暂停；最终没有遗留执行 Pod。工作台/API 仍显式 open，无 auth/用户/OAuth；匿名公网 `/api/runs` 本轮返回 403，未开放公网管理路由。webhook 和 workload 身份校验保持原规则。
- **GitHub 与证据**：App 权限和事件仍保持原值，只新增第三仓库的 selected 授权。新测试仓库为私有，原生 GitHub Actions 已关闭；创建 workflow 时遗留的一条原生排队任务已请求取消。私有证据保存在 `/Users/guo/.config/forgelet/live-20261023/onboarding/`，含配置/Deployment 备份、目录/绑定/运行/Checks/日志/产物、重启前后文档和页面截图；未向项目写入凭证。

本批没有自动提交或推送 forgelet 项目 Git。后续：批量启用、自动 rename 迁移和 installation 生命周期/后台同步，详见 plan 6.3b / 6.5；第一批的实时授权检查只覆盖启用/预检/首跑，不能代替后续撤权队列与凭证处理。

## 接入诊断（本轮实现前）

2026-10-24 使用本 App API 只读核对：账号 shitamachi 的 installation `174462839` 未暂停，repository_selection=selected，可访问仓库仅 `forgelet-e2e-app` / `forgelet-e2e-gitops`；App 权限为 Contents/Checks/Pull requests 写、Metadata 读，事件为 push/PR。
因此其他仓库当前不在 App 授权范围，手填仓库和 installation ID 不能解决权限问题；代码尚缺安装/仓库自动发现与授权导航，且阶段 5 配置页尚未部署。
拟按 Buildkite 的连接入口与 Woodpecker 的选择/激活仓库模式补齐接入引导，详见 plan 阶段 6。上述为实现前的诊断；后续第一批实施和部署结果另记。

## 上批：仓库、workflow 与发布目标配置

- **可操作页面**：新增 `/connections`、`/workflows`、`/targets`。仓库登记 installation、启停、移除及固定 SHA 的 GitHub App 接入检查；workflow 路径、启停、Git 源码读取/控制面 YAML 编辑、固定 SHA/ref/事件/inputs 运行；target 的来源、GitOps、镜像与 Flux/应用字段编辑。
- **持久化与一致性**：配置为 SQLite metadata 单行有界文档，version/CAS 拒绝过期编辑；数据库提交后原子切换运行快照，并与调度 tick 串行。首次保存前从安装配置生成初始值，保存后这三类配置以数据库为准，重启不丢失；数据库快照包含配置。
- **执行语义**：关闭仓库会阻止新事件和已接收待处理事件创建运行；不取消现有执行。每仓库有登记规则时仅执行已启用路径；控制面 YAML 不写回 Git，纯控制面规则无需读取 Git workflow 目录。缺失 Git 文件记录事件错误。已有 run 计划与 delivery 的 target 快照保持原样。
- **校验与边界**：受支持 YAML 字段、依赖/表达式、输入定义和 profile 校验不会被分支/path 不匹配掩盖；实际执行仍按触发规则。路径、重复配置、来源仓库、target、installation/数量/体积均校验。GitHub App 授权和 Secret/profile/基础设施仍由原安装流程管理，页面不回显私钥或 token。

| 验证 | 结果与范围 |
|---|---|
| `make verify` | 通过：构建、Go race、lint 0 issues、generate、TypeScript/格式、20 个 Vitest、9 组 Chromium |
| Go / SQLite / HTTP | 保存与 CAS、重启、关闭仓库前后事件、inline/Git workflow 启停、Git 缺失诊断、非法配置、跨源请求、target 请求使用新配置而已存记录不变 |
| Workflow 单测 | 核心覆盖率 90.3%；Validate 与实际 Compile 分离，非匹配分支/path 仍发现 job 错误，dispatch 必填与错误输入类型检查，实际执行不绕过 filters |
| 浏览器 | 仓库接入检查、workflow 编辑/运行、target 编辑删除/刷新持久化、409 保留未提交内容；原有工作台操作回归通过 |
| 独立 k3s 专项 | 通过，133.519 秒；真实 Chromium 三组用例 26.1 秒，页面 → Go/SQLite → Pod；配置保存后重启，再以签名 push 执行持久化 YAML，深链接与身份边界仍正常 |
| 全量 Testcontainers | 18 个场景全部通过，765.561 秒；三组真实 Chromium、配置重启/签名 push → Pod，及原 Actions、BuildKit、GitOps/Flux、磁盘和保留清理回归通过，临时集群/测试容器已清理 |
| 实际 homelab | 当批未部署；本批已随阶段 6 第一批部署，真实接入结果见上文；原 UI 发布/回滚证据保留于下文 |

没有新增 auth/用户体系、常驻服务或依赖。设计、配置优先级、API 和操作说明已同步 architecture/deployment/compatibility；未自动提交或推送项目 Git。

## 上批：Web 工作台

- **技术选型**：React 19.3.1、TypeScript 7.0.2、Vite 8.3.5、TanStack Router 1.170.46 / Query 5.104.5、Tailwind CSS 4.3.3、Lucide；Vitest 5.0.5、Playwright 1.64.1。版本锁定，Node 24 仅用于开发/构建。
- **页面与操作**：工作台、运行筛选/深链接、needs 分层图、job/step 结果、手动提交 YAML/inputs、取消/重跑、完整日志/产物下载、发布/Flux/GitOps 详情与回滚、vars/secrets、系统水位/保留/备份/清理/事件诊断。
- **低常驻成本**：静态资源嵌入现有 Go server，页面按路由拆包、字体本地提供，无额外服务、数据库或存储组件。
- **无登录模式**：新安装清单显式 `admin_access: open`，同源 JSON 写入；不创建用户、角色或会话。省略该项的旧部署仍要求 token，升级不会自动开放管理 API；webhook 与 workload identity 没有放宽。
- **新增后端**：脱敏配置摘要 API、逐页流式日志下载、SPA fallback/资源缓存/CSP；未知 API 和内部路径不会误返回入口 HTML。
- **实际修正**：真实 k3s 首轮暴露快速任务完成后产物列表未刷新的问题，已在终态转换时刷新并加入浏览器回归；默认选中依赖图起点，避免依赖 API 的任务排序。变量编辑表单补明确可访问名称；小字号文本与状态颜色对比度、移动导航隐藏行为已调整。

| 验证 | 当前结果与范围 |
|---|---|
| Go 单测 / SQLite / HTTP | 通过：显式访问模式、同源/跨源/表单请求、无登录创建/取消/重跑、secret 名称只读、独立 webhook/Pod 身份、600 块日志完整下载与过期、静态路由与缓存 |
| Vitest | 19 个通过：运行过滤、耗时、matrix/循环依赖图、step 结果、日志游标/转义、API 错误与写请求 |
| Playwright 交互 | 7 个通过：桌面/移动导航、URL 筛选与刷新、全部日志分页/文本安全/下载/过期、取消/重跑/manual inputs、vars/secrets、回滚冲突/成功、磁盘/维护/错误重试、终态产物刷新 |
| 正式镜像 + Testcontainers | 18 个场景全部通过，765.511 秒；包含两组真实 Chromium 用例（18.7 秒）、API/Pod、下载、取消、仓库设置及重启，以及原 CI/CD 全量回归 |
| 实际 homelab | 已部署：无登录访问、手动运行/needs、321 块日志搜索/下载、运行中取消/重跑、新版发布及 Web 回滚全部通过 |

自动验证、预览数据与真实环境明确区分。项目变更未自动提交或推送；开发入口、open 模式与兼容边界见 deployment/architecture。

## Web UI：homelab 部署与实际验证

- 内网工作台：[https://forgelet.k.mygo.rocks](https://forgelet.k.mygo.rocks)。显式 `admin_access: open`，匿名页面与 config/status/runs API 为 200，没有新增 auth、用户或会话。公网 `/api/runs` 仍为 404，GitHub webhook 和执行器身份继续独立校验。
- 正式 amd64 server 固定为 `registry.shitamchi.com/forgelet/server@sha256:7b925fdec3329b2ef55143aea4701fc0ad3c3f91c152ab2c3f732714ccd4688c`。Deployment 1/1 Ready；私有安装清单同步镜像和 open 模式。部署前保留 ConfigMap、Deployment 和 SQLite 备份。
- **页面运行与日志**：从手动运行表单创建 `6200e5b4e5e9428cd2e2f76b`，verify → follow 的 needs 执行成功。日志搜索、任务切换、持续读取超过 256 块均正常；点击完整下载得到全部 321 个 chunk，核对首行标记及最后 `320`，没有截断到第一页。
- **页面取消/重跑**：`2be98a4da46da4fa7034ed12` 在 running 时通过确认框取消；页面重新运行创建 `f5a57d2e8fe9fefb2ded0b741433553a`，再次确认 running 后取消。两个记录均 cancelled，原历史保留，无遗留执行 Pod。
- **实际新版发布**：[测试源码 commit 4fd3f5c9](https://github.com/shitamachi/forgelet-e2e-app/commit/4fd3f5c977195fc92272423074286436fab3aee4) 自动触发 run `9ccb08f3b41eba1a35f13eac`，test/build/deliver 全 success。发布记录 `135d78786530fa7b606605305350122d` 在页面显示 deployed；[GitOps commit 0c4f5a14](https://github.com/shitamachi/forgelet-e2e-gitops/commit/0c4f5a14f6dd52ea44ff15b42e77dc6772624aec)，应用返回 `v-web-ui-20261024 / healthy=true`。
- **页面回滚**：在该发布详情点击“回滚到上一版本”并确认，页面跳转新记录 `5087c75cb5493271b25e301a1e5b0501`，最终显示 deployed、Flux 已应用、应用就绪。[回滚提交 4a8225e2](https://github.com/shitamachi/forgelet-e2e-gitops/commit/4a8225e281071b043d0b82b9f8202026ac3f95f9) 与目标/观测 revision 相符；实际 image 恢复为 `sha256:4e3571b3d47ab34e66280ffb293491ab4c138ac2574d1fd4ffd66a105bb9aa9c`，Deployment 1/1 Ready，HTTP 返回健康 `v1.10-disk-guard`。
- **收尾与证据**：调度正常、默认磁盘阈值保留、执行 Pod 全部回收。配置、备份信息、浏览器截图、完整日志、实际状态和 Flux/应用结果保存在本机私有目录 `/Users/guo/.config/forgelet/live-20261023/web-ui/`，主要文件 `final-state.json`、`rollback-final.json`、`full.log`。本批外部源码变更仅发生在专用测试仓库，forgelet 项目未自动提交或推送。

## 上批：1.10 homelab 部署验证

- **部署**：正式 Dockerfile 构建 amd64 server，推送并固定为
  `registry.shitamchi.com/forgelet/server@sha256:8986d4ce612a67ee3108b2a594e8fc2d7090453413f851d73a9c483bba88d230`。
  部署前保存配置、Deployment 和 SQLite 备份，备份完整性检查为 `ok`；升级后控制面和状态 API 健康。
- **压力方法**：在真实数据 PVC 上写入最多 1 GiB 的测试文件，截断为 512 MiB 后再删除。
  暂时将暂停/恢复阈值设为 `515358965760` / `515895836672` 字节；实际可用空间始终超过 479 GiB，验证时没有填满节点磁盘。
- **暂停与重启**：真实 push 接收后，三个 job 在多个调度 tick 内保持 queued 且没有 Pod；低水位重启仍暂停。
  原运行任务的 Pod UID `31aa1f44-f2ed-46aa-a8d4-c6bffaf2624a` 被接管并继续完成；暂停期间日志、运行中/排队取消、Pod GC 和 1 个到期测试备份的清理通过。
- **滞回与恢复**：释放一半文件后，可用空间介于两阈值之间；连续 tick 和再次重启都没有恢复调度。
  删除剩余文件后，同一个控制面 Pod 自动恢复；原 job ID 保持不变，依赖任务成功，真实 GitHub workflow 开始执行。
- **配置收尾**：已恢复默认低于 512 MiB 暂停、达到 1 GiB 恢复，并重启确认；测试文件已删除。
  私有安装清单同步为新 server digest 和默认阈值，旧安装清单与数据库备份可用于恢复。
- **CI/CD 复验**：[源码 push `17bfd096`](https://github.com/shitamachi/forgelet-e2e-app/commit/17bfd09653b8e727d48b4d6e4e8734c3b7fd8e00)，run `fb40be6df24b7f2d3cc774b0` 仅创建一次，test/build/deliver 和三个 GitHub Checks 全部 success；下载的 tests artifact 含 Go 测试 PASS。
  CD `f24955f56d72fe5ed05e8a2e58d5028c` 为 deployed；[GitOps commit `6c903e8c`](https://github.com/shitamachi/forgelet-e2e-gitops/commit/6c903e8cdaf35fe0e97d32deabf2c5752da23884) 与 Flux source / lastAppliedRevision 一致。
  应用实际 image digest 为 `sha256:4e3571b3d47ab34e66280ffb293491ab4c138ac2574d1fd4ffd66a105bb9aa9c`，Deployment 1/1 Ready，HTTP 200 返回 `v1.10-disk-guard / healthy=true`，`/healthz` 也为 200。
- **最终状态**：forgelet 单控制面健康，调度未暂停，执行 Pod 全部回收；三个手工验证 run 分别为 success / cancelled / cancelled。
  本批验证了新版本的健康发布；故障发布与回滚的真实记录继续保留在“上批：真实 GitHub / homelab 接入”。
- **验证记录**：配置/备份、分阶段磁盘/任务/Pod 快照及验证脚本保存在
  `/Users/guo/.config/forgelet/live-20261023/disk-1.10/`（私有目录），完整结果见 `pressure-result.json` 与 `10-final.json`。
  本批构建并部署 server、运行实际集群验证，未修改产品代码或重跑自动测试；1.10 的单测、集成测试和 Testcontainers 结果见下一节。

## 上批：1.10 磁盘低水位保护实现

- **实现**：使用 data_dir 文件系统的非特权可用字节，默认低于 512 MiB 暂停、达到 1 GiB 恢复，可配置。
  中间区间保持状态；探测错误暂停新任务，初次启动/重启须达到恢复阈值。水位状态不额外写入数据库，也没有增加常驻组件。
- **调度**：queued 不预留，新创建 Pod 前再次检查；已预留无 Pod 的 attempt 同样受保护，恢复后使用原身份继续。
  已有 Pod 的接管、结果/日志、取消、失败传播、Checks、GC、CD 和保留清理继续执行。
- **运维**：认证 `GET /api/status` 和首页显示暂停原因/阈值/最新容量；健康检查不因低水位失败。
  安装清单、架构、部署说明同步更新，不将 PVC request 当成硬配额，也不声称覆盖所有写入或其他文件系统。
- **单测与进程内集成**：阈值边界/滞回、探测失败、状态并发读、严格配置、SQLite 零调度额度下失败传播、
  HTTP 结果/日志/取消、Checks、到期备份清理、重启接管/预留恢复、预留后空间骤降通过。
- **完整验证**：`make verify` 通过（构建、race、lint 0 issues、generate）；调度核心（scheduler.go + concurrency.go）覆盖率 89.6%，workflow 89.9%。
  `make test-integration` **17 个场景全部通过，873.615 秒**；涵盖原有 CI、真实 Actions/工具链、BuildKit、GitOps/Flux 部署与回滚，临时集群和测试容器已清理。
- **磁盘 Testcontainers**：实际安装清单的数据 PVC 位于独立 256 MiB 文件系统，测试阈值 32/64 MiB；真实写入、截断、删除填充文件。
  低水位暂停、运行中重启保留原 Pod UID、日志/结果/取消/GC、到期备份清理、恢复区间内再次重启、恢复后并发 2 且无重复执行全部通过（专项 25.04 秒）。
  夹具修正了 k3s 模块覆盖普通 tmpfs 配置的问题，维护验证改为访问容器内实际数据目录，避免假定 PV 必须使用 HostPath；不填满宿主磁盘。
- **当时的集群范围**：该批代码交付使用 Testcontainers 独立 k3s，不读取用户 kubeconfig；后续实际 homelab 升级与验证见上一节。

## 上批：真实 GitHub / homelab 接入

- Edge 创建私有仓库 `shitamachi/forgelet-e2e-app` / `shitamachi/forgelet-e2e-gitops`，关闭这两个测试仓库的原生 Actions。
- GitHub App `forgelet-shitamachi-homelab`（App ID `5441016`，installation `174462839`）仅安装到两个测试仓库；Contents/Checks/Pull requests 读写、Metadata 只读、push/PR 事件。
- 三节点 amd64 k3s v1.33.9；forgelet 单副本与 local-path 10 GiB PVC 在 `forgelet`，应用在 `forgelet-demo`。
- Flux v2.6.4 仅部署 source/kustomize controllers；只读 deploy key 拉取 GitOps，namespace RBAC 限定应用部署范围，没有给 Flux cluster-admin。
- 内网 `https://forgelet.k.mygo.rocks/healthz` 返回 200；公网 `https://forgelet.mygo.rocks/webhooks/github` 无签名 POST 返回 401，公网 `/api/runs` 返回 404。
- Cloudflare Tunnel → Cilium → HTTPS forgelet；Cilium 使用 BackendTLSPolicy 验证内部 CA，执行器仍使用内部 HTTPS。
- amd64 正式镜像推送至现有 `registry.shitamchi.com/forgelet/`，server/executor 配置均固定 digest。
  首次 Docker 上传中断，随后完成重试与 crane 上传；修复后的 server 镜像已实际部署。
- 私有配置及凭证保存在 `/Users/guo/.config/forgelet/live-20261023/`（目录 0700），凭证仅进入 Kubernetes Secret，不写入 Git。
- 首轮真实 push `700ed85` 暴露了默认 checkout 到 root 所有的 emptyDir 根目录会被 Git 拒绝的问题。
  改为执行器创建自己所有的 workspace 子目录；补充默认/嵌套 checkout 单测及真实 k3s 所有权/Git 回归用例。
  原失败 run `fcb489b677741fa84df3496d` 保留，GitHub Checks 正确显示 test 失败、build/deliver skipped。
- 构建中重启真实控制面，原 Pod `7fd2df83-77c0-47c1-a656-81f7bf0cb36d` 继续完成，没有重放构建。
- GitHub 重投 delivery `3b830de0-cedc-11f1-9712-d4023cf649fd` 返回 202；相同 commit 仍只有一个 run 和三个 Checks。
- 此次真实环境使用 direct GitOps；PR/实际 merge SHA 路径仍属于 Testcontainers 验证范围，不混称已在该 GitHub App 上验证。

### 真实链路结果

| 场景 | 源码 / forgelet run | GitOps 与应用观测 |
|---|---|---|
| 健康发布 | 源码 `eeb848d`；run `b5d91300b4ac0d56440dcd72`，test/build/deliver 全部 success | GitOps `def8526`；CD `0437a09ebd0d2d6adbb8f9352b68dd79` 为 deployed，Flux revision 相符，应用 HTTP 返回 `v1 / healthy=true` |
| 故障发布 | 源码 `004e39e`；run `41635b5b2e111015a87945e4`，CI 全部 success，故意让 readiness 返回 503 | GitOps `933b323`；新 Pod 不就绪，Deployment 为 ProgressDeadlineExceeded、Flux Not Ready，CD `153e0492a72ccd83fba5bb44057666d8` 保持 delivered；旧 v1 继续提供服务 |
| API 回滚 | 连续两次 POST 故障 CD 的 rollback API，均返回 `739c5f215d20e184fedc9e7fe369b6a1` | 新 GitOps commit `fc298be` 仅恢复上一 digest，Flux Ready，CD deployed，应用 HTTP 返回健康 v1 |
| 收尾恢复 | 源码 `a5dd050` 恢复健康配置；run `22829178dd435cc84a2ccf66`，三个 Checks 全部 success | GitOps `2d8ba43`；CD `e5cce3aff707be2472a465cc02f4dd4a` deployed，Flux revision/digest/就绪副本一致，应用 HTTP 返回健康 v1 |

表中状态为相应阶段的实际观测；后续投递替代旧记录后，旧记录转为 superseded，历史结果仍可核对。
首次健康与回滚镜像为 `sha256:9b34fc704be244a0f69766dd848d6a030d3b1d5adb43ce2f08a74a4b3b26aadc`，
故障镜像为 `sha256:35cfcb3d6d943ce0b7a04da3581fe84ba48fdf289661eca80f7fa98b90460248`。
恢复源码后重新构建的最终健康镜像为 `sha256:b9728964f76e257abbbe7a9c2f5a919883791215f15801913a9ff3969d9d97cd`。
[最终 GitHub Checks](https://github.com/shitamachi/forgelet-e2e-app/runs/120417659389) 与
[回滚 commit](https://github.com/shitamachi/forgelet-e2e-gitops/commit/fc298bee5359b6769c7483f723bda69f92b33907) 可直接核对。
正常、故障、回滚的 API/Flux/Deployment/HTTP 快照以及 rollback diff 保存在私有配置目录的 `evidence/`。
收尾时应用 1/1 Ready、Flux 两个 controller 各 1/1 Ready，forgelet 仅保留单个控制面 Pod，执行 Pod 全部回收。

### 实际接入批次自动验证

- `make verify` 通过：两个二进制构建、race 单测与进程内集成、lint 0 issues、generate。
- 默认/嵌套 checkout 与 fetch-depth 0/1 的固定 SHA 单测通过；新增 Testcontainers workspace 所有权与实际 Git 操作回归通过。
- `make test-integration` 最终全量通过：**16 个场景，727.420 秒**，包括 BuildKit、artifact/cache、真实 Git/Flux、重启、故障/回滚、真实 Node/Go 工具链和清理。
- 首次全量运行的工具链外网下载超过通用 150 秒等待，并因活跃任务未结束影响后续 retention 用例。
  工具链用例改为单独等待最多 7 分钟，仍保留 workflow 的 6 分钟超时；失败时记录任务日志并取消遗留任务，完整重跑通过。
- 本轮构建并推送了 amd64 正式 server/executor 镜像；Testcontainers 仍使用自己创建的 arm64 临时 k3s，不读取用户 kubeconfig。

## 上批：阶段 3 CI 到 CD

- **artifact/cache**：同 PVC 文件存储，默认 256 MiB/对象、2 GiB 总量、4096 对象；临时上传计入限额，原子发布、重启清理、到期 GC。
  仓库/run/ref/版本隔离，校验 digest、tar 路径和链接；适配 upload/download-artifact@v4、cache@v4/restore/save 的明确输入子集。
- **镜像构建**：`forgelet/build@v1` 调真实 buildctl，job 按需启动 BuildKit v0.20.2 rootless 原生 sidecar；
  registry repository allowlist、Secret 认证和日志脱敏、不可变 digest 输出，失败/取消随 job 清理。没有 Docker socket、hostPath 或 privileged 容器。
  rootlesskit 所需 SETUID/SETGID、提权和 Unconfined 设置仅给 builder，限制与节点前提见 architecture/deployment。
- **GitOps**：`forgelet/deploy@v1` 仅提交预登记 target/digest；整个 run 成功后，控制面以目标仓库 App token 修改固定 Kustomize images 条目。
  SQLite v4 持久化意图与 CAS 版本；固定 base/commit、非强推 direct、固定分支 PR、响应丢失重试、每 target 串行及冲突诊断。
- **部署状态与回滚**：PR 等实际 merge SHA；核对 Git 分支、Flux 来源/revision/generation/inventory、Deployment image 和实际就绪副本。
  区分 awaiting_merge/delivered/deployed/superseded，成功后仍可观测退化。回滚生成新 commit/PR，只恢复先前 digest，保护其他字段和新镜像；重复回滚幂等。
  等待/失败重试每 30 秒轮询，已部署状态每 2 分钟观测，降低空闲时 GitHub/API 请求；新意图立即随 tick 处理。
- **运维与恢复**：增加 artifact 下载、CD 查询/回滚 API，终态 CD 历史清理；queued 投递保护源 run，数据库显式恢复隔离活跃 CD 意图。
  SQLite 备份不包含 blob 文件，PVC/配置/Secret 备份边界已记录。新增可调整的 Flux 只读 RBAC 与 CI/CD workflow 示例。

没有增加 Go 运行时依赖、常驻服务或产品 Docker API；executor 增加 buildctl 二进制，builder/registry/Flux 的资源独立计算。
`golang.org/x/crypto` 因容器测试使用 bcrypt 从间接依赖改为直接依赖；Flux 仅复用 client-go dynamic client。

## 阶段 3 上一批自动验证

| 验证 | 结果与范围 |
|---|---|
| `make verify` | 通过：真实构建、race 单测/进程内集成、integration tag lint 0 issues、generate；包含本批最终回归测试 |
| blob/executor 单测 | 原子发布/重启、配额下重试、TTL 替换/GC、并发首写、隔离、路径/链接攻击、builtin 产物/缓存恢复、成功后保存、build 参数/digest |
| SQLite/HTTP 集成 | 投递持久化、CAS、请求身份/来源/不可变数据、幂等、恢复隔离；queued run 保护和终态 CD 保留清理 |
| GitHub/状态机单测 | Git 对象/限权 token、非强推、响应丢失恢复、PR merge SHA、run 成功门禁、闭合/改动 PR、冲突、回滚及重复请求；远端轮询/失败重试节流不延迟新意图 |
| Flux observer 单测 | 来源/分支不符、旧 generation、revision 不一致、缺失 inventory、旧副本、未就绪、缩容至零、缺失容器均不误报健康 |
| 核心覆盖率 | workflow 89.9%；表达式 91.5%；调度核心（scheduler.go + concurrency.go）89.3%；delivery 84.4%；storage 整包 76.8%，不混称全部包达到 85% |
| Testcontainers CD 验证 | 真实 rootless BuildKit 构建并推送到认证 registry，日志凭证脱敏、失败与运行中取消、Pod 清理；真实 Git + Flux direct/PR、实际 merge SHA、控制面重启、故障镜像及健康回滚全部通过 |
| `make test-integration` 全量 | 通过：15 个场景，710.964 秒；包含最终轮询间隔与取消用例，阶段 1/2 回归全部通过，临时 k3s 和测试容器已清理 |
| `make images` | 通过：正式 server/executor 构建；executor 含真实 buildctl，仅保存在本机，不推送 |

临时 k3s v1.34.4（Linux arm64）安装实际 forgelet 清单，运行真实 BuildKit、认证 registry、Git 仓库、Flux v2.6.4 source/kustomize controllers 和应用 HTTP 就绪探针。
GitHub API 使用测试 HTTPS fixture，其 Git Data/PR 操作落到真实 Git 对象与分支；没有用 API 返回成功冒充 Flux 已部署。
这轮修正了重复重启测试误把 terminating Pod 算成当前控制面的问题，并使测试 Checks API 返回并保存完整身份，避免无效模拟响应。
构建取消测试拼接分块日志，并在长任务输出足够数据后识别执行标记，避免被跨块脱敏尾缓冲影响；确认任务仍在运行后才发取消。
测试程序只访问 Testcontainers 创建的集群，不读取用户 kubeconfig。

以上阶段 3 自动测试交付时，仅对实际环境做过只读确认；真实接入结果见本页“上批：真实 GitHub / homelab 接入”部分。
forgelet 项目代码仍在 `chore/homelab-reset-20261023`，没有自动提交或推送项目 Git；本轮外部提交仅用于两个专用测试仓库。

## 上批：阶段 2.2–2.6

- **Matrix 与并发组**：静态笛卡尔积、include/exclude、max-parallel、fail-fast、job continue-on-error；
  logical needs 等待全部 matrix 变体并聚合结果/输出；workflow/job concurrency 与 cancel-in-progress，SQLite 恢复组约束。
  活跃任务确认停止后释放额度，排队变体取消不创建 Pod，容错后的失败保留原始 result。
- **表达式**：通用插值、typed inputs/matrix、常用字符串/JSON 函数，按接收、调度、执行阶段限制上下文；
  Action 输入作用域独立，明确拒绝未支持语法、字段、引用和能力。
- **真实 Actions**：公开固定 SHA 源码、有界解包和路径检查、action.yml/action.yaml、真实 Node 20/24、
  pre/main/post、inputs/state/outputs、逆序 post、嵌套 Composite、共享 workspace/file commands。
- **工具安装**：固定 setup-go v6.5.0 与 setup-node v6.5.0 SHA，关闭缓存；真实下载 Go 1.25.0、Node 24.14.0，
  检查工具版本、Action outputs、PATH，并运行生成的 Go/JS 程序。executor 改为 Debian/glibc，server 仍为 Go/Alpine。
- **日常触发**：同仓库 PR 使用 merge SHA；push branch/tag/path filters、PR base branch/path filters、typed manual inputs；
  fork PR 忽略，缺失 merge SHA 或无法取得完整变更文件明确诊断。GitHub API 临时故障仍可重试。
- **仓库设置**：管理员 secrets/vars API；secrets 使用 AES-256-GCM，主密钥单独挂 server，SQLite 仅存密文，
  计划仅存引用；按任务身份下发引用值并预注册 mask。vars 在创建 run 时快照，重跑读取最新 secrets。
- **数据迁移与交付**：SQLite v1/v2→v3 事务迁移；安装清单支持可选加密主密钥；部署和兼容文档同步更新。

没有增加 CRD、常驻 Node 服务、外部数据库或队列。公开源码与工具下载会使用外网，工具缓存仅在单个 job 生命周期内存在。
版本、输入、取消/pre/post 差异等精确边界见 [compatibility.md](compatibility.md)。

## 阶段 2 历史验证

| 验证 | 结果与范围 |
|---|---|
| `make verify` | 通过：两个二进制构建、`go test -race ./...`、integration build tag lint（0 issues）、generate |
| 编译/表达式单测 | matrix 展开/边界、早期值、活动类型/filters/inputs、metadata/路径/输入诊断、短路与类型、Action 上下文隔离 |
| 执行器单测 | 真实 Node/Composite、pre/main/state/outputs/post 顺序、普通失败后清理、post 失败、取消/超时、未知输入/路径逃逸/递归限额 |
| SQLite/HTTP 集成 | matrix 额度/fail-fast/COE、两级并发组、pending 替换、跨依赖阶段组占用、密文绑定/篡改、远程执行器领取/脱敏与输出保护 |
| GitHub adapter 测试 | PR merge SHA、fork 忽略、固定 before/head 比较/重命名、300 文件截断诊断、永久错误与临时重试区别 |
| 核心覆盖率 | workflow 89.9%；表达式 91.5%；调度核心（scheduler.go + concurrency.go）89.3%；storage 整包 78.3%，不混称全部包达到 85% |
| `make test-integration` | 通过：13 个场景，约 292 秒；包含 Action 整体 inputs/pre-if、matrix 排队/活跃取消的最终回归；测试集群已清理 |
| 临时 k3s 范围 | 并发/DAG、身份/取消、重启恢复/Pod 丢失、签名 push/PR/filters、matrix 活跃/排队取消、并发组重启、Node 20/24/Composite/post、secrets/vars/dispatch、真实 setup 工具链、保留清理 |
| `make images` | 通过：正式 `forgelet-server:dev` / `forgelet-executor:dev` 构建；仅保存在本机，不推送 |

容器测试使用 `rancher/k3s:v1.34.4-k3s1`（Linux arm64）、正式 Dockerfile 环境及实际安装清单。
GitHub App API 由测试 HTTPS sidecar 模拟；公开 Action 源码、工具链下载与执行是真实的。
本批测试发现并修复了 GitHub 归档全局 PAX 记录解析、macOS 临时目录符号链接、跨依赖阶段并发组占用、matrix 全活跃且无排队任务时的 fail-fast、
Action 输入作用域、整体 inputs/matrix 表达式及 pre-if 状态、PR 默认 activity types 与永久比较错误重试等问题，并保留回归用例。
原有日志重试、进程组取消、动态脱敏、幂等和清理测试继续通过。

阶段 2 的所有集群操作仅发生在 Testcontainers 创建的临时 k3s，没有读取用户 kubeconfig。
当时没有创建真实 GitHub Check、部署到 homelab、推送镜像、提交或推送 Git；该批变更保留在 `chore/homelab-reset-20261023`。

## 仍未完成

- 磁盘保护只控制新任务调度，运行中写入及去重身份仍会占用空间；其他文件系统不在 data_dir 水位检测范围内。
- 完整 Marketplace Actions、远程 reusable、动态 matrix、artifact v4、OIDC、fork PR、多租户和 HA 尚未承诺；正式 act profile 已支持 Docker Actions，同仓库 reusable 已实现，具体边界见 compatibility。
- 尚未测量常驻资源；requests/limits 是起始配置，本轮不安排资源基线门禁。

## 下一阶段

Web 工作台的独立 k3s 验证与 homelab 实际部署均已完成；真实环境使用含 Web 的 server，保留默认磁盘阈值。
按 [plan.md](plan.md) 和实际使用需求继续扩展 workflow/Actions。
真实测试仓库与集群部署可继续复用；保持单测、集成测试、Testcontainers 和进度文档，不引入 spec 审批或资源基线流程。

## 旧实现恢复信息

清理前 HEAD：`1b41037e386708d3595680f9d661415fa318c538`。
Git 历史仍在，没有重写 main 历史。

本机仓库外备份目录：

```text
/Volumes/Dev/codes/go/forgelet-backups/20261022T174154Z-before-homelab-reset/
```

目录名用 UTC，对应本地 2026-10-23。包含 `working-tree.tar.gz`、`history.bundle`、
`staged.patch`、`unstaged.patch`、`status.txt`、`sha256.json` 和恢复说明。
归档包括本地配置，保持私有，不提交进仓库。需要恢复时先解到另一个空目录核对，避免覆盖新工作。
原 .idea 等本地编辑器设置保留；清理的是旧产品与开发部署实现。
