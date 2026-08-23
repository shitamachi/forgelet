# Tasks — Spec 0011 Deployment and k3s Support

- [x] T1 M0：`internal/server` 组合根（webhook、ingest+compile、dispatch、collector、
      PlanStore、secrets/status/observed 内部 API、auth 中间件）
- [x] T2 M0：controller HTTP 投影 client + `cmd/controller`
- [x] T3 M0：`cmd/server`（flags、后台 dispatch/collect 循环、优雅退出）
- [x] T4 M0：进程内端到端闭环测试（FR-D3 全链路）
- [x] T5 M0：部署件（namespace/SA/RBAC/deployment 骨架）+ `hack/kind-up.sh`（实验性）
- [x] T6 V1：PostgreSQL adapter 替换内存 store（0002 T8 语义）
- [x] T7 V1：TokenReview verifier、GitHub content API workflow 源、真实 Check Run 联调
      （adapter 与 server 接线全部落地；真实环境联调随 T8 k3s smoke）
- [ ] T8 V1：k3s 支持矩阵 + 真实集群 M0 smoke + 镜像发布流程
