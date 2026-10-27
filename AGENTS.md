# AGENTS.md

forgelet 为我们自己的 k3s homelab 提供低常驻资源的独立 CI/CD。
代码托管在 GitHub 等外部服务，GitHub Actions 兼容范围按实际使用逐步扩展。

## 当前工作方式

2026-10-23 起暂停 spec-driven 开发。**不创建 spec/plan 审批门禁，不要求 accepted 状态。**
以后是否重新引入由项目 owner 决定；旧 specs 的批准和完成状态不适用于重启后的项目。

开始有实际影响的工作前读：

1. `docs/status.md`：已经完成什么、验证结果、当前任务和问题。
2. `docs/plan.md`：实施顺序、任务清单和各阶段完成条件。
3. `docs/architecture.md`：当前设计与取舍。
4. 修改 workflow 支持时再读 `docs/compatibility.md`。

工作完成时同步更新状态与相应任务清单；设计或兼容范围变化时更新对应文档。
小修复不需要新建文档。计划、代码实现、自动测试、真实集群验证必须分别说明，不能混称完成。
当前已进入分阶段快速实现；不设独立基线审核或资源基准门禁。随实现运行单测、集成测试和 Testcontainers。

## 产品与设计边界

- Git 托管在外部服务；GitHub App 是第一个接入。不自建 Git 平台。
- forgelet 自己接收事件、调度、保留日志和历史；不使用 GitHub Runner、ARC 或 Tekton 执行。
- 目标形态：一个常驻 Go 控制面，SQLite + 单个数据 PVC，按需创建执行 Pod。
- 当前不需要 CRD、独立 controller 服务、PostgreSQL、消息队列、Loki 或 MinIO。
- 一个 job attempt 使用一个主 Pod。native steps 共享主容器；显式 act profile 由 supervisor 管理同 Pod 自有引擎中的 job 容器，steps 共享该容器和 workspace。
- `runs-on` 匹配管理员配置的执行 profile；不承诺 GitHub 托管镜像中的所有工具。
- Actions 支持必须有明确版本、输入和行为测试；未知字段/能力应给出诊断。
- JS Actions 使用真实 Node 子进程；Go 内嵌 JS VM 不能代替 Node。
- 持久化计划只含 secret 引用。执行 Pod 无 Kubernetes API 权限，不挂宿主 Docker socket、hostPath 或 cluster-admin 凭证；act profile 仅使用本 Pod 自有 emptyDir socket。
- 镜像构建用按需 BuildKit；CD 优先通过托管 GitOps 仓库与 Flux 衔接。
- 默认单控制面、可配置多任务并发（默认 4）、仅可信仓库；多租户、HA、完整 Actions 兼容不是当前目标。

## 工程规则

- Go >= 1.27，单 Go module；`net/http` + chi、`gopkg.in/yaml.v3`、按需使用 client-go。
- 不预建空模块或抽象框架。新增依赖应对应眼前需求，记录明显的资源/运维成本。
- `cmd/` 只做依赖装配；`internal/workflow` 保持纯逻辑；执行器不依赖 Kubernetes client 或数据库。
- 依赖方向、拟议目录见 `docs/architecture.md`；不创建 `common/shared/helpers/utils` 包。
- 用 `fmt.Errorf("...: %w", err)` 包装错误；无库内 panic、`context.Background()` 或 init 副作用。
- Context 显式传递；后台工作可取消，时间与外部 I/O 可替换。
- 快速分阶段交付，随功能运行单测 + 集成测试 + Testcontainers；不单独设基线审核/性能验收门禁。
- 默认 native profile 不依赖 Docker；owner 授权的显式 act profile 可为可信仓库按需启动 Pod 自有 privileged Docker，引擎随 job 退出，无空闲常驻服务。不得使用宿主 Docker socket。
- 新行为有行为测试，修复有回归测试；优先 table-driven。工作流与调度核心覆盖率目标 ≥ 85%。
- 重启恢复、幂等、安全和取消不能只用 happy-path 或 fake client 验证；需要真实 k3s 测试。
- `make verify` 执行真实构建、race 单测/进程内集成、lint 和 generate。
- `make test-integration` 用 Testcontainers 创建临时 k3s，验证实际安装清单与执行链；不读取用户 kubeconfig。

## Git

- 默认分支 `main`；工作分支 `feat/...`、`fix/...`、`chore/...`。
- Conventional Commits；保持变更可独立检查。
- 清理旧内容前保护未提交和未跟踪文件；不自动提交、推送或操作实际集群。
- 旧实现已经留有 Git 历史与仓库外完整快照，恢复位置见 `docs/status.md`。


<claude-mem-context>
# Memory Context

# $CMEM forgelet 2026-10-24 12:57am GMT+8

No previous sessions found.
</claude-mem-context>