# Actions job 内核对比

开发验证工具，不是生产 backend；不改变 forgelet 当前执行器、数据库或安装清单。

候选固定为 `nektos/act v0.2.89` 和 `gitea/act v0.262.0`，均为 MIT。
两者都声明 `github.com/nektos/act` module path，因此使用独立临时构建目录。
`locks/*.mod` / `*.sum` 锁定各自依赖，不向产品 go.mod 加 replace，不向 server 链接任何新依赖。

## 重现

```sh
make test-kernels
```

需要 Docker；用 Testcontainers 创建独立 k3s，从不读取用户 kubeconfig。
准备阶段自动构建 worker 镜像、下载固定 SHA 的公开 Action 归档并拉取基础设施镜像。
`actions.json` 记录实际回归版本。归档通过两套库现有的 ActionCache 接口提供，仍执行原版 Action 代码。

覆盖单 job 选择、外部 needs 状态/outputs 注入、失败、跳过、secret 脱敏、日志并发和分片脱敏；
真实 Pod 覆盖 Node/Composite/post、取消、workflow_call/typed inputs/inherit/outputs、失败传播、
缓存冷→热命中、Docker Action、login/metadata/build-push → 临时 Registry → 拉取和运行。
构建用 BuildKit secret 和命名 context。metadata 的 GitHub HTTP 调用使用验证 token 的只读 fixture，
镜像只推送该 Pod 自己的 Registry。不能将此 fixture 写成真实 GitHub / steam-tasks 全链路成功。

完整 workflow 失败场景记录 stock runner 是否执行后续 always job；这是一项能力观测，
通过只代表失败被观测、普通下游未运行，不代表 stock workflow 调度语义完整。
Gitea 的 `build-push_github_api_gap` 是明确的负向探针：要求复现它强制写入 `/api/v1` 的地址差异，
不是要求构建成功。实际 metadata-action 已在该地址失败；加入前置协议断言让回归稳定且不访问错误的外部地址。
只有上游 act 的 build-push 场景要求 push/pull/run 成功。不要将 16 个探针完成写成两套库全功能通过。

Docker job 才添加 native sidecar：Docker 28.5.1 dind、Pod 独享的 socket/cache/workspace/data emptyDir。
不挂宿主机 Docker socket、hostPath 或 Kubernetes 凭证。sidecar 随主容器结束退出，测试检查终态。
这是 **privileged/rootful PoC**，不是 rootless 或生产隔离已经验收；普通 job 没有 Docker daemon。

`TestDockerBackendRace` 另用 Testcontainers 创建独享 Docker，引擎 TCP 端口仅发布到
本机 loopback，真实 Docker Exec 路径运行 race-instrumented worker，验证步骤和输出。
它通过不表示上面已复现的 stock host PTY race 修复；两条执行路径分别记录。
实际 homelab 的生产形态测试仓库、Node20 profile、PostgreSQL 和真实 registry/Flux 实验见
[productionlike](../productionlike/README.md)，不在普通测试命令中读取实际集群凭据。

可选调试环境变量：

- `FORGELET_KERNEL_KEEP_IMAGE=1`：保留公开 fixture 镜像，便于调试，容器仍清理。
- `FORGELET_KERNEL_IMAGE`：复用自己基于当前 worker 构建的镜像，不能用旧镜像冒充最新验证。
- `GOPROXY`：仅影响开发工具构建依赖；默认 Go 校验策略不变。

原版 host runtime 在两套候选中都存在 PTY AutoStop 数据竞争。`TestCandidateBehavior`
运行完整非 race 行为测试，并单独对本工具的输入/日志边界运行 race；不把它写成 stock host 全量 race 通过。
复现完整 stock race 检查：

```sh
go run ./test/actkernel/build -candidate upstream -dir /tmp/forgelet-probe-upstream -test
go run ./test/actkernel/build -candidate gitea -dir /tmp/forgelet-probe-gitea -test
```

`-test -race=false` 仅用于区分行为结果与上述已记录上游竞争，不能代替产品的 `make verify`。

## 私有 steam-tasks 原始 CI

显式开启，不在普通 `make test-kernels` 中读取本机账号或私有仓库。
凭据 JSON 必须 mode 0600，包含 `GITHUB_TOKEN`；不要将值放进 shell 参数、日志或提交。

```sh
FORGELET_KERNEL_STEAM_REPO=/path/to/steam-tasks \
FORGELET_KERNEL_SECRETS=/private/path/secrets.json \
FORGELET_KERNEL_EVIDENCE=/private/path/evidence \
go test -tags integration ./test/actkernel -run '^TestSteamTasksOriginal$' -v -count=1 -timeout 50m
```

只使用提交 `8265ad9772c5c92d3724a0372f17c31a4a8e498d` 的 git archive，
校验三份 workflow 与记录的原文件完全一致，不读取未提交/未跟踪源码。
执行原始 `ci.yml` → `reliability.yml`，使用 Linux amd64 环境，匹配原 workflow 的 Atlas 二进制架构。
可用 `FORGELET_KERNEL_BUSINESS_IMAGE` 指定兼容的 Debian/Node amd64 基础镜像；
当前默认是已有开发镜像 `registry.shitamchi.com/forgelet/executor:20261025-4f626da`。
此测试需要网络和真正的私有模块读取权限；不创建 GitHub Check，不推送生产镜像、不部署集群。

两套库利用已经准备好的固定 checkout 快速路径；不是声称原版 checkout Action 全部输入已支持。
测试凭据按本次显式开发配置提供，不代表产品的最小权限 job token 已接入。

当前原 CI 在业务依赖处失败：`steam-proto-go v0.6.0` 不包含源码引用的 `priceregion` 包。
原 image workflow 另缺 Dockerfile 要求的 `steam_proto_go` 命名 context。
这些不能通过换执行库解决，也不能通过删除检查或暗中改用本机 SDK 声称原 workflow 成功。

最终选型、测试数量与生产接入边界见 `docs/architecture.md`、`docs/status.md`、`docs/plan.md`。
