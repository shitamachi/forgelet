# 模块边界与 Monorepo 约定

forgelet 采用 **modular monorepo**：控制面、Kubernetes runtime、Executor、部署资产和未来 Web UI
放在同一仓库中，以便一次变更能够原子地更新 spec、API、实现、部署清单和测试。

初期只使用根目录的一个 `go.mod`。Go package 是代码边界，binary 是部署边界；不为“看起来更模块化”
提前创建多个 Go module。只有组件需要独立版本、独立发布或明显不同的依赖策略时，才新增 `go.mod`，
并使用仓库根 `go.work` 管理本地联调。

## 1. 逻辑模块

| 模块 | 主要职责 | 禁止依赖 |
|------|----------|----------|
| `workflow` | YAML syntax、AST、表达式、编译、matrix、DAG | Kubernetes、PG、HTTP client、provider SDK |
| `run` | WorkflowRun/JobRun 应用状态机、调度、Plan、并发控制 | 具体 provider、具体数据库、Kubernetes client |
| `provider` | GitHub 等 Source Provider adapter | Kubernetes runtime、Executor 实现 |
| `report` | Check/状态报告 port 与 provider adapter | 调度实现、Kubernetes client |
| `runtime/controller` | JobRun/Pod reconciliation、状态观测 | provider SDK、直接访问 PG |
| `runtime/executor` | 单 Job 的 step 执行、file commands、mask、取消 | Kubernetes client、provider SDK、直接访问 PG |
| `storage` | PostgreSQL、S3/MinIO 等 adapter | Kubernetes runtime、provider SDK |
| `security` | workload identity、secret policy、加解密 adapter | workflow parser 具体实现 |
| `observability` | log、metric、trace adapter | 业务状态所有权 |

这里的模块是架构边界，落地时可以包含多个 Go package。不要创建包罗万象的 `common`、`shared`、
`helpers`、`util` 包；共享代码必须归属于一个明确概念。

## 2. 目录目标形态

```text
cmd/
  server/                 API、webhook、scheduler composition root
  controller/             Kubernetes controller composition root
  executor/               Job executor composition root
api/
  v1alpha1/               CRD public API types
internal/
  workflow/
    syntax/               YAML decoding、source locations、validation
    expression/           parser/evaluator and contexts
    compiler/             IR、matrix、DAG compilation
  run/
    model/                provider-neutral run state
    scheduler/            scheduling use cases
    plan/                 immutable Plan and digest
  provider/
    github/               GitHub App adapter
  report/
  runtime/
    controller/
    executor/
  storage/
    postgres/
    object/
  security/
  observability/
proto/                    ConnectRPC schemas（需要时创建）
deploy/                   Helm/Kustomize/manifests（需要时创建）
web/                      Web UI（进入对应里程碑后创建）
specs/                    product requirements and accepted plans
```

目录按实际 spec 渐进创建，不预先生成空 package。

## 3. 依赖规则

依赖只允许从外向内：

```text
cmd (composition roots)
  └─ adapters: provider/github, storage/postgres, runtime/kubernetes
       └─ application: run/scheduler, runtime/controller, runtime/executor
            └─ domain/pure logic: workflow, run/model, run/plan
```

- 核心模块定义自己消费的最小 port；adapter 实现 port。
- `cmd/*` 是唯一允许组装具体 adapter 的位置。
- `workflow/**` 和 `run/model` 必须保持确定性；时间、随机数、文件系统和外部 I/O 通过显式依赖注入。
- Controller 通过 control-plane port 报告 durable state，不直接 import PostgreSQL adapter。
- Executor 只调用 forgelet control-plane/runtime 服务，不 import Kubernetes client。
- Provider 的原始 payload 可以作为不可变数据传递，但核心模块不得依赖 provider SDK 类型。
- 模块间禁止通过全局变量、`init()` 或隐式 singleton 交换状态。

新增跨模块 import 时，PR 必须说明它符合哪一条允许的依赖方向。后续有代码后，用架构测试或
依赖检查脚本自动阻止反向依赖。

## 4. 多模块升级条件

满足以下至少一项，才考虑从单 Go module 升级为多 module workspace：

- Executor SDK 或 API types 需要被外部仓库独立引用和版本化；
- 某组件有独立发布节奏或必须显著缩小依赖图；
- 安全/许可证策略要求依赖物理隔离；
- 单 module 已对构建、工具或所有权造成经验证的问题。

升级必须有独立 spec/ADR，说明版本策略、CI matrix、跨 module 兼容性和发布流程。不要使用
`replace` 指向本地路径作为提交后的长期方案；多 module 本地开发统一使用 `go.work`。
