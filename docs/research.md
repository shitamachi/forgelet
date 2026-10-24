# Homelab CI/CD 设计参考

访问日期：2026-10-23。本文是[架构设计](architecture.md)与[实施计划](plan.md)的设计输入，
记录官方资料与我们的判断。技术选择属于当前建议设计，尚未完成产品实现和验证。

新的产品方向是独立、轻量的 CI/CD：Git 保留在 GitHub 等托管服务，调度和执行由自己的 k3s
集群负责，先支持实际使用的 workflows 和 Actions，再逐步扩大兼容范围。

## 1. Woodpecker：小型控制面与外部 Git 接入

Woodpecker 的 server 处理 UI、webhook、API 和 workflow 配置；agent 通过 gRPC 连接 server，
可选择 Docker、Kubernetes 或 local backend。默认使用 SQLite，大型实例可换外部数据库。
这些选择说明，外部 Git 服务、小型 CI 控制面和按需求扩展存储可以独立组合。
来源：[整体架构](https://woodpecker-ci.org/docs/administration/general)、
[数据库配置](https://woodpecker-ci.org/docs/administration/configuration/server)。

其 Kubernetes backend 将每个 step 放入独立 Pod，通过临时 PVC 传递文件。
forgelet 可以借鉴并发上限、队列与执行后端分离，却不宜照搬这个 step 模型：GitHub Actions
同一 job 的普通步骤需要共享工作目录、工具安装及后续步骤可见的环境变更；额外 Pod 和 PVC
也带来启动与存储操作。Woodpecker 本身不提供 GitHub Actions workflow 兼容。
来源：[Kubernetes backend](https://woodpecker-ci.org/docs/administration/configuration/backends/kubernetes)。

## 2. Forgejo、Gitea 与 act：兼容范围必须公开

Forgejo 官方说明 Actions 面向熟悉 GitHub Actions 的用户，但不以兼容为设计目标。
Forgejo Runner 从 Forgejo 实例领取任务，执行模式包括 Docker/Podman、LXC 和 host。
它不是直接连接任意 GitHub 仓库的独立 CI 控制面，也不符合本项目不自建 Git 服务的默认部署。
来源：[Forgejo Actions 定位](https://forgejo.org/docs/latest/user/actions/overview/)、
[Runner 配置](https://forgejo.org/docs/latest/admin/actions/configuration/)。

Gitea 明确以兼容 GitHub Actions 为目标，同时维护差异清单。这种可查询的兼容矩阵值得采用，
但 forgelet 对不支持的关键字应明确报错，不能因为其他实现忽略某字段就照样忽略。
来源：[Gitea 与 GitHub Actions 的差异](https://docs.gitea.com/usage/actions/comparison/)。

act 是表达式、action metadata、生命周期和执行环境差异的重要参考，不是完整兼容的保证。
官方列出了 concurrency、permissions 等方面的缺口；默认容器镜像也不等于 GitHub-hosted
runner 的完整工具环境。act 存在 host 模式，因此不能笼统称它必然要求 Docker；但把 act
塞进 Pod 并不会自动获得 Kubernetes 原生的隔离、恢复和资源管理。
来源：[act 不支持的功能](https://nektosact.com/not_supported.html)、
[act 执行环境](https://nektosact.com/usage/runners.html)。

我们的结论：先建立真实 workflow 样例与逐项验收清单；明确区分 YAML 语法、调度语义、
执行环境和 GitHub Runtime Service 协议。JavaScript action 需要真实 Node.js 运行时，
不能把通用 JavaScript 解释器当作 Node.js 的等价替代。

## 3. ARC 与 actions/scaleset：原生 GitHub Actions 路线的对照

ARC 在 Kubernetes 中管理临时 GitHub Runner。controller 和 listener 常驻，空闲 runner
可缩到 0；workflow 解释和任务分配仍由 GitHub Actions 服务完成。因此“零空闲 runner”
不等于“零常驻组件”。其 Kubernetes 容器模式使用 hooks 创建执行 Pod，并非只能使用 DinD。
来源：[ARC 架构](https://docs.github.com/en/actions/concepts/runners/actions-runner-controller)、
[缩容与容器模式](https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller/deploy-runner-scale-sets)。

[actions/scaleset](https://github.com/actions/scaleset) 是从 ARC 提取的独立 Go client，
提供 scale set、消息长轮询与 JIT runner 配置；访问时标记为 Public Preview。
它适合研究临时执行环境和容量控制，但仍使用官方 runner，并依赖 GitHub 控制面。

用户已选择独立 CI/CD 路线，forgelet 因而不采用上述两者作为核心。可以借鉴每次执行使用
干净环境、空闲不保留执行 Pod 和容量上限；不能宣称自研 Actions 子集具有同等兼容覆盖。

## 4. Flux 与 BuildKit：构建和部署各自收敛

Flux 的 source 获取期望状态，kustomize controller 持续 reconcile，并能观测健康状态与纠正漂移。
适合借鉴的交付流程是：CI 测试并构建镜像 → 得到不可变 digest → 更新 Git 中部署声明 →
集群内 CD 控制器应用并报告健康状态。已有 Flux 时应优先对接；没有时也不应把完整 GitOps
平台强制变成 CI 的安装前提。Flux 本身不解释 Actions workflow。
来源：[Flux Kustomization](https://fluxcd.io/flux/components/kustomize/kustomizations/)。

BuildKit 提供镜像构建、缓存与输出能力，可用于按需构建阶段，无需把 Docker socket 暴露给
普通 CI job。rootless 模式存在内核、snapshotter 和容器安全配置要求；能否在目标 k3s
节点安全运行，必须通过实际验证，不能仅凭 rootless 名称保证默认可用或隔离充分。
来源：[BuildKit](https://github.com/moby/buildkit)、
[rootless 限制](https://github.com/moby/buildkit/blob/master/docs/rootless.md)。

## 5. 对本项目计划的约束

- SQLite、单控制面进程、使用原生 Kubernetes 资源而不引入自定义 CRD、本地保存日志和产物，
  是面向低资源目标的当前建议设计；实施时需要验证它们在目标 k3s 环境中的适用性与限制。
- 兼容承诺从自己的 3–5 个代表性 workflow 开始，覆盖成功、失败、取消和明确拒绝；
  解析成功或单个演示通过不能代表完整 GitHub Actions 兼容。
- 资源预算必须测量常驻控制面、必要依赖、执行 Pod 和构建器，分别记录闲置与执行峰值。
  本次没有运行这些项目的资源基准，未证明任何固定内存、CPU 或启动时间指标。
- 首版必须验证重复事件、控制面重启、节点丢失与状态回报失败；部署操作不能因为普通重试
  被悄悄执行两次。低资源设计仍须保留持久化、幂等和恢复边界。
