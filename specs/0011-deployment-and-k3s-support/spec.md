# Spec 0011 — Deployment and k3s Support（M0 wiring 切片）

- **Status**: accepted（M0 切片；真实 k3s 冒烟与支持矩阵为 V1 任务）
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner（M0 收尾整体授权）
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted）；覆盖 NFR-1 的 M0 部分与
  0001 plan §2 M0 integration order
- **Depends on**: 0002–0008 全部 M0 切片

## Requirements（M0）

- FR-D1 `[P0]` 三个 composition root 就位：`cmd/server`（webhook + 调度 + 执行器内部 API）、
  `cmd/controller`（JobRun reconciler + HTTP 投影）、`cmd/executor`（0008）。`make build`
  产出三个 binary。
- FR-D2 `[P0]` server 内部 API（与 0008 httpclient 契约一致）：`GET plan`、`POST secrets`、
  `POST status`，加 `POST observed`（controller 投影）；全部经 0003 identity 验证
  （scope + JobRun 绑定 + 0003 policy 下发判定）。
- FR-D3 `[P0]` 进程内端到端闭环测试覆盖 0001 plan §2 全链路：signed push → delivery 去重 →
  workflow parse/compile（本地 workflow 源）→ durable run/job → dispatch（确定性 CR）→
  （执行器角色）认证 FetchPlan/FetchSecrets → 多 step 执行（共享 env/secret 注入）→
  status 投影 → run 终态 → Check Run queued→success → GC。
- FR-D4 `[P0]` 部署件草案：CRD（0004 codegen 产物）、namespace、SA/RBAC（controller 最小权限；
  executor SA 零 RBAC）、server/controller Deployment 骨架；`hack/kind-up.sh` 可创建集群并
  应用部署件（实验性）。
- FR-D5 `[P0]` M0 偏差显式记录：durable store 用内存实现（PG adapter = 0002 T8）、identity 用
  本地 HMAC verifier（TokenReview = 0003 T5）、workflow 源为本地目录（GitHub content API 未
  实现）、Check Reporter 提供 dev 实现（真实 GitHub App 联调 = 0005 V1）。上述替换点全部为
  port 注入，不影响协议测试语义。

## Requirements（V1，任务化）

- FR-D6 `[P1]` PostgreSQL adapter 接入 server（替换内存 store，语义以 0002 测试为准绳）。
- FR-D7 `[P1]` TokenReview/JWKS verifier、GitHub content API workflow 源、真实 Check Run 联调。
- FR-D8 `[P1]` 固定 k3s 版本支持矩阵、真实集群 M0 smoke test（webhook→Check Run）、
  镜像构建与发布流程。

## Acceptance criteria

**AC-M0**：`make verify` 全绿且包含进程内 e2e；`make build` 产出 3 个 binary；部署件可通过
`kubectl apply --dry-run=client` 校验。**AC-V1**：真实 k3s 上 M0 smoke 通过。
