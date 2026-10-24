# forgelet

给自己的 **k3s homelab** 使用的轻量 CI/CD：仓库放在 GitHub 等托管服务，
任务在自己的集群执行，逐步支持常用 GitHub Actions workflow。

**当前状态：CI/CD 核心与 Web 工作台已实现；自动验证和实际环境接入分别记录在 [项目状态](docs/status.md)。**

## 新方向

- 一个常驻控制面；SQLite 和一个本地 PVC 保存状态、日志、产物与缓存。
- 并发上限可配置（默认 4），执行 Pod 按需创建，空闲时没有执行 Pod。
- 数据磁盘低水位时暂停新调度，空间恢复后自动继续；运行中任务和保留清理继续处理。
- GitHub App 接收事件、读取代码、回写 Checks；以后再接其他托管 Git 服务。
- 支持 shell、真实 Node 20/24、Composite、静态 matrix、并发组、加密 secrets、artifact/cache。
- 按需 rootless BuildKit 构建并推送 digest；GitOps direct/PR 投递、Flux 健康确认和镜像回滚。
- 可操作接入配置：仓库/installation、workflow 路径与 YAML、GitOps/Flux 目标；SQLite 持久化并即时生效。
- Web 工作台：管理员/GitHub 登录、运行/needs/实时日志、手动运行/取消/重跑、产物、发布/回滚、仓库设置、磁盘与维护。
- React/TypeScript 静态页面嵌入 Go 二进制，生产环境没有新增 Node 服务。
- 先服务自己的真实仓库；不追求全平台、多租户或完整 Actions 兼容。

## 从哪里看

| 文档 | 内容 |
|---|---|
| [项目总览](docs/overview.md) | 代码地图、端到端流程、状态机、设计取舍与当前风险 |
| [当前状态](docs/status.md) | 实际完成情况、验证、下一步、旧代码备份 |
| [实施计划](docs/plan.md) | 阶段、任务清单、完成条件 |
| [设计方案](docs/architecture.md) | 架构、数据、执行、恢复与边界 |
| [部署与开发](docs/deployment.md) | 镜像、k3s 安装、API 示例、备份恢复 |
| [兼容范围](docs/compatibility.md) | 每项 workflow / Actions 能力的支持计划与限制 |
| [参考项目](docs/research.md) | Woodpecker、act/Forgejo、ARC、Flux 等设计依据 |

当前暂停 spec 模式；直接按计划开发，并在每次变更后更新状态文档。

## 开发验证

需要 Go >= 1.27、Node 24/npm、golangci-lint；容器集成测试另外需要 Docker（只用于测试）。

```sh
make verify             # 静态 UI/两个二进制、race、Vitest/Chromium、lint、generate
make test-integration   # 临时 k3s：CI、Actions、BuildKit/Flux、故障回滚、磁盘保护、真实浏览器
make images             # server 与 executor 镜像
```

安装入口是 [deploy/install.yaml](deploy/install.yaml)，先按 [部署说明](docs/deployment.md) 配置镜像、TLS、仓库和 GitHub App。
新安装默认 `admin_access: session`，首次启动初始化 admin，初始密码写入数据目录 `initial-admin-password`（0600），也可用安装 Secret 指定。登录后可关联 GitHub OAuth，获取账号仓库并启用 CI。已有 open/token 配置继续兼容；升级不隐式切换管理模式。配置和凭据说明见 docs/deployment.md。
默认执行镜像有 bash/git 和 Node 20/24；可用已验证的 setup-go/setup-node 安装工具链，版本和输入见兼容文档。
CI/CD 示例见 [cicd.yml](test/fixtures/cicd.yml)，GitOps 配置和只读 Flux RBAC 见部署说明。
真实 GitHub → homelab 的 push、CI、BuildKit、GitOps、Flux 部署与故障回滚已验证，run/commit 证据见项目状态。
Testcontainers 另外使用独立集群与 GitHub API fixture 验证恢复、隔离和异常路径。
