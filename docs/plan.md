# 实施计划

更新：2026-10-24。按 owner 要求分阶段快速实现，多个任务并发从首版支持。
不使用 spec/accepted 审批，不安排独立基线、资源测量或审核阶段。
用单测、集成测试、Testcontainers 随实现验证，完成后更新本页和 [status.md](status.md)。

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

## 阶段 8：仓库接入授权修复与页面重写（代码与自动验证完成）

- [x] **8.1 真实原因诊断**：只读 GitHub API 确认 yuki-project 为 admin、OAuth scopes 为 read:user/repo；独立 OAuth token 访问 App 安装接口被拒，不误报为仓库管理员权限不足。
- [x] **8.2 授权流程修复**：移除不支持的 OAuth 安装授权调用，结构化返回待 GitHub 授权/安装/恢复/权限更新；复核前不改可信配置，拒绝过期版本、伪造仓库与错误绑定。
- [x] **8.3 页面重写**：已接入仓库主视图、独立 CI/授权/最近运行状态；搜索筛选、停用、检查运行、管理与高级登记。添加弹窗按搜索展示并分页，账号过滤、权限提示、局部错误、刷新恢复待授权选择。
- [x] **8.4 单测和浏览器**：Go race、真实 HTTP/SQLite、35 个 Vitest、13 组 Chromium、make verify 通过；fixture 不再假装支持错误令牌类型。
- [x] **8.5 Testcontainers 回归**：全量 19 个场景通过，1020.446 秒；真实 k3s 验证待授权不启用、外部确认后实际 Pod、session/配置重启和原 CI/CD。工作台 4 组浏览器 26.0 秒、认证 1 组 8.0 秒，认证专项 86.19 秒；临时容器已清理。
- [ ] **8.6 实际 homelab 更新**：本轮未更新实际集群；线上仍为阶段 7 镜像。新仓库 GitHub 授权变更需 owner 明确选择，不自动增加 App 范围。

## 接下来

GitHub 自助接入和阶段 7 登录已部署；现在通过 GitHub 登录 → Connections 搜索账号仓库 → 启用 → workflow 检查/首跑接入。未获 App 授权的仓库需在 GitHub 确认后返回复核，新组织仍需安装/批准。下一批按实际需要实现 6.3b / 6.5；批量、rename 和后台同步继续按需实现。

阶段 1–3 已完成当前清单；1.8 / 3.6 有真实 homelab 部署/回滚记录，1.10 的代码、独立 k3s 和实际 homelab 磁盘验证均已通过。
1.10 实现批次的 `make verify`、17 个 Testcontainers 场景全部通过；本批完成实际部署和真实 GitHub → CI → GitOps → Flux 复验，应用 HTTP 200 且版本/digest/revision 相符，完整结果见 status。
Web 工作台已完成自动验证与 homelab 实际部署，镜像和运行/发布/回滚证据见 status。
后续按实际使用扩展 workflow/Actions；homelab 已使用磁盘保护的默认阈值和新 server 镜像。
继续按阶段实现并更新进度，不引入 spec 审批、独立基线审核或额外管理平台。
