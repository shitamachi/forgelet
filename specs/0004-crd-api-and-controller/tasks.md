# Tasks — Spec 0004 CRD API and Controller

- [x] T1 `api/v1alpha1`：三个 CRD types（spec/status/kubebuilder markers）、scheme 注册、
      deepcopy + CRD manifest 生成接入 `make generate`（FR-C1）
- [x] T2 `internal/runtime/controller` ports：DurableProjection、JobRunSource（FR-C3.4）
- [x] T3 JobRun reconciler：ensure pod、phase 映射、幂等投影、终态不重建、
      RunnerClass 缺失 condition（FR-C2、FR-C3）
- [x] T4 ActiveExecutionStore Kubernetes adapter：create-or-get CR、级联 Delete（FR-C4）
- [x] T5 fake client 测试矩阵（AC-M0 1–5）+ 协议一致性（CR 名 = model 派生）
- [ ] T6 envtest 层（真实 API server）：ownerRef 级联、schema 校验、watch（V1，opt-in target）
- [x] T7 部署清单（namespace/SA/RBAC/CRD apply）与 0011 联动（V1）—— `deploy/manifests` 已随烟雾验证
