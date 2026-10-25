# GitHub Actions 兼容范围

更新：2026-10-23。阶段 3 后实现以下子集，测试结果见 [status.md](status.md)。
forgelet 独立接收事件、调度和执行；兼容 workflow 的常用行为，不提供完整 GitHub Actions 云服务。

## Workflow 与触发

| 能力 | 当前支持 |
|---|---|
| 文件 | 默认固定事件 SHA 读取 `.github/workflows/*.yml/.yaml`；可在管理页面按仓库登记/启停路径，或显式保存控制面 YAML；执行计划持久化，源码模式不扩大 Actions 兼容能力 |
| push | 分支/tag；branches、branches-ignore、tags、tags-ignore、paths、paths-ignore |
| pull_request | 仅同仓库可信 PR，使用 payload 中完整 merge commit SHA；默认 opened/synchronize/reopened，可显式添加 ready_for_review |
| workflow_dispatch | forgelet 管理 API 提交源码与 inputs；string、boolean、number、choice、default、required；不是 GitHub 的 Run workflow 按钮 |
| job | name、runs-on、needs、if、outputs、env、steps、timeout-minutes、strategy、continue-on-error、concurrency |
| shell step | id/name/if/continue-on-error/run/env/shell/working-directory/timeout-minutes；真实 bash（默认）或 sh |
| 共享状态 | 同 job 内 shell、JS、Composite 共享 workspace、GITHUB_ENV、GITHUB_PATH；输出使用 GITHUB_OUTPUT；shell export 不跨 step |
| 配额 | 最多 64 个逻辑 job、展开后合计 256 个 job，每 job 最多 256 steps；默认 job 超时 20 分钟，可设整数 1–1440 分钟 |

Filters 支持字面量、`*`、`**`、`?` 和按顺序覆盖的 `!` 排除；不支持字符组、`+`、转义语法。
同类 include/ignore 不能同时声明；每列表 1–100 项，每 pattern 最多 256 bytes，至少一个正向 pattern。
branch 与 path filters 同时存在时均须匹配；tag push 不检查 paths。PR 分支过滤使用 base branch。
路径使用固定 before/base 与 head SHA 的 GitHub compare 结果，重命名同时检查新旧路径。
列表达到 API 的 300 文件上限、缺少有效 base（例如新分支）或无法比较 commits 时给出事件诊断，不用残缺列表触发。
临时 API 故障仍重试；管理 API 的 `changed_files` 可显式提供完整文件列表。

Fork PR 忽略且不持久化执行事件；没有 merge SHA 的同仓库 PR 明确拒绝，不回退到 head SHA。
当前支持的 PR activity types 之外不执行。手动 inputs 最多 25 项，字符串最多 8 KiB，类型不隐式转换。

## Matrix、依赖与并发

- 静态标量轴的笛卡尔积、include/exclude；轴值可为 string/number/boolean。动态或对象轴 matrix 不支持。
  job `if` 的上下文不包含 matrix；同一条件应用于该逻辑 job 的全部变体。
- `strategy.max-parallel` 为 1–256；`fail-fast` 默认 true。普通失败、超时、interrupted 会取消同组其他变体，
  job continue-on-error 接受的普通失败不触发 fail-fast。活跃任务确认停止后才释放全局额度。
- 全局 `max_parallel` 默认 4；starting/running 计数，等待 needs 或并发组不占额度。
- workflow/job `concurrency` 支持字符串或 group/cancel-in-progress 对象；按仓库隔离，group 忽略大小写，最多 256 bytes。
  同组保留一个活跃执行和最新 pending；新 pending 替换旧 pending，cancel-in-progress 还会取消活跃执行。
  workflow 和 job 的 group 分别管理；job 组在依赖完成且 if 满足后参与竞争。组状态由 SQLite 恢复。
- `needs` 接受 ID 或 ID 数组，校验未知、自身、重复、循环依赖；等待直接依赖的全部 matrix 变体终态。
  聚合结果传播失败/取消/跳过，outputs 合并；同名 output **按 matrix 展开索引较大的值覆盖**，不依赖完成顺序。
  需要不同变体的所有输出时使用不同 output 名；此覆盖顺序是 forgelet 的明确规则。
- 普通失败后默认 steps 和依赖 job 跳过；failure/always 分支仍可执行，清理成功不抹掉失败。
  跳过 job 不创建 Pod，全部跳过的 run 保持 skipped。重跑生成新 ID，并清空旧 needs 结果快照；重新检查仓库是否启用、当前 profile 是否存在及 BuildKit 能力，不绕过停用配置。
- step/job continue-on-error 支持布尔或返回布尔的表达式，仅忽略普通非零退出。step outcome 保留原始结果，
  conclusion 为容错后的结果；job API 的 `state` 为结论、`result.state` 保留原始失败。超时和基础设施错误不能被吞掉。
- 单 step 超时仍可执行后续 failure/always 清理，但 job 保持 timed_out。整个 job 超时或显式取消停止新代码，
  不再启动该 job 的清理 step 或 Action post；取消整个 run 也取消排队 job。这与 GitHub 的取消流程有差异。
- job outputs 在 steps 结束后求值，支持显式 steps 和直接 needs；不支持 env。每组最多 100 项/64 KiB，
  全部 step outputs 合计限 64 KiB；含注册 mask 的值不发布到历史或下游。

单 Plan 序列化限制为 900 KiB，创建时所有 Plan 合计最多 16 MiB；调度合入 needs 输出后再次检查单 Plan 限制，超限在创建 Pod 前失败并保留诊断，COE 不会忽略。

执行器默认容忍临时控制面中断 5 分钟，可由管理员 profile.control_plane_timeout 调整，最大 1 小时；不会因中断自动重新执行 job。这个窗口不替代 workflow timeout，也不延长结束后的 20 秒最终上传期限。Pod 终止宽限为 45 秒，原 Pod 可在终止中上传最后日志/结果；超出收尾期限仍可能 interrupted。控制面的永久运行时拒绝或连续 12 次临时失败会终止 attempt，不能被 continue-on-error 吞掉。

## 表达式与求值时间

支持 `${{ }}` 通用插值；if 也可写裸表达式。支持布尔、null、数字、单引号字符串、括号、`!`、
`&&`、`||`、`== != < <= > >=`，逻辑短路并保留操作数值，字符串比较忽略大小写。

函数：`success()`、`failure()`、`always()`、`cancelled()`、`contains()`、`startsWith()`、`endsWith()`、
`format()`、`join()`、`toJSON()`、`fromJSON()`。if 未写状态函数时隐含 success 门禁；普通插值没有该门禁。
failure 包含传递祖先失败，timed_out/interrupted 在条件上下文映射为 failure。

| 求值阶段 | 上下文与边界 |
|---|---|
| 接收时固定 | inputs 类型/default、vars 快照；runs-on/name、job continue-on-error、workflow/job concurrency；后者可用 github/inputs/vars，job 再加 matrix |
| 调度时 | job if 在 needs 全部完成后评估；github/inputs/vars/直接 needs，无 matrix/env/secrets |
| 执行时 | workflow/job env、step if/env/run/with/工作目录、step continue-on-error；按位置增加 matrix/strategy/env/先前 steps/直接 needs |
| Action 内 | inputs 是当前 Action 的输入；Composite steps 有独立 steps 上下文，可用 github.action_path；STATE 仅提供给对应 JS 生命周期 |

可用 github 属性：sha、ref、ref_name、repository、repository_owner、event_name、actor、head_ref、base_ref、
workflow、job、run_id、server_url、api_url。run_id 是 forgelet ID；地址为 github.com/api.github.com。
可用 `matrix.KEY`、`inputs.KEY`、`vars.NAME`、`env.NAME`、`needs.ID.result/outputs.NAME`、
`steps.ID.outputs.NAME/outcome/conclusion`、`strategy.job-index/job-total/max-parallel/fail-fast`、
执行期 `runner.os/arch/temp/tool_cache`。matrix 和 inputs 整体对象可用 toJSON。

同层 env 不互相引用；step 自己的 env 在 if 前合并，覆盖不泄漏到下一步；GITHUB_ENV 才会传递。
secret 只允许执行字段引用，不进入调度条件；通过 workflow env 或 with 传给自定义 Action。
表达式上限 8 KiB、1024 token、深度 64；函数结果/输出有大小限制，单次插值最多 1 MiB。
不支持数组/对象索引、对象过滤、任意 github.event、hashFiles、动态 matrix、完整 GitHub 上下文。
未知语法/引用在相应编译阶段诊断，包含不会执行的短路分支；未知 Action 输入在加载 metadata 后诊断。

## 真实 Actions

| Action | 固定版本与已验证输入/行为 |
|---|---|
| actions/checkout | 受限 builtin `@v4`；当前仓库固定事件 SHA，path、fetch-depth（默认 1，0 为完整历史）；不下载原 Action |
| actions/setup-go | `v6.5.0` → `924ae3a1cded613372ab5595356fb5720e22ba16`；go-version、cache: false、token: ''；真实安装 Go 1.25.0、PATH、go-version output、执行 Go 程序与 post |
| actions/setup-node | `v6.5.0` → `249970729cb0ef3589644e2896645e5dc5ba9c38`；node-version、package-manager-cache: false、cache 为空、token: ''；真实安装 Node 24.14.0、PATH、node-version output、执行 JS 程序与 post |
| 自定义本地/公开 Action | `./path` 或 `owner/repo[/path]@完整40位SHA`；action.yml/action.yaml、node20/node24/composite；按实际 metadata 支持输入，仓库 fixture 验证嵌套、状态、输出、失败和清理 |

两个 setup Action 可写表中的完整 SHA 或精确 `@v6.5.0` 别名；别名在代码内解析为固定 SHA。
其他 tag/branch 不接受；两个 setup Action 的其他 SHA、输入（例如版本文件、registry 配置）、非空 token 和缓存能力尚未支持。
临时 k3s 验证了表中工具版本的 Linux arm64 链路；真实 homelab 另外验证了 Linux amd64 的 setup-go/Go 1.25.0。
setup-node 安装的 Node 工具链尚未单独验证 amd64；其他工具版本/架构仍需对应行为测试，不能据此承诺全部支持。
公开源码和工具链下载不自动使用 clone token，受外部服务连通性/限流影响。

执行器默认是 Debian/glibc，带独立 Node 20.20.0 与 24.14.0 runtime、git/bash/CA/tar/xz/unzip。
Action 使用真实 Node 子进程；setup-node 修改用户 PATH 不会替换 Action 的 Node runtime。
Node 仅在按需 executor 镜像中，常驻 server 仍是 Go + Alpine。`runs-on` 匹配管理员 profile，不提供 GitHub 托管镜像全集。

- JS 支持 pre/main/post、pre-if/post-if、INPUT_*、GITHUB_STATE、STATE_*、GITHUB_OUTPUT、ENV/PATH 与 mask。
  pre 在该 Action 调用前执行；不是预先执行整个 job 的所有 pre。post 在 job 结束时按注册逆序执行，普通失败也执行默认 always post。
- Composite 使用相同主容器/workspace，可调用 shell、JS、其他 Composite，嵌套深度最多 8、总调用最多 256；inputs/steps/outputs 隔离。
- 下载同一 repo/SHA 在一个 job 内复用；压缩包最多 32 MiB、展开最多 128 MiB/10,000 项。
  拒绝路径逃逸、符号/硬链接及特殊文件，脚本限制在 Action 目录；metadata 最多 256 KiB。
- GITHUB_STEP_SUMMARY 文件可写，但尚不发布摘要；problem matcher、annotation 等只保留日志，不提供 GitHub UI 等价展示。
- runner.tool_cache 和下载内容在 job 内临时保存，不是跨 job cache。私有远程 Actions、Docker Actions、reusable workflows 不支持。
- checkout 不支持其他版本、ref/repository/token/submodules/lfs/sparse-checkout 等输入，也不重复 checkout 同目录。
  默认和 `path` 子目录均已验证固定 SHA；执行器创建自己所有的 workspace，避免 emptyDir 挂载根的 Git 所有权错误。
  工作流使用 `GITHUB_WORKSPACE` 引用根目录，不应硬编码 `/workspace`。

## Artifact、cache 与 CD builtins

以下精确 `uses` 是 forgelet 原生适配器，使用自己的 PVC/API，不下载对应 GitHub Action，也不连接 GitHub 的 artifact/cache 服务。
未知输入直接诊断；没有完整原版 Action 的兼容承诺。

| uses | 输入 | 输出 |
|---|---|---|
| `actions/upload-artifact@v4` | path 必填；name 默认 artifact；retention-days 默认 7，1–90；if-no-files-found 为 warn/error/ignore；include-hidden-files 默认 false | artifact-id、artifact-digest（tar 的 SHA256） |
| `actions/download-artifact@v4` | name 默认 artifact；path 默认 `.` | download-path |
| `actions/cache@v4` | path/key 必填；restore-keys 多行前缀；fail-on-cache-miss、lookup-only 默认 false | cache-hit、cache-primary-key、cache-matched-key |
| `actions/cache/restore@v4` | 同 cache，但不注册结束时保存 | 同 cache |
| `actions/cache/save@v4` | path/key 必填 | 无 |
| `forgelet/build@v1` | image 必填；context 默认 `.`；file 默认 workspace 下 Dockerfile；target 可选；build-args 为多行 NAME=value | digest、image（repository@digest） |
| `forgelet/deploy@v1` | target、digest 必填 | deployment-id、state（queued） |

artifact/cache 路径仅支持 workspace 内相对路径及多行列表，不支持 glob、`!`、`~`、绝对路径、链接、特殊文件；最多 100 路径、10,000 文件、256 MiB 归档。
上传单目录时移除该目录前缀，多路径取共同父路径；download 仅限当前 run 的命名 artifact，不支持跨 run/repo、pattern、artifact IDs 或 merge-multiple。
tar 是 forgelet 格式，不是 GitHub ZIP；name 在一个 run 内不可覆盖。同 job 重试内容必须一致，不同 job 上传同名失败。
下载先校验 digest 和所有归档条目，再逐文件原子写入目标目录；已有无关文件保留。

cache 作用域为仓库 + **当前完整 ref** + OS/架构/路径版本，不回退默认分支，也不提供跨 OS 缓存。
先精确 key，再按 restore-keys 顺序选择最新前缀匹配；cache-hit 精确命中为 `true`，前缀为 `false`，未命中为空。
缓存默认保留 7 天，按创建时间到期，不是 LRU；同 key 首次写入胜出。cache@v4 仅在整个 job（含 JS post）成功后保存，失败/取消不保存。
lookup-only 不下载且不保存；cache/save 空路径匹配失败。两个 setup Action 的内建缓存仍关闭；请显式使用这里的 builtin，
不支持任意 JS Action 调 GitHub cache SDK。服务端配额默认 256 MiB/对象、2 GiB 总量、4096 对象，管理员可缩小。

build 只支持 job **顶层 steps** 显式引用，不自动扫描 Composite 来启用 builder；profile 必须启用 build 并列出精确 image repository。
当前使用 BuildKit v0.20.2 rootless 原生 sidecar，只构建所在节点架构，不支持多平台、Docker Actions、build-push-action、SSH/secret mounts 或持久化层缓存。
image 不能带 tag/digest；推送临时 `forgelet-<jobID>` tag，后续部署必须消费 exporter 返回的 sha256 digest。
Registry 凭证由管理员 Secret 挂载，不进入持久化计划；预注册 auth/password/token 日志脱敏，但可信 step 同容器可读取该 Secret。
build-args 会进入构建参数，不能用来传秘密。builder 所需的 user namespace/安全配置见设计与部署文档。

deploy 只接受管理员登记的 source repository/ref，事件限 push 或 forgelet workflow_dispatch；PR 不能提交部署。
step 成功表示持久化意图，整个 run 成功后才写 Git；不在 step 内等待部署，避免占用执行 Pod。
仅修改登记 Kustomization YAML 的一个 images 条目，direct 使用非强推更新，pr 等实际 merge SHA。
查询 `/api/deployments/{id}` 区分 queued/preparing/awaiting_merge/delivered/deployed/superseded/conflict/cancelled；
只有 Flux 来源、实际 Git revision 和应用 Deployment 的 digest/健康都匹配才标 deployed。
回滚为新投递和新 revert commit，仅恢复前一个有效 digest，保留其他变更；没有前一个 digest 或当前镜像已被替换则拒绝。

## Secrets 与 vars

由 forgelet 管理，无法读取 GitHub 已保存的 Actions secrets。API 仅管理员可写、只允许已登记仓库。
Secrets 使用 AES-256-GCM，随机 nonce，认证数据绑定仓库和名称；32 bytes 原始主密钥单独挂载给 server。
计划只存 secret 引用；executor 通过绑定 Pod/任务的短时身份领取该任务引用的值，执行前注册 mask。
含 secret 的输出不发布。Masking 不能阻止可信代码主动外传或把值变换后输出。

每仓库最多 100 个 secrets 和 100 个 vars；secret 最多 4 KiB、var 最多 16 KiB，名称区分大小写，最多 64 字符。
名称不允许 GITHUB_/FORGELET_ 前缀。Secret 列表只返回名称；vars 明文保存并在创建 run 时快照。
重跑保留 vars 快照，secrets 在执行时读取最新值；缺失引用为空字符串。主密钥备份/恢复见部署文档，尚无在线轮换流程。
不提供隐式 GITHUB_TOKEN、permissions 或 OIDC；clone token 只用于 checkout。

## 可运行样例与后续

```yaml
name: Go CI
on:
  push:
    branches: [main]
concurrency:
  group: '${{ github.workflow }}-${{ github.ref }}'
  cancel-in-progress: true
jobs:
  test:
    runs-on: homelab
    strategy:
      max-parallel: 2
      matrix:
        go: ['1.25.0']
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16
        with:
          go-version: '${{ matrix.go }}'
          cache: false
          token: ''
      - run: go test ./...
```

样例中的工具版本需满足项目自己的 go.mod。真实工具链测试见 [toolchains.yml](../test/fixtures/toolchains.yml)，
Node/Composite 见 [actions.yml](../test/fixtures/actions.yml)，DAG 与失败传播见 [conditions.yml](../test/fixtures/conditions.yml)。

CI 到 CD 示例见 [cicd.yml](../test/fixtures/cicd.yml)；替换仓库、image、target 和项目测试命令后使用。
schedule、services、job.container 后续按需求安排；真实 homelab 接入状态见 status。
接管前关闭对应 GitHub 原生 workflow 或设置 forgelet allowlist，避免双跑；forgelet 不自动修改 GitHub 设置。
本仓库 `.github/workflows/ci.yml` 仍由 GitHub 原生 Actions 执行，不作为 forgelet 自举兼容声明。
