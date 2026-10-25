# 项目状态

更新：2026-10-25（Asia/Shanghai）。当前：**阶段 9 已部署 homelab（schema v5→v6，server/executor 新 digest）；迁移、健康、session、匿名 401、嵌入 UI 与备份/数据计数验证通过。未触发真实 CI，也未扩大 GitHub App 授权。**
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
- 完整 Marketplace Actions、Docker Actions、reusable workflows、OIDC、fork PR、多租户和 HA 均不在本阶段范围内。
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
