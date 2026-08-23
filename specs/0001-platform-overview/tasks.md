# Tasks — Spec 0001 Platform Overview

总纲通过子 spec 落地。此文件同时跟踪实现前门禁与子 spec 的创建/完成。

- [x] 解决 schedule/webhook、replacement/coexistence mode 边界
- [x] 定义 PG/CRD 状态所有权和非原子故障窗口要求
- [x] 定义 Executor workload identity 与 Plan/secret 顶层边界
- [x] 明确 M0、V1、P2 slice
- [x] 建立 modular monorepo 模块边界
- [x] 建立 docs-only 可通过的 `make verify` 与 CI 基线
- [x] Spec 0001 accepted，并创建本 plan

- [ ] 0002-state-consistency-and-scheduler（accepted，M0 已合入；T8 PG adapter、T9 schedule 见其 tasks.md）
- [ ] 0003-security-identity-secrets（FR-5.1、FR-9）
- [ ] 0004-crd-api-and-controller（FR-4）
- [ ] 0005-github-events-and-checks（FR-1、FR-8）
- [ ] 0006-workflow-syntax-and-compiler（FR-2）
- [ ] 0007-expression-engine（FR-3）
- [ ] 0008-executor-runtime（FR-5）
- [ ] 0009-builtin-actions（FR-6）
- [ ] 0010-observability（FR-10）
- [ ] 0011-deployment-and-k3s-support（NFR-1）
- [ ] M0 端到端闭环演示（push → Check Run success）
