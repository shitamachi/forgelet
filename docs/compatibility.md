# GitHub Actions 兼容范围

更新：2026-10-29。包含阶段 10 当前实现，以下为实际实现的子集，测试结果见 [status.md](status.md)。
forgelet 独立接收事件、调度和执行；兼容 workflow 的常用行为，不提供完整 GitHub Actions 云服务。

专用生产形态测试仓库的 act/Docker 实验单独记录在 [test/productionlike](../test/productionlike/README.md)。
该历史实验执行官方 Actions，并使用真实 GitHub、私有 SDK、PostgreSQL 和 registry。
正式 act executor 已在 10.5b 接入；实验、正式临时 k3s 验证和 homelab 原始业务 workflow 验收分别记录，不能互相替代。

## Workflow 与触发

| 能力 | 当前支持 |
|---|---|
| 文件 | 默认固定事件 SHA 读取 `.github/workflows/*.yml/.yaml`；可在管理页面按仓库登记/启停路径，或显式保存控制面 YAML；执行计划持久化，源码模式不扩大 Actions 兼容能力 |
| push | 分支/tag；branches、branches-ignore、tags、tags-ignore、paths、paths-ignore |
| pull_request | 仅同仓库可信 PR，使用 payload 中完整 merge commit SHA；默认 opened/synchronize/reopened，可显式添加 ready_for_review |
| workflow_dispatch | forgelet 管理 API 提交源码与 inputs；string、boolean、number、choice、default、required；不是 GitHub 的 Run workflow 按钮 |
| job | name、runs-on、needs、if、outputs、env、steps、timeout-minutes、strategy、continue-on-error、concurrency；同仓库 reusable uses/with/secrets 见下 |
| 标准前端 | actionlint v1.7.12 + YAML v4.0.0-rc.3；标准语法通过后仍检查具体执行能力；root/job defaults.run 支持 sh/bash 和 workspace 内 working-directory；run-name 明确未实现 |
| permissions | contents: read/none，调用不能提权；none 禁止 checkout。act 的显式 packages:write 另需管理员 Secret 引用绑定，见下文；其他权限诊断 |
| shell step | id/name/if/continue-on-error/run/env/shell/working-directory/timeout-minutes；真实 bash（默认）或 sh |
| 共享状态 | 同 job 内 shell、JS、Composite 共享 workspace、GITHUB_ENV、GITHUB_PATH；输出使用 GITHUB_OUTPUT；shell export 不跨 step |
| 配额 | 最多 64 个逻辑 job、展开后合计 256 个 job，每 job 最多 256 steps；省略 job timeout 时使用 GitHub 默认 360 分钟，显式整数 1–1440 分钟仍保留 |

act profile在workflow预算之前单独提供最多15分钟的管理员runner镜像及镜像目录准备时间。原timeout-minutes覆盖之后的worker及全部workflow步骤，准备和执行均可取消；native计时不变。这是Forgelet的环境准备策略，不承诺与GitHub托管runner计时完全一致。

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
  全部 step outputs 合计限 64 KiB；含注册 mask 的值不发布到历史或下游。可复用 workflow 引用未发布的 job output 时为空值。

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

可用 github 属性：sha、ref、ref_name、ref_type、repository、repository_owner、event_name、actor、head_ref、base_ref、
workflow、job、run_id、server_url、api_url。run_id 是 forgelet ID；地址为 github.com/api.github.com。
可用 `matrix.KEY`、`inputs.KEY`、`vars.NAME`、`env.NAME`、`needs.ID.result/outputs.NAME`、
`steps.ID.outputs.NAME/outcome/conclusion`、`strategy.job-index/job-total/max-parallel/fail-fast`、
执行期 `runner.os/arch/temp/tool_cache`。matrix 和 inputs 整体对象可用 toJSON。

同层 env 不互相引用；step 自己的 env 在 if 前合并，覆盖不泄漏到下一步；GITHUB_ENV 才会传递。
secret 只允许执行字段引用，不进入调度条件；通过 workflow env 或 with 传给自定义 Action。
表达式上限 8 KiB、1024 token、深度 64；函数结果/输出有大小限制，单次插值最多 1 MiB。
支持字符串属性索引、整数数组索引、对象过滤和原始 github.event payload；needs/steps 仍只含已允许的直接依赖/先前步骤。toJSON 使用格式化 JSON。控制面/原生表达式适配器的 hashFiles 当前明确缺少 workspace 能力，不会扫描控制面文件系统；act steps 使用上游 workspace 求值器，不能据此推导控制面规划也支持 hashFiles。动态 matrix、对象/数组之间的直接比较和完整 GitHub 云上下文尚未实现。
未知语法/引用在相应编译阶段诊断，包含不会执行的短路分支；未知 Action 输入在加载 metadata 后诊断。

## 可复用 Workflow（阶段 10 第一批）

- 支持 job `uses: ./.github/workflows/file.yml` / `.yaml`，从调用者同一次固定 SHA 文件快照解析；未启用为顶层入口的辅助文件也能被调用。远程 ref 当前明确诊断。
- callee 必须声明 workflow_call；支持 string/boolean/number inputs、default、required、字符串模板和 needs 完成后的整个表达式求值，不把字符串 `"false"` 转为 boolean；needs/inputs 依赖由 AST 判断，括号访问、整对象与动态索引同样受分阶段求值约束。普通触发事件、caller github 上下文保持不变；caller env 不自动传入 callee。
- secrets 未传递时为空作用域；支持 inherit 和显式 `TOKEN: ${{ secrets.SOURCE }}`，嵌套继承不能越过父作用域。只保存引用；required 在 admission/rerun 检查配置名称，值按执行时读取。不能用明文/任意表达式作为持久化 secret 参数。
- workflow_call.outputs.value 使用 `jobs.ID.outputs.NAME`，等子图终态后发布到 caller 的 `needs.CALL.outputs.NAME`。失败/跳过/取消持久化传播，cleanup 成功不覆盖失败；跳过调用不启动内部 always。
- gate/result 是无 Pod 的持久化控制节点，API 以 plan.control 区分；实际 child job 独立执行，沿用静态 matrix、全局并发和输出聚合规则。整个展开图最多 256 节点，嵌套最多 8 层，检测循环。
- 调用 job 的 matrix/concurrency、callee workflow concurrency、依赖运行期输入的 runs-on/name/早期 concurrency 等尚未实现，明确拒绝；运行期 env/run/if/with 中的输入按 gate 结果执行。动态 matrix 仍在后续。
- GitHub 入口自动加载 bundle；管理员源码 API 可提供 workflow_files 路径→源码 map（100 文件/4 MiB，逐文件 1 MiB），不是从 executor 工作目录动态读取。持久化计划固定，重启不重新读取可变 ref；rerun 保留原始触发参数。

## 真实 Actions

阶段 10.5a 的独立工具对比发现 Gitea GitHub API 地址差异、共同 stock host race 与完整 workflow always 缺口。
10.5b 已正式接入上游 act 的单 job Docker Exec worker，10.5d 已部署；正式能力见下文 act profile，
本节表格保留 native profile 的固定适配范围，不能将 native 限制应用到 act profile。
steam-tasks 原 ci → reliability 在两套候选均因 SDK v0.6.0 缺少 priceregion 包失败，未完成业务 CI；
2026-10-26 只读核对当前默认分支仍为相同提交，不能把旧失败推断为已修复。
完整记录见 [architecture.md](architecture.md) 与 [status.md](status.md)。

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
- GITHUB_STEP_SUMMARY 文件可写，但尚不发布摘要；problem matcher仍只保留日志。阶段11.1已发布结构化步骤进度和标准notice/warning/error注解：先脱敏后按文本展示，每attempt最多64条注解；最终result/日志ACK仍决定终态，不承诺GitHub UI等价能力。
- runner.tool_cache 和下载内容在 job 内临时保存，不是跨 job cache。私有远程 Actions、Docker Actions、远程 reusable workflows 不支持；同仓库 reusable 支持范围见下。
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

cache 写入作用域为仓库 + **当前完整 ref** + OS/架构/路径版本。读取按当前ref→同仓库PR目标分支（仅已验证PR事件的BaseRef）→仓库默认分支回退；默认分支来自控制面已保存的GitHub目录，目录缺失时不猜测main/master。native和act v1/v2共用相同服务端规则，不提供跨OS缓存。
每个ref内先精确key，再按restore-keys顺序选择最新前缀匹配；当前ref的前缀命中优先于后备分支的精确命中。只有未命中才回退，损坏或不可读对象仍报错。cache-hit精确key命中为 `true`（包含后备分支），前缀为 `false`，未命中为空。
默认分支不能读取子/兄弟分支或PR的缓存；查询参数不能指定读写分支/仓库，写入始终限定当前ref。仓库、版本和原配额/不可变对象规则保持。
缓存默认保留 7 天，按创建时间到期，不是 LRU；同 key 首次写入胜出。cache@v4 仅在整个 job（含 JS post）成功后保存，失败/取消不保存。
lookup-only 不下载且不保存；cache/save 空路径匹配失败。两个 setup Action 的内建缓存仍关闭；请显式使用这里的 builtin，
不支持任意 JS Action 调 GitHub cache SDK。服务端配额默认 256 MiB/对象、2 GiB 总量、4096 对象，管理员可缩小。

build 只支持 job **顶层 steps** 显式引用，不自动扫描 Composite 来启用 builder；profile 必须启用 build 并列出精确 image repository。
native builder 使用 BuildKit v0.20.2 rootless 原生 sidecar，只构建所在节点架构，不支持多平台、Docker Actions、build-push-action、SSH/secret mounts 或持久化层缓存；这些限制不描述 act job 自有 Docker/BuildKit 的能力。
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
不提供隐式 GITHUB_TOKEN 或 OIDC；permissions 仅实现上述 contents 子集，clone token 只用于 checkout。

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


## 正式 act profile（10.5b）

以下能力仅在管理员显式配置 act profile 后生效；前文 native 限制仍适用于普通 profile。
同一 workflow 可将 CI/build 放在 act profile，CD 放在 native `homelab` profile。

- 固定 nektos/act v0.2.89，只执行单个已规划 job。工作流调用、typed inputs/inherit/outputs 和 needs 仍由产品调度。
- 真实 JS Node20/24、Composite、post、Docker Actions，以及 job 自有 Docker/BuildKit/Testcontainers。
  不使用有已知 PTY race 的 host backend；workspace-local Action 的 `pre` 当前明确失败诊断（上游不执行该 pre），可使用 native profile。
- 10.6b 新 act 运行支持公共 `owner/repo[/path]@tag/branch/完整 SHA`，创建运行时解析并保存固定 commit，
  包含远程 Composite 的远程依赖锁；精确 tag 优先于同名 branch，annotated tag 有界解引用。
  保留原 uses 供诊断，重跑与恢复使用原 SHA；缺失版本/metadata/依赖锁或 Node runtime 不支持时明确失败。
  原生 profile 和无锁旧计划继续使用 `internal/action/source.go` 的固定版本，不把通用引用当原生执行兼容。
- 可选 supervisor 镜像可以预置固定 SHA 的公共 Action 归档（`tools/actions`），运行前验证归档 SHA256、GitHub commit header 和提取边界；命中后不下载源码，损坏不回退，缺少的新 SHA 保留原下载路径。
  本批七个实际公共 Action 已验证；不改原 Action JS 或 workflow 输入，不代表 Action 内部工具、源仓库 checkout 和外部服务离线可用。
- setup-go 的下载、toolcache、真实 Node、Go 与 cache 行为来自原 Action；不再在 YAML parser 中禁用标准输入。
  管理员负责配置 runner 工具；不承诺等同 GitHub ubuntu-latest 镜像。
  内层 root job 共享 supervisor 创建的 workspace；镜像仅信任固定 `/workspace/repository` Git 路径，
  保留正常 Go VCS stamping，不使用通配 safe.directory 或 `-buildvcs=false` 绕过校验。
- cache v1/v2 facade 对接现有 PVC blob 服务，包括 setup-go 冷/热缓存与独立 BuildKit type=gha（10.6c）。缓存不是共享 Docker data，job 结束后引擎清理。
- contents:read 时注入源仓库只读短期 GITHUB_TOKEN；管理员可显式配置依赖仓库 token secret 引用。
  不自动扩大 GitHub 权限，静态 registry secret 仍使用加密设置服务。
  `permissions: {}` 与默认权限在持久化后仍保持区别，空权限不会自动获得源仓库 token。
- 原生 `forgelet/*`、upload/download-artifact 适配器留在 native job；act profile admission 给出诊断。
  尚未实现 GitHub artifact v4 远端协议。job timeout、取消、日志脱敏/ACK/游标和重启恢复保留；
  act 的逐 step timeout 尚未可靠接入，因此 admission 明确拒绝，不能静默忽略。
- 远程 reusable、动态 matrix 等仍按上文边界诊断；原先生产形态专用仓库之外，本批已验证首批两个业务仓库的固定提交；不能据此称所有业务 workflow 全面兼容。

10.6b 已在临时 k3s 验证 steam-platform-server 的原 check.yml（Go/race）和 steam-admin-web 的原 ci.yml / image.yml。
Go/npm 冷热缓存、28 个原始主步骤和实际镜像发布 digest 核对通过；固定提交、运行 ID 与隔离范围见 [status.md](status.md)。
以上是 10.6b 的 3 份原始 workflow 执行记录；10.6c 的独立 BuildKit 验证另见下节和 status，未由此新增 artifact v4 或业务 Flux CD。

## 独立 BuildKit 与缓存（10.6c）

- job-local facade 新增 CacheService v2 的 create/get-download/finalize、Azure 风格 block/single PUT 与 range/HEAD，兼容旧 v1。默认开启 ACTIONS_CACHE_SERVICE_V2，持久作用域仍是当前仓库/ref/version，不回退其他分支。
- 独立 docker-container BuildKit 使用可达的 Pod IP 端点；缓存能力为每 attempt 独立 JWT/随机路径，job 结束失效，不等于 GitHub OAuth/App token。512 MiB 保留 payload、256 MiB/对象、最多 256 blocks/对象及上传数量有界，错误不伪装命中。
- 顶层 setup-buildx 的默认 image 可由现有固定镜像目录提供；默认 TOML 由管理员 act.buildkit_config 提供。显式 workflow image/config 优先。独立 BuildKit 不自动继承 engine 已预加载的所有镜像；构建 FROM 与工具下载仍需真实连通。
- packages:write 仅在管理员登记 package_tokens → 仓库加密 Secret 名后生效，PR/native 拒绝。计划只有引用，领取时检查绑定，包 credential 与源 clone token 分离；YAML 声明不生成权限，也不将 write PAT 宣称为可降权的 packages:read token。
- 支持显式 env GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}；其他 GITHUB_/RUNNER_/FORGELET_ 身份变量、任意 literal token 及 file-command 改写继续拒绝。新增 github.ref_type（branch/tag）并与 native/act 环境一致。
- 本批实际通过的 Action SHA、输入与测试结果以 status 为准；编译样本和实现不自动算完整业务运行。artifact v4、OIDC、全部 GHES、跨分支 cache 和未验证的其他 builder driver 不在此声明内。

## 镜像目录和日志（10.5d）

管理员可在 act profile 配置 `image_mirrors`，将明确的 Docker Hub tag 对应到同 registry 的固定 digest。
任务启动前自动拉取并登记原名，适配默认 Testcontainers PostgreSQL/Ryuk、Docker Action 和 Docker driver
本地构建。源不存在、授权失败或 config ID 不符时失败，不忽略目录、不自动改拉公网。
目录不支持 digest 形式的目标 alias、私有 registry alias 或任意 tag 的自动解析；相同 tag 的 upstream
更新不会自动改变目录锁定的内容。强制拉取/独立构建引擎仍可能访问 upstream，需要另行配置。

日志 API 仍以 seq/step_id/data 返回；data 可包含多行，不保证一行一个记录。
合并不跨 step；最大块 8 KiB、低流量每 200ms 刷新，脱敏先于批量上报。
连续 ACK、取消后尾部排空、重启继续原 attempt 及最终 LastLogSeq 语义保持不变。

## steam-* 业务仓库盘点（2026-10-26，只读检查）

以下为 **10.6a 当时的代码检查快照**；10.6b 已替换公共 Action tag 的白名单拒绝逻辑，
本表中的历史拒绝不代表新解析器仍然拒绝该 tag，也不代表对应 Action 已做完实际执行验证。
原业务 workflow 的最新执行结果与自动验证范围见 status；其他仓库继续按 10.6c–10.6e 推进。

使用 gh 扫描 shitamachi 账号的 15 个 steam-* 仓库；9 个有 Actions，共 13 份 workflow。
各仓库按默认分支当时的完整 SHA 获取源码，再调用本项目当前 `workflow.ValidateFiles`，包含同提交辅助文件。
**6 份规划检查通过，7 份明确拒绝；没有在 Forgelet 运行这些业务 workflow。**
规划检查不覆盖 profile admission、凭据、安装工具、实际构建/测试、外部发布或部署结果。
以下短 SHA 的完整值可从对应 GitHub commit 获取，完整快照与检查 JSON 保存在本机私有证据目录。

| 仓库 / 当前提交 | Workflow | 当前检查和下一步实际需求 |
|---|---|---|
| [steam-platform-server](https://github.com/shitamachi/steam-platform-server/commit/e37944b787fc89cc4e96c327e377dab09a09a940) `e37944b787fc` | check.yml | 规划通过；Go 1.26、vet/short/race/build；当前提交 GitHub CI 成功。原 workflow 没有镜像发布步骤。首批 Go 接入对象。 |
| [steam-admin-web](https://github.com/shitamachi/steam-admin-web/commit/40140fc9e3281467d4199586f2f5f988fad0731f) `40140fc9e328` | ci.yml、image.yml | image 规划通过；CI 拒绝 setup-node@v4。实际要求 Node24、npm ci/check/test/build、生成文件不漂移、自有 registry 镜像发布；当前提交 GitHub 两份运行成功。首批 Node/镜像对象。 |
| [steam-admin](https://github.com/shitamachi/steam-admin/commit/2d6d2f5eb0f7236ff68bec49b3db1aa09a38e07d) `2d6d2f5eb0f7` | ci.yml、image.yml | image 规划通过；CI 拒绝 bufbuild/buf-setup-action@v1。还需要 Buf 1.72.0、Go generator、make check 使用的 rg、生成文件 diff 检查；当前提交 GitHub 两份成功，不能只验证 Action 下载。 |
| [steam-platform-etl](https://github.com/shitamachi/steam-platform-etl/commit/8c93d444e46f43bbfa1444a040e54fbb667397de) `8c93d444e46f` | check.yml | 拒绝 setup-go@v6。当前只允许 workflow_dispatch，执行 make check；原 workflow 不含镜像或数据库集成。较老 GitHub PR 的失败不能当作当前提交结果。 |
| [steam-platform-web](https://github.com/shitamachi/steam-platform-web/commit/75ed69647af3c5aa4a44c416b5b60d1c491def48) `75ed69647af3` | check.yml | 拒绝 pnpm/action-setup@v4、setup-node@v4、setup-buildx@v3。实际要求 pnpm 9.15.9、Node24、真实 Chromium/Playwright、smoke、独立 BuildKit/type=gha 缓存与镜像发布；当前提交 GitHub 成功。 |
| [steam-api](https://github.com/shitamachi/steam-api/commit/1992654114520fd13842a212ddebc7ccc8f8a850) `199265411452` | ci.yml | 拒绝 setup-buildx@v3、build-push@v5；还需要 type=gha 缓存、BuildKit secret、Go 测试及 Codecov。当前 GitHub test job 失败，具体为 TestRedisAdmissionAndParentSnapshotIntegration，build 被跳过；需读取业务测试后区分环境和逻辑错误，不推定为兼容层问题。 |
| [steam-login](https://github.com/shitamachi/steam-login/commit/0a5ce9753cf4b6f429cf273eea000353a9a19106) `0a5ce9753cf4` | docker.yml | 拒绝 setup-buildx@v3、build-push@v5；此外 packages:write 当前不支持，源仓库只读 token 不能充当 GHCR 发布凭据。保留 GHCR + 自有 registry 双发布、type=gha 缓存；Bun 在应用 Dockerfile 内安装，不能误列为 runner 必装工具。当前提交 GitHub 成功。 |
| [steam-cm](https://github.com/shitamachi/steam-cm/commit/62a86e085a4620a76934d2bfd70fdcd123306cbf) `62a86e085a46` | rust-ci.yml | 拒绝 pwsh；checkout 使用完整 SHA，不缺 tag 解析。需要 ubuntu-24.04 profile、Rust 1.97/rustup、rustfmt/clippy、Python3、PowerShell 和 Docker lifecycle/managed 测试；当前提交 GitHub 成功。仅 manual publish=true 时发布 tested image，不自动部署。 |
| [steam-tasks](https://github.com/shitamachi/steam-tasks/commit/8265ad9772c5c92d3724a0372f17c31a4a8e498d) `8265ad9772c5` | ci.yml、image.yml、reliability.yml | 三份规划通过，含同仓库调用/inherit。当前提交 GitHub reliability 也因 SDK v0.6.0 缺 priceregion 失败；image 未向 Dockerfile 提供 steam_proto_go 命名 context。还需要 Atlas、race、显式打开的 PostgreSQL/Testcontainers 验收、BuildKit secret 和镜像发布。不能用编译通过或默认跳过数据库测试冒充全链通过。 |

没有 workflow 的 6 个仓库：steam-platform、steam-lake、steam-proto-go、steam-proto、steam-etl、steam-game-scraper。
暂不自动生成/启用它们的流水线。SDK `steam-proto-go` 当前公开，v0.6.0 的树确实没有 priceregion；
私有依赖 token 是其他真实私有依赖出现时的显式配置需求，不是本次已确认的 SDK 授权故障。

共同需求与兼容边界：

- 本批未发现远程 reusable、动态 matrix、artifact upload/download 或 OIDC 引用；这些不作为下一批首要功能。
- 6 个尚未解析的别名为 setup-node@v4、setup-go@v6、pnpm/action-setup@v4、bufbuild/buf-setup-action@v1、docker/setup-buildx-action@v3、docker/build-push-action@v5。目标是统一 ref→固定 SHA 与真实 Action 执行，不再逐个复制 Action 的业务逻辑；每个版本仍需行为验证。
- 10.6a 盘点时的 job-local cache v1 facade 只验证 setup-go 等缓存，未验证 BuildKit type=gha；10.6c 已新增 v2 并完成独立 BuildKit 冷热/重启/失败/取消验证。独立 BuildKit 的网络、能力 token、分片/对象传输、版本和 cache scopes 均需真实测试。Docker 官方 GitHub cache 服务已转 API v2，不能据此推断我们自建 v1 facade 自动满足同样协议，见 [Docker 缓存文档](https://docs.docker.com/build/ci/github-actions/cache/)。
- GitHub Actions 托管的 GITHUB_TOKEN 与本产品 mint 的只读 token 能力不同。GHCR 外部发布需独立凭据配置和权限边界；不能靠接受 packages:write 字段获得授权，见 [GitHub GHCR 认证](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。
- 当前 13 份 workflow 都没有部署到 Flux 的步骤；发布镜像与 CD 是两项验收。需要部署的仓库应接上现有 native CD，并明确 GitOps 路径、镜像 digest、环境、迁移/健康检查和回滚；不自动改写 steam-cm 等明确只发布的业务语义。

实施顺序与待完成项见 [plan.md](plan.md) 的 10.6b–10.6e；盘点表保留 10.6a 的只读检查结果，后续执行验收记录见下文。

steam-platform-web 的比较状态失败已在 Forgelet 外的同提交 Chromium 慢 CPU 对照复现：
业务控件在 hydration 前可操作，初始化恢复与早期输入存在竞态。源码修正与三项回归位于
steam-build 的显式 hydration.patch；原始 YAML 不变。源码有补丁时分别记录 base/fixture SHA，
不能宣称原 GitHub 提交全部字节未变；最终冷热缓存、CI 和镜像发布结果见 status。

2026-10-27 最终验收：保留 platform-web 原 check.yml/Dockerfile/E2E 字节，对源码显式应用 hydration 修复，
在提供官方 Node24/Chromium 的 act profile 完成冷/热两轮原 CI + needs:image；pnpm false→true、
新 builder 依赖 RUN 层 CACHED、缓存 export 与两轮 latest/SHA 标签 digest 对照均通过。
实际缓存客户端超过 128 个 1 MiB blocks 的行为已有默认 129 MiB Testcontainers 回归；
重复精确 key/version 复用本 attempt 的不可变缓存文件，原字节预算与仓库/ref/version 隔离保持不变。

### 2026-10-27：platform-web 真实 GitHub push

已部署正式 Web act profile，从 GitHub 读取已提交的 aff95ca / c6ac5b5，
运行原始 check.yml、Dockerfile 与测试；两次 14 个主步骤及 GitHub App Checks 全部成功。
[冷轮](https://forgelet.k.mygo.rocks/runs/47c54f11751abcb1ff782a7d) pnpm cache-hit=false；
[热轮](https://forgelet.k.mygo.rocks/runs/c5313da9aff6f2592720df87) cache-hit=true，并在新 Pod/独立 builder 中实际命中依赖 RUN 层。
两轮分别核对 Action digest、latest 与完整源码 SHA 标签，结果见 status。
Node24.14.0 toolcache / Playwright1.63.0 Chromium 是显式镜像能力；应用缓存独立保存。
这项验收覆盖实际使用的 workflow，不扩大 artifact v4、其他工具链或业务 Flux CD 的兼容承诺。

### 本轮新增能力（实际部署与验收见 status）

- workflow run/defaults 可声明 `shell: pwsh`；只有显式 act profile 允许，管理员必须提供真实 PowerShell。
  native profile 在 admission 时拒绝，不能误用 bash 解释 PowerShell。Ubuntu24.04/Rust1.97/PowerShell7.6.9 为可选镜像 target；
  工具镜像构建、临时 k3s 和业务原 workflow 验收分别记录，不把语法支持算作工具运行完成。
- setup-go 的固定 SDK 使用 64 MiB Azure blocks，缓存接口现接受该边界，超过单块限额返回 413；
  对象 256 MiB、attempt payload 512 MiB、最多 256 blocks 等预算保留。
- 管理员可以在目标配置中绑定原始 workflow 的镜像任务；仅匹配 push 追加原生 CD，不改 GitHub YAML。
  等待全部原任务组成功，固定 digest；具体来源、发布/健康状态和使用边界见 deployment。


### act 临时目录与嵌套 Docker bind

job `/tmp` 与同 attempt 的 Docker 引擎共享有界目录（计入 act.data_limit），支持 `mktemp -d` 后以绝对路径交给嵌套 `docker run --mount type=bind`。
工作区继续共享；其他 job 镜像自身路径不自动出现在引擎文件系统。supervisor 的私有临时元数据不共享，无宿主路径挂载。
此行为由临时 k3s 中非 root 子容器读取临时文件、supervisor 私有文件不可见、缓存/重启/失败/取消回归验证；真实 Rust managed tests 另行记录。


act 的 Docker/Buildx 资源约束要求 Linux cgroup v2 和配套升级后的 supervisor，正常容器及显式 /docker/buildx 父组均保留在引擎资源祖先下。
job 内层还设置向上取整后的 CPU limit，供 Node 等工具选择自动并发数；不足1核时逻辑值为1，精确总配额继续由引擎祖先约束。现存 attempt 不回写。
默认缓存对象256MiB不变；管理员可显式将 opaque Actions/BuildKit cache 对象提高至2GiB，artifact仍最大256MiB。
单块64MiB、全局保留/并发上传预留、每attempt有界暂存、错误/ACK/取消语义继续保留；未获配置的超限不会静默成功。
分块清单XML的字节上限随允许的block数量增长（每block预留128字节、整体1KiB格式开销）；支持最长64字节的base64 block ID，不再用固定32KiB截断合法的大对象清单。block数量、payload与对象额度分别继续校验。

新编译计划的默认 job timeout 对齐 [GitHub 官方语义](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idtimeout-minutes)：360 分钟。此前 Forgelet 独自采用20分钟，会在受限 CPU 的冷构建及外部 Action 下载时提前终止；显式 timeout-minutes 不变，已持久化计划/重跑仍保留原快照，零值旧计划的运行时20分钟兜底不变。

2026-10-28真实Rust验收：steam-cm 8e265947原rust-ci.yml在显式ubuntu-24.04 profile完成test、独立release构建、smoke和release内两个managed lifecycle测试，Checks成功；原普通push不触发远程publish，不扩展到真实Steam或自动部署。homelab使用自定义Rust1.97 Debian builder alias而非保证上游镜像字节一致；临时k3s与真实业务结果分别见status。

原生依赖的CPU要求仍是执行镜像/worker契约，不能由YAML解析兼容保证。DuckDB2.0的默认amd64原生库在无AVX的N5105上会SIGILL；可选`tools/duckdb`镜像以同版本x86-64-v2共享库和官方`duckdb_use_lib` tag解决这一特定ABI/硬件组合，需管理员显式选用。默认profile不隐式改写应用依赖或workflow；构建、容器回归和实际Tasks验收分别记录于status。


2026-10-28 实际验收范围调整：steam-tasks 的 owner 提交40af05d移除了MongoDB运行时/配置/依赖/测试；后续原reliability workflow的数据库选项仍为PostgreSQL18。验收跟随该原始源码，不再要求Mongo7或因此调整worker的AVX约束；当前测试同步修复不改workflow/golden。可选DuckDB2.0共享库的CPU/ABI边界保持上述限制，原CI、镜像及手动PG18的具体通过结果另见status，不能由源代码检查推定。


显式2Gi缓存对象对应的大对象传输期限为最多5分钟，artifact仍2分钟，父任务取消/超时不会被延长。Go SDK与BuildKit缓存继续使用原作用域、immutable key、摘要与ACK；实际归档超出配置时保持明确警告/失败语义，CI自身success不代表缓存已保存，实际冷热命中需单独核对。


2026-10-28 Atlas安装按owner授权切换：Tasks workflow明确固定官方v1.3.0/Linux amd64/SHA256，通过现有Zot OCI artifact blob下载并使用actions/cache@v4；runtime Dockerfile使用相同blob的ADD --checksum/--chmod，不再引用已失效的旧业务image digest。迁移校验、原业务测试、PG18输入和20分钟保持，但安装/cache/Dockerfile是显式仓库修改，不再声称YAML与旧样本字节相同。此CLI镜像策略不是Forgelet对所有公共二进制下载的透明代理，未新增解析能力；cold/warm、错误hash、截断、损坏cache、真实迁移及BuildKit正负例已独立验证，具体CI/镜像/PG结果见status。


2026-10-29 PG18验收优化为显式业务workflow修改（Tasks22b8b32），不是Forgelet改写任意测试：workflow临时启动一个真实PG18，再通过既有测试后备接口为每个原测试创建/清理独立数据库，覆盖子进程继承的测试DSN。原测试/断言/迁移/race/20分钟保留，fsync等持久化设置不变；不增加平台常驻数据库或放宽执行器权限。开发者默认per-test Testcontainers保持。取消时CLI停止整个测试进程组并在有界独立cleanup context销毁自有容器；具体自动/真实workflow结果分别见status。

该版本已在homelab以原手动postgres=true入口98b5a9fa完成：53包完整PG/race通过，PG步骤4分26秒、步骤合计9分30秒，含Zot准备11分43秒；实际仅一个PG18、Go缓存true、最后日志序号248及终态持久化，精确GitHub Check成功。172个既有测试文件字节未改，原job1200秒与测试参数保持。同源CI/镜像发布/标签与Action摘要/Checks和Atlas迁移smoke均已核对。Codecov仍沿用原缺token时非阻断行为，本轮没有成功上传覆盖率。上述是业务workflow夹具与固定执行环境的实测结果，不扩展到所有数据库/网络场景的20分钟保证，也不意味着阶段10所有Actions能力已经完成。


管理员可在workflow管理配置选择act镜像分组（阶段11.3已部署并完成真实冷热对照，Tasks冷缓存边界见status），不修改仓库GitHub YAML。选择保存为运行计划中的固定digest目录；未配置组时保留完整镜像目录。分组必须显式包含shell/Testcontainers动态使用的镜像，不能从workflow解析成功推导这些依赖已满足。native/act执行能力与原有权限边界保持分别描述。


PR opened/reopened/synchronize/ready_for_review的同仓库事件若测试合并SHA尚未生成，先持久化等待，再通过GitHub API核对原始head/base、读取PR merge ref并验证合并提交的两个父SHA后固定测试merge SHA；兼容2026-03-10已移除merge_commit_sha的REST响应。不会退回head SHA。冲突/关闭或head/base变化不执行新的源快照，事件诊断保留；恢复后应使用新事件或明确重试。固定结果先持久化再准入，重启/重复投递不重新解析成其他提交。
