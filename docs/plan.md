# 实施计划

更新：2026-10-27。按 owner 要求分阶段快速实现，多个任务并发从首版支持。
不使用 spec/accepted 审批，不安排独立基线、资源测量或审核阶段。
用单测、集成测试、Testcontainers 随实现验证，完成后更新本页和 [status.md](status.md)。

## 本轮执行顺序（2026-10-27）

- [x] **1. Go/Node 实际接入验收**：platform-server 与 admin-web 的真实 push、原 workflow、缓存冷/热及原流程的镜像发布通过。Go 的 64 MiB 缓存分块问题完成单测、临时 k3s 和部署后真实 false→true 验证；两个仓库托管 Actions 恢复原配置。
- [x] **2. platform-web 业务 CD**：原 YAML 绑定自动 native CD，限定 GitOps/Flux 权限；真实 push baa4b54 / 2e2d793b 的 digest 部署、健康、回滚与恢复通过。显式管理 API 失败探针 c257c949 验证 CI 失败不产生发布；托管 Actions 恢复原配置。中间 registry EOF 与 native 镜像拉取配置修复分别保留，不混称首轮通过。
- [ ] **3. Rust 与其余原始 workflows**：显式 Ubuntu24.04/Rust1.97/PowerShell profile、shell 行为；逐库修正实际阻塞并验证发布语义。保持 CM 默认不发布、ETL 手动触发；GHCR 另用最小 Packages 凭据，不能复用只读 source token。

## 阶段 0：重新开始

- [x] 仓库外保留旧文件、未提交改动、未跟踪文件及 Git 历史。
- [x] 移除旧实现、CRD、多服务部署与 specs，改用轻量设计、计划和状态文档。
- [x] 确定单控制面、SQLite/PVC、原生 Pod、外部 Git 与逐步 Actions 兼容方向。

## 阶段 1：并发 CI 核心

目标：GitHub push → 固定 SHA 的 workflow → 多个独立 job → shell/checkout → 日志 → Check。
先把自己的 k3s 跑起来，不等完整 Actions 平台。

- [x] **1.1 进程与存储**：配置、HTTP、SQLite 事务与目录锁、事件去重、计划、运行状态、Checks 待发送记录、一致性备份与安全恢复准备。
- [x] **1.2 执行器**：真实 bash/sh/git、共享 workspace、file commands、最小插值、超时取消、进程组清理、跨块脱敏与日志重试。
- [x] **1.3 并发与 k3s**：默认并发 4、超额排队、按需 Pod、资源 profile、短时身份、固定 UID、Pod GC、server 重启恢复。
- [x] **1.4 Workflow 子集**：push、多独立 job、steps.run/env/工作目录/超时、受限 checkout@v4，未知能力明确拒绝。
- [x] **1.5 GitHub 适配**：App 验签、固定 SHA 读文件、限权 clone token、Checks 创建与更新、响应丢失恢复；通过模拟 API 验证。
- [x] **1.6 最小管理**：认证手动运行、取消、重跑、事件错误查询、run 列表/详情、日志分页/SSE、备份 API。
- [x] **1.7 交付资产与自动测试**：两个镜像目标、最小安装清单、开发部署说明、race 单测与进程内集成、Testcontainers k3s 测试。
- [x] **1.8 实际接入**：自己的 GitHub App、HTTPS 入口和工具链镜像已接入；私有 Go 测试仓库的 push → checkout → setup-go → go test → Check 已在 homelab 通过。
- [x] **1.9 日常存储管理**：历史、日志、事件 payload、备份分批清理；保护活跃任务/Pod/待回写 Checks，保留去重记录。
- [x] **1.10 磁盘低水位**：暂停/恢复双阈值、探测失败暂停、重启保守恢复、状态 API/首页；单测、HTTP/SQLite 集成和 Testcontainers 验证通过。已部署 homelab，实测暂停、低水位/恢复区间重启、原 Pod 接管、结果/取消/清理及自动恢复；默认阈值已恢复，真实 push → CI → GitOps → Flux 健康发布复验通过，证据见 status。

本地核心、自动测试与真实接入分别记录在 status。1.8 已由真实 GitHub push 和 Checks 验证，不依赖模拟 API 声明完成。

## 阶段 2：日常 workflow 与真实 Actions

按使用频率逐批实现，每批可运行、可测试。先用仓库内样例验证，自己的 workflow 提供后补入用例。

- [x] **2.1 needs 与条件**：依赖 DAG、失败/跳过传播、job/step if、状态函数、step/job outputs、step continue-on-error。
- [x] **2.2 matrix 与并发组**：静态 matrix、include/exclude、max-parallel、fail-fast、job continue-on-error、concurrency/cancel-in-progress。
- [x] **2.3 表达式**：通用插值、typed inputs/matrix、常用字符串/JSON 函数；固定早期、调度与执行求值阶段，未支持语义明确拒绝。
- [x] **2.4 Action resolver 与执行**：固定 SHA、action.yml、路径与大小限制、真实 Node、pre/main/post、Composite、inputs/state/output。
- [x] **2.5 常用 Actions**：固定版本的 setup-go、setup-node 和自己的 Composite；先关闭缓存，通过真实文件与工具版本验证结果。
- [x] **2.6 使用场景**：同仓库 PR merge SHA、branch/path filters、manual inputs、加密 secrets/vars；继续拒绝不可信 fork PR。

每批检查成功、失败、取消和资源清理；不以 YAML 可解析或 mock 返回成功当作 Action 兼容。
完整版本与输入边界维护在 [compatibility.md](compatibility.md)。

## 阶段 3：CI 到 CD

- [x] **3.1 artifact/cache**：PVC 本地有界存储、保留期、仓库/run/ref 隔离、原子上传与 GC；适配 upload/download-artifact@v4、cache@v4 及 restore/save。
- [x] **3.2 镜像构建**：按需 rootless BuildKit sidecar、registry 凭证、不可变 digest、失败/取消清理；没有产品 Docker socket。
- [x] **3.3 GitOps 交付代码**：预登记 target、成功 run 门禁、SQLite 持久化投递、GitHub App 固定对象与非强推、direct/PR、冲突与重试恢复。
- [x] **3.4 部署结果代码**：关联实际 PR merge SHA、Flux GitRepository/Kustomization revision、应用 digest 与 Deployment 健康；成功后继续观测退化和 superseded。
- [x] **3.5 回滚代码与自动验证**：生成 revert 意图的新 commit，仅恢复该 target 的前一个 image digest，保护其他字段与后续镜像；临时 k3s 注入故障镜像并验证恢复。
- [x] **3.6 实际环境交付**：自己的 GitHub App 与两个私有测试仓库已接通；真实 push → CI → BuildKit/registry → GitOps → Flux 健康部署、故障发布和 API digest 回滚均已验证，run/commit/HTTP 证据见 status。

尚未观察到真实应用健康时不能标记 CD 完成。常驻占用和构建峰值随实际使用再优化，不额外设性能审核流程。

## 阶段 4：Web 工作台（无登录）

- [x] **4.1 技术栈与交付**：React 19.3、TypeScript 7、Vite 8.3、TanStack Router/Query、Tailwind 4.3；页面按路由拆包并嵌入原 Go 二进制，生产环境不增加常驻 Node。
- [x] **4.2 CI 操作**：工作台、最近 100 条运行筛选、needs 分层、job/step 结果、固定 SHA 手动运行、取消/重跑、产物下载。
- [x] **4.3 日志体验**：游标排空全部分页、step/文本筛选、跟随输出、每页 1000 行、完整下载、过期状态。
- [x] **4.4 CD 与运维**：发布列表/详情、真实 revision/digest/Flux 状态、回滚；vars/只写 secrets；磁盘保护、保留、备份/清理、事件诊断。
- [x] **4.5 无登录接入**：安装清单显式 open 管理模式，同源 JSON 写请求；旧 token 部署兼容，webhook/Pod 身份保持独立。未新增用户、角色、会话系统。
- [x] **4.6 自动测试代码**：Go/SQLite/HTTP、Vitest、Chromium 交互/移动布局、Testcontainers 真实页面 → API → Pod 及重启用例；`make verify`/CI/Dockerfile 接入 Web 构建。
- [x] **4.7 最终自动验证**：make verify 通过，18 个 Testcontainers 场景全部通过（765.511 秒），含真实 Chromium。
- [x] **4.8 homelab 部署**：无登录 Web 已部署；页面手动运行/needs、321 块日志/下载、取消/重跑、新版发布及回滚均通过，真实 Flux/revision/digest/HTTP 证据见 status。

## 阶段 5：可操作的接入与配置

- [x] **5.1 持久化配置**：SQLite metadata 单文档、版本冲突保护、先提交再原子切换运行快照；重启加载，首次保存前以安装配置为默认值。
- [x] **5.2 仓库接入页**：登记/修改/启停/删除可信仓库和 installation ID，固定 SHA 检查真实 App 读取权限；移除仓库时明确同时移除关联 workflow/target 配置。
- [x] **5.3 Workflow 页**：路径登记、启停、Git 固定提交源码读取、控制面 YAML 编辑与校验、固定 SHA/ref/事件/inputs 手动运行；签名 push 使用已保存规则，缺失 Git 文件有事件诊断。
- [x] **5.4 发布目标页**：GitOps direct/PR、来源 ref、镜像映射、Flux/Deployment 字段的新增/编辑/删除；新投递使用新配置，已持久化投递保留原目标快照。
- [x] **5.5 自动验证实现**：Go/SQLite/HTTP 与 workflow 校验回归、Vitest、九组浏览器交互；独立 k3s 三组真实 Chromium、配置保存后重启及签名 push → 真实 Pod 通过。
- [x] **5.6 最终全量回归**：18 个 Testcontainers 场景全部通过（765.561 秒），含三组真实 Chromium、配置重启与签名 push → Pod；结果记录在 status。本批配置页面未部署 homelab。

## 阶段 6（第一批已交付）：GitHub 仓库自助接入

2026-10-24 只读诊断确认：当前 App `forgelet-shitamachi-homelab` 仅安装于 shitamachi，selected repositories 仅含两个测试仓库，未暂停；当前仅处理 push/PR。登记安装 ID 无法扩大 GitHub 授权，阶段 5 页面也尚未部署 homelab。
上述为实施前诊断。第一批代码、自动测试和实际 homelab 部署/第三仓库首跑与 push 均已完成，记录见 status；各项独立勾选，不把后续生命周期能力计入本批。

- [x] **6.1 连接与发现**：使用 App JWT 获取本 App 安装，分页列出安装可访问仓库；显示账号/组织、权限、暂停状态、最近同步结果。installation ID 后端解析绑定，普通表单不再手填。
- [x] **6.2 自助授权**：“连接 GitHub / 调整仓库授权”跳转当前 App 的 GitHub 安装配置页；用户选择仓库后手动返回内网 forgelet 并刷新。组织授权待批准显示操作提示（未批准记录 API 不可见）；新 installation 必须通过本 App 服务端 API 验证，不信任回调参数，不引入 forgelet 登录/用户/OAuth 体系。
- [x] **6.3a 单仓库启用**：搜索、选择并启用已授权仓库；发现与启用分离，授权新增仓库不自动运行，GitOps 仓库也不自动启用 CI。用 GitHub repository ID 追踪重命名，Installation ID 与展示 full_name 独立；rename 检出后明确要求手动迁移。
- [ ] **6.3b 批量与 rename**：批量启用、自动迁移名称及关联配置，保留历史关联。
- [x] **6.4 首次运行引导**：自动读取默认分支并解析固定 SHA，发现 workflow，检查兼容字段和 profile；保留修改建议/模板，不自动改写远端 workflow。确认后触发首个固定 SHA 运行，展示 Check、日志与产物结果；CD 目标选填，不阻塞首次 CI。
- [ ] **6.5 同步与失效**：签名 installation/installation_repositories 事件触发权威列表同步；提供手动刷新及低频、可取消的后台校正。失败同步不视为仓库移除；只有完成的权威快照才确认失权。撤权/暂停后阻止新任务及新授权凭证，不伪装为“未启用”，保留历史。
- [x] **6.6a 第一批自动验证**：分页/metadata scope/权限/伪造 ID/撤权/rename/部分失败单测与 HTTP/SQLite；浏览器授权返回参数、仓库搜索/启用/固定 SHA 首跑；全量 Testcontainers 配置重启/签名事件/真实 Pod 与 CI/CD 回归通过。
- [x] **6.6b 第一批实际验证**：部署 homelab，用第三个真实仓库跑通页面接入、首次运行、GitHub push → CI → Check、日志/产物和重启。
- [ ] **6.6c 后续同步验证**：跨账号、签名安装生命周期事件/重复事件、后台校正和撤权队列/凭证处理的真实 k3s 回归，随 6.5 交付。

产品参考：[Buildkite GitHub provider](https://buildkite.com/docs/pipelines/source-control/github)、[Woodpecker 仓库激活](https://woodpecker-ci.org/docs/usage/intro)。
GitHub 接口与边界：[App installations](https://docs.github.com/en/rest/apps/apps#list-installations-for-the-authenticated-app)、[installation repositories](https://docs.github.com/en/rest/apps/installations#list-repositories-accessible-to-the-app-installation)、[Setup URL 的参数不可直接信任](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-setup-url)。

## 阶段 7：管理员登录与 GitHub 账号授权（代码、自动验证与 homelab 部署完成）

2026-10-24 owner 决定开始加入账号系统，替代此前“暂不添加 auth”的限制。本批只实现单个 homelab 管理员与其 GitHub 关联，不开放外部自助注册、团队或多租户。

- [x] **7.1 初始化与凭据**：初次启动初始化 admin；安装 Secret 可指定密码，或生成随机初始密码到数据目录 0600 文件；bcrypt 哈希入库，重启不重置。认证密钥复用已配置主密钥，或在数据 PVC 单独生成。
- [x] **7.2 登录会话**：数据库中保存随机 session 的 SHA-256 摘要，HttpOnly/Secure/SameSite Cookie、7 天绝对到期、登录限速、退出与密码修改/其他会话撤销。同源 JSON 写入，webhook/执行器身份继续独立。
- [x] **7.3 GitHub 登录**：本地管理员先关联；OAuth state、浏览器 Cookie、PKCE、一次性回调和管理员会话绑定；以 GitHub 不变 ID 关联，后续可直接 GitHub 登录。token 加密保存与过期刷新，不自动创建外部管理员。
- [x] **7.4 账号仓库接入**：OAuth `read:user repo` 列出账号仓库，服务端重新核对仓库管理权限；启用前验证本 App installation；未获授权时引导 GitHub 确认，再复核保存绑定（旧版错误的 OAuth 自动加入假设由阶段 8 修正）。沿用 App 集中 webhook、短期 clone 凭据和 Checks，不为同一仓库重复建立另一条事件链。
- [x] **7.5 页面**：登录、密码更新、退出、GitHub 关联与重新授权；Connections 使用 GitHub 账号仓库列表。登录成功与退出立即切换页面并清理私有查询缓存。
- [x] **7.6 自动验证**：单测/HTTP/SQLite、浏览器与临时 k3s：会话/令牌/配置重启、改密撤销、回调重放/跨浏览器、App 授权复核与真实执行 Pod（旧 fixture 的自动授权错误由阶段 8 修正）；原 CI/CD 完整回归。
- [x] **7.7 实际环境配置与部署**：经 owner 授权创建真实 OAuth App 和 Secret，部署 homelab session 模式。管理员/GitHub 登录、50 个账号仓库发现、重启保留与真实 push → 两级 CI/Checks/日志/产物通过；现有 CD 健康保持，实际证据与部署修复见 status。新真实仓库自动扩展 App 授权及组织批准未在本轮操作。

## 阶段 8：仓库接入授权修复与页面重写（代码、自动验证与 homelab 部署完成）

- [x] **8.1 真实原因诊断**：只读 GitHub API 确认 yuki-project 为 admin、OAuth scopes 为 read:user/repo；独立 OAuth token 访问 App 安装接口被拒，不误报为仓库管理员权限不足。
- [x] **8.2 授权流程修复**：移除不支持的 OAuth 安装授权调用，结构化返回待 GitHub 授权/安装/恢复/权限更新；复核前不改可信配置，拒绝过期版本、伪造仓库与错误绑定。
- [x] **8.3 页面重写**：已接入仓库主视图、独立 CI/授权/最近运行状态；搜索筛选、停用、检查运行、管理与高级登记。添加弹窗按搜索展示并分页，账号过滤、权限提示、局部错误、刷新恢复待授权选择。
- [x] **8.4 单测和浏览器**：Go race、真实 HTTP/SQLite、35 个 Vitest、13 组 Chromium、make verify 通过；fixture 不再假装支持错误令牌类型。
- [x] **8.5 Testcontainers 回归**：全量 19 个场景通过，1020.446 秒；真实 k3s 验证待授权不启用、外部确认后实际 Pod、session/配置重启和原 CI/CD。工作台 4 组浏览器 26.0 秒、认证 1 组 8.0 秒，认证专项 86.19 秒；临时容器已清理。
- [x] **8.6 实际 homelab 更新**：2026-10-25 部署含 review #1/#2 修复的 server/executor（amd64 digest 见 status）；健康、session、匿名 401、嵌入 UI 与备份/数据计数通过。该次部署未登录管理界面、未触发真实 CI，也未改变 GitHub App 授权范围。

## 阶段 9：Review 正确性与可靠性修正（代码、自动验证与 homelab 部署完成）

- [x] **9.1 结果可信度与大小限制**：确认已有 #1/#2 修复并补齐 needs 输出合入后的大小检查；增加 executor → HTTP → SQLite 的 COE 基础设施失败回归，沿用计划 900 KiB / run 16 MiB 限制。
- [x] **9.2 Checks 与事件投递**：空详情 URL、远端 Check 404 重新对账与 CAS；持久化退避/12 次预算/最终诊断/管理员重试，失败项不阻塞后续记录，详情页可独立重试 Checks。
- [x] **9.3 调度与取消**：永久/临时错误分类、重试耗尽释放并发、45 秒 grace、终止 Pod 有界收尾、5 分钟默认可配置控制面中断容忍。
- [x] **9.4 配置与身份**：rerun 校验启用/profile/build、同名新仓库 ID 冲突、Flux path；工作负载 JobID 比较、旧计划默认 deadline、内部环境过滤、加密密钥丢失/替换启动失败。
- [x] **9.5 列表开销**：运行列表从 SQL 移除重复执行 payload/步骤/变量/详细结果；详情/claim 保持完整。
- [x] **9.6 自动验证**：单测/HTTP/SQLite/race、make verify、35 个 Vitest、14 组 Chromium；全量 Testcontainers 20 个场景通过（1046.235 秒），覆盖 70 秒停机后同 UID 恢复、真实 Pod 所有权漂移与取消收尾。全量启动后追加的 needs 大小检查另用最新镜像专项通过（104.174 秒）；全量中的工作台 4 组和认证 1 组真实浏览器亦通过。
- [x] **9.7 homelab 部署**：2026-10-25 部署 server `sha256:fbc91710...`、executor `sha256:cc230abb...`（amd64 digest 见 status）；schema v5→v6 迁移先在本地生产快照副本上验证，再升级线上库。健康/session/匿名 401/UI 与部署前后数据计数通过；未登录管理界面、未触发真实 CI。v5 备份和旧镜像记录保留，旧程序不能直接打开 v6。

## 阶段 10：以完整 Workflow 运行驱动兼容层（10.6c 已完成 platform-web 真实 push 验收，其余业务库按序推进）

owner 要求先做 GitHub Actions YAML 解析与运行，以整个 workflow 能执行完为目标；优先级高于新增仓库批量/后台同步和更多 UI 功能。不创建 spec 或审批门禁。

- [x] **10.1 库与实现调研**：检查 actionlint v1.7.12、act v0.2.89、GitHub 官方 languageservices/runner；三份 steam-tasks 原始 workflow 的 parser PoC、7 个表达式对照、本机 act host 成功/失败运行试验完成。来源、依赖、限制见 architecture；调研阶段没有修改产品依赖或宣称真实 CI 通过。
- [x] **10.2 标准 AST 与诊断（第一批）**：引入 actionlint 解析适配；识别 permissions/defaults/run-name 等标准字段，区分语法和执行能力错误；保留完整语义到中间计划，清除基于少数字段的误拒绝。
- [x] **10.3a 同仓库可复用 Workflow**：固定提交文件快照、workflow_call、typed inputs/default/required/模板、显式 secrets/inherit、outputs、嵌套作用域与循环/深度限制；持久化 gate/子图/result，继续使用现有并发/日志/重启协议，保留 rerun 原始触发参数。
- [ ] **10.3b 调用能力扩展**：远程 ref 解析并固定 SHA；调用 job 的 matrix/concurrency、callee workflow concurrency、运行期输入驱动的提前执行设置；第一批明确诊断。
- [x] **10.4a 表达式引擎**：actionlint AST + act v0.2.89 MIT 局部适配（无 runner/model/Docker 依赖），事件对象、索引/过滤、转换、比较优先级、有界求值、状态回调和分阶段上下文；语义小样例与前置官方 PoC 对照。
- [ ] **10.4b 运行期规划扩展**：动态 matrix、workspace hashFiles 等按实际需求补齐；不允许控制面隐式扫描本机文件。
- [ ] **10.5 通用 Actions 运行**：tag/ref→SHA、metadata 与真实 Node/Composite/pre/post；移除 setup 系列的解析器硬编码，实际接上所需工具链、cache/artifact 和权限受限凭据能力；按 10.5b 的单 job Docker Exec 内核逐项扩展，不把整个调度外包给 act；原版 artifact v4 等协议仍未实现。
- [x] **10.5a 执行内核对比工具与选型**：固定 act v0.2.89 / Gitea act v0.262.0、独立锁定模块、真实单 job/依赖快照、Node/Composite/post、调用与缓存、临时 k3s/按需 Docker。优先上游 act；记录 Gitea 强制 /api/v1、空结果、共同 PTY race 与完整 workflow always 缺口，不能将负向探针称为完整兼容。真实 steam-tasks 原 CI 已在两者执行到业务依赖失败，详见 status。
- [x] **10.5b job worker 产品接入**：正式独立 act 单 job worker，使用 Docker Exec 避开 stock host PTY，不替换 SQLite 图和全局调度；接续 claim/日志 ACK/result/取消/重启协议，真实 Node20/24、受限 token、PVC cache facade 和显式按需 Docker profile。单测、HTTP/SQLite、Testcontainers 和 homelab 部署完成；专用仓库真实 push → CI/build → 原生 CD → GitOps → Flux 健康应用通过。全量外网失败项及专项补跑结果分列于 status，不混称全量整体 PASS。
- [x] **10.5c 生产形态测试仓库与 homelab 实验**：按 owner 最新要求暂停用业务库试验；复用三个专用测试仓库，增加 Go/PostgreSQL 应用、固定版本私有 SDK、近似 steam-tasks 的 ci/reliability/image workflows；先部署基线，再验证真实 Actions、冷/热缓存、必跑数据库验收、镜像发布、CI 失败阻断、故障健康检查和 GitOps/Flux 回滚。单测、集成、Testcontainers 与实际操作分开记录，不将 operator 桥接当产品内核接入。
- [x] **10.5d 执行准备与日志效率**：管理员固定 digest 公共镜像目录、自动拉取/原名登记、Zot Docker 28.5.1 引擎、失败阻断；8 KiB/200ms 同 step 日志合并，保留脱敏、不可变序号重试、ACK、背压、取消及重启恢复。make verify、HTTP/SQLite 与最终 25 个 Testcontainers 场景完整通过；部署 homelab，原测试 workflow 真实 push → CI/build → GitOps → Flux、回滚/恢复和两种取消阶段验证完成，无手工预热。不增加资源基线门禁；强制拉取/独立构建引擎的源配置仍为明确边界。
- [ ] **10.6 steam-* 实际仓库交付**：以账号中已有 Actions 的 9 个仓库为验收对象，按以下批次交付；完整清单、固定提交与当前阻塞见 compatibility 的业务仓库盘点。不删除原测试、不以跳过 cache/publish 或空实现冒充兼容。
  - [x] **10.6a 只读盘点与当前编译检查**：使用 gh 扫描 15 个 steam-* 仓库，9 个包含 13 份 workflow；按各默认分支固定 SHA 下载并调用当前 ValidateFiles，6 份规划检查通过、7 份诊断拒绝。读取 Dockerfile、模块/脚本、近期 GitHub 运行记录；这不是 Forgelet 实际 CI/CD 成功。
  - [x] **10.6b Go/Node 首批原始 workflow 接入**：公共 Action tag/branch/full SHA 与远程 Composite 依赖在新运行保存前固定，重跑/重启/重复 delivery 不重新追踪 tag；编译保持纯逻辑。补齐可重试源故障和 job API provider 上下文。steam-platform-server 原 check 与 steam-admin-web 原 ci/image 在临时 k3s 完成 5 次运行、28 个原步骤：Go/race、Node24/npm、冷热缓存和原 login/metadata/build-push 的 manifest digest 核对通过。make verify、追加 race/lint、HTTP/SQLite 与最终全量 Testcontainers 25 场景通过；本批不部署 homelab，不算真实账号 push/Checks/CD 接入。通用解析支持账号盘点中的合法 tag，但 setup-go@v6、pnpm、Buf、setup-buildx 等仍需在后续对应仓库验证真实 metadata/运行行为；解析成功不等于执行完成。
  - [x] **10.6c 镜像构建与缓存协议**：代码、单测/集成和临时 k3s 专项验收完成；包括实际大层分块、重复查询复用和原始业务两轮发布。已部署 homelab，并以 steam-platform-web 两次真实 push 验证原 workflow、pnpm false→true、独立 builder 依赖层 CACHED、镜像发布和 GitHub Checks。
    - [x] CacheService v2、独立 BuildKit 可达端点/能力 token/有界分块、type=gha 冷热缓存；真实控制面重启保留执行、失败不覆盖发布、取消清理。单测/HTTP/SQLite、make verify、全量 Testcontainers 26 个必选场景及最终修改后的独立 BuildKit 专项通过；完整命令与最后小修复验证范围分别见 status。
    - [x] 管理员 builder image/TOML 默认配置，显式 workflow 输入优先；明确独立 builder 不自动继承 engine 镜像目录，拉取失败保留诊断。
    - [x] packages:write 显式 Secret 引用、领取时权限复核、源只读 token 分离，拒绝 PR/native；原始 steam-admin image 与 steam-login 的自有 registry/GHCR 两次隔离发布及 digest 验证通过。
    - [x] steam-platform-web 原 check 冷热两次与各自 needs:image 完整通过：在 Forgelet 外确认并修复业务 hydration 竞态，保留原 workflow/E2E/Dockerfile 字节；显式源码补丁与标准工具 profile 下，共 28 个原主步骤、56 Playwright、pnpm false→true、新 builder RUN 层 CACHED 及 latest/SHA 标签 digest 核对通过。默认 129 MiB 实际 SDK 分块、重启/失败/取消专项及 make verify 通过；单对象与暂存字节预算不变。steam-api 本批仍为原 YAML 编译样本，真实业务测试问题留在 10.6e。
  - [x] **10.6d Rust 执行环境**：steam-cm 原 rust-ci.yml 使用 ubuntu-24.04、Rust 1.97/rustup、pwsh、Python3 和 Docker smoke/managed tests；补齐显式 profile、shell 行为及取消/退出码测试。保持 publish=false 默认，不把普通 push 改成发布，更不绕过容器安全/生命周期验收。工具与引擎均随 job 按需运行，不新增常驻服务。
  - [ ] **10.6e 业务修正与 CD 接续**：steam-tasks 的 SDK priceregion 与独立 Docker context 已修正并提交，尚需事故后的原 workflow 与 PostgreSQL 实际验收；steam-api 原 SHA 的本地测试及事故后原 workflow 测试/镜像/Checks和标签digest均已验收成功（b8f4702/8ca9acc3）；steam-platform-web 比较竞态已修复并完成原 workflow 冷/热与镜像发布专项，相关修复已提交/推送，执行环境已部署，真实 push 冷/热 CI 与镜像发布通过；业务 CD 接续继续单独交付。分别记录业务失败与平台失败，修正需保留真实测试语义。SDK 当前公开，不将其误报为已确认的私有授权阻塞。为实际需要部署的仓库明确镜像 digest、GitOps repo/path、环境与健康检查，接上现有 native CD → Flux；逐库记录 CI、镜像发布、部署、失败阻断、回滚/恢复，不能将原 workflow 的镜像 push 当成 CD 完成。跨仓库短期 token 仅在真实私有依赖需要时按管理员显式范围扩展。
- [x] **10.7a 第一批自动验证**：make verify、单测/真实 HTTP/SQLite/执行器、23 个全量 Testcontainers 叶子场景通过（1117.652 秒）；最新整对象诊断追加 2 场景专项通过（217.507 秒）。覆盖嵌套/括号 typed inputs/secret 作用域/outputs、失败/跳过/取消、rerun、并发、真实重启和原 CI/CD；workflow 88.2%，调度/调用核心细项见 status。修复真实 Pod 发现的领取→凭据阶段取消竞态。
- [ ] **10.7b 完整业务与实际环境验证**：后续每批继续单测/集成/Testcontainers；独立记录 steam-tasks 完整 workflow 与 homelab 实际部署、GitOps/Flux 结果，解析样本或独立 fixture 不能代替业务运行。

10.6 后续每批随功能验证：单测覆盖 ref 固定/错误诊断、权限与 shell 退出/取消；HTTP/SQLite 覆盖持久化与重跑不漂移；Testcontainers 使用临时 k3s 验证真实 Go/Node/Buf/pnpm/Chromium/Rust、缓存冷/热与失败隔离、BuildKit 发布和取消/重启。随后独立记录真实仓库固定 SHA 的运行、Checks、镜像 digest 及需要部署时的 Flux/健康/回滚。保持低常驻资源，不引入 spec、独立审核或性能基线门禁。

## 接下来

阶段 8、review #1/#2、阶段 9 和 10.5d 已部署；现在通过 GitHub 登录 → Connections 搜索账号仓库 → 启用 → workflow 检查/首跑接入。添加仓库弹窗默认展示全部账号仓库并滚动加载。未获 App 授权的仓库需在 GitHub 确认后返回复核，新组织仍需安装/批准。专用测试仓库已验证产品完整 GitOps/Flux 链路；10.6b 已在临时 k3s 验证 steam-platform-server / steam-admin-web 的原始 workflow，10.6c 已完成大层缓存协议、admin/login 隔离发布及 platform-web 修复后的原始 workflow 两轮验收；10.6d Rust原workflow与smoke/managed验收已完成，接下来完成10.6e的Tasks原流程/PG-Mongo和已接续业务CD的收尾；以 9 个实际仓库的原 workflow 驱动兼容，不先扩展这些仓库未使用的远程 reusable、动态 matrix 或 artifact v4。steam-platform-web 已完成 homelab 实际 push/Checks、冷/热缓存与镜像发布；其他仓库继续按实际接入逐项验收；6.3b / 6.5 暂列后续。

阶段 1–3 已完成当前清单；1.8 / 3.6 有真实 homelab 部署/回滚记录，1.10 的代码、独立 k3s 和实际 homelab 磁盘验证均已通过。
1.10 实现批次的 `make verify`、17 个 Testcontainers 场景全部通过；本批完成实际部署和真实 GitHub → CI → GitOps → Flux 复验，应用 HTTP 200 且版本/digest/revision 相符，完整结果见 status。
Web 工作台已完成自动验证与 homelab 实际部署，镜像和运行/发布/回滚证据见 status。
后续按实际使用扩展 workflow/Actions；homelab 已使用磁盘保护的默认阈值和新 server 镜像。
继续按阶段实现并更新进度，不引入 spec 审批、独立基线审核或额外管理平台。


## 2026-10-27：etcd 告警后的执行资源保护

- [x] 历史告警/延迟、节点负载与 k3s 重启时间核对，保留现场指标；不将相关性当作唯一根因证明。
- [x] 临时 homelab 配置：所有执行 profile 使用显式 worker 标签，暂时并发 1，保持控制面服务。
- [x] 引擎 CPU/内存独立配置与有效资源校验；race 单测及实际 Pod 配置回归用例。
- [x] homelab 轻量调度探针：一个 worker Pod 准入，另一个资源不足 Pending，node1 不匹配；探针已删除。
- [x] Testcontainers 真实 act/restart/cancel 回归及受限资源断言通过（首轮公共 Action 源 EOF 失败，独立重跑 PASS 468.427 秒；不是全量回归）。
- [x] 保护版本部署：引擎 1 CPU / 4Gi 真实预算、worker 限定；恢复最多 2 个 starting/running，容量不足继续等待。
- [x] 部署后健康与 etcd 告警复查、状态文档同步；事故处置阶段未恢复重型业务全链路。
- [x] 修复嵌套Docker/Buildx绕过祖先配额、job CPU发现失真与大缓存256MiB限制；make verify、完整回归与最新CPU专项分别记录于status，已配套部署；包含CPU发现修正的新完整回归也已PASS1522.854秒（27必选叶场景，2可选fixture SKIP）。
- [x] 修复大对象分块清单XML仍限32KiB的协议缺口；最大长度ID/分块额度回归、make verify、384MiB真实BuildKit专项及完整Testcontainers PASS1654.999秒（27必选、2可选SKIP），新supervisor已部署；具体业务结果仍单列。
- [ ] Tasks保留原20分钟：旧1/2CPU冷编译超时及12Gi引擎临时空间驱逐分别留证；后续CPU request1/limit3、内存4Gi、act-data24Gi、worker-only和并发2。实际3CPU内核/Node可见性已验证，待原workflow、PG/Mongo、镜像和etcd健康完整验收。
- [ ] 在受限预算下逐个恢复真实 workflow，观察共享 registry/Longhorn I/O；owner 已要求继续，当前 API 测试通过、镜像运行中，Tasks/PG/Rust 待完成。保持现有预算，不自动增加任务槽位。

2026-10-28 03:50追加：Tasks f495原race阶段最终缺失终态ACK，保留interrupted；停止确认由等待容器文件系统删除改为杀停并复查运行状态，新增回归及含后台容器/256MiB层的真实k3s超时验证，自动测试中，尚未部署。homelab公共Go标准库编译缓存层构建中，仅优化冷启动；API/Tasks/PG/Mongo/Rust仍按原条件逐项验收，不提前勾选10.7b。

2026-10-28 04:17追加：停止确认修正8220b7c的make verify及全量Testcontainers PASS1575.496秒（27必选叶场景、2可选私有fixture跳过），实际API b8/8ca9原测试、镜像、缓存导出、Checks及标签digest验收成功；Tasks/PG/Mongo/Rust继续待验收，10.7b尚不勾选。

2026-10-28 04:45追加：API实际原workflow与镜像已通过。Tasks e352证实kill后Docker状态延迟导致缺ACK；新增有界准备15分钟/原workflow预算分离、停止状态轮询，make verify及专项race通过，新的真实k3s/全量/部署与业务重跑仍待完成。Rust新真实push 8e265947已启动，尚不勾选验收。

2026-10-28 04:56追加：c1d1929准备预算/停止轮询真实k3s专项PASS507.893秒，已部署server724e776d/supervisor083b2a4d并保留活跃任务。全量仍进行；Tasks原fe3两入口7f025d00/86901c7e入队，Rust原test九步成功/image进行中，仍按实际结果逐项勾选。

2026-10-28 05:02追加：10.6d实际8e265947/50d91035原test/image、GitHub Checks、原smoke及release内两项managed生命周期测试全部通过；普通push publish=false保持且托管Actions恢复。10.6e/10.7b仍待Tasks两个原入口与手动PG18/Mongo7，不因Rust成功提前勾选。

2026-10-28 05:24追加：c1d1929全量Testcontainers PASS1653.055秒（27必选叶场景、2可选私有fixture跳过，临时资源已清理），make verify/专项/全量齐备；真实Rust已通过，Tasks两入口和手动PG/Mongo仍待结果，10.7b继续未完成。

2026-10-28 05:30追加：3CPU原冷CI 7f025d00仍20分钟timed_out，但已正确ACK/清理。只将homelab Ubuntu引擎突发limit调整为4核、request1/内存4Gi/并发2/worker隔离不变，Rust仍3；原固定计划7d1b8239/54637ffc新运行待验收。保留原20分钟和真实失败，Tasks/PG验收继续未完成。

2026-10-28 06:12追加：Ubuntu4CPU实际可见性通过，原Tasks CI7d1b8239在原20分钟内返回failure，race暴露DuckDB2.0原生库SIGILL和fence测试异步时序；3CPU旧日志也包含SIGILL。fence测试本地修复/race重复验证通过但未推送；固定版本的CPU兼容共享库正在本机限额交叉编译，尚未部署。image54637ffc独立遇到Atlas下载TLS失败，发布skipped。保持原workflow/断言、worker隔离/内存准入，不把CPU调整或本地回归视为Tasks完整验收，10.6e/10.7b继续待完成。

2026-10-28 06:24追加：DuckDB同2.0.0/x86-64-v2可选工具镜像已构建，make verify和真实Go驱动Testcontainers PASS35.310秒；物理N5105原生SQL探针通过并清理。固定镜像6ace4272已只替换Ubuntu runner，资源/隔离策略保持；测试同步修复7c5d09a已真实push，原YAML的CI1b17a3c4/image221882fe正在运行，随后验收手动PG18/Mongo7，清单尚不勾选。
