# Plan — Spec 0004 CRD API and Controller

- **Status**: complete
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner
- **Completed**: 2026-08-24, v1-wave9 实现并通过验证
- **Spec**: `specs/0004-crd-api-and-controller/spec.md`（proposed）
- **Boundaries**: `docs/module-boundaries.md`（runtime/controller：禁止 provider SDK、PG adapter）

## 1. Package layout

```
api/v1alpha1/                 RunnerClass / WorkflowRun / JobRun types + deepcopy + CRD gen
deploy/crds/                  controller-gen 产出的 CRD manifests
internal/runtime/controller/
  port.go                     consumer-side ports: DurableProjection（ApplyObserved）、
                              JobRunSource（durable 记录 → CR spec 数据）
  jobrun_controller.go        JobRun reconciler（ensure pod、status、投影）
  active.go                   scheduler.ActiveExecutionStore 的 Kubernetes adapter
```

依赖方向：controller → run/model、run/scheduler（仅 port 接口）；cmd 装配 fake/真实 client 与
durable adapter。api 包零内部依赖。

## 2. Types（核心字段）

```go
JobRunSpec{ RunID, JobKey, RunnerClass, PlanID, PlanDigest string; Attempt int32 }
JobRunStatus{ Phase string; PodName, PodUID string; StartedAt, FinishedAt *metav1.Time;
              Conditions []metav1.Condition }
WorkflowRunSpec{ RunID, DeliveryKey, Event, Ref, SHA string; Repository RepositoryRef }
WorkflowRunStatus{ Phase string }
RunnerClassSpec{ Image string; Resources corev1.ResourceRequirements;
                 NodeSelector map[string]string; Workspace WorkspaceSpec;
                 Security SecuritySpec }
```

Workspace{Mode(auto|emptyDir|pvc), StorageClassName, Size}；Security{AllowSecrets bool,
Labels map[string]string}（P2 字段保留结构，M0 不消费）。

## 3. Reconciliation

```
JobRun CR 变化
  ├─ RunnerClass 缺失 → status condition Ready=False/RunnerClassMissing，返回 retry
  ├─ 终态（succeeded/failed）→ 不创建 Pod；仅维护 status
  ├─ Pod 不存在 → create（确定性名 = CR 名 + "-pod"，ownerRef JobRun，见 §4 模板）
  └─ Pod 存在 → phase 映射写 status；phase 变化时经 DurableProjection.ApplyObserved 投影
```

phase 映射：Pod Pending→pending、Running→running、Succeeded→succeeded、Failed→failed。
投影幂等由 0002 ApplyObserved 的单调性保证；controller 侧再以 status.Phase 变化做去重。
FinishedAt 在终态时写入；Pod 被外部删除（GC）且 CR 终态 → 不重建。

## 4. Pod 模板（M0）

- name：`<jobrun-cr-name>-pod`；labels：`ci.forgelet.dev/jobrun-id`、app=forgelet-executor。
- `automountServiceAccountToken: false`；`serviceAccountName: forgelet-executor`（无 RBAC）。
- projected volume `control-plane-token`：serviceAccountToken audience
  `forgelet-control-plane`、path `token`、expirationSeconds 3600；挂载 `/var/run/forgelet`。
- `restartPolicy: Never`；容器 `job`：RunnerClass 镜像、占位 command `/ci/executor`、
  resources、nodeSelector；emptyDir volume `workspace` 挂 `/workspace`。

## 5. Ports

```go
type DurableProjection interface {
    ApplyObserved(ctx, model.JobRunID, model.ObservedPhase, now time.Time) error
}
type JobRunSource interface {
    Get(ctx, model.JobRunID) (model.JobRunRecord, error)   // CR spec 数据来源（durable）
}
```

ActiveExecutionStore adapter（active.go）：CreateOrGet → JobRunSource.Get → 填充 spec →
create-or-get CR；Delete → 删除 CR（级联 Pod）。

## 6. Codegen 与构建

- `api/v1alpha1/groupversion_info.go` 内 `//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen`
  生成 deepcopy（object）与 CRD manifest（output deploy/crds）。
- `make generate` 幂等；CI（make verify）在无网络缓存后仍可运行（controller-tools 进入 go.mod）。

## 7. Testing strategy

- fake client（controller-runtime/client/fake）覆盖 AC-M0 1–5：reconcile 幂等、Pod 形状断言、
  phase 矩阵、投影去重（计数 port）、终态不重建、RunnerClass 缺失/补齐、adapter create-or-get
  与级联声明（ownerRef 断言）。
- envtest（T5，opt-in：`make test-envtest`，未安装时跳过）。
- 覆盖率目标 ≥ 80%。
