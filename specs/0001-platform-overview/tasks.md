# Tasks — Spec 0001 Platform Overview

总纲通过子 spec 落地。此文件同时跟踪实现前门禁与子 spec 的创建/完成。

- [x] 解决 schedule/webhook、replacement/coexistence mode 边界
- [x] 定义 PG/CRD 状态所有权和非原子故障窗口要求
- [x] 定义 Executor workload identity 与 Plan/secret 顶层边界
- [x] 明确 M0、V1、P2 slice
- [x] 建立 modular monorepo 模块边界
- [x] 建立 docs-only 可通过的 `make verify` 与 CI 基线
- [x] Spec 0001 accepted，并创建本 plan

- [x] 0002-state-consistency-and-scheduler（implemented；T1–T9 全绿）
- [x] 0003-security-identity-secrets（implemented；T1–T7 全绿）
- [x] 0004-crd-api-and-controller（implemented；T1–T7 全绿）
- [x] 0005-github-events-and-checks（implemented；T1–T9 全绿）
- [x] 0006-workflow-syntax-and-compiler（implemented；T1–T8 全绿）
- [x] 0007-expression-engine（implemented；T1–T7 全绿）
- [x] 0008-executor-runtime（implemented；T1–T10 全绿，JS/Composite 另立 0012）
- [x] 0009-builtin-actions（implemented；T1–T7 全绿）
- [x] 0010-observability（implemented；T1–T5 全绿）
- [x] 0011-deployment-and-k3s-support（implemented；T1–T8 全绿）
- [x] 0012-js-composite-actions（implemented；T1–T6 全绿）
- [x] M0 端到端闭环（进程内 e2e 全链路绿：push→去重→编译→dispatch→认证执行→状态投影→Check Run→GC；真实 k3s 冒烟待 0011 T8）
