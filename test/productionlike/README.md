# 接近实际应用的 homelab 实验

仅操作已有的三个专用测试仓库，不读取或修改 steam-tasks、业务 SDK 或业务部署：

| 仓库 | 用途 |
|---|---|
| `shitamachi/forgelet-e2e-app` | Go HTTP 任务 API、PostgreSQL、迁移、可靠性和镜像 workflows |
| `shitamachi/forgelet-onboarding-e2e` | 独立私有 Go SDK，固定 `v0.1.0`，保留原 onboarding workflow |
| `shitamachi/forgelet-e2e-gitops` | 测试应用和 PostgreSQL 的 Flux 清单 |

应用覆盖幂等创建、请求冲突、乐观锁领取、归属校验与完成状态；PostgreSQL 集成测试要求
20 个并发领取者只能成功一个、迁移重入及重新连接后数据保留。HTTP 测试检查输入、状态码与错误边界。
不是复制完整业务逻辑；目的是复现业务 workflow 的外部依赖与执行结构。

`ci.yml` / `image.yml` 调用 `reliability.yml`，传递 boolean inputs、`secrets: inherit`
和 workflow → job → step outputs。原版 checkout@v4、setup-go@v5（go.mod + cache）、
Codecov@v4、login@v3、metadata@v5、build-push@v6 实际执行；另包括私有模块下载、
vet/build/race、Atlas 校验和 PostgreSQL Testcontainers。镜像使用私有 SDK checkout、
BuildKit secret、命名 context、不可变 digest。Codecov 未配置该私有仓库的上传 token，
`fail_ci_if_error: false` 只验证 Action 可运行，不能视为外部上传成功。

## 与产品的关系

本目录保留 10.5c 的开发用 operator 工具。上游 act 固定 v0.2.89，worker 和公开 Actions 锁定方式沿用
[actkernel](../actkernel/README.md)。10.5c 当时没有切换产品执行内核。10.5b 的正式执行器接入见
[architecture](../../docs/architecture.md) 和 [项目状态](../../docs/status.md)：
由产品自动接收 push、规划单 job、签发受限凭据、执行 act，原生 CD job 接续 GitOps/Flux；
这条正式路径不需要下面的 operator `collect` / `deliver` 桥接。
`homelab.yml` 是现有产品支持的手动 CD 入口；只有本工具确认镜像 workflow 成功、
Pod 终态、临时引擎退出、digest 格式正确，才把固定源码 SHA 和 digest 交给产品 GitOps → Flux。
不能把这个分段链路写成产品已经自动调度任意 Actions，或真实业务仓库已兼容。

## 运行

需要显式授权实际集群和测试仓库操作。`lab.py` 会读取当前 kubeconfig，**与不读取用户
kubeconfig 的 Testcontainers 自动测试不同**。私有 state 目录须位于代码仓库之外：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s test/productionlike -v
python3 test/productionlike/lab.py --state-dir /private/operator-state setup
python3 test/productionlike/lab.py --state-dir /private/operator-state run clean-ci \
  --workflow ci.yml --postgres --cache-scope acceptance
python3 test/productionlike/lab.py --state-dir /private/operator-state collect clean-ci
python3 test/productionlike/lab.py --state-dir /private/operator-state run image \
  --postgres --cache-scope acceptance
python3 test/productionlike/lab.py --state-dir /private/operator-state collect image
python3 test/productionlike/lab.py --state-dir /private/operator-state deliver image
python3 test/productionlike/lab.py --state-dir /private/operator-state cancel another-running-label
```

state 中 `profile-image.json` 是当前工具构建镜像的 buildx metadata；`docker/config.json`
是专用 registry 凭据；`cookies.txt` 是已登录 operator 的 Forgelet session。
父目录的 GitHub App 私钥仅供 operator 换取短时 token，不下发执行 Pod。
源码 token 只读应用仓库，SDK token 只读 SDK 仓库；输出中不显示秘密。
每次从实际 GitHub 固定 SHA 获取干净快照，workflow 内仍实际执行 checkout。

## 隔离与收尾

实验 namespace `forgelet-lab`，每轮一个 Job/临时 Docker native sidecar，`overlay2`，
socket/data/workspace 都属本 Pod，没有 hostPath、宿主 socket 或 Kubernetes token。
这是 **privileged/rootful 实验配置**，不代表生产隔离已完成。引擎随 worker 退出，
12 GiB emptyDir 限制防止实验无限扩张；`vfs` 曾超过此限制，失败记录保留。
执行镜像拆分 apt 层并使用 gzip，以通过当前 registry 入口的单请求大小限制。

唯一持久执行资源是 `local-path` 5 GiB cache PVC（该 provisioner 不提供硬配额保证）。
归档缓存由 worker HTTP 服务提供，每个新 job 的 Go module/build 目录是新的；
同一 `--cache-scope` 可验证真实恢复，不同 scope 可验证冷启动。
临时请求 Secret 包含短时凭据；采集结果后必须显式删除对应 Job 和同名 Secret，
Job TTL 不会自动删除无 ownerReference 的 Secret。取消直接删除对应 Job，然后确认 Pod/引擎消失。
`cancel` 会验证实验归属标签、等待 foreground 删除并确认没有剩余 Pod，再移除同名 Secret。
不要删除业务 namespace、控制面 PVC 或他人的 Docker builder。

实际运行、发布、故障和回滚证据见 [项目状态](../../docs/status.md)。
