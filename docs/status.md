# 项目状态

更新：2026-10-30（Asia/Shanghai）。当前：**阶段13优先三项已完成代码、自动测试、homelab部署及真实仓库验收：GitOps自动发布防倒退、GitHub凭据获取隔离/恢复、缓存满额淘汰。Admin Web原workflow重跑完成CI→Zot→GitOps→Flux；steam-cm原test/image完整成功。最终服务端a05d530e已上线，代码仍为本批未提交工作区。** 完整Testcontainers通过2642.225秒（7项外部可选夹具SKIP），最终发布队列补丁另经专项677.700秒通过。Tasks冷缓存预算、其余仓库CI/CD接管，以及12.3仍单列未完成；不将本批计为全部业务接管。
按阶段快速实现，暂停 spec 审批，不设独立基线或性能审核；计划、代码、自动测试与实际环境接入分别记录。

## 阶段10首批逐仓库验收（历史固定提交，后续交付见文末）

以下固定提交代表已经核对的结果，不将后续业务新提交自动视为通过。镜像发布与实际部署分别记录。

| 仓库 | 已核对源码/运行 | 原 CI / workflow | 镜像 | 实际 CD |
|---|---|---|---|---|
| steam-platform-server | 冷e4b90b1 / 热ac3f90d；1ace135 / 9fe77a46 | 五步通过，Go缓存false→true | 原workflow未发布镜像 | 非本批部署目标 |
| steam-admin-web | 冷1c0ebb6 / 热b9061de；a81b/4209 / fbc8/a25c | 原CI/镜像workflow通过，npm false→true | digest302929e…，标签/Action一致 | 非本批部署目标 |
| steam-platform-web | baa4b54 / 2e2d793b | 48单测、28 Playwright、缓存与Checks通过 | digestc85afdd… | GitOps→Flux健康、失败阻断、回滚/恢复通过；当前revision564223da |
| steam-login | a177934 / a212d600 | 原六步全部通过；原流程没有单测步骤 | GHCR6f3b286… / 自定义registry3384791…，latest/完整SHA一致 | 非本批部署目标 |
| steam-admin | aeca6d4；保护版本下新d2bf88f / 4e9a8016+60a17111 | 新提交原CI/Checks通过 | 新digest97d5b404…，latest/sha-d2bf88f/Action一致 | 非本批部署目标 |
| steam-platform-etl | 6e57692 / 1ad78435 | 原手动workflow通过，SDK公开版本修正已提交 | 原workflow未发布镜像 | 非本批部署目标 |
| steam-api | b8f4702；修复后重跑8ca9acc3成功，原失败留存 | 原test/镜像workflow及Checks成功，Go缓存true，缓存导出400未再发生 | digest5da83526…，latest/提交标签/Action一致 | 非本批部署目标 |
| steam-tasks | 22b8b32；CI06db20fc / image39fb828f / PG98b5a9fa；旧失败保留 | CI/reliability/构建与精确7个Checks成功；完整PG53包通过4分26秒、含镜像准备11分43秒，独立Check成功；Go缓存保存/重启热命中已验证，MongoDB已移除 | Zot forgelet/apps/steam-task digestf683f162…，latest/sha-22b8b32/Action一致；非root只读断网Atlas/迁移smoke通过 | 非本批部署目标 |
| steam-cm | 新8e265947 / 50d91035；旧失败保留 | 原test/image及Checks完整成功，托管Actions恢复 | 本地release镜像004cbb45…，smoke与两个managed测试成功；普通push原publish=false跳过远程发布 | 不连接真实Steam，不自动部署 |

验证期间收到的额外业务新提交单列：Admin Web `4d5cd9d02d…` / run `3267d26c5adb97cff86de1a7` 的32个单测中31通过，`src/features/games/api.test.tsx:72` 找不到“提交补采”按钮；实际1 CPU失败尚未精确复现归因，保留原失败，不覆盖上表b906验收结果，也不改测试门禁。其CI与镜像原本是两个独立workflow，镜像任务继续按原定义运行，未接生产CD。 03:10追加结果：同一4d5提交/原快照重跑3178ee3a在3CPU新Pod成功，原14文件/32测试全部通过17.25秒，类型检查/构建/生成文件检查、npm缓存true及GitHub Check完成success；原失败留存，业务代码/测试断言未修改。该对照确认新运行通过，不证明原1CPU失败的唯一根因。

实现与资源/缓存修复已提交并推送 `c2aa4fa`。完整回归第一轮（默认超时修正之前）PASS2582.609秒，27个必选叶场景、2个需外部私有业务目录的可选SKIP；包含默认超时修正的最新完整回归也已PASS2066.079秒：27个必选叶场景、2个可选SKIP，真实SDK Actions35.05秒、浏览器33.61秒、登录/授权重启106.74秒、保留清理16.26秒；公共Action元数据首次读取失败后，在未持久化运行前有界重试成功。SDK预载仅使用官方SHA验证归档，不计公网SDK下载。实际仓库证据与可选fixture是否执行分别记录，不能互相替代。

## 2026-10-28 01:10：job CPU 可见性修正（验证中）

真实 worker 的引擎祖先 `cpu.max=100000 100000` 已约束1 CPU，但内层 Node24 `availableParallelism()` 返回4，Vitest默认由此启动3个worker。新实现把管理员引擎CPU limit向上取整，通过supervisor/临时act请求设置job `--cpus`；精确聚合预算、并发2和worker隔离保持不变，现有Pod不回写。

资源配置/协议回归race通过，`make verify`通过（Go race/HTTP/SQLite、35前端单测、14浏览器31.5秒、构建/lint/generate）；临时k3s专项PASS478.430秒（执行子场景249.40秒），直接断言job内Node可见CPU=1，重启/缓存/失败/超时/取消/清理继续通过；临时资源已清理。包含此修正的全量Testcontainers回归PASS1522.854秒：27个必选叶场景通过，2个需私有业务目录的可选fixture跳过；act241.35秒、Rust99.25秒、300MiB缓存168.90秒、真实SDK15.82秒、浏览器34.91秒、登录授权/重启106.15秒、GC17.06秒。临时k3s已清理；源码b10fa93，实际业务仓库验证另行记录。

Admin Web `4d5cd9d` 的原32个测试在独立Node24.14容器中，1 CPU和2 CPU各三轮通过；强制3worker时1 CPU和2 CPU也各三轮通过。极限0.2 CPU/3worker的三轮均失败，其中复现同一补采按钮断言；这说明资源压力可造成该症状，但不是实际1 CPU环境的精确复现，不能据此宣布唯一根因或已修复。业务代码、断言和workflow未修改。追加0.2 CPU/1worker对照32项通过54.60秒；尚不将其等同实际集群根因证明。

01:14已部署 server `749b0bc62b7aa43a7b916684c1c3212304bf7e23efa80f6bd2ff13a1d84f09f7` + supervisor `949b3b5f3d4162becde64b266b39d0cbc6b4dd5cd6a31b793d4703aaada688f6`，保留全部活跃Pod UID和配置预算、工具runner、镜像别名。原Admin Web失败计划重跑 `3178ee3aa00a1c6f77ed287d62216c91`已入队，使用同一4d5提交和固定Action快照；尚未运行，原失败历史保留。

01:08实际集群etcd无当前告警、leader=1，P99 fsync约39ms/commit约61ms；Tasks已进入原版Go构建，API仍在队列。额外Admin Web镜像workflow已成功，独立CI失败仍保留。

## 2026-10-28 01:24：Tasks 冷编译与 worker CPU 预算调整

旧业务提交a69da5a的CI `3710b197a7dabb9108b417f8` 最终failure：reusable子任务 `check/reliability` timed_out，模块校验通过，尚在静态检查/编译，未到Atlas/业务测试。原20分钟和失败结果保留；这个attempt使用CPU可见性升级前的Pod，不回写。

观察时node3 CPU1182m/29%、内存39%，其中执行Pod约1 CPU；node2 CPU749m/18%。为验证原workflow在有界预算内能否完成，将后续act引擎CPU request保留1、limit从1调为2，内存request=limit=4Gi、并发2与worker-only selector保留。调度仍按request准入，CPU有争用时不保证持续2核；不是增加常驻执行器，也没有放宽业务timeout或减少检查。现有1 CPU Pod不变，新attempt才获得2 CPU祖先限额与内层可见CPU。01:24配置rollout成功，原活跃Pod UID保留；新2 CPU任务的实际限额/流程结果仍待核对。原API344及旧Tasks继续正常排队，不取消其他业务push。

## 2026-10-28 01:46：完整回归与线上配额核对

CPU可见性修正后的完整Testcontainers PASS1522.854秒（27必选叶场景，2可选私有fixture SKIP），全部临时集群/registry已清理，无新增代码修正。实际API旧Pod `f-316f23ed5e1b780a0fa8479a` 仍为1 CPU/4Gi：祖先cpu.max=100000/100000、memory.max=4294967296，内层NanoCpus=1000000000，Node24实际availableParallelism=1（宿主4核）；证明升级保留已有配额且修正可见性，不计作该workflow已通过。新Tasks Pod `f-b728251526edc9398fde1b26` 的spec为request1/limit2 CPU、4Gi、CPU_COUNT=2，尚Pending，内核配额待运行核对。

旧Tasks a69的CI及镜像入口均因reliability子任务20分钟超时而failure；镜像构建正确skipped，非业务测试失败。最新fe3两个入口仍待验收。01:40现有Web HTTP200、Flux Ready/revision564223da、Deployment digestc85afdd保持。node2预留CPU3300m/4、内存7214Mi/~9.8Gi，不能接纳4Gi引擎加256Mi supervisor；node3当前只能准入一个重任务，并发上限2不等于同时可运行两个重任务。本轮保留这些预留与现有业务任务，不以降低内存预留制造吞吐。若需稳定两重任务并行，需要额外worker余量。

## 2026-10-28 02:19：Tasks 原超时保留与后续 CPU 预算

最新fe3普通CI `c984f955` 的原20分钟任务 timed_out：模块/静态检查/构建通过（11分3.87秒），Atlas安装通过（1分1.89秒）、迁移校验通过，进入原short/race检查后达到总超时；不能记作测试失败或整库验收通过。其2CPU祖先限额与内层Node可见CPU=2已实测，内存4Gi保留。

后续act引擎CPU limit由2调整到3，request保留1、内存request/limit4Gi、worker-only与并发2不变；02:19配置rollout通过，已有Pod UID及预算保留。争用时仍不保证持续3核，不提供磁盘I/O隔离。原fe3 CI固定快照重跑 `f495f8ff5a85b462d30d16cd057d3c43` 已入队，业务源码、Actions固定SHA与显式20分钟原值保持，旧失败记录保留。相同提交多attempt验收按每个原workflow的最新attempt核对，同时记录历史attempt，不将失败覆盖成成功。

额外API344提交的原test任务已success，Go缓存已保存；Codecov因缺少token拒绝上传，原fail_ci_if_error=false允许Action成功，因此仅确认覆盖率生成，不能声称外部上传成功。原image仍运行中；目标APIb8、Tasks发布/PG/Mongo及Rust仍未完成。本次只调整运行配置和验收记录，无新增代码改动，沿用已通过的完整自动回归。

## 2026-10-28 02:34：真实 API 缓存导出暴露分块清单边界

额外344提交的镜像已推送b4d11d54…，但原build-push因缓存PUT HTTP400而failure，不能计作完整发布验收。代码检查与回归复现发现：允许512MiB/1GiB对象及更多分块后，提交XML清单仍固定32KiB；合法的最大长度block ID列表会被拒绝。该缺口已修正为按block额度有界预留XML空间，未调整对象/payload/块数/单块额度，尚待真实API验证确认其是否为本次400的全部原因。

新增256/512/1024分块和最长64字节ID的HTTP handler回归：修复前512/1024失败400，修复后race通过，包含完整组装、幂等重试、超数量及超XML字节拒绝。真实独立BuildKit Testcontainers随机层由300MiB增到384MiB，跨过旧XML边界；make verify已通过（Go race/进程内集成、构建/lint/generate、35前端单测、14浏览器32.2秒），缓存专项Testcontainers已PASS349.282秒（真实BuildKit场景209.62秒，cold3090dfe0/warm e43ae36f、digest3dfd40e8…，包含重启/失败/取消/清理），临时集群已清理；完整Testcontainers仍在运行。02:42已部署supervisor c021018d1899befc3460ab1876320c3355838408255572531e9d01dcfd50fd73，server仍为749b0bc6；只更新后续执行器镜像，已有Pod UID/配额和全部镜像别名/工具runner/worker selector保留。原API目标b8的test已success：Go缓存true，普通测试22.54秒、覆盖率25.96秒；Codecov上传缺token仍为原定义软失败。实际Pod f-079a44a6 位于node3、祖先cpu.max=300000/100000、memory.max=4Gi、内层NanoCpus=3e9/Node24 availableParallelism=3（宿主4核）；Tasks和Rust仍待后续原workflow验收，不覆盖本次失败记录。

## 2026-10-28 02:54：Tasks 临时引擎空间修正

fe3镜像入口 `f7c385c7` 的reliability子任务为interrupted：Kubernetes事件明确记录act-data emptyDir超过12Gi被Evicted，cancel_requested=false，镜像正确skipped。该attempt尚未完成原检查，不能称业务测试失败或沿用前次超时结论。根因范围为本Pod磁盘额度；预加载公共镜像与Go编译缓存叠加需要更多临时空间，不是SQLite/PVC低水位触发。

调整前实际worker文件系统余量：node2约66.7GiB、node3约341.4GiB，Memory/Disk/PIDPressure均False。将两个act profile的data_limit由12Gi提高至24Gi；保留workspace原预算、CPU request1/limit3、内存4Gi、并发2、worker-only、所有镜像别名与工具runner。24Gi是每attempt emptyDir上限，并非磁盘预留或瞬时强制写入限速；kubelet可在观测到超限后驱逐。执行完成清理随Pod回收，无新常驻引擎。配置rollout通过，现有Pod UID/原额度保留；新原快照重跑已入队，仍使用显式20分钟，不删除检查。本次只调整配置，自动测试代码版本未变，完整回归仍运行中。

02:44现有Web HTTP200、Flux Ready/revision564223da、digestc85afdd保持；etcd当前告警为空、leader=1、P99 fsync约12ms/commit约15ms。Rust仓库已有新业务提交d3d62cd，后续验收前按原计划fetch/fast-forward并保留业务变更。

## 2026-10-28 03:01：分块清单修正后的完整自动回归通过

源码1842d17的完整Testcontainers已PASS1654.999秒：27个必选叶场景通过，2个依赖私有业务fixture目录的可选场景SKIP，另有工具链归档校验拒绝测试通过。临时k3s 2c3cbbd3945e及registry0776253e05cc均已清理；384MiB BuildKit冷热/重启/失败/取消场景199.64秒（cold63fa1c44/warmcceb5295，digest979c1596…），act250.35秒、Rust101.26秒、自动发布依赖/失败阻断94.89秒、真实SDK14.40秒、浏览器34.68秒、登录/授权重启104.03秒、保留清理17.66秒。公共Action元数据首次读取临时失败后，在未持久化运行前有界重试成功。此前make verify与缓存专项已通过，无新代码变更或自动测试待重跑。

这不代表全部真实业务仓库已验收：APIb8原test成功/Go缓存true，但原image仍使用更新前创建的执行器；Tasks原失败/驱逐历史保留，3CPU/24Gi的新原快照重跑仍待执行；PG18/Mongo7手动原workflow和Rust真实push尚未完成。进度清单保持相应待办，不以可选fixture跳过或模拟API替代homelab证据。

## 2026-10-28 03:04：旧 API 执行器结果保留，启动修复后重跑

b8原run2af5b0f8最终failure：test及Go缓存命中成功，镜像9b324d69…已push，但旧supervisor949b3b5f中的缓存分块清单提交HTTP400。现场只读取文件名/大小：24.upload已接受363个分块、共379721285字节，未超过1GiB对象预算；合法大清单超过旧32KiB上限，与回归重现一致。该Pod创建于缓存修复前，未回写运行中镜像或旧额度。

同一b8固定原计划重跑8ca9acc34a41e8a89d433427d451ebce已入队，后续采用supervisorc021018d、CPU1/3、内存4Gi、act-data24Gi；源码、原Steps/Actions SHA和默认360分钟快照不变。旧校验进程在原失败后退出，已启动唯一可恢复验收流程，后续复用已有Tasks push、手动集成run和Rust验证push，避免重复写入。真实验收仍未完成，不把镜像push单独记作workflow成功。

## 2026-10-27：homelab etcd 告警排查与资源保护

owner 反馈集群 etcd 告警后，停止推进新的真实 push、构建与部署；排查时 Forgelet 命名空间已无执行 Pod，保留控制面服务。
Prometheus 最近两小时记录确认 node1 的 etcdHighCommitDurations / etcdHighFsyncDurations warning 曾 firing；
22:10 左右 5 分钟窗口 P99 commit 约 1.963 秒、WAL fsync 约 1.924 秒，node1 load1 峰值约 61.57。
同一时间段 Forgelet act 任务集中在唯一的 control-plane/etcd 节点 node1，重型构建与控制面共享节点和磁盘，存在明显资源争用风险；尚未量化各工作负载贡献，不能称已完成唯一根因定位。
k3s 于 22:13:42 exit-code=1 退出、22:14:01 恢复 active，实际 API 短时 connection refused；本次窗口未检索到内核 OOM 记录。
22:23 左右 etcd 告警已不在当前告警列表，has_leader=1、proposals_failed_total=0、数据库约 75.4 MiB / 配额 2 GiB；
WAL fsync P99 约 31 ms、commit 约 59 ms，节点 Ready、API readyz 包含 etcd 检查全部通过，但 k3s 仍有慢请求，不能称风险已消除。
当前仍有 Longhorn 卷故障/用量、业务健康与资源超配等告警，单独保留，不在本次排查中删除数据或重启存储服务。
已落地保护配置并于约 22:45 部署，不恢复重型业务验证作为本轮负载探针：
- 所有 profile 改为 `node_selector: {forgelet.dev/execution: 'true'}`；仅 node2/node3 获此标签，node1 不参与执行。
- 新增 `act.cpu_request/memory_request/cpu_limit/memory_limit`，校验正数及 request 不超过包含默认值的最终 limit；错误在 Kubernetes 调用前拒绝。保留未配置时的旧默认，实际 homelab 显式覆盖。
- ubuntu-latest / ubuntu-24.04 引擎 request=limit=1 CPU / 4Gi；supervisor request=100m / 256Mi、limit=250m / 1Gi。native builder 单独 request=limit=1 CPU / 2Gi；其余 native supervisor 同预算。
- 全局并发临时降到 1，部署保护版本后设为 2。节点余量只容一个重任务时，其余等待，不回退到 node1；Pending 仍受现有 starting 额度和超时机制约束，不承诺无限等待。
- 真实集群只运行两个休眠调度探针：一个准入 node3，另一个 Insufficient cpu / Unschedulable，node1 selector 不匹配；探针全部删除。
- `make verify` PASS（Go race / HTTP / SQLite、构建、lint/generate、35 Vitest、14 Chromium）；追加无效量/有效预算单测 race PASS。
- Testcontainers act 专项 PASS 468.427 秒，实际子场景 359.52 秒：实际 Pod 资源与 selector、真实 Node/Docker Actions、65MiB 缓存冷热恢复、原 UID 重启接管、失败和取消清理。首轮在固定 SHA 的 actions/cache 元数据 HTTPS 请求 EOF 时失败，尚未进入执行；保留失败记录与独立成功重跑，不混称全量 Testcontainers 回归。
- 已部署 server `zot.k.mygo.rocks/forgelet/server@sha256:f5289644b6abb8a79e6378e48134bbe270a59dc7e85a9b07da41a467a7385219`；控制面 Ready，启动日志 max_parallel=2。rollout 后首个外部健康探针短暂 503，后续 HTTP200、Service endpoint 与 API readyz 正常。
- 部署后复查 etcd 当前告警为空、has_leader=1；WAL fsync P99 约 36ms、commit P99 约 55ms。此时没有重型构建，不能将此结果当作高负载验证或唯一根因已修复。

保护措施沿用 Kubernetes 原生准入，不引入额外常驻组件。CPU/内存限制不提供 IOPS 隔离，
Zot/Longhorn 共享后端仍可能影响 node1；下一步按受限预算逐个恢复真实 workflow，观察后再增加并发。
如需要更高吞吐，优先增加独立磁盘 worker，并核对共享 registry/存储的负载，再扩充任务槽位。
历史指标证据保存在本机私有目录 stage10-delivery-20261027/etcd-incident-20261027/，无凭据写入仓库。

## 2026-10-27 23:50：恢复验证发现的两个实际缺口（代码已修复，验收继续）

- **资源约束不能只看 Pod spec**：真实 API 构建中嵌套 BuildKit 超过2核，而 act-engine Kubernetes 指标只有数 millicores；其 PID 位于根 /docker/buildx，绕过引擎 cgroup。前述1CPU/4Gi请求/限额解决准入，但尚未约束该嵌套进程，原判断已修正。
- **正式处理**：一次性 act-bootstrap 从 supervisor 镜像复制同版本执行器到 Pod 自有 emptyDir；引擎启动器建立私有 cgroup/mount namespace，重新挂载以本引擎为根的 cgroup v2，委派 cpu/memory/pids 后 exec dockerd；缺失能力立即失败。普通 Docker 与显式 Buildx 父组均留在资源祖先下。不新增常驻组件/引擎镜像，不挂宿主路径/身份。
- **真实探针**：node2 的250m/512Mi有界独立 Pod，两个普通/显式 Buildx 父组忙循环共5秒消耗1785035µs CPU，读取父 cpu.max=25000/100000、memory.max=536870912，保存两条完整 PID 祖先；探针已清理。Docker stats 显示宿主总内存不代表内核祖先限额失效，检查实际 cgroup。
- **API 缓存失败**：`c2ee4cb` / run `571ceefe8f6aff98b73e8556` 的测试成功，原镜像及latest/short-SHA manifest已推送 `6df02765f6cea27f7bfec5e6255877e9cd221486d4eeb04bd697b093952614e9`，但 BuildKit cache-to 超过256MiB对象上限返回413，任务整体 failure，不能计作验收通过。
- **显式缓存预算**：新增 blobs.cache_object_bytes 与 act.cache_object_bytes，最大1GiB；默认256MiB、artifact最大256MiB不变。Store 按种类预留，HTTP/下载/envelope/v1/v2一致限额；每attempt逻辑payload预算2倍、SDK单块64MiB，BuildKit1MiB blocks数按预算有界扩展；磁盘流式传输，仍拒绝超限。封装/组装会有额外临时副本，需要节点临时空间，不能将逻辑额度等同物理占用。
- **自动测试**：新资源/缓存代码 make verify PASS（Go race/HTTP/SQLite、35 Vitest、14 Chromium31.9秒、lint/generate/build）。前次全量 `testcontainers-resumed-full.log` 整体 FAIL2654.463秒：其余必选场景通过，BuildKit冷/热/负向构建已成功，取消探针在到达实际构建前因公共 setup-buildx 下载两次超时及EOF失败。保留失败，不记全量PASS。新版本专项已通过 act 内核限流/私有临时元数据隔离/重启/失败/取消，Rust与300MiB构建缓存冷热/重启/失败/取消仍在运行；最新完整命令另已启动，结果待收尾。
- **配套部署**：server `15264fa2b167ef0c44c1d99d1bb72db8474a4fbc3ef9700dadd5dcdf731c9e2a`、act supervisor `855544c3cc3d9f61e6c207411b6c071d646a64138b3caa75e8df966a376bdcac`，保留 worker/并发2/1CPU4Gi和现有工具runner；ubuntu-latest显式cache对象1GiB、全局blob6GiB（现有PVC声明10GiB）。真实API新执行Pod读到1CPU/4GiB内核祖先预算。
- **实际恢复**：API新真实push `a02aa7b99927fa5f07d6c105b73624a410ea1e86` / run `1ed6df7b9e8b1e152a07af5e`，原workflow运行中；旧README重复验证文案合并为CI说明。Tasks/手动PG/Rust依次等待，不删步骤、不放宽原job timeout/断言。检测到额外steam-admin真实push d2bf88f，保留其正常排队，不取消业务触发。
- **现有CD**：Flux forgelet-platform-web Ready，仍为回滚恢复后的revision564223da；没有将其他仓库的镜像push写作Flux部署完成。

## 2026-10-28 00:12：专项通过与默认超时修正

- 新嵌套引擎/大缓存专项 Testcontainers **PASS 1836.080秒**：act协议/CPU祖先限额/私有临时目录隔离383.74秒、Rust/PowerShell130.45秒、真实300MiB随机层的独立BuildKit冷/热缓存、重启保留UID、失败/取消清理1150.63秒。临时k3s已清理；完整回归仍在运行，结果分别记录。
- API `a02aa7b` / `1ed6df7b9e8b1e152a07af5e` 最终 **timed_out**：两轮业务测试及覆盖率测试成功，Codecov Action外部下载未完成时触及Forgelet旧20分钟默认值，image skipped；不是业务测试失败，也不是镜像验收成功。
- 对照 [GitHub workflow timeout默认360分钟](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idtimeout-minutes)，新编译计划修正为360分钟，显式timeout-minutes原值不变；旧计划、重跑快照和零值运行时兜底不回写。新增默认/显式20/45分钟单测和HTTP持久化断言，race单测/集成与make verify通过（35 Vitest、14 Chromium30.4秒）。部署与新实际push待完成。
- Mongo7.0原版镜像已完整SHA校验并同步Zot，供Tasks原版集成流程使用；PG/Mongo真实运行尚未完成，不能将普通unit模式跳过Mongo计作集成通过。
- 00:13已部署默认超时修正 server `afa361e14261a9c2afa44f952412086896dfa4ac506a49c843d67bdd8190a0c2`，保留 supervisor `855544c3` 与全部worker/资源/缓存预算；仅新增Mongo7.0精确上游镜像别名。原执行Pod UID保留。API新真实push `b8f4702df72a5548b346f36ccc6ed75705d57853` / `2af5b0f85e41d382d4586ee4`已入队，两job计划均为21600秒；同步保留新业务提交，原workflow不变。包含默认超时的新完整Testcontainers回归另已启动，尚无最终结果。
- Tasks复用最新业务提交已经触发的原始workflow，不额外制造重复构建；随后手动启用原版PG/Mongo集成，最后验证Rust。00:18复查现有Web HTTP200、Flux Ready、digest c85afdd/GitOps564223da保持；00:19 etcd无当前告警、leader=1、fsync约54ms/commit约63ms。

## 进行中：Go/Node 实际接入 → 业务 CD → Rust 与其余仓库

owner 已授权按上述顺序完成三批工作。2026-10-27 已重新读取 9 个仓库的默认分支 SHA 与原 workflows；
GitHub App 经确认新增 8 个业务仓库，加上后续单独授权的业务 GitOps 仓库，当前共 13 个限定仓库。
首批从 GitHub 当前版本独立检出，接入提交仅增加 CI 说明，原始源码与 workflow 保持。
验证期间临时暂停托管 Actions 并保存原配置，两仓库验收完成后均已恢复。
admin-web 冷 1c0ebb6、热 b9061de 的原 CI/镜像 workflow 已通过，npm cache false→true，Action/tag/registry digest 一致；托管 Actions 已恢复原配置。
中间 8dada0ad 镜像任务因 registry 登录网络超时失败，保留独立失败记录。
platform-server 原先两次 CI 成功但 Go 缓存保存失败的问题已修复：64 MiB 块边界、413 与默认 256 MiB/对象、512 MiB/attempt 预算保留。
缓存专项 Testcontainers PASS（1534.701 秒，实际场景 558.76 秒，65 MiB 随机数据、重启/热恢复/失败/取消）。
部署后真实冷 e4b90b1 / run 1ace135202cdfe4918bc5bf0 保存 181338460 bytes；热 ac3f90d / run 9fe77a46b5d9ee3cf43d834c cache-hit=true。
原五步、GitHub Checks 全部成功，Pod UID 不同；两仓库托管 Actions 均已按保存配置恢复。
自动 CD 补齐默认事件归一化：省略 event_name 的管理 API 与原编译器同为 push；新回归修复前失败、修复后 race 通过。
自动发布/Flux 专项 Testcontainers PASS（633.030 秒；新增自动图场景 96.10 秒），覆盖持久化配置、重启保留 UID、digest 投递和 CI 失败不产生发布。
业务 GitOps 仓库经授权加入 App，独立只读 deploy key、限定 Deployment 写权限及健康检查所需 ReplicaSet 只读权限已就绪，Flux 基线 Ready。
业务原 workflow 真实 push → 自动 CD / Flux 健康 → 回滚/恢复已通过，具体记录见下。
首轮 247a5d6 / f897af27 的原 CI 与镜像发布成功，native CD 在 node3 拉取外部旧 executor 时卡住。取消后 Pod 仍 Terminating；核对 UID、取消意图、未启动容器且无 containerID 后，仅强制清理该验证 Pod。保留人工恢复证据，不将此情形记作自动取消通过。
native homelab/builder 已切换本批 Zot executor ef6a469，控制面成功重启；真实 push da2ec10 / 44844cf722f4ea74cbc877db 的 48 单测、28 Playwright、pnpm cache-hit=true 通过；镜像任务登录外部 registry 时 GET /v2/ EOF，native CD 正确 skipped，无发布记录。
同节点独立 Pod 连续 6 次无凭据 /v2/ 探测：5 次预期 401、1 次 ECONNRESET，复现偶发连接故障，未改业务 YAML 或放宽 TLS。新真实 push baa4b54 / 2e2d793b86c11a33a8cae62e 三个 job 与 GitHub Checks 全部成功，48 单测、28 Playwright，pnpm cache-hit=true。
镜像 Action、latest 与完整 SHA 标签 digest 均为 c85afddacd8f6d1b024692b7489200c91894856faf8931ee88020ba1d75418ce；GitOps/Flux revision 4f6695e98b498821cd60c35830c58de352838b27，实际 Deployment ready 且 HTTP200。
原 native Pod 首次拉取缺 Zot 凭据，补齐其引用 Secret/执行 SA 后同一 UID a8d2149a-7c7c-47b3-b2a9-57900d684695 恢复成功，未重建 attempt。
显式管理 API 失败探针 c257c94983053b8d30eb63b4 使用旧源 SHA 与故意修改的 workflow，build 失败，image/native skipped，无 Pod/新发布，线上 digest/revision 不变；不把它记作原始 GitHub push。
实际回滚 e08720b9 → 基线 25e02b1 / GitOps ae647ceb，再恢复 1f4811bf → c85afdd / GitOps 564223da，均核对 Flux Ready、Deployment 与 HTTP200。Web 托管 Actions 已按保存配置恢复。
完整 Testcontainers 首轮在并发组场景超时：大缓存场景结束后将共享 tmpfs 从512缩回256MiB，保留数据令 available=59314176 低于64MiB恢复阈值，产品正确暂停。夹具现保留容量至容器清理，磁盘压力场景仍用原256MiB；首轮不记 PASS。后一次全量因公网 SDK 下载耗时耗尽测试总 context，亦保留失败记录。当前重跑显式预装经过官方固定 SHA256 校验的 Go1.25.0/Node24.14.0 SDK，真实 setup Actions/版本断言仍运行，公网 SDK 下载不计入该次兼容验收。

第三批进展（2026-10-27 23:15，尚未全部完成）：

- steam-login：真实 push `a1779349f75b0ced28068761cafbcd4ec927c940` / run `a212d60035016b2ddec78fbf`，原六步成功。GHCR latest/full-SHA digest `6f3b286beb9c9ec3cf1de316d9a26d4854e49060c61876a2150141d5a56306e6`；原业务 registry latest/full-SHA digest `33847916197b338a6918178b9748278357bf185cfed748062f35215d57becfa2`，均与 Action 输出相同，Checks 成功、托管 Actions 已恢复。原 workflow 没有单测，不能称该仓库单测通过。
- steam-admin：真实 push `aeca6d405f73b181d204229c63ffc90c85e84ccd`，原 CI `31f170f5` 与 image `77e621bf` 均成功；Go1.26/Buf1.72/生成差异检查实际执行，latest/sha-aeca6d4 digest `502de2aaee7ad693ffbe987b72cec245c6a69633c96f8cd33777abac9bece01a`，Checks 成功、托管 Actions 已恢复。两份原 workflow 仍独立触发。
- steam-platform-etl：修正过旧 SDK 缺少 priceregion 的业务依赖，固定公开 `v0.6.1-0.20261023131147-712898751565`；业务提交 `6e57692518448b715158631dd759d37689b83961`。原手动 workflow run `1ad7843522bb27c33002e83d` 成功，本地 make check 亦通过；保持手动触发，不修改生产 CDC/数据库。前一轮代理404保留失败，工具镜像 GOPROXY 增加 direct 回退后成功。
- steam-api：本地原 SHA Go1.26 测试成功；首次真实运行因 Redis 拉取超时失败，增加精确 Redis 公共镜像映射，未放宽测试超时或断言。事故后的真实 push `c2ee4cb1a129d45173a784f9b53e1194b83b701a` / run `571ceefe8f6aff98b73e8556` 测试任务成功，RedisAdmissionAndParentSnapshotIntegration 两次实际执行，镜像任务尚在运行。可选业务 fixtures 的 skip 不计入实际执行。
- steam-tasks：已修正 SDK 固定及 standalone Docker context，业务提交 `ec7e7667bea44b6987901e316516ad9caff5fd36`，本地 short/race 通过。此前真实任务被集群事故中断，原 workflow 尚未验收；接下来重跑原两份 push workflow，再显式手动 postgres=true 验证真实 Testcontainers PostgreSQL。
- steam-cm：原 Rust 测试任务成功，但最后一次 image 任务在 managed lifecycle 的临时目录 bind 失败。原因是 job 私有 /tmp 不在同 Pod Docker 引擎命名空间；已实现有界共享 /tmp，supervisor 元数据继续私有。专项 Testcontainers PASS 581.050 秒（实际 act 场景423.63秒），最新更强私有文件断言已随正在运行的全量通过 act 场景；真实 Rust 完整 workflow 待重跑，不记完成。

GHCR 经典 PAT 经 owner 确认后生成：write:packages（含 read，不含 repo/delete），2026-11-26 到期；本机0600，服务端加密保存，仅 steam-login 的 package_tokens 引用，source clone token 分离。
Rust 工具环境使用显式 Rust1.97/PowerShell7.6.9/Cargo mirror，workspace12Gi；自定义 compiler 镜像不是上游镜像逐字节副本。普通 push 保留 publish=false，不做实际 Steam 登录或生产部署。

恢复验证使用所有 profile worker selector、max_parallel=2、act engine request=limit 1CPU/4Gi，逐库执行重型任务；控制面 node1 不接收执行 Pod。
当前 server `zot.k.mygo.rocks/forgelet/server@sha256:53f5a7c3bcb9323dac0e16d3b996c24730ca067f38e2853c190a8f4bf0af6676`；act supervisor `zot.k.mygo.rocks/forgelet/executor-act@sha256:0ebfea0bf604225d612c9b89c1c56015265deb9824ef3c3583c0e69fca310053`。显式工具 runner_image 不变；部署从实时配置保留全部资源保护，活跃 API 测试 Pod UID 未更换。
最新 make verify 通过（Go race/HTTP/SQLite、35 Vitest、14 Chromium、lint/generate/build）；全量 Testcontainers 已通过 act 临时目录/私有元数据隔离、缓存、重启与取消场景，整条命令仍运行中，不能提前记 PASS。
恢复 API 重负载时复查 etcd 当前告警为空、leader=1，fsync P99约31ms/commit59ms；这不证明共享存储 I/O 已完全隔离。
本轮证据目录：/Users/guo/.config/forgelet/stage10-delivery-20261027/。

## 已交付

- 单 Go 控制面、SQLite + 一个数据 PVC、按需原生 Pod；默认并发 4，可配置，多任务超额排队。
- GitHub App adapter、HMAC/幂等、固定 SHA 文件与 checkout、Checks 回写、日志/历史、管理 API 和 Web 工作台。
- needs DAG、条件、失败/跳过传播、job/step outputs、超时/取消、重启恢复、历史/日志/事件/备份保留清理。
- 数据磁盘低水位暂停新调度、恢复后自动继续；可配置双阈值、探测失败保护、状态 API 与首页提示。
- 正式 server/executor Dockerfile、最小 k3s 安装清单、race 单测、进程内集成与 Testcontainers 临时 k3s。
- artifact/cache PVC 存储、按需 rootless BuildKit、GitOps direct/PR、Flux 健康观测、限定 image digest 回滚。
- 旧实现已清理并保留仓库外完整快照；恢复位置见本页末尾。
- 2026-10-25 阶段 8 server/executor 已部署 homelab；review #1/#2 已修复并验证。
- 2026-10-25 阶段 9 可靠性修正（review #3–#10 及 UI 仓库列表）已提交并部署；schema v6，详见本轮记录。

## 本轮：提交、homelab 部署与真实 push 验收（完成）

- Forgelet 的阶段 10 依赖实现与缓存修复已提交/推送：2504478；Web 工具镜像构建选项为 2970d8b，分支 chore/homelab-reset-20261023。业务修复 aff95ca 与验证说明 c6ac5b5 已推送 main，包含此前本机的价格修复 1b5dfad。两次 push 的 workflow、Dockerfile、pnpm lockfile 与原 75ed696 字节一致。
- 经 owner 确认，GitHub App 仅新增 steam-platform-web，保留原三个仓库；原 check.yml 已启用，发布凭据经管理 API 加密保存。实际接收 GitHub push，读取对应提交源码，执行原始公开 Actions；每轮 14 个主步骤全部成功、48 单测与 28 Playwright，通过生产构建及 SSR smoke。

| 运行 | 实际源码 | Forgelet | pnpm cache-hit | 单测 / Playwright |
|---|---|---|---|---|
| 冷 | [aff95ca](https://github.com/shitamachi/steam-platform-web/commit/aff95caaa8a75ad149eab293128e8a06a47d01a1) | [47c54f11](https://forgelet.k.mygo.rocks/runs/47c54f11751abcb1ff782a7d) | false | 48 / 28 |
| 热 | [c6ac5b5](https://github.com/shitamachi/steam-platform-web/commit/c6ac5b56ac14759f6813df7d6bc07dbc3d46b7e4) | [c5313da9](https://forgelet.k.mygo.rocks/runs/c5313da9aff6f2592720df87) | true | 48 / 28 |

- 冷发布 latest / sha-aff95caaa8a75ad149eab293128e8a06a47d01a1 / Action 输出均为 `sha256:72f7bd00a5ca2c9c9057ff506f8de99127110bc6cb6ff5d2b519b2f926ab8ce8`。
- 热发布 latest / sha-c6ac5b56ac14759f6813df7d6bc07dbc3d46b7e4 / Action 输出均为 `sha256:3dd0eedeeb7b395d18fdebe366a1cd447d6be385a8b68f0c07583bb5cecdfcba`。新 image Pod UID 为 `6c8e053e-abaa-4f54-9cf8-6f657166a598`；冷轮为 `306004e6-d9da-479e-9012-22803ce7a6a6`。热轮实际导入 GHA manifest，原 RUN pnpm install --frozen-lockfile 层 CACHED。两轮共 4 个执行 Pod、28 个主步骤、96 单测与 56 Playwright。
- 两次提交各有 2 个 GitHub App Checks，全部 completed/success。验证期间临时暂停该仓库的 GitHub 托管 Actions，避免并发覆盖相同标签；验证结束恢复原 enabled=true / allowed_actions=all。后续 push 会按原配置触发 GitHub 托管 Actions。
- 最终 server：`zot.k.mygo.rocks/forgelet/server@sha256:b0285b7105c3d51b45f8d5a7c2c25b538cc737f7c9cebe943037190cda272521`；native：`registry.shitamchi.com/forgelet/executor@sha256:9b407caad70711c8112fa0e704640a2bdc76ec3455dcd779da8f7bf5f4ee723e`；ubuntu-latest Web act：`zot.k.mygo.rocks/forgelet/executor-act-web@sha256:e6bbad13b83f13e3cf8ba34b72516a11dec2138777cc6a46eb8d4fd3a2e7a9df`。BuildKit 固定源为 Zot 的 98cc6a3fc462；引擎继续使用既有 Pod 自有 Docker28.5.1，不挂宿主 socket 或 Kubernetes 身份。Web 工具 target 增加镜像体积，不增加空闲常驻服务。
- server 最初从外部 registry 拉取，rollout 成功后的首个会话探针遇到短暂 503，随后独立 health/session 检查通过。已将同一 amd64 manifest 从原构建 index 49dd15d469a4 同步到 Zot b0285b7105c3，并在冷/热之间切换；无代码或 schema 变化，冷轮历史与应用缓存跨控制面重启保留。升级前保存原子 SQLite、PVC、Deployment 和 ConfigMap，最终服务 Ready。
- Web 镜像提供官方 Node24.14.0 完整 toolcache 与 Playwright1.63.0 / Chromium153。原 setup/浏览器安装命令仍执行；冷/热指 pnpm 和 BuildKit 应用缓存，预装工具不计作在线下载验收。官方浏览器归档经官方 MD5 及 ZIP CRC 校验，并记录完整 SHA256，构建转发保持官方 HTTPS 上游及 APT 签名校验，软件源安装后恢复；node1 的 Node20/24/Chromium 实际启动检查通过。临时镜像站与端口转发已关闭，真实任务使用集群中的镜像与服务。
- 自动验证：本轮复跑 make verify（Go race/HTTP/SQLite、实际构建、lint/generate、35 Vitest、14 Chromium）与业务 48 单测、tsc、28 Playwright 均通过。同一 Go 修复的默认 129 MiB 和原 workflow Testcontainers 专项见下方前批记录；本轮没有重跑全量 26 场景。实际 push 验收与 Testcontainers 证据分别保存；原 workflow 的终点为镜像发布，业务 Flux CD 接续仍属于 10.6e。
- 备份、两轮分页日志/结果、Checks、registry 对照与构建来源位于 `/Users/guo/.config/forgelet/homelab-web-20261027`。真实运行没有注入 fixture 源码补丁，任务启动与缓存恢复不依赖本机代理。

## 前批：steam-platform-web 比较竞态与完整 workflow（临时 k3s 专项验收记录）

- **归因已确认**：原提交 `75ed69647af3c5aa4a44c416b5b60d1c491def48` 的失败用例在 Forgelet 外、相同 Node24/pnpm9.15.9/Chromium 下，普通单 worker 两次通过；仅增加 CDP 6 倍 CPU slowdown、桌面/移动端各重复三次后 6/6 失败，均等待“开始比较”，页面只剩 1 款。应用修复后同一条件 6/6 通过（24.5 秒）。这是业务 hydration 竞态，环境时序使它暴露。
- **业务修复**：本机 `steam-platform-web` HEAD `1b5dfadd791a205fdec2a8a74768fcc32c4ef77c` 的相关逻辑与原提交一致；修改 `src/shared/state.tsx`、`src/shared/components.tsx`，增加 `src/shared/state.test.tsx`。状态恢复前禁用比较按钮，操作同时检查 hydration/ID，保留选择/移除/三款上限。新回归在修复前 2 failed/1 passed，修复后 3/3 通过；工作区 48 单测、tsc、28 Playwright（14.2 秒）通过。
- **真实构建发现并修复缓存协议边界**：实际 BuildKit 客户端使用 1 MiB blocks，旧 128 blocks 限制导致合法大层失败；现为最多 256 blocks，保留原 32 MiB/block、256 MiB/对象、512 MiB/attempt 及对象数量限制，分别诊断块数/对象/暂存超限。热运行导出还会重复查询大层；现对同 attempt 已恢复的精确不可变 key/version 复用 URL/文件，fallback 仍查后端，避免遮蔽新 primary。无新依赖、常驻服务或 schema 迁移。
- **单测/集成**：256 blocks/257 拒绝、字节限额分类、重复查询不增加文件/字节、fallback 后新 primary 优先均有回归。最终 `make verify` 通过：Go race/真实 HTTP/SQLite、lint/generate、35 Vitest、14 Chromium（31.1 秒，`verify-cache-reuse.log`）；独立缓存回归 `cache-reuse-unit.log` 通过。
- **默认大层 Testcontainers 完整命令通过**：`buildcache-large-reuse.log` PASS（720.983 秒，场景 519.35 秒）。真实 129 MiB 不可压缩层覆盖 SDK 超过旧 128 块上限的行为，继续验证冷/热、重启保留 Pod、失败不覆盖发布、运行中取消清理。冷/热 `9c3e071d891578cba795c200` / `27e7289eba301b13d3d3704f`，失败 `f504723f0511770257680169`、取消 `35cbfdba70e6fec10a4eac38`。tmpfs 仅该场景临时扩到 512 MiB 后恢复，磁盘压力用例仍为原 256 MiB；不设性能门禁，不依赖私有源码。
- **原始 workflow 验证来源**：`hydration.patch` SHA256 `f75a19ea428ea581d88af7c7de1730513266136315399dc43a7cec1bbb796917` 显式应用到临时 clone，生成固定测试对象 `17ea226bfde9179845fbfeb745bcb6ed5670a8ed`。原 YAML/Dockerfile/E2E 断言字节不变；源码带修复，不把它宣称为 GitHub 原 SHA 的全部原字节。未更新输入快照或 owner 的 HEAD/index，业务修复留为未提交改动。
- **最终业务专项**：`platform-workflow-reuse-final.log`。冷运行 `0072294bf20fda672b4e5e61` 的 14 个原主步骤全部成功（47 单测、28 Playwright、build、smoke、原 setup-buildx/login/build-push 与 cache export）；pnpm cache-hit=false。latest/SHA 标签与 Action digest 核对一致：`sha256:da3a3e05ba99777c3d87dffe4fdeb77746477ce06fef8b0b81c66d975677a7b5`。热运行 `779b52250521d030a504343b` 也完整通过：pnpm cache-hit=true，47 单测、28 Playwright（17.0 秒），新 builder UID `52732faa-6464-4394-9968-2cef3f10afa0` 的 `RUN pnpm install --frozen-lockfile` 实际 CACHED；latest/SHA 标签与 Action digest 均为 `sha256:dd003f342fd89bbeaf5c3325873ce176d8ed6c0af7ad6b632ea139e16e57ec42`。完整专项命令 PASS（1550.930 秒，业务场景 1348.72 秒）；共 4 个执行 Pod、28 个原主步骤、94 项单测与 56 项 Playwright，原 source workflow/YAML 不变。
- **执行环境边界**：显式 `FORGELET_TEST_TOOL_CACHE_IMAGE` 提供官方 Node24.14.0 toolcache 与 Playwright1.63.0 / Chromium153；浏览器归档经 HTTPS、官方 MD5/ZIP CRC 校验，Node 来源和 digest 记录于证据目录。setup Actions、浏览器安装命令仍执行；pnpm/GHA 数据缓存从空开始。原始在线工具安装曾在前述排查运行成功，最终预装工具 profile 不算新的在线下载验收，也不改变 formal executor 默认镜像。
- **保留失败证据**：`platform-workflow.log` / `platform-workflow-retry.log` 的 413 最初误判为总额度；实测约 195 MiB、128 个 1 MiB blocks 后确认块数限制，已撤回非必要额度配置。`platform-workflow-tools-final.log` 冷运行完整通过，热运行虽然依赖 RUN 层 CACHED 仍因重复恢复超限而整体 FAIL（1642.832 秒），促成查询复用修正。其他 TLS/下载失败、主动取消的环境排查与最终通过分开，不吞错或跳过测试。
- **范围与证据**：`/Users/guo/.config/forgelet/platform-web-fix-20261027/` 保存本批前快照、失败 trace/截图、固定来源、完整运行与日志。本批运行 make verify、单测/HTTP/SQLite、默认大层与原业务 Testcontainers 专项；未重跑上批全量 26 场景，不冒充本批全量。仅临时 k3s/隔离 registry，不写真实 Checks/业务 registry，不提交/推送业务仓库，不部署 homelab。

## 上批：10.6c 镜像构建、缓存与包发布凭据（历史记录，platform-web 阻塞现已解除）

- 复用原控制面/PVC 与按需引擎，在 supervisor 中接入 CacheService v2 JSON、Azure 风格 block/single PUT、block list、finalize、HEAD/range 下载；旧 v1 路径保留。新 runtime token 为每 attempt 的短时 JWT 兼容能力标识，服务端仍按精确随机能力校验，不信任客户端 claims。
- 缓存监听 Pod IP，供同 Pod 引擎内的 bridge BuildKit 访问；native 不新增 Docker 依赖。临时服务随 attempt 退出，持久隔离/不可变 key/配额/保留继续由原 blob 服务负责。
- 管理员可用 act.buildkit_config 设置 builder 默认 registry/TLS 配置；现有镜像目录中的 moby/buildkit:buildx-stable-1 固定源作为 setup-buildx 的默认 image。显式 workflow 输入优先，不把引擎预加载扩称独立 BuildKit 自动继承镜像目录。
- packages:write 需要 act profile、push/manual 事件与管理员 package_tokens → 加密 Secret 名绑定；计划只存引用。运行时 package credential 与 source clone token 分开；隐式 checkout 继续只读源 token。禁止 PR 获取包发布凭据，缺绑定/缺 Secret/撤销时明确失败。
- 原始 workflow 暴露的 github.ref_type 与显式 GITHUB_TOKEN Secret 环境绑定已补齐，保留其他 runner 身份变量和 file-command 写保护。原始样本位于 test/fixtures/steam-build；源码固定提交的仓库外快照通过 gh 只读获取。
- 公共 Action 归档读取补上 HTTP/2 早关流回归：只为读取 Git PAX commit 标识的归档请求使用独立连接，避免下一次请求卡在已取消的大流；metadata 完整读取继续复用连接，不携带用户凭据。真实临时 Pod 连续请求两次成功，HTTP/2 回归单测通过。
- **单测/进程内集成**：最终 `make verify` 通过（`verify-last.log`）：构建、Go race、真实 HTTP/SQLite、lint/generate、35 Vitest、14 Chromium（32.0 秒）；workflow 覆盖率 88.1%。覆盖 v2 分块/整对象/重试/限额/版本隔离、Put Blob 替换未提交 blocks 后的磁盘清理与计数、凭据引用/加密/重启/轮换/撤销、PR 拒绝、源 token 分离、默认 builder 与显式输入优先。
- **独立 BuildKit Testcontainers 已通过**：使用真实 BuildKit v0.20.2、原版 setup-buildx@v3/login@v3/build-push@v6；冷运行 `efc2df4d289d3ef3c7324d24`、热运行 `a78857db45c2f5f3e18deef0`，实际 RUN secret mount 层恢复后显示 CACHED。冷运行期间重启控制面保留 Pod UID `24e238f0-f2f3-44dd-8b24-3f1e58187e80`；失败运行 `2a925c58b954e307180f76c4` 不改变镜像 digest，取消运行 `3acf9d66f9f53df392911cdf` 清理按需引擎/Pod。
- **原始 steam-admin image 已通过**：固定提交 `2d6d2f5eb0f7236ff68bec49b3db1aa09a38e07d`，运行 `ee279133c662c5c86e9d39a6`，隔离 registry digest `sha256:98c203cfaf30ccc5b4a4ac1c012950ecaf9462fb9f4c26c16cb559b152f57a4b`。原 YAML、源码、Dockerfile 不改字节。
- **原始 steam-login docker 已通过**：固定提交 `0a5ce9753cf4b6f429cf273eea000353a9a19106`，专项命令 PASS（335.048 秒），运行 `97d46b224f59e593edc53f7d`。原 checkout/buildx/login/build-push@v5 和 packages:write 六个主步骤成功；自有 registry digest `sha256:692bbf076be8de170978ddac847c80e1094d018e72d0f6595ad97b16fc43e8f6`，隔离 GHCR namespace digest `sha256:10aff2802ccaf9718a47aa81ac9dba10e9d77fc275b2e63b6e53a962aea7c6e6`。两处均核对 Action 输出与实际 manifest；GHCR 代理单独校验测试 package token，不连接真实 GHCR（`login-original.log`）。
- **原始 steam-platform-web 仍受阻**：固定提交 `75ed69647af3c5aa4a44c416b5b60d1c491def48`，运行 `384e5eb6db2dafdd19234d37`。Node24、pnpm9.15.9、安装、单测与 Chromium 安装成功；真实 Playwright 26 passed、1 flaky、1 failed。桌面 `tests/e2e/catalog-search.spec.ts:52` 切换搜索页后找不到“开始比较”链接，重试也失败；镜像 job 按 needs 正确 skipped。源码中 CompareButton 未等 hydration，toggleCompare 可能覆盖 sessionStorage 恢复值，是待独立复现的线索，不能直接认定根因。未改业务代码、删测试或把跳过发布记为成功；冷热两次完整 workflow 尚未完成。
- **最终缓存修正复验**：Put Blob 替换旧 blocks 的磁盘清理/计数回归通过（`cache-replacement.log`）；最终产品代码的独立 BuildKit 专项 PASS（702.191 秒，`buildcache-last.log`），包括冷热、重启、失败和取消。该次发现取消触发所依赖的 Buildx 日志可能延迟刷新，测试现改为观察本 job 独立 builder 中的真实 RUN 进程，避免日志时序影响。最终专项 PASS（339.990 秒，场景 216.29 秒；`buildcache-process-signal.log`），冷/热运行 `f48636c67969f3a87b48c780` / `824845023363eeaa7a177b3e`，失败 `63559e864603e4e4cea159c4`、取消 `c6c7b4c1d5714a3e2081e37e`；日志/ACK 本身继续由独立日志场景验证。最终 integration 编译与 lint（0 issues）通过。
- **组合全量结果**：`testcontainers-combined-final.log` 用时 2138.808 秒，26 个必选产品叶子场景均通过，但命令整体 FAIL（上述可选 platform-web 业务 E2E）。包括实际 BuildKit/GitOps/Flux、低水位、取消/恢复、日志、原生 Go/Node、Web 与认证；不能把这个命令报告成全量 PASS。默认 `make test-integration` 独立完整命令 PASS（1763.748 秒，`testcontainers-product-final.log`）：26 个必选叶子场景通过，两项需私有源码目录的可选业务测试显式 skip。真实 Go/Node 工具链 71.61 秒，集群 Web 4 项 27.1 秒、认证浏览器 1 项 8.3 秒，保留清理通过。该全量命令在最后的 Put Blob 旧分块清理修正前已启动；修正后的最终代码另经上述 make verify、缓存回归和 BuildKit 专项验证，最终取消测试改进也经专项验证，不将其称为重新执行全部场景。
- 本批证据和工作区修改前快照：`/Users/guo/.config/forgelet/stage106c-20261026/`；未提交/推送任何仓库，未部署 homelab 或写真实 GHCR/业务 registry。

## 上批：10.6b 公共 Action 引用与原始业务 workflow

- **实现**：纯 workflow 编译器接受公共 tag/branch/full SHA 的合法语法；控制面在新 act 运行保存前解析精确引用与远程 Composite 依赖，保存 Action.SHA / ActionPins。默认从 GitHub Git 归档的 global PAX commit 标识固定 SHA，再读取该 SHA 的 metadata；精确 tag 优先于 branch，避免匿名 REST core 配额。可选受控 REST 源保留 annotated tag 解引用。重跑、重复 delivery 与恢复沿用已保存值；旧原生版本限制和无锁旧计划恢复保留。不新增依赖、常驻服务或数据库迁移。
- **运行上下文修正**：原 metadata-action 的 repoData 调用暴露了 provider 地址未传入 job 的问题；新计划保存既有 github_api_url，并传入 act/native 的 github.api_url 与 GITHUB_API_URL。重跑沿用原地址；公共 Action 源解析仍无账号 token，不与 job API 凭据混用。
- **单测与进程内集成**：最终 `verify-accepted.log` 的 make verify 通过：构建、Go race/HTTP/SQLite、lint/generate、35 Vitest、14 Chromium（30.7 秒）。覆盖 mutable tag 更新、新运行刷新而重跑/重启/重复请求不漂移、远程 Composite/循环/子目录、上下文取消、非法 metadata/源地址、临时限流返回 503 与事件退避、不保存半份计划和首次运行只解析一次。追加的归档 404/限流/中断回归另经 action 包 race 验证通过；workflow 包覆盖率 88.1%。
- **原始业务 Testcontainers 验收已通过**：一次完整专项命令 PASS（728.470 秒；业务场景 615.60 秒）。steam-platform-server 固定提交 `e37944b787fc89cc4e96c327e377dab09a09a940` 的 check.yml 两次、steam-admin-web 固定提交 `40140fc9e3281467d4199586f2f5f988fad0731f` 的 ci.yml 两次和 image.yml 一次，共 5 个运行、28 个原始主步骤全部成功。Go/npm 均验证 cache-hit=false → true；3 份 YAML、源码与 Dockerfile 保留原字节。
- **原始运行记录**：Go 冷/热为 `c3db1faed139aaec5e677a7b` / `0edb0235ce88c995e30df60f`；Node 冷/热为 `4fb0647e58c360c8f5c07025` / `5536a4c237a004f2692bf1ee`；image 为 `c2bffa8dcc2166d7cc715c4e`。实际执行原版 checkout、setup-go、setup-node、login、metadata、build-push；其中 setup-node@v4 固定到 `49933ea5288caeca8642d1e84afbd3f7d6820020`。临时 registry 的 manifest 与 Action 输出相同：`sha256:b102ce41a284135eb66c4d45b4c569ff772d169ed4ac35c8ad37262ab26abdda`。
- **全量回归**：最终 `make test-integration` 一次完整命令 PASS（1344.762 秒，`testcontainers-accepted.log`），25 个既有叶子场景全部通过。包括真实 BuildKit/GitOps/Flux、重启恢复/取消/身份隔离、低水位、Unicode 日志 ACK、act 缓存/运行时（217.45 秒）、matrix/并发组、真实 Go/Node 和保留清理；集群 Web 4 项（27.7 秒）及认证 1 项（8.0 秒）通过。默认命令未设置业务目录，显式 skip 可选原业务场景；该场景已由上述单独的最终 5 次运行验证。追加测试后的最终 lint 也通过（lint-last.log）。
- **实验中发现的问题**：修正测试引擎 ctr 入口、重复 insecure-registry 配置、内层 registry 域名路由/TLS 与 API fixture 的 token scheme；随后真实源解析遇到 REST 匿名限流，改用 Git 归档 commit 标识并加回归。初轮失败、仅 image 专项和最终 5 次运行分别保存，只有最终命令计入完整业务验收。
- **范围**：这是临时 k3s 中的真实执行；源码 checkout 经同 SHA Git 对象镜像，API/发布使用隔离测试替身，公共 Actions 与 Go/npm 依赖真实下载。不携带宿主 gh 凭据，不写真实 GitHub Checks 或业务 registry，不等于账号 push 接入或 homelab 部署。build-push 的 build record 上传仍有 artifact 协议警告，不记为 artifact v4 兼容；现有原始 ci/image 独立触发，没有隐含的跨 workflow 发布门禁或 Flux CD。
- 证据与工作区变更前快照：`/Users/guo/.config/forgelet/stage106b-20261026/`，最终业务证据 `business-final.log`，最终全量 `testcontainers-accepted.log`，最终单测 `verify-accepted.log` / `archive-final-unit.log`。未提交、推送本项目或业务仓库，未部署 homelab。

## 上批：10.6a 业务仓库盘点与后续实施顺序（仅检查和文档）

- **读取范围**：使用当前 gh 登录的 shitamachi 账号，扫描全部 15 个 steam-* 仓库的默认分支固定 SHA；9 个包含 Actions，获取 13 份原始 workflow，以及关联 Dockerfile、go.mod/package.json、Makefile 和 Buf 配置。未执行仓库脚本、触发业务 workflow、提交或推送业务改动。
- **当前代码检查**：临时 Go 探针直接调用当前 `workflow.ValidateFiles`，加载同 SHA 的全部辅助 workflow。13 份中 6 份规划通过、7 份拒绝；另逐条检查 Action 引用。主要拒绝点是 6 个未支持的 tag 别名与 pwsh；源文件、完整 SHA、诊断 JSON 和 GitHub 记录保存在 `/Users/guo/.config/forgelet/steam-inventory-20261026/`。探针已移除。此结果不包含 profile admission、真实步骤或外部发布验证。
- **实际仓库差异**：Go/race/Buf/rg、Node24/npm/pnpm/真实 Playwright、Rust1.97/pwsh/Python3，以及 Buildx/type=gha/双 registry 发布均有真实需求。当前源码未引用远程 reusable、动态 matrix、artifact v4 或 OIDC，暂不优先扩展这些能力。
- **业务问题已重新核对**：steam-tasks 默认分支仍是 `8265ad9772c5`；GitHub run `39965363438` 失败于 `steam-proto-go/priceregion` 缺包，v0.6.0 的树也没有该目录。Dockerfile 必需的 steam_proto_go context 仍未由 image workflow 提供。steam-api 当前 run `39959378343` 失败于 `TestRedisAdmissionAndParentSnapshotIntegration`，尚未进一步诊断该业务测试的原因，不能归类为 Forgelet 执行失败。SDK steam-proto-go 当前公开，不能把它列为已确认的私有授权阻塞。
- **优先顺序**：先 steam-platform-server（当前 GitHub 同提交 CI 成功）与 steam-admin-web（当前 GitHub 同提交 CI/image 成功），交付通用 Action ref 固定、真实 Go/Node 与镜像发布；再覆盖 Buf、pnpm/Chromium、独立 BuildKit 缓存、GHCR 与 Rust profile；随后逐库接续所需 GitOps/Flux、健康检查与回滚。全部 13 份原 workflow 均无 Flux 部署步骤，镜像发布不等于 CD。详情见 [compatibility.md](compatibility.md) 业务仓库表和 [plan.md](plan.md) 10.6b–10.6e。
- **验证与完成范围**：本批只有 gh 只读盘点、当前编译器探针和文档修改，未修改产品行为，未重跑 make verify、单测/集成或 Testcontainers，未部署 homelab。下文 10.5d 的最终 25 场景 PASS 仍是该实现批次的历史结果，不冒充本批业务验收。

## 上批：10.5d（代码、自动验证与 homelab 全链路完成）

- **公共镜像目录**：管理员将 Docker Hub tag 映射到 runner 同 registry 的固定 SHA256，最多 16 项，拒绝重复别名、未固定的源及其他 credential host。supervisor 自动拉取、登记原名并核对完整 config identity，再启动 worker；失败阻断步骤，无公网回退。Secret 仍只供 supervisor/Kubelet，不进入 engine/内层任务。Docker 引擎也使用镜像 digest，startup probe 检查实际 28.5.1；不增加常驻缓存或共享 Docker data。
- **Zot 实际目录**：按 amd64 原 manifest/blob 同步 PostgreSQL 18-alpine、Ryuk 0.14.0、Go 1.26.4-bookworm、Alpine 3.22、Dockerfile frontend 1.7 与 Docker 28.5.1-dind；在 `zot.k.mygo.rocks/forgelet/mirrors/` 保存固定源。上传采用本机 OCI 暂存后经临时 SSH 隧道写入现有 Zot Service，避免慢速上游流式上传中断；执行仍用正常 HTTPS。没有改动 Zot 服务配置，原 workflow/Dockerfile 镜像名均未修改。
- **日志**：同 step 小写入合并为最多 8 KiB、每 200ms 刷新的块，保留原 seq/SQLite/ACK 协议、1 MiB 待确认上限、大写入背压。首次尝试后内容不可再合并；ACK 丢失以相同字节重试。网络 I/O 不持队列锁，上传串行、等待可取消，避免上传挂起阻塞 job/step 超时；终态前以独立有界 context 排空并上报已确认序号。Unicode 与动态秘密跨块边界保持完整。控制面独立复制 profile 的目录 map，避免调用者修改运行配置。
- **构建**：Dockerfile 分离不随 Go 代码变动的系统工具层，native/act 继续各自交付，单 module 和四个真实二进制不变。`distribution/reference` 仅将已有依赖转为直接使用，负责镜像引用校验；没有新增 registry 客户端 Go 运行时依赖。

| 自动验证 | 最终结果 |
|---|---|
| `make verify` | 通过：真实构建、Go race/进程内集成、lint/generate、35 Vitest、14 Chromium（31.7 秒）；最后格式检查通过 |
| 日志/配置单测 | Unicode/动态脱敏、同 step 合并、ACK 丢失且尾部继续到达、永久上传失败停止代码、上传挂起时本地 job 超时、源/tag identity、目录/版本/credential host 校验与 profile 深拷贝通过 |
| HTTP/SQLite | 真实 executor → HTTP → SQLite 验证连续 ACK、精确文本、脱敏、步骤归属和最终 LastLogSeq，通过 |
| 最终 `make test-integration` | **25 个叶子场景全部通过，1746.332 秒**；一次完整命令 PASS，不用专项结果替代 |
| 新核心 k3s 场景 | 持续 Unicode/脱敏输出时重启控制面，原 Pod UID、完整行计数及顺序、唯一 seq 均保持（最终场景 3.82 秒）；act 场景 207.05 秒，包含私有 registry 自动准备、原名 Docker Action FROM、不可用 mirror 阻断、cache/调用/超时/取消/引擎退出 |
| 既有链路回归 | 真实 BuildKit/GitOps/Flux、故障/回滚、身份隔离、磁盘保护、多任务/并发组、Go/Node、浏览器/认证、保留清理通过 |

首轮 24 场景 23 个通过（1674.686 秒），唯一失败为浏览器点击保存后立即 GET，未等待异步 PUT 完成。
测试改为等待真实 200 响应再检查持久化结果，未放松断言；最终全量已包含修正。
上传锁问题另以挂起请求/秘密尾部/本地超时回归复现和修正，修正后的执行器用于最终全量和部署。

### 10.5d 实际部署与原 workflow

- 最终 server `registry.shitamchi.com/forgelet/server@sha256:99c01b9ace81809850a9f2e418b3869390299fc499c5995b14073da1d3cc2981`；native executor `registry.shitamchi.com/forgelet/executor@sha256:c0783df3e13e4219c79fad964705cfb28edbb8c7acac83463f4885ac917d3e5d`；act executor `zot.k.mygo.rocks/forgelet/executor-act@sha256:68189ab98e304637ca70532c05bdf98618c0b06b3ad44ddc61720b593497fe9e`。升级前已保存 ConfigMap/Deployment/原子 SQLite；会话保留，无 schema 变化。
- 仅提交专用测试应用 README，提交 `7f31a49360bbe55f42d2eff83f107b14775b804a`；workflow、Dockerfile、应用逻辑和 SDK tag 保持原样。run `e73fb60d516157d9c6df5a8f` 的 reliability/call gate/build-push/native deploy 全成功。原版公开 Actions、私有 SDK、Atlas、race、必跑 PostgreSQL 验收、cache-hit=true、BuildKit secret/命名 context 和真实 registry push 通过；Codecov 沿用非阻断策略，不记为外部覆盖率上传成功。
- 两个 act job 各自动准备全部 5 个公共别名，使用新的空引擎，无手工预热。控制面运行中重启，reliability 以原 Pod UID `3a595bff-7bd5-4309-87fe-9942741623bc` 完成，没有重放；Pod 完成后已 GC，以持久记录和重启前快照核对 UID。build-push 保存 109 个已 ACK 块、56,535 bytes；10.5b 同结构流程为 1624 块、51,477 bytes，这是两次实际运行的观察值，不作为性能基线或普遍速度保证。
- 三个本次 push 的 [GitHub Checks](https://github.com/shitamachi/forgelet-e2e-app/commit/7f31a49360bbe55f42d2eff83f107b14775b804a/checks) 均 completed/success。release digest `sha256:8ed01d4b12c9e8235e5e8dd90328c5599c801acf2b8b6405642fb38db60627d0`；delivery `21a38fd52ae5436aec42e4d0e8dbc643`，GitOps `385e2e04ec347150633cec9e963b119097e9203c`，实际 deployed / Flux Ready / Deployment image / HTTP 新 SHA 均相符。
- 回滚记录 `f3cf3c14149ec19cd5a25e09e1482a05`、GitOps `68cddb6cc66b207ffec874bdea05205cdf86c965` 恢复上一 digest `sha256:303ec3408f323cab25ddc01456f95887766829dded75c0707c63517677eccf42`，应用健康；随后记录 `d9cdb2c92af96ce52f91c80daf679c13`、GitOps `dc3a148ddbc3043d62aab4bbd95e125fc2b0244c` 恢复新镜像，最终 deployed / Flux Ready / HTTP SHA=7f31a493。三次观测中任务 id=1 均为 completed、worker=homelab-worker、version=3，内容完全相同；历史被替代的记录转为 superseded。
- 实际取消探针使用旧测试 SHA，避免影响本次三个成功 Checks。`b0e9ae87a6b2a426163eb0da` 在准备/执行交界发出取消，日志显示 step 已开始 sleep，按运行中取消记录，后续命令未执行；`86b879c5ca4a55129d2978d3` 在 runner 拉取阶段取消，尚未准备目录/启动用户 step。两者均 cancelled，原 Pod 与 engine 移除。取消是轮询后的停止，不把请求前快照冒充“用户代码从未启动”。

证据与升级前备份保存在私有目录 `/Users/guo/.config/forgelet/live-20261023/stage10d/`：
`verify-complete.log`、`testcontainers-final.log`、`public-images.json`、`images-release.json`、各 run/分页日志、
GitHub Checks、重启快照、三次 Flux/HTTP/Deployment 快照及取消记录。未修改或运行业务仓库，本项目没有自动提交或推送。

**范围**：这是管理员镜像目录，不是所有 Docker Hub tag 的透明代理；强制拉取/独立 BuildKit 引擎仍需配置源。
归档与版本维护方式见 deployment/compatibility；执行器仍只适用于显式 opt-in 的可信仓库/profile。

收尾复核：forgelet namespace 仅保留一个健康控制面 Pod，执行 Pod 数为 0，forgelet-lab 无 Pod。
停止本轮临时 SSH 隧道，移除本轮 builder、Testcontainers 镜像 tag 和可重建的 OCI 暂存；
用户的 lg-multiarch builder、Zot 镜像/Secret、控制面数据、应用与数据库 PVC 均保留。
最终三个成功 Checks、三次发布/回滚/恢复的 digest/revision/HTTP/任务数据、两次取消及日志秘密扫描均已复核。

## 本批：10.5b 正式单 job worker（已部署，真实链路通过）

- 已接入独立 act worker 子进程、显式 act profile/Pod 自有按需 Docker、Node20/24 分派；控制面继续 SQLite 图调度，原 executor 保留 claim/日志 ACK/control/result 协议。取消由父进程终止 worker 并停止自有引擎容器，避免 stock 的取消后 post 行为；不走有已知 PTY race 的 host backend。
- 已实现 job 本地缓存协议 facade，归档仍进入原控制面 blob 服务，保持仓库/ref/version、配额和保留语义；短期依赖仓库 token 引用由管理员显式配置，按领取身份临时签发，不持久化 token。
- `make verify` 最终一轮已通过：真实构建、Go race/进程内集成、lint/generate、35 Vitest、14 Chromium（31.0 秒）。workflow 单测覆盖率 88.1%；reserveGraph 89.6%、dependencyContext 100%、调用状态处理 88.6%。
- 实际 k3s 的 act 专项最新通过（829.803 秒，act 场景 679.02 秒）：Node20/24/post、Docker Action workspace/outputs、typed workflow_call/inherit/outputs、冷/热缓存、同 Pod UID 重启恢复、真实 exit 7/失败与 always 下游、job 超时、取消后全部引擎清理；新增真实 Git workspace 信任回归。测试日志按游标拼接，检查点输出越过脱敏尾部缓冲，避免匹配命令回显或漏读 post。
- 最终全量回归实际结果：24 个叶子场景中 23 个通过（整轮 1355.210 秒），包含 act（680.45 秒）、工具链（98.21 秒）、真实浏览器、认证和保留清理。唯一未通过项为 GitHub 下载固定 Flux v2.6.4 清单时连接重置，CD 场景中止；为测试夹具添加三次有界重试、4 MiB 限制，永久 HTTP 错误不重试。race 回归通过（2.228 秒），真实 BuildKit/GitOps/Flux 发布、故障、回滚和构建取消专项补跑通过（492.600 秒）。24 个行为场景已跨全量与补跑验证，不能将全量命令写成整体 PASS。
- 较早整轮曾因真实 Node 下载超过 6 分钟 fixture 窗口失败；仅将该下载 fixture 的 job/测试等待改为 12/13 分钟，未改变产品默认 deadline 或测试断言。工具链专项通过（514.473 秒），最终全量中的该项也通过。
- 新增不可变镜像预加载快路：校验 registry manifest SHA 和本地完整 config digest 后复用已加载镜像；任何校验失败回退标准 pull。单测/race/make verify 与真实 Docker Testcontainers 通过（4.111 秒）。不写 Docker 内部元数据，不信任 tag 或未经验证的缓存。
- 权限回归额外发现空 permissions map 被 omitempty 省略，恢复成默认能力；HTTP/SQLite 已复现并修复，保留 null/{} 区别。最终镜像专项和全量中的 act 场景包含无权限 job 不获得 token。
- act v0.2.89 只链接独立 worker；单产品 Go module 保留。go-archive 固定兼容 v0.1.0（v0.2.0 移除 act 所用弃用别名，上游 diff 已核对）；完整产品和 Testcontainers 一起验证，待上游迁移后移除此兼容 pin。

### 10.5b 实际 homelab 验证

- 正式镜像固定为 server `sha256:c362fd59b22ed212c28ee67f72fdb42c8fbdc0a7151c101c773e0c88549db312`、native executor `sha256:3d204029bf622689f4e190c29a69c929370c5e1e451b05c84ed65019d8767434`、act executor `sha256:480bc4a1aa5ff07662b83884290f3957ded3cc35dd1336bcc522f981f96db33a`，对应 `registry.shitamchi.com/forgelet/{server,executor,executor-act}`。单控制面位于 node3，act 任务在 node1 按需运行。
- 只在三个专用测试仓库操作，未修改/运行业务仓库。启用测试应用原 `image.yml` 的 push 规则，调用 reliability，再通过 needs.digest 接续 native `forgelet/deploy@v1`；GitHub 原生 Actions 仍关闭。registry secrets 由原加密设置服务保存，依赖 SDK token 按管理员的单仓库引用临时签发。
- 成功源码 `6777baadefac3574ed1899a68a586f556877fc8a`，run `77f821bf3dcf0c8514ad5d19`。原版 checkout/setup-go/Codecov/login/metadata/build-push 均实际执行；私有模块校验、vet/build、Atlas、race、必跑 PostgreSQL Testcontainers（3.45 秒）、私有 SDK v0.1.0 checkout、BuildKit secret 和命名 context 通过。Codecov 有网络/无上传 token 警告，非阻断，未计为上传成功。
- 运行中实际重启控制面，reliability Pod UID `01070288-51b1-4bba-9b85-b675d67f9832` 保持不变，executor 重启数为 0；原任务继续完成。三个实际 [GitHub Checks](https://github.com/shitamachi/forgelet-e2e-app/commit/6777baadefac3574ed1899a68a586f556877fc8a/checks) 均 completed/success。
- 构建 digest `sha256:303ec3408f323cab25ddc01456f95887766829dded75c0707c63517677eccf42`；delivery `6426676552d79ab2ded88852b57acbb0` 为 deployed；产品提交 GitOps `d17859cf10454486557072ae0d2517ae1a7b5c2f`，Flux Ready/Healthy=True、实际 Deployment digest 相符。HTTP 返回 healthy=true / production-like / 上述源码 SHA；原任务 id=1 仍为 completed、worker=homelab-worker、version=3。没有 operator 传递构建 digest 或代替产品提交 GitOps。
- 实际调用原 rollback API：记录 `45854256cbc6688f73381f6d83d1a539`、GitOps `2f75f7993fbe61ce9cf2e633b9ee8d3f910b62ce`，恢复前一个 v2 digest，deployed / Flux Ready / HTTP healthy。回滚 commit 只修改 `apps/demo/kustomization.yaml` 的一行 digest。随后回滚该回滚记录以恢复新版：记录 `c4f4bd6795a671fff96325651b770af4`、GitOps `3df5af133477d61efb886ccbb88a1a50927f81c2`，最终 deployed、应用为本次源码 SHA。三次发布/回滚/恢复均核对实际 Deployment digest，任务 id=1 内容完全相同；原发布记录随后为 superseded，历史快照保留。
- 环境准备边界：节点到 Docker Hub 曾返回 EOF，私有 runner 镜像冷拉取也较慢。本轮 operator 向相应 job 自有引擎预加载了相同版本的 runner/PostgreSQL/Ryuk/Go/Alpine/Dockerfile 镜像，保留了日志；没有共享 Docker data、跳过步骤或以预建应用镜像替代构建。细粒度日志逐条 ACK 也使终态可见性延迟；镜像分发和日志批量效率留待后续优化，本批不宣称无预热冷启动已优化。
- 失败记录保留：`7763c989c7a653be86c55a75` 在冷拉取中 interrupted；`45002450931a28b84b98bfcc` timed_out；`e6a53245d04200cd208672e5` 在 Go VCS 状态检查失败，修复精确 workspace Git 信任；`d3bb4a8e78c71e1f4cff3707` 在 Docker Hub 拉取 PostgreSQL 时 EOF。各次构建/发布均被依赖门禁阻断，没有错误交付。

证据保存于本机私有目录 `/Users/guo/.config/forgelet/live-20261023/stage10b/`：最终 `verify-release-final.log`、`testcontainers-complete-final.log`、`testcontainers-flux-retry-final.log`、`testcontainers-git-workspace-act-final.log`、`testcontainers-preloaded-image-final.log`，以及 run/日志/Checks/Flux/HTTP/重启快照。升级前的 patch、未跟踪文件、原子 SQLite、PVC/config/Deployment 备份均保留；本项目未自动提交或推送。

收尾：forgelet namespace 仅一个控制面 Pod，执行 Pod 数为 0，forgelet-lab 也无 Pod；临时 Testcontainers 容器全部清理。删除本轮 18 个本机 integration 镜像 tag、私有 builder 和可重建的镜像归档/节点临时脚本，停止预热 watcher 与 Zot 端口转发，保留原 `lg-multiarch` builder。未改变 Zot 配置、用户业务部署或数据库 PVC。最终作业日志未发现实际 registry 密码或 GitHub token 模式泄露。

### 后续配置：Zot 内网执行镜像（2026-10-26）

- owner 提供现有 `forgelet/zot-registry`（kubernetes.io/dockerconfigjson）；实际认证 `https://zot.k.mygo.rocks/v2/` 返回 200。未读取 README 中的密码，也未回显凭据。
- 将已验证 act 镜像原样同步到 `zot.k.mygo.rocks/forgelet/executor-act`，manifest digest 仍为 `sha256:480bc4a1aa5ff07662b83884290f3957ded3cc35dd1336bcc522f981f96db33a`。只修改 `ubuntu-latest` profile 的 image 和 act.registry_secret 为该镜像及 `zot-registry`，重启原控制面；server/native 镜像、应用构建 registry 和 GitOps 目标保持原配置。配置与数据库先备份。
- HTTPS 入口的大层上传曾返回 404 upload unknown / 503 reset；最终通过临时 Kubernetes 端口转发直接同步到 Zot Service，完成后关闭转发。执行器仍使用正常 HTTPS/证书验证，无 insecure registry、Zot 服务配置变更或额外常驻服务。
- 实际手动探针 run `ffb52f11c898aa4a4426afa4` 成功：新 Pod、自有空 Docker data，无手工镜像预热，自动从 Zot 拉取 runner，运行 Docker 28.5.1、真实 Node20.20.0/24.14.0。内层任务确认不可读取 registry config、workload token 和 Kubernetes token，空 permissions 下无 GITHUB_TOKEN；日志包含 ZOT_COLD_PULL_OK，未发现 Zot 密码或 auth 值泄露。节点 containerd 已缓存相同 digest 的层，主 Pod 拉取引用为 261ms；这不代表节点全冷启动耗时，内层空引擎拉取确实执行。
- 本次只有部署配置/相同镜像分发和实际探针，没有修改产品代码，也未重复 make verify 或 Testcontainers。此前 CI/CD 发布与回滚记录仍有效；workflow 自行拉取的 Docker Hub 镜像未切换到 Zot，日志逐条 ACK 的效率也尚未改变，10.5d 保持未完成。

本批证据与配置备份位于私有目录 `/Users/guo/.config/forgelet/live-20261023/zot-20261026/`。后续升级 act 镜像时须先同步新 digest 到 Zot，再更新 profile；原 registry 镜像保留可用于配置回退。

## 上批：10.5c 生产形态测试应用与实际 homelab 实验

按 owner 最新要求，不再用现有业务仓库试验。本轮只修改、提交并推送三个已有专用测试仓库；forgelet 工作区保留未提交变更，没有提交或推送本项目，也没有升级线上控制面。

- **应用与私有依赖**：`forgelet-e2e-app` 当前固定实验提交 `1941631595363a2e2c67e9baf1d1c3ee09008168`。Go/PostgreSQL 任务 API 包含幂等创建、请求冲突、乐观锁领取、归属校验、完成和持久化；迁移有事务、版本记录和 advisory lock。`forgelet-onboarding-e2e` 新增 contracts SDK，提交 `67ce93e`、发布专用 `v0.1.0`；20 个并发领取者只能有一个成功，迁移重入、重新连接和严格 HTTP 输入均有测试。未修改业务 SDK 或业务 tag。
- **接近实际的 workflow**：ci/image → reliability，typed inputs、secrets inherit、step/job/workflow outputs；原版 checkout@v4、setup-go@v5、Codecov@v4、login@v3、metadata@v5、build-push@v6。实际执行私有模块下载/校验、vet/build/race、Atlas、PostgreSQL Testcontainers、私有 SDK checkout、BuildKit secret/命名 context、真实 GitHub API 与 registry push。公开 Actions 固定 SHA，版本见 `test/actkernel/actions.json`。Codecov GPG 签名验证在最终 profile 通过；未配置该私有仓库上传 token，不能记为覆盖率上传成功。
- **先部署再实验**：`forgelet-e2e-gitops` 提交 `d8dc972` 增加应用迁移 initContainer、PostgreSQL Deployment/Service；数据库 Secret 和 1 GiB PVC 独立创建。先以基线 digest `sha256:da919d55470f4d87c72a1b710d1641442ed27b0dced1b73da7a864d5d911e5d9` 部署健康任务 API，再开始新内核 workflow。数据库属于被测应用，控制面仍是 SQLite。真实创建/重复请求/领取/完成任务 id=1；数据库 Pod 重启后仍为 completed、version=3。
- **真实运行与缓存**：修正事件输入后，`clean-ci-v2` 的 setup-go cache-hit=false、必跑 PostgreSQL 步骤通过；`verified-image` 在新 job 目录实际恢复 cache-hit=true，`TestPostgresAcceptance` 通过（47.03 秒，整步 51.29 秒），两个 workflow job 成功、SDK output=v0.1.0。源码和 SDK 分别使用只读单仓库 installation token，operator session/App 私钥/Kube 凭据不进入 CI。
- **失败与取消**：`ci-failure` 确实执行注入失败点，reliability=failure，构建未执行、无 digest，实际调用发布工具被拒绝；不把 stock 引擎留下的空 job 结果冒充 skipped。取消探针记录运行中 Pod/engine，foreground 删除后 Pod 数为 0，同名凭据 Secret 删除。所有最终 CI Pod 均确认 engine 已退出，采集结果后清理 Job/Secret。
- **发现并修复的实验问题**：vfs 超过 12 GiB emptyDir 被驱逐，改 overlay2；初始 dispatch 把 boolean 直接放进 event 导致条件步骤跳过，改为事件字符串并增加必跑步骤验收和回归测试。初始 `cold-overlay` 的镜像成功不计为完整 PostgreSQL 验收；上述 v2 运行才计入。补齐 GPG；执行镜像分开 apt 层，采用 gzip 通过 registry 入口，最终验证 profile 为 `sha256:39211b7e2e1946e5afe06eb35c7b92b03bfa3ccc5645f3711065a11887df4e18`，保留 `production-like-20261025-verified` tag。失败、取消和准备日志保留，不混称全部尝试都成功。

| 验证 | 本轮结果 |
|---|---|
| 产品 `make verify` | 最终通过：真实构建、Go race/进程内集成、lint/generate、35 Vitest、14 Chromium（31.3 秒） |
| `make test-kernels` | 最终通过，319.571 秒；16 个 k3s 叶子探针（含既有 3 项缺口观测）+ 新 Docker backend race Testcontainers（29.62 秒），两个候选各 9 项行为测试；私有业务探针明确 SKIP |
| 应用单测/集成 | SDK race、应用 HTTP/领域 race、真实 PostgreSQL Testcontainers 通过；本机应用最终整包 9.624 秒；线上另实际运行必跑数据库验收 |
| operator 工具 | 8 个行为测试通过：事件类型、条件误跳过、发布门禁、被驱逐记录、凭据范围、私有文件权限、取消归属 |
| 全量产品 k3s 23 场景 | 本轮未重复；上批最终 1072.166 秒记录保留，不能混称本轮再次通过 |

实际发布记录：

| 阶段 | 结果与证据 |
|---|---|
| 正常发布 v2 | CI digest `sha256:3c5780c83251125b2f453ada865d38ed510b50d274c1f81b0e2fe0c2dd70b188`；CD run `23caa78a929660316af4b2f6`，delivery `20ac3c2247f6d31c442c9b3994473983`，GitOps `d69c4c6834a009980c2c5319a378c10c596757fd`；deployed、HTTP healthy/version/SHA/digest 相符，任务 id=1 保留 |
| 故障发布 | digest `sha256:44e974af332ef29ac65321b8411db705796fcaa383b7cbedc8b7bd40f9136321`；CD run `f1f7c1b19ac0b3e2a33e89cf`；CI 成功，应用 readiness 故意失败；Flux Ready=False/HealthCheckFailed，delivery 保持 delivered，坏 Pod 不进入 Ready endpoints，旧 v2 继续服务，任务数据不变 |
| 回滚 | 原 rollback API record `580da273b9d01a4af3c0c9fb76dc3465` 恢复前一个 v2 digest；GitOps `475d734e933795760094e92e9ef1c549cf5dd4bb`；deployed、Flux Ready=True、HTTP healthy/version/SHA/digest 相符，坏 Pod 消失，原任务仍为 completed/version=3 |

证据保存在本机私有目录 `/Users/guo/.config/forgelet/live-20261023/production-like/`：`forgelet-verify-final.log`、`kernels-final.log`、`app-tests-final.log`、`operator-tests-final.log`、各轮脱敏日志/结果，以及 `verified-image-live.json`、`database-restart-http.json`、`unhealthy-image-live.json`、`rollback-live.json`、`cancel-probe-cancel.json`。顶层证据/凭据文件权限 0600、私有根目录 0700，不提交凭据或原始请求 Secret。

收尾确认：`forgelet-lab` 无 Pod、无实验 Job，仅保留 registry Secret 与单个 5 GiB local-path cache PVC；所有短时请求 Secret 已删除。本机只移除本次 `forgelet-lab-20261025` builder 和测试镜像，原 `lg-multiarch` builder 保留；临时端口转发停止。被测应用/PostgreSQL 保持健康，数据库 PVC/Secret 保留供后续使用，没有空闲 Docker 引擎。

**边界**：这是独立 homelab sandbox 的完整 workflow 实验，再由 operator 把固定 SHA/digest 送入现有手动 homelab workflow → GitOps → Flux。产品 allowlist 仅保留该手动 CD 路径，GitHub 原生 Actions 继续关闭；产品自动 push 调度、claim/result/日志协议与新内核整合仍属 10.5b，不能称为已接管。执行器是 privileged/rootful Docker Exec 实验配置，无宿主 socket/hostPath/Kube token；没有宣称 stock host PTY race 修复或生产隔离完成。工具与重现方法见 [test/productionlike](../test/productionlike/README.md)。

## 上批：10.5a 执行内核对比（历史，未生产接入）

- **代码与选型**：新增 `test/actkernel` / `make test-kernels`，分别构建固定 nektos/act v0.2.89、Gitea act v0.262.0；依赖 mod/sum 隔离在临时模块，产品 go.mod 未引入这两个执行库。用导出的单 RunContext、stdin JSON/结果、job logger 和 ActionCache 做窄接入，验证单 job 选择、外部 needs 快照、outputs、skipped 与秘密边界。**优先上游 act**：Gitea 的平台特化导致额外 GitHub API/结果适配，详细取舍见 architecture。
- **真实公开 Actions / 按需形态**：两者都验证 Node/Composite/post、失败清理、取消、workflow_call/typed inputs/inherit/outputs、实际缓存冷→热命中和 Docker Action。上游 act 跑完真实 login@v3 → metadata@v5 → build-push@v6 → BuildKit secret/命名 context → Pod 内临时 Registry push → pull/run。四个公开 Action 固定 SHA，源码归档通过库现有 ActionCache 接口提供，未改脚本。metadata 通过带 token 校验的只读 GitHub HTTP fixture；这不是实际 GitHub token/CD 成功。
- **明确复现的兼容缺口**：Gitea withGithubEnv 强制把 GITHUB_API_URL 写成 instance + `/api/v1`，使真实 metadata-action 调错端点失败；已改为确定性的 `build-push_github_api_gap` 负向回归，不能称 Gitea 构建通过。两者完整 workflow 模式遇子 workflow 失败都会提前结束，后续 always job 未执行；保留 forgelet 的 SQLite 图/调度，不委托完整 workflow。Gitea disabled/父调用还可能留下空结果，probe 显式转换，禁止发布未求值 outputs。
- **单测与竞争检查**：两套候选各 **9 个行为叶子用例**通过；输入拒绝与日志边界另跑 race 通过，覆盖并发/分片脱敏、固定 Action 归档路径/前缀、needs 输出和失败跳过、不重跑依赖、取消。**两套 stock host 的完整 race 均失败**，定位 PTY AutoStop 读写竞争；原失败日志保留，未打补丁或关闭检查后宣称 stock host 无竞争。生产接入前还须修复/替换该路径、保持严格 Node runtime 与现有可靠取消/日志协议。
- **新 Testcontainers**：临时 k3s 的 **16 个叶子探针完成，TestKernelPods 184.98 秒**；连同候选单测整个命令 **196.192 秒通过**。其中两项完整 workflow always 与一项 Gitea GitHub API 是明确的缺口观测，其余为行为回归，不能将 16 项写成两套库全面兼容。Docker 只对相关 Pod 添加 native sidecar，数据/socket/cache/workspace 都是本 Pod 的 emptyDir，无宿主 socket、hostPath 或 Kube token；测试确认主任务完成时 engine 已退出。PoC 是 privileged/rootful/vfs，**不是生产/rootless 隔离完成**，普通 job 无引擎，未增加常驻服务。
- **真实业务运行（失败，不记为通过）**：使用 steam-tasks 提交 `8265ad9772c5c92d3724a0372f17c31a4a8e498d` 的干净 archive，并校验 ci/image/reliability 三份原文件一致，运行原 ci → reliability；Linux amd64 匹配 Atlas 下载架构。两者实际完成 setup-go@v5（版本文件 → Go 1.26.4 / cache restore）、私有依赖下载与 go mod verify，随后报 `no required module provides package github.com/shitamachi/steam-proto-go/priceregion`。本地只读核对也证实 SDK v0.6.0 无该目录、当前 SDK 提交有。没有用本机 SDK 替换固定依赖，没有删步骤；Atlas、业务 race/Codecov 与 image push 尚未执行成功。另确认原 image workflow 未传 Dockerfile 必需的 steam_proto_go 命名 context，后续需业务仓库明确修正。
- **产品回归**：本轮 `make verify` 通过（构建、Go race/进程内集成、lint/generate、35 Vitest、14 Chromium）；新增工具最终另跑 race/lint 0 issues 和候选单测。现有全量临时 k3s **23 个叶子场景通过，1072.166 秒**，包括调用、取消/重启/磁盘、Node/工具链、artifact/cache、BuildKit → GitOps → Flux 发布/回滚、真实浏览器和登录授权；与上面的内核探针分别统计。
- **操作与资源**：没有修改 GitHub 仓库、SDK tag、生产镜像或实际 homelab；没有提交/推送。开发业务探针临时使用本机已登录账号读取私有依赖，不代表产品最小权限 job token 已接入；临时凭据文件已删除，容器/临时集群全部清理，公开测试镜像在收尾清理。在线产品兼容范围不变。

证据：`/tmp/forgelet-kernel-comparison-final.log`、`/tmp/forgelet-kernel-candidate-unit-final.log`、
`/tmp/forgelet-kernel-product-testcontainers.log`、`/tmp/forgelet-kernel-verify-final.log`；
真实业务失败为 `/tmp/forgelet-kernel-evidence/{upstream,gitea}-steam-ci.log` 与 `/tmp/forgelet-kernel-business.log`；
stock host race 为 `/tmp/forgelet-kernel-{upstream,gitea}-native-final.log`。
早期镜像准备、默认 Git 克隆超时、API 地址及测试 fixture 修正前的失败保留在其他 kernel 日志，不计为最终通过。

## 上批：阶段 10 第一批（代码与自动验证完成，未部署 homelab）

- **解析与表达式**：固定 actionlint v1.7.12 和其 YAML v4.0.0-rc.3，workflow 使用标准语法前端与有界 lowering；系统配置仍用 YAML v3。删除旧手写表达式 parser，以固定 act v0.2.89 的 MIT evaluator 源码作局部适配，保留 LICENSE/NOTICE。移除 act runner/model/Git/Docker 依赖与隐式文件访问，控制面仍为 Go；修正递归字符串转数字、null 比较、比较优先级、JSON 数组索引、format 大小/非法占位符和状态函数语义。增加事件对象、索引、对象过滤与格式化 JSON；仍保留上下文和大小限制。
- **可复用 workflow**：从一次固定提交快照读取同仓库 `./.github/workflows/*.yml/.yaml`，即使辅助 workflow 未被选为顶层触发入口也可被调用。持久化 call_gate → 子 job → call_result；支持嵌套、循环/深度限制、typed inputs/default/required、普通字符串模板、needs 完成后的参数求值、workflow outputs → needs。实际 job 继续使用独立 Pod 和既有 matrix/全局并发；控制节点不占执行额度、不创建 Pod。gate 保留原始触发 inputs，解析出的调用参数单独保存，避免重跑丢失原始参数。
- **权限与秘密**：支持 contents read/none 的受限声明，调用不能提权；不自动生成 GITHUB_TOKEN。支持显式 `secrets.NAME` 引用映射及 inherit，未传递时为空作用域，嵌套不能恢复已隐藏的 root secret。required 在 admission/rerun 检查配置名称，值仍按执行身份解密领取并脱敏，不持久化明文。
- **实际业务样本**：收录 steam-tasks 固定提交 8265ad9772c5c92d3724a0372f17c31a4a8e498d 的三份原始 workflow。标准解析通过，产品检查会给出 setup-go@v5/docker Actions 等真实执行能力阻断。没有删减业务步骤，也没有宣称整个应用 CI/image workflow 已成功运行。
- **本批边界**：远程 reusable ref、调用 job 的 matrix/concurrency、被调用 workflow 的 concurrency、依赖运行期 inputs 的提前执行设置、dynamic matrix、隐式 token/cache 与更多 Actions 后续实现，当前明确诊断。调用内部静态 matrix 已接入逻辑 job 聚合；新 API `workflow_files` 仅用于管理员提供的源码快照，GitHub 正常接入自动加载辅助文件。
- **修正真实回归发现的问题**：领取后、获取凭据前收到取消时，凭据接口继续拒绝发放，执行器通过原 UID 的 control 状态确认取消并正确上报 cancelled；不能把未确认的权限错误伪装成取消。补齐确定性单测、真实 HTTP/SQLite/执行器集成与真实 Pod 回归。参数求值用 AST 判断 needs/inputs 依赖（包含括号、整对象和动态索引），不再猜测表达式文本；禁止占位值决定早期 profile/name。没有 job output 时 workflow output 返回空值。
- **自动测试**：最终 `make verify` 通过（真实构建、race 单测/进程内集成、lint/generate、35 个 Vitest、14 组 Chromium）。workflow 覆盖率 **88.2%**；调度核心 reserveGraph **89.6%**、dependencyContext **100%**、调用状态处理 **88.6%**；storage 全包 **77.6%**，不将其混称为核心覆盖率。
- **Testcontainers**：全量临时 k3s **23 个叶子场景通过，1117.652 秒**，含两个新增调用场景、既有 needs/matrix/COE/fail-fast、70 秒停机恢复、磁盘保护、真实 Node/Composite/setup 工具链、artifact/cache、BuildKit → GitOps → Flux 发布/回滚、4 组真实工作台浏览器与登录授权回归。全量启动后补充的整对象早期设置诊断，另用最新代码/镜像专项 **2 场景通过，217.507 秒**，包含真实 API 拒绝该输入、嵌套调用、secret 脱敏、outputs、同 UID 重启恢复、rerun 参数保留、取消、失败/跳过与无 Pod 控制节点。
- **测试准备与重现**：早期冷构建/外网镜像拉取导致的准备失败没有计为通过；曾暴露取消竞态和未完成 builder 占用并发的连带失败，均在最终回归重验。提供可选 FORGELET_TEST_GOPROXY / FORGELET_TEST_DEBIAN_MIRROR；本轮 Go 构建用 https://goproxy.cn，Debian 使用官方 HTTPS 源，未关闭 go.sum、校验数据库、CA 或 Debian 签名。FORGELET_TEST_IMAGES 可导入本机缓存的官方 k3s/Registry/BuildKit/Flux 镜像，FORGELET_TEST_KEEP_IMAGE=1 用于调试重试；普通任务仍不使用 Docker。临时集群总 context 40 分钟、Go test 45 分钟，只有集群启动等待放宽至 10 分钟，功能等待/断言保持原值；失败时记录节点/系统 Pod/事件。临时公共依赖缓存代理已停止并清理。

验证日志：`/tmp/forgelet-stage10-verify.log`、`/tmp/forgelet-stage10-testcontainers.log`、`/tmp/forgelet-stage10-calls-testcontainers.log`；覆盖率为 `/tmp/forgelet-stage10-final-coverage.out`。初次全量失败记录保留在 `/tmp/forgelet-stage10-testcontainers-before-fixes.log`，不将旧失败版本的结果当作最终验证。

- **部署与仓库操作**：本批没有修改实际集群、GitHub 仓库、授权或远端提交。沿用 schema v6；新增调用数据在持久化 Plan JSON 内，不需要迁移。

## 阶段 10 前置调研记录（历史）

以下记录为第一批实现前的调研和本机 PoC；本轮产品变更与验证以本页上节为准。目标为完整 GitHub workflow 可运行，尚未运行真实 steam-tasks CI。

- **源码与选型**：首选 actionlint v1.7.12 的 Go Workflow/表达式 AST；表达式优先薄适配 act v0.2.89 pkg/exprparser，执行生命周期借鉴 act host；官方 @actions/workflow-parser/@actions/expressions 0.3.61 作为语义与测试对照。完整 act runner 不接管 SQLite/全局调度，官方 runner 不部署。候选均为 MIT；固定版本与上游限制见 architecture。
- **实际 PoC**：actionlint、act model、官方 parser 都接受 steam-tasks 三份原始 YAML；官方读取同仓库被调用 workflow 成功。act 与官方的 7 个共享表达式例子一致，另验证 act 对事件对象过滤的求值。act 的表达式包与 actionlint v1.7.12 组合通过这些试验；另运行 act pkg/exprparser 上游测试通过（1.418 秒）。不把小样例当作完整兼容证明。
- **执行试验**：act host 在 Docker 地址不存在的情况下跑完独立 fixture 的 workflow_call → Composite → 真实 Node 24/main/post → workflow output → needs 下游；另一个用例失败后跳过普通下游、执行 always 清理。首次 fixture 未 checkout 导致本地 Action 不存在，补齐正常 checkout 后通过；不是修改 steam-tasks 来绕过不支持能力。
- **边界与进度**：生产仍有 permissions/workflow_call/setup-go 等已知阻断；cache/凭据、构建 secret/命名上下文、工具链和 Docker 相关 Actions 运行环境仍需实作。未运行本批 make verify/Testcontainers，未修改 go.mod/产品代码、未部署、未提交或推送；阶段 10 实施顺序已写入 plan，不设审批或基线门禁。

临时试验位于 /tmp/forgelet-workflow-research.TgaxYz，包含各候选的固定依赖、程序与 result.txt，以及 host-runtime.log/host-failure.log。完整设计和来源见 [architecture.md](architecture.md) 阶段 10。

## 本轮：评估并修正外部 Review（阶段 9）

附件评审基于更早的重写版本。开始本轮时工作区干净，#1 COE 错误与 #2 序列化大小限制已经由 089f3d8 / 93d5d42 修复；保留已有实现，同时补上调度时合入 needs 输出后的大小复查：超限在创建 Pod 前明确失败，不能被 COE 吞掉，也不阻塞后续任务。当前保留单 Plan 900 KiB、创建时 run 合计 16 MiB 的限制，并增加真实 executor → HTTP → SQLite 的基础设施失败回归。

- **已实现 #3/#7**：空 public_url 不生成相对详情链接；已存 Check ID 的 GET 404 后按本 App/SHA/external_id 重新查找/创建并 CAS 替换。事件/Checks 持久化重试计数与到期时间，2 秒起指数退避至 5 分钟，连续 12 次后暂停并保留诊断，后续正常项可推进；新增受保护的管理员重试 API 与详情页 Checks 重试入口，不重跑 job。
- **已实现 #4/#5/#6**：运行时永久错误立即 interrupted，临时错误有持久化退避和 12 次预算，释放并发；取消删除失败保留独立预算。Pod grace 改 45 秒、最终 flush 保持 20 秒；修正终止 Pod 的身份校验，允许原 UID 收尾，禁止再 claim/取凭据。控制面中断容忍默认 5 分钟，可按 profile 配置，最大 1 小时。
- **已实现 #8/#9/#10**：rerun 检查仓库启用和当前 profile/BuildKit 能力；同名新 repository ID 返回冲突；Flux spec.path 必须匹配目标目录。
- **其他采纳**：旧零 timeout 计划 deadline 1320 秒、身份比较 JobID、普通步骤过滤内部传输环境变量、缺失/错误加密主密钥阻止启动、运行列表在 SQL 移除执行大字段、token/open/session 请求规则文档对齐。
- **明确暂缓/保留**：run 级共享 event/vars 存储改造、GHE clone 配置、可信代理与登录限速模型后续按实际需要实现；blob 目录未知文件继续 fail-closed。没有新增产品依赖、服务或审批流程。
- **提交与远端**：`32e3916`（添加仓库弹窗默认展示全部仓库、滚动加载）、`4ac1ca4`（阶段 9 可靠性批次，含 schema v6）、`4f626da`（阶段 9 文档）。分支 `chore/homelab-reset-20261023` 已推送 `origin`，`main` 尚未合并。
- **自动验证**：批次记录中的 make verify 已通过：Go 构建/race、HTTP/SQLite、lint 0 issues、generate、TypeScript/格式检查、35 个 Vitest、14 组 Chromium（最终 32.6 秒）；另补的 YAML 时长配置 race 回归通过。全量 Testcontainers **20 个叶子场景通过，1046.235 秒**；真实工作台 Chromium 4 组 48.4 秒、认证 Chromium 1 组 8.0 秒。部署前复核了 `go test -race ./...`、lint 0 issues、`tsc -b`、35 个 Vitest 和 14 组 Chromium；本机 `make verify` 在 Playwright 浏览器安装阶段因沙箱无法写 `~/Library/Caches/ms-playwright` 失败，浏览器测试已单独通过，不把该安装失败计为代码失败。全量测试启动后追加的 needs 大小 guard 使用最新镜像另跑专项，不混称该次全量已包含此新增场景；临时 k3s 新增 70 秒控制面离线后原 UID 恢复、真实 Pod 标签漂移不接管/删除、取消结果收尾断言。schema v5 → v6 迁移与待处理工作保留有独立回归。
- **实际部署**：2026-10-25 部署 server `sha256:fbc91710a659b85b17650f39115472b4ac992de578e01d62f005eaefadaaf885`、executor `sha256:cc230abba6a212797beccf88cb792b2e8c9faaf7ecba6d945961059af786d59c`（linux/amd64）；ConfigMap 2 处 executor digest 已更新。rollout 1/1 Ready、0 restarts。升级前保存 Deployment/ConfigMap 与 v5 SQLite 快照；本地副本上先执行 `PrepareRestore` + `Open` 迁移验证（`user_version=6`、`integrity_check=ok`、16 runs/34 jobs/1 admin），再在生产库启动迁移。部署后 v6 快照为 16 runs/34 jobs/1 admin/8 events、`integrity_check=ok`。旧 stage 8 server/executor digest 分别为 `sha256:fbbd4fd1...`、`sha256:751bd3c6...`；旧程序不能直接打开 v6，回退需使用部署前 v5 快照和原密钥。
- **实际验证**：从集群内访问公网入口，`/healthz` 200；`/api/auth/session` 返回 `mode=session`、`github_enabled=true`；首页为当前嵌入 bundle；匿名 `/api/runs` 401。未用管理员密码登录，未触发新的 GitHub push/manual CI 运行。仓库添加弹窗默认展示全部仓库，首屏 20 条、滚动/按钮加载更多；14 组 Chromium 覆盖默认列表、50 仓库增量加载、搜索过滤和无权限项。

验证日志：`/tmp/forgelet-review-verify-latest.log`、`/tmp/forgelet-review-integration-final.log`、`/tmp/forgelet-review-integration-size.log`；升级/重试行为见 architecture 和 deployment。

## 上批：阶段 8 部署与 review #1/#2 修复（2026-10-25）

- **提交与远端**：`2d808da`（单进程实现）、`9a79c4d`（文档重写）、`089f3d8`（continue-on-error 不再隐藏执行器错误）、`93d5d42`（Plan 大小上限）。分支 `chore/homelab-reset-20261023` 已推送 `origin`；`main` 尚未合并。
- **review 修复**：所有 `Execute` 错误路径现在都携带非空 `Result.Error`，job `continue-on-error` 不再把执行器/协议错误转换为成功；创建和重跑 run 时校验 Plan 序列化大小（单 Plan 900 KiB、单 run 16 MiB），避免创建无法被 executor claim 的任务和 SQLite 无界增长。新增 executor、storage 和 server admission 回归。
- **自动验证**：`go test -race ./...` 通过；`golangci-lint run --build-tags integration` 0 issues；当前前端 `tsc -b` 与 35 个 Vitest 通过。本批未重跑 `make verify`、Playwright 或 Testcontainers；本页第 8 阶段此前的 `make verify`/19 场景结果仍是其记录范围，不混称为本次运行。
- **部署**：server `registry.shitamchi.com/forgelet/server@sha256:fbbd4fd1d3d5579eac306f807f7127829ca6fbc22a57323b37239801939893bf`，executor `registry.shitamchi.com/forgelet/executor@sha256:751bd3c6e346e4e58a4eba1123b57fb336af6fb4885d1da52f1901026ed2c373`，均构建为 linux/amd64。ConfigMap 中 2 处 executor digest 已更新；Deployment server digest 已更新；rollout 1/1 Ready、0 restarts，无遗留执行 Pod。旧 server digest 为 `sha256:27d3bf56eff5aa1320269fd2f5ef91daf49f9c12b6de13a3c3ff0161fc77b75d`，旧 executor 为 `sha256:e571477be78896227359587d6e218452eb5d66e9ea61ffb83b314a274114114f`，部署前 Deployment/ConfigMap 已保存。
- **备份**：部署前保存 Deployment/ConfigMap，并生成带 `restore_required` 标记、`integrity_check=ok` 的 SQLite 快照（640 KiB；16 runs、34 jobs、1 admin）。PVC 上为 `/data/backup-manual-20261024T180905Z.db`，部署后另生成 `/data/backup-manual-post-20261024T183231Z.db`（同样 16/34/1）；本地证据目录 `/tmp/forgelet-deploy-20261024T180612Z/`。没有复制或输出 Secret 明文。
- **实际验证**：从集群内访问公网入口，`/healthz` 返回 200，`/api/auth/session` 返回 `mode=session`、`github_enabled=true`，首页标题与嵌入 bundle 为当前构建；匿名 `/api/runs` 仍返回 401。未用管理员密码登录，未触发新的 GitHub push/manual CI 运行。
- **当时剩余边界（本轮修正见阶段 9）**：review #3–#10（Checks 空 public_url/404、永久 reconciliation 卡住、cancel grace、executor 1 分钟容忍、outbox 重试、rerun 禁用检查、同名新 ID 重绑、Flux path 校验等）尚未修复；GitHub App 授权范围未扩大。

## 上批：修复仓库启用 403 与重写接入页（阶段 8 代码与自动验证）

- **根因**：截图中的仓库管理员权限并未缺失。使用已有加密凭据做只读诊断：`GET /user` 为 200、scopes=`read:user, repo`；`GET /repos/shitamachi/yuki-project` 为 200 且 permissions.admin=true；`GET /user/installations` 明确拒绝独立 OAuth App token。旧实现把 OAuth token 用于 GitHub App 安装授权管理，fixture 却错误返回成功，导致真实新仓库启用时 403。此处是实现与测试假设错误，不能归因于用户权限。
- **代码修复**：移除 OAuth 的安装授权调用；未授权时返回 HTTP 202 `authorization_required`，区分仓库待授权、未安装、暂停和权限更新，附服务端确定的操作链接；用户在 GitHub 确认后返回复核，验证通过再启用。待授权/失败/版本冲突不改变可信配置，不创建运行，不扩大 OAuth/App 范围。
- **页面重写**：主视图只保留已接入仓库，CI 开关、授权和最近运行分别显示；搜索与状态筛选、停用/恢复、检查运行及管理菜单。新增仓库在独立弹窗搜索、选择、确认，每页最多 6 个结果，支持账号筛选和无权限/无结果/请求失败状态；打开弹窗才请求账号目录，不再在主页铺开全部仓库。刷新恢复待授权选择，错误就近显示。
- **当前验证**：make verify 通过，Go race/HTTP/SQLite、35 个 Vitest、13 组 Chromium（最终 29.6 秒），包含旧工作台回归和新增授权等待/复核、刷新、50 仓库分页、无权限与移动布局。发现并修复新弹窗与旧 CSS 同名导致搜索框不可见的问题。全量 Testcontainers **19 个叶子场景通过，1020.446 秒**；工作台真实 Chromium 4 组 26.0 秒，认证真实 Chromium 1 组 8.0 秒，认证/重启专项 86.19 秒。授权前两次复核仍保持待接入；测试中单独模拟用户在 GitHub 确认后，真实 Go → SQLite 保存绑定 → 执行 Pod 成功；改密、session/OAuth 凭据与配置重启、原 Actions/BuildKit/GitOps/Flux/磁盘/清理回归全部通过。测试 OAuth 安装修改接口明确返回 403，外部确认通过仅存在于测试进程的本机桥接完成，产品未增加绕过授权的 API。临时容器和本机预览进程已清理。该代码验证批次当时真实集群尚未更新；实际部署见本轮，不将本机预览或受控 GitHub fixture 计为线上新仓库授权成功。

验证日志：`/tmp/forgelet-repositories-verify-current.log`、`/tmp/forgelet-repositories-k3s.log`；本机预览截图：`/tmp/forgelet-repositories-overview.png`、`/tmp/forgelet-repositories-picker.png`、`/tmp/forgelet-repositories-mobile.png`。本批不新增功能依赖，不自动提交或推送项目 Git。

历史阶段 7 文档中的“OAuth 自动加入 App installation”和其 fixture 成功结论在此撤回；实际阶段 7 上线只验证了已有授权的三个仓库，本批修正 architecture/deployment/plan 的相应承诺。没有添加 PAT、自动授权所有仓库、直接向执行 Pod 下发 OAuth token 或额外常驻服务。

## 上批：管理员账号与 GitHub 登录

- 新安装清单显式 `admin_access: session`。初始化单个 admin，Secret 指定或生成随机密码，bcrypt 哈希、SQLite 会话摘要、Cookie、退出与改密；重启不从 bootstrap 文件重置密码。旧 open/token 显式配置保留兼容，新会话模式不允许旧 admin token 绕过登录。
- 首次关联 GitHub 需要本地管理员会话，后续可以 GitHub 登录；不开放匿名 GitHub 自助注册。OAuth state/PKCE/浏览器和管理员会话绑定，一次性消费；GitHub 不变 ID 防止账号错绑。token 与 refresh token 使用 AES-GCM 加密，浏览器只收到 session Cookie。
- GitHub OAuth 请求 `read:user repo`，列出账号可访问的仓库，按实际管理权限决定可接入项；当时尝试用 OAuth token 将仓库加入 App installation；此调用不受支持，已在阶段 8 移除并改为用户在 GitHub 确认后复核。已有 App 的 webhook、clone 和 Checks 链路继续使用；没有重复创建每仓库 webhook。新账号/组织首次仍需安装 App，组织策略拒绝时给出诊断，不假报接入成功。
- Go 专项 race、构建/lint/generate、21 个 Vitest 和 11 组 Chromium 已通过；首轮浏览器发现登录后 Query 缓存清理导致页面停留在登录页，已修复并加入回归。首次容器回归暴露旧测试对同名仓库两个表格的定位不唯一，已改为明确选择登记表格行；重跑 19 个场景全部通过（951.913 秒）。

### 阶段 7 验证结果与边界

| 层次 | 结果 |
|---|---|
| Go / HTTP / SQLite / race | 初始化幂等、密码哈希/文件权限、session 摘要/数量/到期、跨源拒绝、退出/改密撤销、重启、备份恢复撤销、OAuth state/PKCE/浏览器/管理员会话绑定、回调重放、GitHub ID 防错绑、授权范围不足/重定向拒绝、其余认证验证通过；当时的 OAuth 自动授权/HTTP 304 fixture 不能证明真实 GitHub 支持，该错误假设由阶段 8 撤回 |
| 浏览器 | 21 个 Vitest、11 组 Chromium；首次登录、失败后重试、立即切换、账号/改密/退出、GitHub 用户仓库启用、清理私有查询与不使用 localStorage 保存凭据，原工作台回归通过 |
| 全量 Testcontainers | 19 个场景全部通过，951.913 秒；原工作台 4 组真实 Chromium 27.6 秒，认证 Chromium 1 组 7.8 秒；App 授权后新仓库实际创建执行 Pod，原 Actions/BuildKit/GitOps/Flux/磁盘/保留回归通过 |
| 认证真实 k3s 专项 | 104.19 秒；正确 Cookie 属性、session/加密 GitHub token/绑定重启保留、退出拒绝旧 Cookie、改密撤销另一个会话、再次重启后新密码仍有效而 bootstrap 旧密码被拒、无浏览器 Cookie 的回调与重放被拒，webhook/Pod 身份校验不被登录替代 |
| 实际 GitHub / homelab | 已配置 OAuth App/Secret 并部署 session 模式；真实浏览器关联与 GitHub 再登录、50 个仓库发现、重启保留、真实 push 的两级 CI/Checks/日志/产物通过。详细证据见下文 7.7；不将 fixture 测试计为实际账号验证 |

认证继续内置于一个 Go 控制面与 SQLite/PVC，复用已有 x/crypto；没有新增常驻服务或依赖。新安装清单默认 session，已有显式 open/token 不会升级后被隐式改变。首次 admin 密码为随机值或 Secret 指定，不提供固定默认密码；GitHub Client Secret 和真实授权 token 未写入项目。
部署步骤、凭据/密钥备份和 schema v5 回退边界已更新 deployment/architecture；本批未自动提交或推送 Git。

## 7.7 真实 OAuth App 与 homelab 部署（2026-10-24）

owner 明确授权配置和上线，并确认 `read:user repo` 授权；已在 shitamachi 创建 [Forgelet Homelab OAuth App](https://github.com/settings/applications/3960356)，回调为 `https://forgelet.k.mygo.rocks/api/auth/github/callback`，不启用通配回调或 Device Flow。生成 Secret 的过程中，GitHub 确认页最终返回 404，设置页显示五个 Never used 的 Secret；经 owner 逐项范围确认后全部撤销，重新生成并保留唯一一个。Client Secret 仅保存在本机私有文件及集群 `forgelet-oauth` Secret；GitHub 登录 token 由服务端加密保存，没有写入项目或输出明文。

- **备份和升级**：升级前保存 ConfigMap/Deployment、完整 management version 2，以及 SQLite v4 一致性快照 `backup-ce8e827c9db32251e5b2d3e0008eb3d7.db`；本机副本完整性及 schema=4 检查通过。升级后 v5 快照为 `backup-18c8edbd08cb91c1c6de77eeb2e59e61.db`，完整性、bcrypt、GitHub ID、token 密文与 session 摘要验证通过；旧 server 回退须还原 v4 快照。
- **正式部署**：amd64 server 为 `registry.shitamchi.com/forgelet/server@sha256:27d3bf56eff5aa1320269fd2f5ef91daf49f9c12b6de13a3c3ff0161fc77b75d`，镜像上传重试后成功，远端 digest 与本机 archive 一致。`admin_access: session`，OAuth Secret 只读挂载 server 的 `/auth`；继续使用原加密主密钥，executor digest、并发和磁盘阈值保持原配置。最终 Deployment 1/1 Ready，私有安装清单已同步。
- **部署修复**：首次将 OAuth Secret 命名为现有 `forgelet-auth`，覆盖了该对象声明中的旧键，导致新 Pod `CreateContainerConfigError`。已从原本机私有凭据恢复 `admin-token` / `webhook-secret`，OAuth 改为独立 `forgelet-oauth`；随后启动、重启及真实签名 push 均通过。最终原 Secret 仅保留原两个键，新 Secret 仅保留 OAuth Secret，避免后续配置再次相互覆盖。
- **真实登录**：Edge 使用初始化 admin 登录后关联 GitHub ID `46468521 / shitamachi`；退出立即返回登录页，再点击 GitHub 登录无需管理员密码即可返回 Connections。新匿名 Chromium 只显示登录页；管理 API 匿名和旧 Bearer token 均 401。实际 Set-Cookie 检查 Secure、HttpOnly、SameSite=Lax 和 7 天到期通过；未修改管理员初始密码。
- **仓库与页面运行**：OAuth `/user/repos` 返回 **50 个仓库**，App 仍仅 selected 3 个；页面可搜索账号仓库。用现有 `shitamachi/forgelet-onboarding-e2e` 预检固定 `bde29de9266dfc9812e20efd82a15b7cbf316101`，从已登录页面创建 [e9adf54c82be6880ff446f96](https://forgelet.k.mygo.rocks/runs/e9adf54c82be6880ff446f96)，verify → follow、两个本 App Checks、完整日志和 `onboarding-report` 产物均成功。
- **重启与真实 push**：真实重启控制面后，原 Cookie、GitHub 绑定、加密凭据读取及 50 仓库发现继续有效，完整 management/version 与升级前相同。向专用测试仓库 README 写入 [真实 push 0f50f962](https://github.com/shitamachi/forgelet-onboarding-e2e/commit/0f50f962e64a9ceeef0617b7f99cdaba291a60b2)，签名 webhook 自动创建 [9c9d935f8aa67558297cfdca](https://forgelet.k.mygo.rocks/runs/9c9d935f8aa67558297cfdca)，两级 job、两个 Checks、日志 SHA/标记、下载产物内容均通过。
- **现有 CD 与范围**：既有发布 `5087c75cb5493271b25e301a1e5b0501` 仍 deployed，demo HTTP 返回健康 `v1.10-disk-guard`，磁盘未暂停，最终仅保留控制面 Pod；公网 `/api/runs`、`/api/auth/session` 均 403。本批没有新建发布/回滚或授权其他组织，也没有向 App 新增第四个仓库；当时 fixture 曾错误允许 OAuth token 扩展安装授权；此结论已由阶段 8 诊断撤回，不能当作真实新仓库接入成功。

本批只做正式构建、凭据配置与线上验证，没有修改功能代码；沿用阶段 7 已通过的 make verify / 19 场景 Testcontainers，不重复记为新测试运行。本机私有证据位于 `~/.config/forgelet/live-20261023/auth/`，包含前后数据库、配置、登录与重启状态、运行/Checks/日志/产物和 `final-status.json`。Edge 保留已登录账号页，用户现在可直接使用 GitHub 登录；初始化密码保存在数据 PVC 和本机私有文件，不粘贴到对话。项目未自动提交或推送。

## 上批：GitHub 自助接入第一批

- App JWT 验证本 App、分页读取安装与仓库，metadata-only token；只有完整快照成功才返回，不因失败改变已登记配置。
- `/connections` 提供 GitHub 安装/仓库授权入口、授权后刷新、账号/组织/暂停/权限提示、仓库搜索与逐仓库启用。绑定由后端按 repository ID 权威解析，不依赖用户填写 installation ID 或回调参数。
- 启用后从默认分支解析完整 SHA，逐 workflow 检查兼容语法、profile、触发条件；支持 push、workflow_dispatch 与 inputs。运行重新读取该固定 SHA，创建原有 run/Pod/Checks，网络重试保持幂等。
- 不增加 auth、OAuth、用户、常驻服务或依赖。批量启用、自动 rename 迁移、生命周期 webhook/定期校正及撤权后的排队/凭证清理属于后续批次。
- `make verify` 已通过：Go 构建/race、lint 0 issues、generate、21 个 Vitest、10 组 Chromium；workflow 覆盖率仍为 90.3%。新增 Go 测试覆盖分页、metadata scope、错误 App/仓库/重复列表、部分失败、权限/撤权、固定 SHA、幂等和 SQLite 重启。
- 已用新增 adapter 实际读取本 App，权威目录包含原有两仓库和新建私有 `shitamachi/forgelet-onboarding-e2e`（ID 1431908513）；保持 Selected repositories，仅新增该仓库。真实 App 权限未增加，测试仓库的原生 GitHub Actions 已关闭。
- homelab 部署前已保存 ConfigMap/Deployment 和 SQLite `backup-e0b934494134169cca1243fa6246de66.db`；原应用 `v1.10-disk-guard / healthy=true`。全量 Testcontainers 已通过 18 个场景，1061.235 秒；4 组真实 Chromium 用例 26.6 秒，新向导通过真实 Go/SQLite 创建执行 Pod。镜像上传后按 digest 部署，实际页面与真实 push 结果如下。

### homelab 实际部署与第三仓库验证

- **部署**：正式 amd64 server 固定为 `registry.shitamchi.com/forgelet/server@sha256:e1b79f1d75c361ec2daf8c40ba95c26526c671c485f547160d23c9e6ebd0752f`；Deployment 1/1 Ready，私有安装清单已同步。executor 保持原 digest，本批没有改变执行器。Recreate 切换后首次入口探测短暂 503，等待 ingress 更新后恢复 200。
- **接入页面**：[仓库接入](https://forgelet.k.mygo.rocks/connections) 使用匿名新浏览器验证；实际显示 App 安装和三个 selected 授权仓库。通过页面启用 `shitamachi/forgelet-onboarding-e2e`，后端绑定 repository ID `1431908513` / installation `174462839`；没有手填 installation。随后为原 `forgelet-e2e-app` 补齐稳定 ID 绑定，management 当前 version 2；`forgelet-e2e-gitops` 没有启用 CI，原 target 保留。
- **真实首跑**：[commit 9c52d616](https://github.com/shitamachi/forgelet-onboarding-e2e/commit/9c52d616e160a98a55a589bf6fb310504c273790) 经默认分支预检，点击页面运行生成 [ea9ca03ecafcc71dc6d1d82d](https://forgelet.k.mygo.rocks/runs/ea9ca03ecafcc71dc6d1d82d)。实际 checkout 私有仓库，verify → follow 两级 needs 全 success，两个本 App GitHub Checks 均 completed/success；完整日志包含固定 SHA 与两级标记，下载 `onboarding-report` 后核对 report.txt 中的 SHA。
- **重启与自动事件**：首跑完成后真实重启 Deployment，完整 management 文档/version/仓库 ID 绑定保持一致，匿名入口恢复。随后通过 GitHub 写入测试 README 产生 [真实 push bde29de9](https://github.com/shitamachi/forgelet-onboarding-e2e/commit/bde29de9266dfc9812e20efd82a15b7cbf316101)，签名 webhook 自动创建 [c5189386a3b6085ac95a7baf](https://forgelet.k.mygo.rocks/runs/c5189386a3b6085ac95a7baf)，两级任务、两个 GitHub Checks、完整日志与产物再次成功；未伪造 GitHub 事件或用手动运行替代 push。
- **现有环境**：原 demo HTTP 返回 `healthy=true / v1.10-disk-guard`，磁盘默认保护正常且未暂停；最终没有遗留执行 Pod。工作台/API 仍显式 open，无 auth/用户/OAuth；匿名公网 `/api/runs` 本轮返回 403，未开放公网管理路由。webhook 和 workload 身份校验保持原规则。
- **GitHub 与证据**：App 权限和事件仍保持原值，只新增第三仓库的 selected 授权。新测试仓库为私有，原生 GitHub Actions 已关闭；创建 workflow 时遗留的一条原生排队任务已请求取消。私有证据保存在 `/Users/guo/.config/forgelet/live-20261023/onboarding/`，含配置/Deployment 备份、目录/绑定/运行/Checks/日志/产物、重启前后文档和页面截图；未向项目写入凭证。

本批没有自动提交或推送 forgelet 项目 Git。后续：批量启用、自动 rename 迁移和 installation 生命周期/后台同步，详见 plan 6.3b / 6.5；第一批的实时授权检查只覆盖启用/预检/首跑，不能代替后续撤权队列与凭证处理。

## 接入诊断（本轮实现前）

2026-10-24 使用本 App API 只读核对：账号 shitamachi 的 installation `174462839` 未暂停，repository_selection=selected，可访问仓库仅 `forgelet-e2e-app` / `forgelet-e2e-gitops`；App 权限为 Contents/Checks/Pull requests 写、Metadata 读，事件为 push/PR。
因此其他仓库当前不在 App 授权范围，手填仓库和 installation ID 不能解决权限问题；代码尚缺安装/仓库自动发现与授权导航，且阶段 5 配置页尚未部署。
拟按 Buildkite 的连接入口与 Woodpecker 的选择/激活仓库模式补齐接入引导，详见 plan 阶段 6。上述为实现前的诊断；后续第一批实施和部署结果另记。

## 上批：仓库、workflow 与发布目标配置

- **可操作页面**：新增 `/connections`、`/workflows`、`/targets`。仓库登记 installation、启停、移除及固定 SHA 的 GitHub App 接入检查；workflow 路径、启停、Git 源码读取/控制面 YAML 编辑、固定 SHA/ref/事件/inputs 运行；target 的来源、GitOps、镜像与 Flux/应用字段编辑。
- **持久化与一致性**：配置为 SQLite metadata 单行有界文档，version/CAS 拒绝过期编辑；数据库提交后原子切换运行快照，并与调度 tick 串行。首次保存前从安装配置生成初始值，保存后这三类配置以数据库为准，重启不丢失；数据库快照包含配置。
- **执行语义**：关闭仓库会阻止新事件和已接收待处理事件创建运行；不取消现有执行。每仓库有登记规则时仅执行已启用路径；控制面 YAML 不写回 Git，纯控制面规则无需读取 Git workflow 目录。缺失 Git 文件记录事件错误。已有 run 计划与 delivery 的 target 快照保持原样。
- **校验与边界**：受支持 YAML 字段、依赖/表达式、输入定义和 profile 校验不会被分支/path 不匹配掩盖；实际执行仍按触发规则。路径、重复配置、来源仓库、target、installation/数量/体积均校验。GitHub App 授权和 Secret/profile/基础设施仍由原安装流程管理，页面不回显私钥或 token。

| 验证 | 结果与范围 |
|---|---|
| `make verify` | 通过：构建、Go race、lint 0 issues、generate、TypeScript/格式、20 个 Vitest、9 组 Chromium |
| Go / SQLite / HTTP | 保存与 CAS、重启、关闭仓库前后事件、inline/Git workflow 启停、Git 缺失诊断、非法配置、跨源请求、target 请求使用新配置而已存记录不变 |
| Workflow 单测 | 核心覆盖率 90.3%；Validate 与实际 Compile 分离，非匹配分支/path 仍发现 job 错误，dispatch 必填与错误输入类型检查，实际执行不绕过 filters |
| 浏览器 | 仓库接入检查、workflow 编辑/运行、target 编辑删除/刷新持久化、409 保留未提交内容；原有工作台操作回归通过 |
| 独立 k3s 专项 | 通过，133.519 秒；真实 Chromium 三组用例 26.1 秒，页面 → Go/SQLite → Pod；配置保存后重启，再以签名 push 执行持久化 YAML，深链接与身份边界仍正常 |
| 全量 Testcontainers | 18 个场景全部通过，765.561 秒；三组真实 Chromium、配置重启/签名 push → Pod，及原 Actions、BuildKit、GitOps/Flux、磁盘和保留清理回归通过，临时集群/测试容器已清理 |
| 实际 homelab | 当批未部署；本批已随阶段 6 第一批部署，真实接入结果见上文；原 UI 发布/回滚证据保留于下文 |

没有新增 auth/用户体系、常驻服务或依赖。设计、配置优先级、API 和操作说明已同步 architecture/deployment/compatibility；未自动提交或推送项目 Git。

## 上批：Web 工作台

- **技术选型**：React 19.3.1、TypeScript 7.0.2、Vite 8.3.5、TanStack Router 1.170.46 / Query 5.104.5、Tailwind CSS 4.3.3、Lucide；Vitest 5.0.5、Playwright 1.64.1。版本锁定，Node 24 仅用于开发/构建。
- **页面与操作**：工作台、运行筛选/深链接、needs 分层图、job/step 结果、手动提交 YAML/inputs、取消/重跑、完整日志/产物下载、发布/Flux/GitOps 详情与回滚、vars/secrets、系统水位/保留/备份/清理/事件诊断。
- **低常驻成本**：静态资源嵌入现有 Go server，页面按路由拆包、字体本地提供，无额外服务、数据库或存储组件。
- **无登录模式**：新安装清单显式 `admin_access: open`，同源 JSON 写入；不创建用户、角色或会话。省略该项的旧部署仍要求 token，升级不会自动开放管理 API；webhook 与 workload identity 没有放宽。
- **新增后端**：脱敏配置摘要 API、逐页流式日志下载、SPA fallback/资源缓存/CSP；未知 API 和内部路径不会误返回入口 HTML。
- **实际修正**：真实 k3s 首轮暴露快速任务完成后产物列表未刷新的问题，已在终态转换时刷新并加入浏览器回归；默认选中依赖图起点，避免依赖 API 的任务排序。变量编辑表单补明确可访问名称；小字号文本与状态颜色对比度、移动导航隐藏行为已调整。

| 验证 | 当前结果与范围 |
|---|---|
| Go 单测 / SQLite / HTTP | 通过：显式访问模式、同源/跨源/表单请求、无登录创建/取消/重跑、secret 名称只读、独立 webhook/Pod 身份、600 块日志完整下载与过期、静态路由与缓存 |
| Vitest | 19 个通过：运行过滤、耗时、matrix/循环依赖图、step 结果、日志游标/转义、API 错误与写请求 |
| Playwright 交互 | 7 个通过：桌面/移动导航、URL 筛选与刷新、全部日志分页/文本安全/下载/过期、取消/重跑/manual inputs、vars/secrets、回滚冲突/成功、磁盘/维护/错误重试、终态产物刷新 |
| 正式镜像 + Testcontainers | 18 个场景全部通过，765.511 秒；包含两组真实 Chromium 用例（18.7 秒）、API/Pod、下载、取消、仓库设置及重启，以及原 CI/CD 全量回归 |
| 实际 homelab | 已部署：无登录访问、手动运行/needs、321 块日志搜索/下载、运行中取消/重跑、新版发布及 Web 回滚全部通过 |

自动验证、预览数据与真实环境明确区分。项目变更未自动提交或推送；开发入口、open 模式与兼容边界见 deployment/architecture。

## Web UI：homelab 部署与实际验证

- 内网工作台：[https://forgelet.k.mygo.rocks](https://forgelet.k.mygo.rocks)。显式 `admin_access: open`，匿名页面与 config/status/runs API 为 200，没有新增 auth、用户或会话。公网 `/api/runs` 仍为 404，GitHub webhook 和执行器身份继续独立校验。
- 正式 amd64 server 固定为 `registry.shitamchi.com/forgelet/server@sha256:7b925fdec3329b2ef55143aea4701fc0ad3c3f91c152ab2c3f732714ccd4688c`。Deployment 1/1 Ready；私有安装清单同步镜像和 open 模式。部署前保留 ConfigMap、Deployment 和 SQLite 备份。
- **页面运行与日志**：从手动运行表单创建 `6200e5b4e5e9428cd2e2f76b`，verify → follow 的 needs 执行成功。日志搜索、任务切换、持续读取超过 256 块均正常；点击完整下载得到全部 321 个 chunk，核对首行标记及最后 `320`，没有截断到第一页。
- **页面取消/重跑**：`2be98a4da46da4fa7034ed12` 在 running 时通过确认框取消；页面重新运行创建 `f5a57d2e8fe9fefb2ded0b741433553a`，再次确认 running 后取消。两个记录均 cancelled，原历史保留，无遗留执行 Pod。
- **实际新版发布**：[测试源码 commit 4fd3f5c9](https://github.com/shitamachi/forgelet-e2e-app/commit/4fd3f5c977195fc92272423074286436fab3aee4) 自动触发 run `9ccb08f3b41eba1a35f13eac`，test/build/deliver 全 success。发布记录 `135d78786530fa7b606605305350122d` 在页面显示 deployed；[GitOps commit 0c4f5a14](https://github.com/shitamachi/forgelet-e2e-gitops/commit/0c4f5a14f6dd52ea44ff15b42e77dc6772624aec)，应用返回 `v-web-ui-20261024 / healthy=true`。
- **页面回滚**：在该发布详情点击“回滚到上一版本”并确认，页面跳转新记录 `5087c75cb5493271b25e301a1e5b0501`，最终显示 deployed、Flux 已应用、应用就绪。[回滚提交 4a8225e2](https://github.com/shitamachi/forgelet-e2e-gitops/commit/4a8225e281071b043d0b82b9f8202026ac3f95f9) 与目标/观测 revision 相符；实际 image 恢复为 `sha256:4e3571b3d47ab34e66280ffb293491ab4c138ac2574d1fd4ffd66a105bb9aa9c`，Deployment 1/1 Ready，HTTP 返回健康 `v1.10-disk-guard`。
- **收尾与证据**：调度正常、默认磁盘阈值保留、执行 Pod 全部回收。配置、备份信息、浏览器截图、完整日志、实际状态和 Flux/应用结果保存在本机私有目录 `/Users/guo/.config/forgelet/live-20261023/web-ui/`，主要文件 `final-state.json`、`rollback-final.json`、`full.log`。本批外部源码变更仅发生在专用测试仓库，forgelet 项目未自动提交或推送。

## 上批：1.10 homelab 部署验证

- **部署**：正式 Dockerfile 构建 amd64 server，推送并固定为
  `registry.shitamchi.com/forgelet/server@sha256:8986d4ce612a67ee3108b2a594e8fc2d7090453413f851d73a9c483bba88d230`。
  部署前保存配置、Deployment 和 SQLite 备份，备份完整性检查为 `ok`；升级后控制面和状态 API 健康。
- **压力方法**：在真实数据 PVC 上写入最多 1 GiB 的测试文件，截断为 512 MiB 后再删除。
  暂时将暂停/恢复阈值设为 `515358965760` / `515895836672` 字节；实际可用空间始终超过 479 GiB，验证时没有填满节点磁盘。
- **暂停与重启**：真实 push 接收后，三个 job 在多个调度 tick 内保持 queued 且没有 Pod；低水位重启仍暂停。
  原运行任务的 Pod UID `31aa1f44-f2ed-46aa-a8d4-c6bffaf2624a` 被接管并继续完成；暂停期间日志、运行中/排队取消、Pod GC 和 1 个到期测试备份的清理通过。
- **滞回与恢复**：释放一半文件后，可用空间介于两阈值之间；连续 tick 和再次重启都没有恢复调度。
  删除剩余文件后，同一个控制面 Pod 自动恢复；原 job ID 保持不变，依赖任务成功，真实 GitHub workflow 开始执行。
- **配置收尾**：已恢复默认低于 512 MiB 暂停、达到 1 GiB 恢复，并重启确认；测试文件已删除。
  私有安装清单同步为新 server digest 和默认阈值，旧安装清单与数据库备份可用于恢复。
- **CI/CD 复验**：[源码 push `17bfd096`](https://github.com/shitamachi/forgelet-e2e-app/commit/17bfd09653b8e727d48b4d6e4e8734c3b7fd8e00)，run `fb40be6df24b7f2d3cc774b0` 仅创建一次，test/build/deliver 和三个 GitHub Checks 全部 success；下载的 tests artifact 含 Go 测试 PASS。
  CD `f24955f56d72fe5ed05e8a2e58d5028c` 为 deployed；[GitOps commit `6c903e8c`](https://github.com/shitamachi/forgelet-e2e-gitops/commit/6c903e8cdaf35fe0e97d32deabf2c5752da23884) 与 Flux source / lastAppliedRevision 一致。
  应用实际 image digest 为 `sha256:4e3571b3d47ab34e66280ffb293491ab4c138ac2574d1fd4ffd66a105bb9aa9c`，Deployment 1/1 Ready，HTTP 200 返回 `v1.10-disk-guard / healthy=true`，`/healthz` 也为 200。
- **最终状态**：forgelet 单控制面健康，调度未暂停，执行 Pod 全部回收；三个手工验证 run 分别为 success / cancelled / cancelled。
  本批验证了新版本的健康发布；故障发布与回滚的真实记录继续保留在“上批：真实 GitHub / homelab 接入”。
- **验证记录**：配置/备份、分阶段磁盘/任务/Pod 快照及验证脚本保存在
  `/Users/guo/.config/forgelet/live-20261023/disk-1.10/`（私有目录），完整结果见 `pressure-result.json` 与 `10-final.json`。
  本批构建并部署 server、运行实际集群验证，未修改产品代码或重跑自动测试；1.10 的单测、集成测试和 Testcontainers 结果见下一节。

## 上批：1.10 磁盘低水位保护实现

- **实现**：使用 data_dir 文件系统的非特权可用字节，默认低于 512 MiB 暂停、达到 1 GiB 恢复，可配置。
  中间区间保持状态；探测错误暂停新任务，初次启动/重启须达到恢复阈值。水位状态不额外写入数据库，也没有增加常驻组件。
- **调度**：queued 不预留，新创建 Pod 前再次检查；已预留无 Pod 的 attempt 同样受保护，恢复后使用原身份继续。
  已有 Pod 的接管、结果/日志、取消、失败传播、Checks、GC、CD 和保留清理继续执行。
- **运维**：认证 `GET /api/status` 和首页显示暂停原因/阈值/最新容量；健康检查不因低水位失败。
  安装清单、架构、部署说明同步更新，不将 PVC request 当成硬配额，也不声称覆盖所有写入或其他文件系统。
- **单测与进程内集成**：阈值边界/滞回、探测失败、状态并发读、严格配置、SQLite 零调度额度下失败传播、
  HTTP 结果/日志/取消、Checks、到期备份清理、重启接管/预留恢复、预留后空间骤降通过。
- **完整验证**：`make verify` 通过（构建、race、lint 0 issues、generate）；调度核心（scheduler.go + concurrency.go）覆盖率 89.6%，workflow 89.9%。
  `make test-integration` **17 个场景全部通过，873.615 秒**；涵盖原有 CI、真实 Actions/工具链、BuildKit、GitOps/Flux 部署与回滚，临时集群和测试容器已清理。
- **磁盘 Testcontainers**：实际安装清单的数据 PVC 位于独立 256 MiB 文件系统，测试阈值 32/64 MiB；真实写入、截断、删除填充文件。
  低水位暂停、运行中重启保留原 Pod UID、日志/结果/取消/GC、到期备份清理、恢复区间内再次重启、恢复后并发 2 且无重复执行全部通过（专项 25.04 秒）。
  夹具修正了 k3s 模块覆盖普通 tmpfs 配置的问题，维护验证改为访问容器内实际数据目录，避免假定 PV 必须使用 HostPath；不填满宿主磁盘。
- **当时的集群范围**：该批代码交付使用 Testcontainers 独立 k3s，不读取用户 kubeconfig；后续实际 homelab 升级与验证见上一节。

## 上批：真实 GitHub / homelab 接入

- Edge 创建私有仓库 `shitamachi/forgelet-e2e-app` / `shitamachi/forgelet-e2e-gitops`，关闭这两个测试仓库的原生 Actions。
- GitHub App `forgelet-shitamachi-homelab`（App ID `5441016`，installation `174462839`）仅安装到两个测试仓库；Contents/Checks/Pull requests 读写、Metadata 只读、push/PR 事件。
- 三节点 amd64 k3s v1.33.9；forgelet 单副本与 local-path 10 GiB PVC 在 `forgelet`，应用在 `forgelet-demo`。
- Flux v2.6.4 仅部署 source/kustomize controllers；只读 deploy key 拉取 GitOps，namespace RBAC 限定应用部署范围，没有给 Flux cluster-admin。
- 内网 `https://forgelet.k.mygo.rocks/healthz` 返回 200；公网 `https://forgelet.mygo.rocks/webhooks/github` 无签名 POST 返回 401，公网 `/api/runs` 返回 404。
- Cloudflare Tunnel → Cilium → HTTPS forgelet；Cilium 使用 BackendTLSPolicy 验证内部 CA，执行器仍使用内部 HTTPS。
- amd64 正式镜像推送至现有 `registry.shitamchi.com/forgelet/`，server/executor 配置均固定 digest。
  首次 Docker 上传中断，随后完成重试与 crane 上传；修复后的 server 镜像已实际部署。
- 私有配置及凭证保存在 `/Users/guo/.config/forgelet/live-20261023/`（目录 0700），凭证仅进入 Kubernetes Secret，不写入 Git。
- 首轮真实 push `700ed85` 暴露了默认 checkout 到 root 所有的 emptyDir 根目录会被 Git 拒绝的问题。
  改为执行器创建自己所有的 workspace 子目录；补充默认/嵌套 checkout 单测及真实 k3s 所有权/Git 回归用例。
  原失败 run `fcb489b677741fa84df3496d` 保留，GitHub Checks 正确显示 test 失败、build/deliver skipped。
- 构建中重启真实控制面，原 Pod `7fd2df83-77c0-47c1-a656-81f7bf0cb36d` 继续完成，没有重放构建。
- GitHub 重投 delivery `3b830de0-cedc-11f1-9712-d4023cf649fd` 返回 202；相同 commit 仍只有一个 run 和三个 Checks。
- 此次真实环境使用 direct GitOps；PR/实际 merge SHA 路径仍属于 Testcontainers 验证范围，不混称已在该 GitHub App 上验证。

### 真实链路结果

| 场景 | 源码 / forgelet run | GitOps 与应用观测 |
|---|---|---|
| 健康发布 | 源码 `eeb848d`；run `b5d91300b4ac0d56440dcd72`，test/build/deliver 全部 success | GitOps `def8526`；CD `0437a09ebd0d2d6adbb8f9352b68dd79` 为 deployed，Flux revision 相符，应用 HTTP 返回 `v1 / healthy=true` |
| 故障发布 | 源码 `004e39e`；run `41635b5b2e111015a87945e4`，CI 全部 success，故意让 readiness 返回 503 | GitOps `933b323`；新 Pod 不就绪，Deployment 为 ProgressDeadlineExceeded、Flux Not Ready，CD `153e0492a72ccd83fba5bb44057666d8` 保持 delivered；旧 v1 继续提供服务 |
| API 回滚 | 连续两次 POST 故障 CD 的 rollback API，均返回 `739c5f215d20e184fedc9e7fe369b6a1` | 新 GitOps commit `fc298be` 仅恢复上一 digest，Flux Ready，CD deployed，应用 HTTP 返回健康 v1 |
| 收尾恢复 | 源码 `a5dd050` 恢复健康配置；run `22829178dd435cc84a2ccf66`，三个 Checks 全部 success | GitOps `2d8ba43`；CD `e5cce3aff707be2472a465cc02f4dd4a` deployed，Flux revision/digest/就绪副本一致，应用 HTTP 返回健康 v1 |

表中状态为相应阶段的实际观测；后续投递替代旧记录后，旧记录转为 superseded，历史结果仍可核对。
首次健康与回滚镜像为 `sha256:9b34fc704be244a0f69766dd848d6a030d3b1d5adb43ce2f08a74a4b3b26aadc`，
故障镜像为 `sha256:35cfcb3d6d943ce0b7a04da3581fe84ba48fdf289661eca80f7fa98b90460248`。
恢复源码后重新构建的最终健康镜像为 `sha256:b9728964f76e257abbbe7a9c2f5a919883791215f15801913a9ff3969d9d97cd`。
[最终 GitHub Checks](https://github.com/shitamachi/forgelet-e2e-app/runs/120417659389) 与
[回滚 commit](https://github.com/shitamachi/forgelet-e2e-gitops/commit/fc298bee5359b6769c7483f723bda69f92b33907) 可直接核对。
正常、故障、回滚的 API/Flux/Deployment/HTTP 快照以及 rollback diff 保存在私有配置目录的 `evidence/`。
收尾时应用 1/1 Ready、Flux 两个 controller 各 1/1 Ready，forgelet 仅保留单个控制面 Pod，执行 Pod 全部回收。

### 实际接入批次自动验证

- `make verify` 通过：两个二进制构建、race 单测与进程内集成、lint 0 issues、generate。
- 默认/嵌套 checkout 与 fetch-depth 0/1 的固定 SHA 单测通过；新增 Testcontainers workspace 所有权与实际 Git 操作回归通过。
- `make test-integration` 最终全量通过：**16 个场景，727.420 秒**，包括 BuildKit、artifact/cache、真实 Git/Flux、重启、故障/回滚、真实 Node/Go 工具链和清理。
- 首次全量运行的工具链外网下载超过通用 150 秒等待，并因活跃任务未结束影响后续 retention 用例。
  工具链用例改为单独等待最多 7 分钟，仍保留 workflow 的 6 分钟超时；失败时记录任务日志并取消遗留任务，完整重跑通过。
- 本轮构建并推送了 amd64 正式 server/executor 镜像；Testcontainers 仍使用自己创建的 arm64 临时 k3s，不读取用户 kubeconfig。

## 上批：阶段 3 CI 到 CD

- **artifact/cache**：同 PVC 文件存储，默认 256 MiB/对象、2 GiB 总量、4096 对象；临时上传计入限额，原子发布、重启清理、到期 GC。
  仓库/run/ref/版本隔离，校验 digest、tar 路径和链接；适配 upload/download-artifact@v4、cache@v4/restore/save 的明确输入子集。
- **镜像构建**：`forgelet/build@v1` 调真实 buildctl，job 按需启动 BuildKit v0.20.2 rootless 原生 sidecar；
  registry repository allowlist、Secret 认证和日志脱敏、不可变 digest 输出，失败/取消随 job 清理。没有 Docker socket、hostPath 或 privileged 容器。
  rootlesskit 所需 SETUID/SETGID、提权和 Unconfined 设置仅给 builder，限制与节点前提见 architecture/deployment。
- **GitOps**：`forgelet/deploy@v1` 仅提交预登记 target/digest；整个 run 成功后，控制面以目标仓库 App token 修改固定 Kustomize images 条目。
  SQLite v4 持久化意图与 CAS 版本；固定 base/commit、非强推 direct、固定分支 PR、响应丢失重试、每 target 串行及冲突诊断。
- **部署状态与回滚**：PR 等实际 merge SHA；核对 Git 分支、Flux 来源/revision/generation/inventory、Deployment image 和实际就绪副本。
  区分 awaiting_merge/delivered/deployed/superseded，成功后仍可观测退化。回滚生成新 commit/PR，只恢复先前 digest，保护其他字段和新镜像；重复回滚幂等。
  等待/失败重试每 30 秒轮询，已部署状态每 2 分钟观测，降低空闲时 GitHub/API 请求；新意图立即随 tick 处理。
- **运维与恢复**：增加 artifact 下载、CD 查询/回滚 API，终态 CD 历史清理；queued 投递保护源 run，数据库显式恢复隔离活跃 CD 意图。
  SQLite 备份不包含 blob 文件，PVC/配置/Secret 备份边界已记录。新增可调整的 Flux 只读 RBAC 与 CI/CD workflow 示例。

没有增加 Go 运行时依赖、常驻服务或产品 Docker API；executor 增加 buildctl 二进制，builder/registry/Flux 的资源独立计算。
`golang.org/x/crypto` 因容器测试使用 bcrypt 从间接依赖改为直接依赖；Flux 仅复用 client-go dynamic client。

## 阶段 3 上一批自动验证

| 验证 | 结果与范围 |
|---|---|
| `make verify` | 通过：真实构建、race 单测/进程内集成、integration tag lint 0 issues、generate；包含本批最终回归测试 |
| blob/executor 单测 | 原子发布/重启、配额下重试、TTL 替换/GC、并发首写、隔离、路径/链接攻击、builtin 产物/缓存恢复、成功后保存、build 参数/digest |
| SQLite/HTTP 集成 | 投递持久化、CAS、请求身份/来源/不可变数据、幂等、恢复隔离；queued run 保护和终态 CD 保留清理 |
| GitHub/状态机单测 | Git 对象/限权 token、非强推、响应丢失恢复、PR merge SHA、run 成功门禁、闭合/改动 PR、冲突、回滚及重复请求；远端轮询/失败重试节流不延迟新意图 |
| Flux observer 单测 | 来源/分支不符、旧 generation、revision 不一致、缺失 inventory、旧副本、未就绪、缩容至零、缺失容器均不误报健康 |
| 核心覆盖率 | workflow 89.9%；表达式 91.5%；调度核心（scheduler.go + concurrency.go）89.3%；delivery 84.4%；storage 整包 76.8%，不混称全部包达到 85% |
| Testcontainers CD 验证 | 真实 rootless BuildKit 构建并推送到认证 registry，日志凭证脱敏、失败与运行中取消、Pod 清理；真实 Git + Flux direct/PR、实际 merge SHA、控制面重启、故障镜像及健康回滚全部通过 |
| `make test-integration` 全量 | 通过：15 个场景，710.964 秒；包含最终轮询间隔与取消用例，阶段 1/2 回归全部通过，临时 k3s 和测试容器已清理 |
| `make images` | 通过：正式 server/executor 构建；executor 含真实 buildctl，仅保存在本机，不推送 |

临时 k3s v1.34.4（Linux arm64）安装实际 forgelet 清单，运行真实 BuildKit、认证 registry、Git 仓库、Flux v2.6.4 source/kustomize controllers 和应用 HTTP 就绪探针。
GitHub API 使用测试 HTTPS fixture，其 Git Data/PR 操作落到真实 Git 对象与分支；没有用 API 返回成功冒充 Flux 已部署。
这轮修正了重复重启测试误把 terminating Pod 算成当前控制面的问题，并使测试 Checks API 返回并保存完整身份，避免无效模拟响应。
构建取消测试拼接分块日志，并在长任务输出足够数据后识别执行标记，避免被跨块脱敏尾缓冲影响；确认任务仍在运行后才发取消。
测试程序只访问 Testcontainers 创建的集群，不读取用户 kubeconfig。

以上阶段 3 自动测试交付时，仅对实际环境做过只读确认；真实接入结果见本页“上批：真实 GitHub / homelab 接入”部分。
forgelet 项目代码仍在 `chore/homelab-reset-20261023`，没有自动提交或推送项目 Git；本轮外部提交仅用于两个专用测试仓库。

## 上批：阶段 2.2–2.6

- **Matrix 与并发组**：静态笛卡尔积、include/exclude、max-parallel、fail-fast、job continue-on-error；
  logical needs 等待全部 matrix 变体并聚合结果/输出；workflow/job concurrency 与 cancel-in-progress，SQLite 恢复组约束。
  活跃任务确认停止后释放额度，排队变体取消不创建 Pod，容错后的失败保留原始 result。
- **表达式**：通用插值、typed inputs/matrix、常用字符串/JSON 函数，按接收、调度、执行阶段限制上下文；
  Action 输入作用域独立，明确拒绝未支持语法、字段、引用和能力。
- **真实 Actions**：公开固定 SHA 源码、有界解包和路径检查、action.yml/action.yaml、真实 Node 20/24、
  pre/main/post、inputs/state/outputs、逆序 post、嵌套 Composite、共享 workspace/file commands。
- **工具安装**：固定 setup-go v6.5.0 与 setup-node v6.5.0 SHA，关闭缓存；真实下载 Go 1.25.0、Node 24.14.0，
  检查工具版本、Action outputs、PATH，并运行生成的 Go/JS 程序。executor 改为 Debian/glibc，server 仍为 Go/Alpine。
- **日常触发**：同仓库 PR 使用 merge SHA；push branch/tag/path filters、PR base branch/path filters、typed manual inputs；
  fork PR 忽略，缺失 merge SHA 或无法取得完整变更文件明确诊断。GitHub API 临时故障仍可重试。
- **仓库设置**：管理员 secrets/vars API；secrets 使用 AES-256-GCM，主密钥单独挂 server，SQLite 仅存密文，
  计划仅存引用；按任务身份下发引用值并预注册 mask。vars 在创建 run 时快照，重跑读取最新 secrets。
- **数据迁移与交付**：SQLite v1/v2→v3 事务迁移；安装清单支持可选加密主密钥；部署和兼容文档同步更新。

没有增加 CRD、常驻 Node 服务、外部数据库或队列。公开源码与工具下载会使用外网，工具缓存仅在单个 job 生命周期内存在。
版本、输入、取消/pre/post 差异等精确边界见 [compatibility.md](compatibility.md)。

## 阶段 2 历史验证

| 验证 | 结果与范围 |
|---|---|
| `make verify` | 通过：两个二进制构建、`go test -race ./...`、integration build tag lint（0 issues）、generate |
| 编译/表达式单测 | matrix 展开/边界、早期值、活动类型/filters/inputs、metadata/路径/输入诊断、短路与类型、Action 上下文隔离 |
| 执行器单测 | 真实 Node/Composite、pre/main/state/outputs/post 顺序、普通失败后清理、post 失败、取消/超时、未知输入/路径逃逸/递归限额 |
| SQLite/HTTP 集成 | matrix 额度/fail-fast/COE、两级并发组、pending 替换、跨依赖阶段组占用、密文绑定/篡改、远程执行器领取/脱敏与输出保护 |
| GitHub adapter 测试 | PR merge SHA、fork 忽略、固定 before/head 比较/重命名、300 文件截断诊断、永久错误与临时重试区别 |
| 核心覆盖率 | workflow 89.9%；表达式 91.5%；调度核心（scheduler.go + concurrency.go）89.3%；storage 整包 78.3%，不混称全部包达到 85% |
| `make test-integration` | 通过：13 个场景，约 292 秒；包含 Action 整体 inputs/pre-if、matrix 排队/活跃取消的最终回归；测试集群已清理 |
| 临时 k3s 范围 | 并发/DAG、身份/取消、重启恢复/Pod 丢失、签名 push/PR/filters、matrix 活跃/排队取消、并发组重启、Node 20/24/Composite/post、secrets/vars/dispatch、真实 setup 工具链、保留清理 |
| `make images` | 通过：正式 `forgelet-server:dev` / `forgelet-executor:dev` 构建；仅保存在本机，不推送 |

容器测试使用 `rancher/k3s:v1.34.4-k3s1`（Linux arm64）、正式 Dockerfile 环境及实际安装清单。
GitHub App API 由测试 HTTPS sidecar 模拟；公开 Action 源码、工具链下载与执行是真实的。
本批测试发现并修复了 GitHub 归档全局 PAX 记录解析、macOS 临时目录符号链接、跨依赖阶段并发组占用、matrix 全活跃且无排队任务时的 fail-fast、
Action 输入作用域、整体 inputs/matrix 表达式及 pre-if 状态、PR 默认 activity types 与永久比较错误重试等问题，并保留回归用例。
原有日志重试、进程组取消、动态脱敏、幂等和清理测试继续通过。

阶段 2 的所有集群操作仅发生在 Testcontainers 创建的临时 k3s，没有读取用户 kubeconfig。
当时没有创建真实 GitHub Check、部署到 homelab、推送镜像、提交或推送 Git；该批变更保留在 `chore/homelab-reset-20261023`。

## 仍未完成

- 磁盘保护只控制新任务调度，运行中写入及去重身份仍会占用空间；其他文件系统不在 data_dir 水位检测范围内。
- 完整 Marketplace Actions、远程 reusable、动态 matrix、artifact v4、OIDC、fork PR、多租户和 HA 尚未承诺；正式 act profile 已支持 Docker Actions，同仓库 reusable 已实现，具体边界见 compatibility。
- 尚未测量常驻资源；requests/limits 是起始配置，本轮不安排资源基线门禁。

## 下一阶段

Web 工作台的独立 k3s 验证与 homelab 实际部署均已完成；真实环境使用含 Web 的 server，保留默认磁盘阈值。
按 [plan.md](plan.md) 和实际使用需求继续扩展 workflow/Actions。
真实测试仓库与集群部署可继续复用；保持单测、集成测试、Testcontainers 和进度文档，不引入 spec 审批或资源基线流程。

## 旧实现恢复信息

清理前 HEAD：`1b41037e386708d3595680f9d661415fa318c538`。
Git 历史仍在，没有重写 main 历史。

本机仓库外备份目录：

```text
/Volumes/Dev/codes/go/forgelet-backups/20261022T174154Z-before-homelab-reset/
```

目录名用 UTC，对应本地 2026-10-23。包含 `working-tree.tar.gz`、`history.bundle`、
`staged.patch`、`unstaged.patch`、`status.txt`、`sha256.json` 和恢复说明。
归档包括本地配置，保持私有，不提交进仓库。需要恢复时先解到另一个空目录核对，避免覆盖新工作。
原 .idea 等本地编辑器设置保留；清理的是旧产品与开发部署实现。

## 2026-10-28 03:29：Tasks 模块下载连接阻塞诊断

fe3镜像入口的自主重跑5cbd3fb3在go mod download阶段等待三个公共模块，持续约7分钟没有写入ZIP；此时Go RSS约23MiB、引擎CPU空闲，不能沿用冷编译资源不足的归因。采集到三个下载goroutine均等待同一个net/http HTTP/2连接的roundTrip；未发现这三个请求等待文件锁。同容器新进程分别使用原代理、HTTP/1.1与官方代理下载能够成功，未改实际业务模块缓存或关闭sumdb。

采栈对我们自主重跑内的go mod download发送SIGQUIT，因此5cbd3fb3最终failure属于明确的诊断中断，不能称原workflow自然失败或业务断言失败；原外部push及其他任务没有取消。对照随后在同Pod自有引擎内以独立0.5 CPU/256Mi临时容器，三组空缓存分别下载三个模块，共9次通过模块校验、耗时19.9秒，容器已自动清理。

03:30已仅为homelab ubuntu-latest工具镜像设置GODEBUG=http2client=0，保留原goproxy.cn与全部镜像层，digest3d9cc8dc…；这是连接复用问题的运行环境绕行，尚未证明代理或Go实现的唯一根因，也不更改项目默认网络配置、业务workflow、模块校验或显式20分钟超时。后续实际原workflow结果另行记录。API8ca9已进入starting，Tasks原快照CIf495正在运行；剩余PG/Mongo及Rust验收继续待完成。

该工具镜像配置已rollout，核对原活跃Pod UID未改变，CPU1/3、内存4Gi、data24Gi、并发2、worker-only、镜像别名与supervisor c021保持。新的原固定计划镜像重跑c43538a7已入队，仍为fe3提交；不把入队视为通过。当前CIf495已通过模块下载，进入go mod verify，使用其启动时的原网络设置。

## 2026-10-28 03:50：超时后的停止确认与冷编译优化（实现/验证中）

Tasks CIf495在原race步骤期间最终interrupted，server记录executor stopped without an acknowledged result；原检查/构建成功9分50.97秒、Atlas59.52秒、迁移1.64秒，未获得race断言结果。事件无Evicted/DeadlineExceeded，监控采样的引擎/执行器OOM计数均0、引擎峰值working set约3.2GiB；executor于03:25:08启动，engine于03:45:20被正常停止。这与20分钟执行预算和10秒清理窗口相符，但终态没有ACK，仍按interrupted保留，不能改写为确认的timed_out或唯一根因已证明。

代码发现停止确认使用docker rm --force，会把大overlay文件系统删除也计入10秒窗口。新增修正只列出运行中容器、SIGKILL后再次确认无运行容器，再允许终态回传；整个私有引擎及磁盘随Pod回收。退出竞态以最终观测为准；kill失败且仍运行、最终观测失败均继续fail closed。新增空引擎、运行容器、退出竞态、列举/kill/确认失败、仍运行及context取消回归，race专项PASS2.904秒。真实临时k3s超时场景增加独立后台容器及256MiB可写层，要求保留timed_out ACK及清理；make verify已PASS（Go race/进程内集成、构建/lint/generate、35前端单测与14浏览器31.4秒），新一轮完整Testcontainers正在运行。尚未部署这段新源码，之前PASS1654.999秒对应1842d17，不能代替本次结果。

同时准备仅含Go1.26.4公共标准库普通/race编译缓存的homelab工具层，在本机有界Go并行度构建，不装业务模块、不保存任何业务token或私有源码、不更改原workflow的20分钟限制。该优化仍需实际完整运行验证，不能预先视为解决冷启动预算。API8ca9原test已开始执行，HTTP/1.1工具镜像的Tasks image重跑c43538a7排队中，业务验收未完成。

03:59追加：make verify已完整PASS。新执行器06162882…（Go1.27.3从8220b7c原生交叉编译executor，act-worker与c021基础镜像层保持）及工具镜像1f8ecd0f…已rollout，活跃Pod UID/全部预算和别名保留；server仍为749b0bc6。公共标准库额外压缩层84.4MiB；3核设置下普通/race net/http均复用缓存，合计1.2秒，无重新编译。仅自主旧工具重跑c43538a7明确cancelled留存，非原业务push；新同fe3/固定原计划CI e35240de及image e4bf1c97已入队，原20分钟不变。原API镜像任务已先分配node3，原test已success；现有任务保留其创建时镜像，不回写。完整临时k3s仍进行，尚不把新源码的完整集成验证记为通过。

## 2026-10-28 04:13：API 原 workflow 完整验收通过

b8f4702原固定计划重跑8ca9acc3最终success：test与build-and-push均成功，原检查/coverage与Go缓存true保留，Codecov无token的上传仍为原软失败，不声称外部上传成功。镜像digest5da8352665c7607968f39582b67ef6636ea8a7456573322736ba5583018916f9，latest与b8f4702d标签及Action输出一致，GitHub Checks核对完成。旧2af5的test成功/镜像缓存400失败历史保留，不能把旧attempt改为成功。此次实际镜像Pod仍是先前c021执行器快照，确认XML清单修复在原业务流程中成功；新0616停止确认另由临时k3s与后续业务任务验证。API托管Actions已恢复。Tasks两原入口、手动PG/Mongo及Rust仍待验收；新源码全量Testcontainers仍进行。

## 2026-10-28 04:17：停止确认修正后的完整自动回归通过

8220b7c完整Testcontainers PASS1575.496秒：27个必选叶场景通过，2个需外部私有业务目录的可选fixture跳过，独立SDK归档checksum拒绝场景通过；act251.19秒（含后台helper/256MiB写层后的真实超时终态ACK、重启、取消、失败和缓存）、Rust102.47秒、384MiB缓存185.66秒、自动CD96.50秒、真实SDK12.18秒、浏览器35.48秒、GitHub登录/授权/重启103.78秒、GC16.30秒。临时k3s7d0ee39dfec5已清理。此前make verify与race专项通过，现有源码自动回归齐备；homelab公共标准库层属于另行构建/复用验证的实际环境优化，未将其算作临时集群SDK外网下载成功。

真实API验收已通过并恢复托管Actions；Tasks e352/e4仍在运行/排队，原20分钟不变，手动PG/Mongo和Rust原业务workflow尚未结束。自动测试成功不能替代这些实际业务结果。

## 2026-10-28 04:45：准备预算与停止状态延迟修正（验证中）

Tasks原固定fe3计划的CI e35240de最终interrupted，实际supervisor stderr明确记录owned act containers are still running；静态检查/编译9分21.33秒、Atlas59.90秒、迁移1.41秒，已进入race，尚未获得断言结果。原20分钟包含此前约7分钟runner及镜像目录准备。公共标准库层有所复用，但不足以单独解决总预算；保留失败历史，不改写为已确认的业务断言失败。自主排队image e4bf1c97已明确cancelled以便换新策略重跑，不取消外部push。

新实现将管理员镜像准备独立限制15分钟，之后完整保留原workflow预算；Pod deadline同步增加有界准备额度。杀停改为5秒信号请求加20秒总窗口内轮询确认状态，继续fail closed，不等待overlay删除。专项race PASS15.389秒（executor）/1.574秒（kube）；make verify完整通过，含35前端单测与14浏览器31.3秒。新的真实临时k3s增加同Pod准备时冻结executor65秒、超过原workflow一分钟后恢复，以及busy Node/2000小文件/256MiB后台helper的超时ACK回归；专项与全量尚待完成，尚未部署新源码。

Rust原workflow已由真实push 8e265947触发run50d91035，正在编译；原publish=false保持，不能把触发视为验收成功。API完整原workflow已通过，Tasks两个入口及手动PG18/Mongo7仍待完成。

## 2026-10-28 04:56：新准备策略专项通过并部署

c1d1929已提交/推送；真实临时k3s专项PASS507.893秒，act子场景342.68秒，包含准备时冻结executor65秒后同Pod成功执行原一分钟workflow，以及busy Node/2000小文件/256MiB helper超时ACK、缓存、取消、重启及失败；临时k3s30b53a078448和registry19d383bba192均已清理。全量Testcontainers另行进行，不能将专项称全量通过。

已rollout server724e776d…与supervisor083b2a4d…，保留ubuntu工具1f8ecd0f/Rust工具、worker selector、CPU1/3、内存4Gi、data24Gi、并发2及原活跃Pod UID。Tasks原fe3/固定计划CI新7f025d00、image86901c7e已入队，仍为原20分钟；既有失败/取消attempt完整保留。Rust50d91035原test九步成功，image构建进行中。etcd当前无告警、leader=1，P99 fsync约30.6ms/commit50.3ms；该现场健康检查不替代剩余实际业务验收。

## 2026-10-28 05:02：Rust 原始 workflow 完整通过

真实push8e265947/run50d91035的原test九步及image五个执行步骤均success，手动publish条件在普通push下按原定义skipped。release镜像本地ID004cbb4536dafdb8ee804fed992b54134f18df31ac6b269a8e497ae7096e6f83；原smoke明确PASS容器UID、只读root、health/HTTP/metrics、SIGTERM及脱敏。managed-test-artifacts以原Dockerfile target构建3分23.82秒，随后在同release镜像内原两项managed测试全通过20.14秒，涉及双transport drain/retry与SIGTERM真实pending job关闭ACK；没有连接真实Steam、执行生产operator或发布/部署。GitHub Checks已核对success，托管Actions恢复。

环境使用显式Rust/PowerShell profile及已记录的homelab自定义Rust1.97 Debian builder alias，不声称该alias与上游官方镜像字节相同。该实际任务启动于本次准备预算部署前，保留当时supervisor快照；新source c1d1929由专项及仍在执行的全量回归另证。Tasks原两入口与手动PG/Mongo仍待完成。

## 2026-10-28 05:24：新准备策略完整自动回归通过

c1d1929全量Testcontainers PASS1653.055秒：27个必选叶场景通过，2个依赖外部私有业务目录的可选fixture跳过，独立SDK checksum拒绝场景通过。act339.39秒（含65秒准备冻结/原一分钟执行预算、busy Node与大写层超时ACK/取消/重启）、Rust98.68秒、大缓存217.61秒、自动CD97.90秒、真实setup SDK11.96秒、浏览器56.18秒、GitHub登录授权/重启83.05秒、GC17.27秒。公共Action源在持久化前两次503，第三次有界重试成功，不跳过原Action或运行断言；SDK为校验后官方归档预载，不计公网下载。临时k3s3a08cb3cbb1a与registrya7168f1f8692均已清理。与本次make verify及专项组成完整自动验证；真实Tasks的CI/镜像/手动PG-Mongo仍需独立收尾，不能以自动测试替代。

## 2026-10-28 05:30：Tasks 冷路径预算仍不足，限定 worker CPU 突发上限

3CPU原固定计划7f025d00明确timed_out；静态检查/构建9分39.10秒、Atlas59.52秒、迁移0.95秒，race已有多个handlers与internal包通过，但原20分钟内尚未全部结束。supervisor实际stderr记录job finished with state timed_out，终态ACK/杀停确认成功，之前interrupted问题本轮未重现。不能因此宣称冷workflow通过，也未延长原20分钟或删减测试。

核对节点及现有准入后，只将homelab ubuntu-latest引擎CPU limit由3改为worker物理4核边界，request仍1；Rust保持3。内存request=limit4Gi、act-data24Gi、worker-only及并发2保留，仍依赖节点准入/真实余量，不保证繁忙worker能持续提供4核。当前Pod不回写；自主旧3CPU image重跑86901c7e明确cancelled留证，新同fe3/固定计划CI7d1b8239、image54637ffc已入队。只替换本次自主诊断运行，不取消外部push。

变更前etcd无告警/leader1、P99 fsync39.35ms/commit53.74ms，Web HTTP200/Deployment ready1/Flux revision564223da和原镜像digest保持。本次仅部署配置调整，源码自动回归仍为c1d1929的make verify、专项及完整Testcontainers通过；4CPU实际可见性、原workflow成功与后续PG/Mongo仍需另证。

## 2026-10-28 06:12：Tasks 原始 workflow 的两处失败已定位

4CPU可见性已在真实Pod验证（内核quota=400000/100000、Docker NanoCpus=4e9、Node availableParallelism=4，内存4Gi），原固定fe3计划CI 7d1b8239最终failure而非timeout：静态检查/构建9分25.02秒、Atlas60.23秒、迁移1.10秒、race8分32.73秒返回exit1，checkout post及终态ACK正常。race暴露internal/lake初始化SIGILL和online/engine的TestEngineStopsWritingWhenFenced时序断言。回查此前3CPU 7f025d00日志也已有lake SIGILL；其最终超时属实，但不能将全部问题归因为CPU预算。

engine生产实现会先取消并join旧worker，再报告OwnerLost；原测试在异步提交尚未确认fence时就记录读计数，可能把合法的fence前读取误判。业务库本地测试修复7c5d09a等待这个现有quiescence信号，不改生产逻辑或删除断言：整个engine包race重复30次PASS38.826秒，目标回归race重复200次PASS26.550秒；尚未推送，原Linux/Go1.26.4 workflow待重新验收。

node3是无AVX/AVX2的Intel N5105；校验官方发布SHA256后，DuckDB2.0.0独立CLI在同worker的限额临时容器中同样SIGILL，ptrace记录故障指令字节c5f81005…（AVX VEX编码）。[上游2.0构建参数](https://github.com/duckdb/duckdb/blob/v2.0.0/scripts/architecture_flags.cmake)默认Linux amd64 CLI采用x86-64-v3，而EXTENSION profile采用x86-64-v2；该硬件与原生依赖二进制不兼容，不能通过调整Go并发修复。正在本机限额交叉编译同版本/固定源提交的兼容共享库（初始2CPU/4Gi，后改4CPU/6Gi与3个编译任务；集群未参与编译），拟通过[官方duckdb_use_lib机制](https://github.com/duckdb/duckdb-go-bindings/tree/v0.20000.0#dynamic-libraries)显式配置可选执行镜像；尚未完成或部署，不提前称成功，不替换模块缓存或降级依赖。

另一个原fe3镜像入口54637ffc在静态检查/构建8分19.92秒成功后，下载Atlas遇到curl TLS错误35，按原定义failure、镜像任务skipped，未进入race；这是独立外部下载失败，不能冒称重现同一业务断言或镜像发布成功。上述自主诊断历史保留。当前etcd无告警/leader1、P99 fsync26.6ms/commit49.4ms，Web HTTP200/ready1、Flux564223da及部署digest保持。源码c1d1929自动验证已齐备；Tasks原CI、镜像和手动PG18/Mongo7仍未完成。

## 2026-10-28 06:24：DuckDB CPU 兼容工具镜像验证并部署

新增可选`tools/duckdb`构建脚本/镜像配方、原生C API探针及独立Go驱动Testcontainers回归，不增加控制面依赖或常驻服务。DuckDB仍为2.0.0/固定源d54cbbe3df17e8e5ce68312f483b4155836112aa，源归档SHA256 e2b89ce6…；Debian12 GCC12跨编译的EXTENSION/x86-64-v2共享库SHA256 9b758e8a960213b19e3b653848a27b4717351bdcec96d3dc33c49d977a507b73，包含core/json/parquet/icu。管理员显式选择的镜像设置官方duckdb_use_lib链接tag/搜索路径，原workflow和Go模块校验不改；不承诺其他DuckDB API版本或默认预编译包的所有可选扩展。

自动验证：本批make verify完整PASS（Go race/进程内集成、构建/lint/generate、35前端单测、14浏览器32.8秒）；独立Testcontainers PASS35.310秒，真实Go1.26.4驱动race重复两轮，版本/聚合/JSON/ICU时区/Parquet写读均通过，先执行原生C API聚合探针。该amd64容器在本机arm64环境运行，不单独作为物理CPU证明。此前c1d1929核心完整临时k3s回归PASS1653.055秒仍对应当时源码；本次没有改控制面或执行协议，不将新增可选工具镜像专项称为全量k3s重跑。

实际集群验证：候选固定镜像在node3/N5105以独立非root、只读root、无Kubernetes token、0.5CPU/256Mi Pod执行原生SQL，明确PASS sum=4950；现场flags确认无AVX/AVX2，probe Pod已删除。镜像zot.k.mygo.rocks/forgelet/executor-act-web-tools@sha256:6ace4272df85694d4c9addd94eed64e3ce7c8d69fa04bc414c8706a6c8edc01f新增压缩层47,178,231字节，固定基础1f8ecd0f及公共Go标准库缓存保留，没有业务源码/模块/token预热。兼容库在本机4CPU/6Gi上以3个编译任务完成，未占用集群编译。

已只替换homelab Ubuntu runner镜像并rollout，server724e776d/supervisor083b2a4d、Rust镜像及CPU1/4与1/3、内存4Gi、data24Gi、并发2/worker隔离/所有镜像别名保持。业务测试同步修复7c5d09a已真实push；三个workflow YAML与原fe3字节一致，GitHub原CI1b17a3c4和image221882fe已接收并进入运行/等待。该触发不是成功验收，原CI/镜像、Checks及手动PG18/Mongo7待实际结果；10.6e/10.7b继续未完成。

06:33追加：实际CI Pod804eb421的job容器镜像ID6659fec9与候选工具一致，直接读取Go环境确认duckdb_use_lib、固定库搜索路径及HTTP/1.1设置生效；setup-go原cache-hit=false，正在原静态检查/构建阶段。独立配方增加显式amd64 runtime平台（源码仍用build-platform交叉编译），Dockerfile frontend检查无告警；不改变已部署产物。仅本次自建的本机交叉编译容器已清理，owner的lg-multiarch builder保持。

## 2026-10-28 07:00：真实 Tasks 结果与 AVX worker 调度方案（等待额外部署授权）

7c5d09a真实冷CI1b17a3c4的静态检查/构建9分28.06秒、Atlas60.92秒、迁移1.43秒成功；race明确`internal/lake` PASS1.073秒，原DuckDB初始化SIGILL没有重现。原fencing测试未再报告失败，但同包`TestTranscriptUnclassifiedBacksOffWithoutCharging`暴露独立时序问题：第四条CM记录在fetch goroutine返回前生成，测试立即stop，可能取消尚未处理该结果的event loop，导致原golden四条attempt paused实际只有三条。最终workflow仍到原20分钟timed_out，race部分失败和超时同时留证；不能称原workflow成功或只有CPU不足。旧原image221882fe属于我们自己的7c验证push，因已知需要下一测试修复而明确cancelled，未取消owner额外push。

业务修复c836ca1等待第四个已处理的attempt paused事件后停止；保留原超时、零commit断言和golden字节，不改生产逻辑。目标race重复500次PASS30.111秒，整个engine race重复30次PASS38.625秒。已推送[独立修复分支](https://github.com/shitamachi/steam-tasks/tree/codex/tasks-transcript-quiescence)，尚未推送remote main，以免在未准备好的环境重复触发已知失败；原三个workflow YAML保持。等待确认期间已恢复Tasks原GitHub托管Actions设置enabled=true，其他已验收仓库不改，未创建手动PG run。

完整数据库验收还有硬件前提：[MongoDB官方说明](https://www.mongodb.com/docs/manual/administration/production-notes/)要求现代amd64的mongod使用AVX，换DuckDB库并不能让Mongo7获得该CPU能力。实际只读检查node2现有系统Pod中的/proc/cpuinfo，确认i5-4210H具备avx/avx2/fma/bmi1/bmi2；node3/N5105缺AVX。node2现有预留为3300mCPU/7214Mi内存，allocatable为4CPU/10032660Ki，不能承接原1100m/4352Mi执行Pod。没有降低任何已有服务或CI的内存预留来制造容量，也没有向控制面安排重任务。

已准备未执行的方案：Windmill app无数据卷、单replica，现有限制2Gi（Pod默认request同为2Gi）、RollingUpdate maxUnavailable=0/maxSurge=1且Ready。只新增nodeSelector使它从node2滚动到node3，所有image/env/资源/数据库与worker配置保持；服务端dry-run通过。然后Ubuntu CI只选择已确认AVX2的worker，CPU request500m/limit4、内存request=limit4Gi、supervisor100m/256Mi，合计600m/4352Mi；node2预期总预留3900m/9518Mi，在当前allocatable内，仍需执行前重查容量与业务健康，不能保证CPU突发吞吐。Rust和并发2保持。迁移涉及Forgelet之外的Windmill服务，因此已单独向owner请求确认，未修改Windmill、节点标签或该CI配置；等待时间不作为授权。proposal/dry-run与容量计算保存在本机私有证据目录，未提交潜在配置凭据。

当前所有本批执行/诊断Pod和本机临时编译/Testcontainers容器已清理，owner的lg-multiarch builder保持。最终etcd无告警、leader1、P99 fsync12.0ms/commit15.3ms，Web HTTP200/ready1、Flux564223da和部署digest保持。10.6e/10.7b仍未完成，剩余为准备AVX容量、合入测试修复后原CI/镜像/Checks/缓存及手动PG18/Mongo7；不以本地回归或计划替代实际运行。


## 2026-10-28 10:16：跟随 Tasks 移除 MongoDB，撤销额外迁移方案

owner确认已移除MongoDB，已fetch并核对main最新04f3dec（移除提交40af05d）。当前运行代码、Go依赖与容器测试不再使用MongoDB；少量历史说明及拒绝旧backend的配置测试不构成MongoDB运行依赖。原reliability.yml仍保留20分钟、完整short/race、手动PostgreSQL输入与postgres:18-alpine。因此本轮不再要求Mongo7/AVX，不执行Windmill迁移、不改node标签或CPU request；旧方案标记superseded，业务代码更新优先于此前迁移授权。

owner04f3dec的原CI dfe66cb8已完成但failure：queue容量取消测试在worker尚未claim时取消，断言期望1个已执行任务、实际0；不是MongoDB或DuckDB非法指令。原image050d1da4仍运行，保持原任务，不取消owner push。候选只修测试同步：transcript等待已处理事件、queue先等执行回调开始，再保留原取消/claimed/pending断言；workflow与golden字节不变。目标queue race500轮PASS7.536秒；相关两包short/race30轮PASS（queue4.244秒、engine36.605秒）。真实PG18回归尚在运行，候选尚未push main，不将局部测试称完整原workflow验收。


## 2026-10-28 10:38：统一 transcript 结束条件与匹配 CGO 的标准库缓存

owner04f原image050d1da4最终timed_out且原image构建skipped；race还记录TestTranscriptExplicitNoPlayerCountSleeps缺少dormant事件，不能只归因为冷CPU耗时。该用例与此前unclassified一样，在worker记录commit后、event loop处理结果前就stop。候选业务修复bd3cdc3统一相关场景，要求原commit条件和最后attempt语义事件同时满足，resource fault等待已处理pause；原golden/超时/生产代码不改。全部transcript race重复300轮PASS77.006秒；相关engine/queue short-race30轮PASS39.840/4.270秒。前一候选c942本地全仓库short/race51个有测试包通过（Go1.27.3/darwin arm64），queue真实PG18 Testcontainers回归PASS98.654秒，均与尚待Linux原workflow分别记录。自主c942两入口已明确cancelled并留证，为完整修复/新工具镜像重跑做准备；owner运行未取消，托管Actions已恢复。

独立断网镜像检查确认：工具层设置CGO_LDFLAGS后runtime/cgo出现build ID mismatch，依赖它的部分公共标准库缓存失效。新增tools/duckdb/warm-std.sh，在最终链接参数下只重建Go1.26.4公共std的普通/race缓存，并检查两种模式没有stale项。构建发生在本机2CPU/3Gi、无网络容器，不含应用源码/私有模块/token，也不伪造setup-go仓库缓存命中。只导出新增275个hash缓存文件与脚本，不打包其他容器差异；额外压缩层18,578,348字节。候选固定镜像c1726fc1保留原6ace的DuckDB库和全部运行配置。

自动验证：make verify完整通过（含35前端单测、14浏览器30.3秒）；候选真实驱动Testcontainers PASS35.509秒，新增标准库两模式有效性断言，原native SQL及Go1.26.4/race×2、JSON/ICU/Parquet全部通过，临时容器已清理。该工具层专项不冒称重跑完整k3s；控制面/执行协议未改，此前核心完整回归1653.055秒仍单独留证。尚未部署c172工具、未push业务bd3，原CI/发布/PG18继续待真实结果。


## 2026-10-28 11:33：node3 原测试通过但缓存收尾超时，执行已批准的容量迁移

c172工具镜像已部署（只增加匹配CGO参数的公共std缓存），实际node3 job镜像ID40816453…与候选一致。业务bd3cdc3真实push触发原CI080dbb1d和image9a301746；CI原静态检查/构建8分54.286秒、Atlas68.728秒、迁移0.900秒、完整short/race8分9.962秒全部成功，engine3.120秒/queue5.505秒/lake1.150秒，原golden和测试断言不改。Codecov无私有仓库token，外部上传失败，按原fail_ci_if_error:false返回success13.495秒，不计外部覆盖率上传成功。然而原20分钟在setup-go post的Go缓存tar/zstd打包阶段到期，叶任务timed_out、整个CI failure，不能把测试通过称完整CI通过。自己的同批image9a30已明确cancelled避免再消耗已知紧张的冷路径；owner运行未取消。

MongoDB移除后原AVX理由已撤销，但新实测证实N5105上的冷编译加缓存收尾仍超预算。因此沿用owner已明确批准的同一Windmill滚动迁移/容量方案，目的改为验证较快node2上的原workflow；未再次扩大操作范围、未放宽原20分钟、未减少检查或完整4Gi内存预留。Windmill app仅新增nodeSelector指定node3，保留原2Gi、无数据卷、maxUnavailable0/maxSurge1、镜像/env/其余spec完全相同；新实例拉取外网慢时旧实例一直Ready。经现有SSH管理账号，将node2缓存的同一镜像以15MiB/s上限流式导入node3的containerd，传输1,472,793,600字节，无额外Pod/hostPath/凭据/镜像地址修改。新旧imageID严格相同de85c0d6…，node3 Ready1，旧实例已退出；服务根路径HTTP200、api/version确认1.789.0。

迁移后重新核对node2预留3300mCPU/5166Mi内存，allocatable4CPU/10032660Ki；执行Pod合计600m/4352Mi后3900m/9518Mi，满足当前准入。Ubuntu engine CPU request500m/limit4、内存request=limit4Gi、data24Gi，仅选择execution=true且hostname=node2；supervisor100m/256Mi、并发2保持。Rust、server/supervisor镜像、runner c172、镜像目录和其余配置逐项对比不变，没有新增AVX节点标签。node2 root空闲约67Gi，未把工作转移到控制面。Windmill只迁移已有常驻服务，没有新增空闲执行服务。

同一bd3真实push的不可变计划已通过管理API重跑CI5865abfb/image7aaf3b92，保留原失败/取消及固定Action SHA；不是重新解析tag或伪造首轮通过。CI Pod ef5f66fc已实际调度node2并进入原workflow，最终CI、缓存、镜像、Checks和手动PG18仍待结果，10.6e/10.7b继续未完成。当前etcd无告警/leader1，P99 fsync30.4ms/commit53.2ms；Web HTTP200、Flux564223da及业务部署digest保持。所有本机镜像缓存准备容器已清理，owner lg-multiarch builder保留。


## 2026-10-28 12:37：原始 CI/镜像已验收，大 Go 缓存需要提升有界对象上限

同一bd3提交在node2重跑CI5865abfb/image7aaf3b92均success；静态检查6分13.901秒、Atlas59.774秒、迁移0.708秒、完整short/race6分26.866秒，缓存post1分14.364秒正常返回并收到终态ACK，首次明确完成原20分钟冷CI。原image callee及build-push全部成功，GitHub Checks completed/success；registry.shitamchi.com/steam-task的latest和sha-bd3cdc3与Action输出digest f557195fc3b0e28b8906b80515d148d121c378ca5dbef63b7f0ee406ff8a6a38一致。可复用调用的call_gate/call_result只有控制结果、没有shell step，验收脚本据此分别检查gate成功与叶任务完整真实step结果，不将其误判为缺失执行。

仍未验收热缓存：实际Go归档超过当前1Gi对象上限，setup-go cache-save警告cache object size exceeded后按原定义成功返回。不能把这次CI成功当作缓存保存成功。原仓库YAML不改，正在把显式缓存对象上限改为2Gi，默认256Mi、artifact256Mi及全局总存储预算保持；v2 block上限按对象预算有界到2048，暂存主预算仍对象两倍。跨worker缓存传输与认证后读取期限使用5分钟，artifact保持2分钟、传输总体重试预算仍5分钟，父Context/原workflow deadline优先，取消仍立即生效；没有放宽原20分钟。此次暂停的仅是自己的验收读取脚本，避免在缓存修复前自动启动PG手动流程，没有取消已成功的原CI/镜像。

自动验证：相关blob/kube/executor race通过1.805/1.997/33.016秒；加入HTTP期限、较短父Context、真实HTTP等待响应取消与认证前不延长期限的回归后，executor/server/blob race分别28.772/35.532/1.518秒通过。最新make verify完整PASS，35前端单测和14浏览器32.5秒。

真实临时k3s扩大原BuildKit缓存用例为1120MiB不可压缩层，显式2Gi对象/4Gi全局测试预算，冷/热、实际RUN CACHED、原UID恢复、持久缓存重启、发布digest、失败阻断、取消/清理和脱敏均PASS；专项653.769秒，叶场景532.22秒。初次使用扩大的tmpfs PVC出现EOF并保留失败349.558秒：tmpfs数据计入写入容器的cgroup内存，与生产磁盘PVC不同。修正只将临时集群存储复制到其自身可回收磁盘后重启，保留安装清单的server512Mi上限；没有修改生产内存或跳过大数据断言。测试容器已清理。大数据专项对应上限/流式路径；后加入的5分钟策略由独立HTTP行为单测验证，包含二者的当前源码全量Testcontainers正在执行，不混称已经全量通过。缓存2Gi/新传输策略尚未部署，热缓存与PG18继续待实际结果，10.6e/10.7b仍未完成。


## 2026-10-28 13:09：2Gi 缓存策略已部署，完整回归与实际缓存验收中

源码3bb6814已推送，server a28ffec4、supervisor ff812b00已部署；Ubuntu工具镜像c1726fc1、node2选择、引擎500m/4CPU与4Gi、并发2及6Gi总blob预算保持。原bd3快照重跑a5888f0a正在执行，待确认真实Go归档超过1Gi且成功保存，再自动重启控制面核对元数据持久性并运行原手动PG18 workflow，要求setup-go实际cache-hit=true。尚未把这些结果标为通过。

完整Testcontainers已通过1120MiB不可压缩层的冷/热、重启、发布digest、失败与取消场景，其余场景仍在执行。核对九个业务源码的Go/Rust/JS/TS/workflow与执行脚本没有Mongo7引用后，只从Ubuntu公共镜像预取映射移除mongo:7.0；registry对象、其他镜像、运行中任务及实际数据库不改。配置随上述已计划的server重启生效，不再为已移除的依赖预拉取镜像。Windmill已完成授权迁移至node3，原因与健康证据见前文，不再等待迁移授权。


## 2026-10-28 13:12：当前源码完整 Testcontainers 回归通过

3bb6814完整回归PASS2360.406秒，27个必选叶场景通过，2个需外部私有业务目录的可选fixture SKIP；独立1120MiB BuildKit大缓存场景777.28秒、act382.68秒、Rust102.60秒、真实SDK71.94秒、浏览器34.92秒、登录授权/重启115.28秒、保留清理17.33秒。实际Cold/Warm缓存与原UID/重启恢复、失败阻断、取消/清理、脱敏均通过，临时k3s/registry容器已回收；本机仅保留owner原lg-multiarch builder。35前端单测与14浏览器的make verify结果见前文。

真实a588缓存验收当前被原Atlas latest公网下载拖慢，非Go静态检查失败：约137.8MB下载只有约80KB/s，HTTP/1.1与默认协议对照相近，本机相同URL约88KB/s。原20分钟不改，不用其他二进制替换或跳过迁移检查。此下载未完成，原大Go缓存保存/重启命中及PG18仍不能宣布完成。13:09复查业务Web HTTP200、ready1、Flux564223da；etcd无告警、leader1、P99 fsync11.1ms/commit15.3ms。


## 2026-10-28 13:16：实际缓存补验因公网依赖超时，未启动手动 PG18

a5888f0a最终failure，执行叶10764894明确timed_out：静态检查/构建6分25.14秒成功，随后原Atlas latest下载到原20分钟期限，未进入迁移/race，setup-go post未执行，不能记作缓存保存成功。控制面收到明确终态并回收Pod，原失败日志完整保留；验收脚本按失败停止，未重启server或创建手动PG18，前述mongo预取CM改动尚待下次正常重启生效。旧5865/7aaf原CI/镜像成功证据保持，但大缓存与PG18仍未完成。

有界下载对照：原URL默认/HTTP1.1、本机、三个官方DNS A地址及集群已有mihomo代理均约54–143KB/s；flybit代理TLS握手失败。没有更改代理规则、节点网络或workflow，没有反复启动已知慢路径。诊断只读取公开文件小范围数据，临时port-forward均已关闭。已向owner询问是否接受将Tasks的Atlas安装改为固定版本/校验和及工具缓存，保留迁移校验、所有原测试与20分钟；尚未修改业务YAML，也不将未回复当作同意。

13:16最终健康复查：业务Web HTTP200、Deployment ready1、Flux仍564223da；etcd无告警、leader1，P99 fsync15.4ms/commit26.5ms。已无本批executor Pod或Testcontainers容器；Windmill app Ready1/1，原镜像和2Gi预算保持。

13:18在所有业务attempt已终态、无executor Pod时，正常重启控制面以应用Mongo预取清理；rollout成功，管理API可读，仍为a28ffec4/ff812b00与原预算。这次空闲重启只确认配置生效/控制面就绪，不作为尚未保存的大Go缓存重启验收。Atlas优化选项仍待owner选择，原业务workflow保持。


## 2026-10-28 15:04：按 owner 授权，将 Atlas 固定工具迁至内网 Zot

owner明确接受改用内网Zot。一次性获取官方标准版Atlas v1.3.0/Linux amd64，125,569,080字节，核对官方SHA256 cfc773e5b4e845bc01d680390c174648938a7f88bf30e5a2c83ae85217c21587；作为OCI artifact原始二进制层发布到已有公开上游命名空间forgelet/dockerhub/arigaio/atlas-cli，manifest 9f00de5842f4ce5be9a8d3f9700c79045442fb7d10b149a2adb09c54de6836d1。匿名流式下载后再次校验完整SHA，匿名写入401；不扩展ACL或给workflow写凭据，不新增服务/PVC。官方公网首次分片下载曾有TLS错误，仅在校验最终官方完整SHA后才发布；不以半成品替代。发布脚本、固定source与说明保存在tools/atlas。

Tasks业务提交f5cff55已真实push到main：明确授权修改的是Atlas安装及新增actions/cache@v4工具缓存，其他原CI/镜像入口、迁移校验、全部业务测试和20分钟不变；不再声称reliability.yml与旧版字节完全相同。安装从固定Zot blob摘要获取且独立SHA校验，损坏缓存重新获取，下载失败/错误hash/截断均不覆盖已有安装，临时文件回收，成功原子替换，无公网回退。新增脚本HTTP行为/race20轮PASS10.322秒；root make verify PASS（35前端、14浏览器31.5秒）。新Testcontainers专项PASS11.170秒，使用与生产相同的c172工具image配置40816453，在真实Linux二进制下验证两套业务迁移、断网热缓存、损坏缓存重取以及修改迁移后的真实checksum拒绝，容器已清理。控制面/执行器代码未变，既有完整2360.406秒回归与本次新增专项分别记录。

真实push初期GitHub installation token发生临时transport错误，事件持久化后自动重试，随后CI c6198848与image d1ea2769均创建；原托管Actions已恢复enabled=true。同一server的无凭据网络探针随后默认HTTP2 GET200/POST401正常，HTTP1出现EOF；没有把HTTP1强加给控制面或改网络/代理规则，临时探针已删除。image的reliability先获得node2，CI等待容量；验收脚本据真实成功叶任务核对>1Gi缓存归属，不假设两个workflow的执行顺序，也不取消任务。完整CI/镜像、真实Go缓存保存/重启命中、手动PG18仍待终态，10.6e/10.7b保持未完成。


## 2026-10-28 16:20：真实 1.48GiB 缓存与重启通过，修复 runtime Atlas 的失效镜像依赖

f5原image入口的reliability/c9bcc842成功，Atlas冷安装4.141秒、迁移0.351秒；实际Go cache存储对象1,592,818,688字节（约1.48GiB）由该成功叶任务保存，没有size exceeded/Failed to save警告。CI c6198848在独立新Pod上setup-go和Atlas cache均true，完整成功；之后server实际重启并Ready，缓存id/digest/size保持一致。完整元数据及cold owner证明已保存，不将工具cache-hit误报为Go cache-hit。

同一f5镜像build-push/f589812d失败原因独立：Dockerfile的atlas-runtime引用registry.shitamchi.com/steam-task@0f79b128…，registry返回not found。该失败留存，PG18尚未启动；不伪造完整镜像验收。按本次owner将Atlas改用Zot的授权，同步将runtime工具stage改为scratch + ADD --checksum --chmod=755，使用与CI相同的固定Zot二进制；其余builder/runtime配置、业务代码及原workflow测试/20分钟不改。

实际BuildKit导出目标stage通过（原CLI SHA、mode755一致）；新增Testcontainers构建实际Dockerfile的target并读回SHA，故意错误digest必须明确拒绝。初次fixture错误使用classic build API、以及负例错误文本匹配过窄的失败均保留；改为明确BuildKit模式及要求digest mismatch同时含实际/预期hash后，安装器+Dockerfile两个专项最终PASS15.207秒，临时容器/fixture镜像已清理。最后lint0 issues；运行时Go代码仍不变，既有make verify/完整回归保持适用，不混称新版fixture曾在早前完整回归运行。

业务新72bd3ba已提交并真实push，原两个入口重跑后再验收镜像/Checks及原手动PG18；cached Go和Atlas可跨此Dockerfile-only提交复用，完整任务仍待终态。f5失败、bd3成功与新提交结果分别保留，不覆盖历史。


## 2026-10-28 17:02：Zot 两处安装均通过，原镜像发布遭遇 registry manifest EOF

72bd新CI3891c1dd完整success，控制面重启后的新Pod实际恢复Go归档约1519MB（metadata对象1,592,818,688B）、Go与Atlas cache-hit均true；静态检查/构建40.008秒、short/race52.865秒、Atlas安装2.448秒。镜像入口2d237的reliability同样全部成功（race52.228秒、Atlas缓存安装2.725秒）。

build-push50264714完成真实构建：Atlas固定Zot ADD/checksum约1.8秒，主程序194.3秒、其他三个二进制3.3/1.0/0.9秒；APT下载曾慢但随后正常完成，没有变更源或跳过运行层步骤。sha-72bd3ba已经发布，registry实际digest5a6c8ecce45f7a927ff275e7a8e03c7ce3bf60b68fe8f40eea21a41aa1e2af5a；随后PUT /v2/steam-task/manifests/latest返回EOF，整个原workflow仍failure，latest仍旧f557，不计完整发布成功。这不是Atlas镜像/应用编译失败，也未手工改写失败状态或latest掩盖结果。

同一不可变原始计划重跑451a713e，保留72bd源码/Action SHA及完整门禁；原失败留存，没有为这次网络EOF改源码、预算或公共代理规则。数据库验收与远端发布独立：已按原workflow_dispatch、postgres:true创建唯一手动f7460c7e，真实PG容器observer已启动；当前先运行重跑的reliability，手动任务等node2容量。尚未宣布PG18或完整发布成功，10.6e/10.7b保持未完成。最终release只读/断网/非root的Atlas smoke将在实际成功发布后按digest运行。


## 2026-10-28 17:31：真实 PG18 找到严格时间断言精度错误，保留断言后修复

72bd原手动f7460c7e最终普通failure、非timed_out；Optional PostgreSQL integration完整执行13分1.231秒，实际观测154个postgres:18-alpine实例及Ryuk，唯一失败为TestRecordShortfallsUpsertsAndSchedulesRecheck，first_seen_at必须保留的严格相等断言。其余包继续执行完成，无第二个失败。没有在失败后改状态或将其称数据库验收通过。

生产upsert明确不更新first_seen_at。真实本机PG18复现用带额外123ns的输入，读回时间恰少123ns，证明差异来自PostgreSQL timestamptz的微秒分辨率（[官方PG18类型说明](https://www.postgresql.org/docs/18/datatype-datetime.html)），不是upsert覆盖。测试输入改为Truncate(time.Microsecond)，原first_seen_at严格相等/attempts/重查安排/成功清除断言保留，新增observed_at严格前进一分钟断言；生产逻辑、SQL、workflow、timeout均不改。真实PG18/race重复5轮PASS48.989秒，复现前失败8.868秒也完整留证。业务测试修复cfd1ca0已push main。

只取消了我们自己尚未开始构建、等待容量的72旧镜像验收重跑451a713e，保留已通过的reliability与原registry EOF失败，没有取消owner任务或正在收集完整PG结果的f746。cfd真实push CI35423f4b/image6178be2b已接收，托管Actions恢复true；旧PG运行/终态/服务容器证据单独归档后，按同一原workflow、postgres:true创建新0b52b754，不复用旧失败为成功。新CI/镜像/PG18及最终发布镜像Atlas smoke继续待终态，10.6e/10.7b仍未提前勾选。


## 2026-10-28 18:47：Zot 镜像缓存与 Ryuk 生命周期对照

owner补充授权：需要下载的镜像可直接缓存在集群现有Zot。继续使用固定digest及显式profile镜像映射；公共上游镜像与私有业务镜像分开命名空间，复用现有权限，不新增常驻代理或Docker服务。Atlas固定二进制已通过Zot获取，现有引擎/Ubuntu工具及Alpine等依赖也使用Zot。此次Ubuntu工具镜像只变更ENV，直接复用Zot中所有现存layers，避免重新下载/上传大工具层。

最新cfd原CI35423f4b完整success；镜像6178be2b的reliability及实际构建通过，但最终PUT latest仍EOF，sha-cfd1ca0已发布，digest5c2b000e03dbd2ee22d74bba2d1f2bbc4a632fbdc41a5a204ab291067031bdf1。使用相同凭据的独立小镜像crane、Docker29、Docker28.5.1以及集群内Docker28.5.1多标签对照全部通过；不能认定Docker版本或基础权限是唯一原因。18:22操作员仅重试该已存在digest的最终manifest写入成功，latest与sha标签一致；原workflow仍failure，不计自动发布通过，也不据此勾选阶段。尚未擅自将业务发布目标从外部registry改成Zot。

将该私有业务镜像缓存进Zot时，远端到Zot的流式copy遇到Zot现有60秒请求读取超时，日志证明请求体读取过慢，Zot无重启/告警；改为先本机完整下载和校验，再通过内网上传，保留Zot超时/预算。下载尚在执行，不宣称已缓存成功。集群诊断Pod、临时Docker引擎与port-forward已回收。

cfd原手动PG18 0b52b754的时间精度修复已验证：原失败包handlers/steam_detail成功20.776秒。但另一个包出现复用Ryuk时等待端口60秒超时，后续容器重新创建成功；整体最终到原20分钟timed_out，保持失败。独立Go Testcontainers v0.44.0双进程/同父session对照：间隔15秒，默认10秒宽限创建新Ryuk，工具镜像2分钟宽限复用同一Ryuk，真实Testcontainers PASS80.757秒，容器与跟踪到的Ryuk均回收；这验证生命周期策略，不冒称已精确重现所有业务失败时序。新增tools/reaper配方与可选回归，Ryuk保持启用，fixture显式清理及Pod结束/取消清理保持；异常断连自动清理可能多等待至2分钟，无额外常驻进程。

make verify通过（35前端单测、14浏览器32.1秒，Go race/集成、构建/lint/generate），之后集成build-tag lint单独执行0 issues；首次与make verify并发时embed dist被生成过程暂时清空导致typecheck失败，生成完成后重新检查通过，不改源代码掩盖错误。Ubuntu新镜像90ad0dc507bf64c1e3bc81b3ea09512dfcd41fe9047e71ff0671c2dcee5f761e已经部署：与c172逐项核对rootfs相同，只有RYUK_RECONNECTION_TIMEOUT=2m ENV变化；配置只改Ubuntu act.runner_image，server/supervisor/资源/并发/Rust/镜像映射保持。空闲时rollout成功，原cfd/固定计划及postgres:true重跑eec5d4f46055b163faef1480c467aa27，真实PG18验收继续。核心Go运行时代码未变，完整2360.406秒回归、新Atlas15.207秒与新Ryuk80.757秒分别记录，不能混称后两者参加过早前全量回归。


## 2026-10-28 19:38：按 owner 授权，将 Tasks 应用镜像发布迁至 Zot

owner明确选择应用镜像发布也迁到Zot。本次先迁正在排查的Tasks原镜像workflow；其他已验收仓库和现有业务Web CD的地址不批量替换。业务提交f6f4105734acfe848907bbdca2f7ba99dd1f9f31已push main：docker/login-action目标改为zot.k.mygo.rocks，metadata镜像路径forgelet/apps/steam-task，改用ZOT_USERNAME/ZOT_PASSWORD仓库Secrets；原reliability依赖、Action版本、tags规则、构建/迁移/测试步骤与20分钟保持。生产和回滚清单继续保留各自历史digest，未自动部署Tasks生产服务。

新增专用forgelet-steam-tasks-push账号，仅exact image repository read/create/update，无delete或其他私有仓库权限；公共上游只读、业务镜像匿名拒绝。密码只写本机私有文件、Zot bcrypt Secret以及Forgelet加密/GitHub仓库Secrets，workflow不接收管理员凭据。Zot现有镜像/资源/PVC不变，Recreate rollout成功，旧admin权限保留。最初候选ACL的空用户actions会回退宽泛defaultPolicy，被真实Testcontainers负例发现（其他仓库写入202），未应用到集群；修正为catch-all默认空、显式保留已有唯一admin用户权限、目标exact path单独授权后才上线。

新增可选TestZotScopedPublisher使用真实Zot v2.1.22与隔离测试环境的专用fixture密码，不读取生产凭据；候选实际ACL版本PASS8.162秒，仓库中无密码/哈希的同等policy版本PASS9.513秒，均验证创建/更新latest与digest、读取、admin保留、上游匿名读、跨仓库写/删除/业务匿名读拒绝。早期用仅TCP端口等待过早请求EOF、以及只等/v2/401的错误就绪假设失败保留，改为实际HTTP200/401就绪再验证授权。真实集群重复ACL正/负验证也通过；测试registry容器已清理。make verify最终通过（Go race/集成、构建/lint/generate、35前端单测、14浏览器28.3秒）；首次新fixture未检查Close错误被lint阻断，修正后通过。核心Go运行时代码未变，完整2360.406秒核心Testcontainers与新增专项分别记录。

此前cfd实际业务镜像已完整下载，所有OCI config/layer长度和SHA逐项通过，缓存到私有forgelet/mirrors/steam-task-cfd，保持原digest5c2b000e03dbd2ee22d74bba2d1f2bbc4a632fbdc41a5a204ab291067031bdf1；本机先完整校验再LAN上传通过，未提高Zot超时/资源。对该实际发布镜像以原appuser、read-only、network=none、256Mi/1CPU smoke通过：Atlas完整SHA及v1.3.0，两套迁移校验全部通过。该镜像的外部latest来源仍是操作员补写，不能把旧image617失败改称自动成功。

Ryuk2m原PG18 eec5重跑最终interrupted，缺少最终ACK，不能记作修复通过；观察到136个实际PG18容器及同一个Ryuk，原精度修复和生命周期专项不等于完整PG验收。最后日志停在short/race完成之后；Pod最终回收，需继续收集完整终止原因，不推断为已确认的新业务错误或OOM。10.6e/10.7b与第三批仍未完成。新Zot真实push CI6a95126d/imagea142a862目前执行中，需完整终态/实际registry与Action摘要及精确job Checks核对后才记自动发布成功。


## 2026-10-28 21:28：Zot 自动发布及固定 SHA Action 源码缓存验收

首轮Zot image a142a862失败发生在任何checkout/login/build之前：下载docker/metadata-action@c299e40c65443455700f0fdfc63efafe5b349051的原始公开源码归档，三次均触发既有2分钟HTTP上限；不是Zot权限或推送错误。失败保留，不盲目重试相同慢路径。

新增通用可选镜像预载：管理员将公开Action的原始tar.gz及独立SHA256清单放进supervisor镜像，worker按repository/完整commit查找、验证SHA256与GitHub全局PAX commit header，再用既有32MiB压缩/128MiB展开/10,000文件和路径安全规则提取并原子接纳。新SHA未预置时仍走原下载；声明的包损坏时失败且不走公网。没有改upstream JS、Node执行、workflow/Action输入、取消/ACK/重启语义或timeout，没有新增配置字段、服务、数据库或PVC。源码目录固定为镜像内/opt/forgelet/action-sources，业务不能选择目录/校验表；数据和构建配方在tools/actions，七份公开源码约15MiB，不将大归档提交到Git。

七份源码从官方codeload固定URL获取，逐项核对长度、SHA256及GitHub commit标识。单测/HTTP覆盖精确固定SHA匹配、预载命中零网络、坏hash/缺文件/坏commit/超限manifest与archive/不安全路径拒绝、取消和临时目录清理；make verify通过，Go race/HTTP/SQLite等、build/lint/generate、35前端单测与14浏览器29.4秒。首次lint的大小写错误文本与error比较被阻断后修正，通过结果不覆盖初次失败。实际最终镜像的七份原始归档在非root、read-only、network=none、1CPU/512Mi Testcontainers中全部通过，专项3.36秒（首次专项3.865秒），容器已回收。

Ubuntu supervisor镜像d7ae262a4c6e61885eb6e88bdb0625ae234b1e272606ed99c9a450fd6e9c4153已发布到Zot并部署。逐项核对原ff812的runtime image config相同、base layers为相同前缀；只加新act-worker及公开源码资产。server仍a28ffec4、runner仍90ad0dc5，Rust/其他profiles、镜像目录、500m/4CPU与4Gi、并发2保持。空闲rollout后对同一f6源码/原workflow/完全相同ActionPins重跑eb0e06f3854898dd28b1d8f6bf9fbb76，真实日志确认使用预载源码；原CI6a95与新image的reliability/build全部成功。

实际docker/login-action使用专用Zot账号，docker/metadata和build-push保持原执行行为。registry latest、sha-f6f4105及Action输出全部为sha256:4e9f5830e101409eecc7106b8538c3993fa8e7bdf913fd7f4130256e5c21052c；精确匹配两个workflow的7个新job external_id，GitHub Checks均completed/success，旧失败Checks未覆盖。该新Zot镜像用专用只读访问能力（发布账号本身含read）、原appuser、只读根、断网、256Mi/1CPU运行smoke通过：Atlas SHA/v1.3.0及两套迁移校验均成功。源仓库checkout和Action内部外部请求仍保留原网络要求，不把归档缓存称为完全离线Actions。

当前源码完整Testcontainers回归仍在执行：初始两个启动因默认公网Go/apt准备过慢在构建阶段中止，使用既有GOPROXY/DEBIAN_MIRROR参数重启；随后第一次真正进入k3s的完整运行因系统DNS/存储未就绪在准备阶段失败400.204秒，未运行产品用例。补齐项目已有FORGELET_TEST_IMAGES、导入本机同版本官方k3s/Docker/Registry/BuildKit/Flux镜像后重新完整执行；不跳过系统部署或修改功能等待条件，不关闭go.sum、CA、Debian签名。将原失败诊断cleanup提前注册到系统就绪检查之前，避免该阶段失败时缺少节点/Pod/事件日志。初次两个自有legacy build容器fac01/b397已回收，owner builder未改。当前已通过并发/取消/重启、低水位及原UID保护，正在CI/CD与act场景，尚不宣称全量通过。

Zot迁移本身已验收，未启动Tasks生产应用。利用全量回归等待时间，以同一f6的原workflow_dispatch、postgres:true新建6752ce401d72e2700684beba补验PG18，保留20分钟/完整测试/严格断言，并持续捕获supervisor日志和Pod终止状态以诊断旧eec missing ACK。该运行尚未终态，10.6e/10.7b及第三批保持未完成。21:24现有Web HTTP200、Deployment ready1、Flux仍564223da；etcd无告警、leader1，P99 fsync约24.3ms/commit49.7ms。


## 2026-10-29 08:27：全量回归准备参数与整套预算修正，保留原失败

昨夜完整运行3309.264秒最终FAIL：前面的并发、恢复、取消、低水位、CI/CD、日志、act与1120MiB独立BuildKit冷/热/重启/失败/取消用例均已通过，其中大缓存场景1428.15秒；随后全局55分钟context耗尽，SDK用例得到context deadline exceeded，浏览器/登录/保留清理尚未执行即连带失败。不是这些后续功能已经验收通过。Rust因未配置既有FORGELET_TEST_RUST_IMAGE明确SKIP，SDK也漏用了已有官方归档预载参数；首次准备阶段漏系统镜像预载的失败与该次全局预算失败分别保留。

完整套件的总context调整为80分钟（Go test外层已有95分钟），只为容纳1.1GiB真实多轮传输及其余用例；单个功能等待、SDK十二分钟job、实际业务workflow二十分钟均不改。使用项目现有Go/apt镜像源、同版本k3s/Docker/Registry/BuildKit/Flux预载、已按官方SHA核对的SDK归档目录及本机生产Rust镜像重新完整执行；不是替换模拟SDK或跳过Rust/大缓存。原失败清理后，临时k3s/Registry容器已回收；本次全量仍在执行，暂不勾选10.6f。

原PG18运行6752最终timed_out：观测138个PostgreSQL/Ryuk实例、读取错误0，日志无明确业务FAIL或reaper错误；supervisor明确输出job finished with state timed_out，控制面已有最终ACK，与旧eec的interrupted分别记录。它仍未完成原二十分钟完整workflow，不能声明PG18通过或提前完成10.6e/10.7b/第三批。已验证的f6完整CI、Zot image重跑eb0、4e9f5830双标签/Action摘要/Checks和实际发布镜像离线smoke结果保持。


## 2026-10-29 09:00：本批完成——Zot 发布、预载源码与当前完整回归

当前源码完整Testcontainers PASS1830.233秒，包含实际bundle镜像断网提取全部七份Action（3.08秒）与全部必需临时k3s用例：act重启/缓存/取消/失败359.99秒，Rust/PowerShell122.52秒，1120MiB独立BuildKit冷/热/重启/失败/取消282.22秒，真实SDK17.02秒，Web浏览器35.17秒，GitHub登录/仓库接入/重启107.13秒，日志/历史保留清理17.08秒。两项需外部私有业务目录的fixture明确SKIP，已单独记录的真实仓库结果不混入该计数；额外可选Atlas/DuckDB/Ryuk/Zot镜像场景本轮未配置，不冒称参加了此全量，它们先前独立专项记录保持。临时k3s、registry与test容器均已清理，仅保留owner原builder；无实际homelab执行Pod，仍只有Go控制面。

make verify今日最终通过：Go race/HTTP/SQLite/解析/执行器等、build/lint/generate、35前端单测、14浏览器29.6秒。f6的CI6a95、同一固定快照image重跑eb0、7个精确job Checks、Zot latest/sha-f6f4105/Action摘要4e9f5830以及实际发布镜像的非root/只读/断网Atlas与两套迁移smoke均通过；专用账号只能read/create/update forgelet/apps/steam-task，跨仓库写入/删除/匿名私有访问均有真实正负验证。再次用专用账号读取latest仍为同一4e9摘要。Tasks业务发布地址迁移完成；现有生产Tasks部署/历史回滚digest保持。

10.6f完成；10.6e、10.7b及第三批继续未完成，因为原PG18完整workflow在6752仍到二十分钟期限，虽然最终ACK已正确持久化，但没有完整测试成功结果。本批不改变该timeout、删减断言或将其误记为通过。阶段10的远程reusable/动态matrix/artifact v4等后续边界仍保持，不称阶段10整体全部结束。

最终homelab业务Web HTTP200、ready1、Flux main@564223da；etcd无告警、leader1，P99 fsync约29.0ms/commit44.5ms。server a28ffec4、Ubuntu supervisor d7ae262a/runner90ad0dc5、Rust旧ff812及全部资源/并发预算保持。Zot及GitHub/Forgelet仓库专用Secrets已配置，托管Actions恢复原enabled=true。源码归档锁/配方、运行库及回归在本批代码中，进度与设计/兼容/重现文档已同步。


## 2026-10-29：PG18 完整 workflow 二十分钟优化（手动完整验收通过）

原6752在20分钟内未完成，并非业务断言失败；更早72bd3ba的PG步骤完整执行13m1.231s，实际观测154个临时PG18服务。旧完整流程另有checkout约3–4分钟、Go缓存恢复约2分36秒、Codecov约4分48秒；网络与重复initdb/停止成本叠加耗尽预算，不以单个测试timeout解释整个job。原失败与最终ACK证据保持。

已实现并推送业务仓库`shitamachi/steam-tasks@22b8b3238b85c2164b248fe88bcf48eddd2f2984`：新增`./scripts/ci/pg18`，只在显式PG步骤启动一个workflow自有的真实Testcontainers PG18，再以既有隔离数据库后备路径执行原命令。每个测试仍创建随机独立数据库并DROP FORCE；子进程DSN强制覆盖为本次临时容器，不使用继承的部署数据库。保留fsync/synchronous_commit/full_page_writes开启及logical WAL、所有172个既有测试文件字节、迁移校验、race/coverage、`go test -p=1 -race -count=1 -timeout=15m ./...`和原20分钟job。开发者直接测试仍默认每测试一个Testcontainer，不增加常驻PG。成功/失败/取消均销毁自有容器；取消杀停整个go-test进程组，保留子命令退出码。

自动验证：新环境覆盖/参数/退出码/孙进程取消race五轮通过；53个包原short/race/coverage通过，go vet通过；真实PG18三库隔离/清理与持久化设置Testcontainers PASS8.961秒；真实wrapper成功/失败/取消生命周期PASS80.517秒（精确核对容器已不存在，退出码0/1/130）。固定源码完整PG18/race原命令53包通过3m58.646s并清理容器。第一次本地全量期间编辑新增测试导致Go import配置失效，保留该失败，不计作成功；固定源码重跑才是上述最终结果。Forgelet本轮make verify通过，含35前端单测和14浏览器32.7秒；本轮未修改Forgelet生产Go，当前源码完整临时k3s回归1830.233秒沿用09:00记录，不冒称本轮重复执行。

真实push的CI`06db20fc97e62fa62d8269b5`成功；手动原入口`postgres=true`运行`98b5a9faf632a54e80258de2`完整success。最终job`547d816a5f2653d7762e7e23`、最后日志序号248与终态已持久化，精确external_id对应GitHub Check123212592746为completed/success；执行Pod已自动清理。53包完整PG/race通过，实际仅1个postgres:18-alpine和1个Ryuk、观测读取错误0。原172个既有测试文件字节未改、条件/全部断言/命令及job1200秒均核对；没有扩容或延长预算。

| 手动完整workflow阶段 | 本轮真实耗时 |
|---|---|
| checkout | 5.858秒 |
| setup-go，实际cache-hit=true | 2分32.500秒 |
| 模块校验、vet、build | 46.160秒 |
| Atlas缓存/安装及两套迁移校验 | 约6.340秒 |
| 原short/race/coverage单测 | 1分8.865秒 |
| PG18完整步骤（含wrapper构建/清理） | **4分25.701秒** |
| Codecov原步骤 | 23.346秒 |
| 所有计时Main/Post步骤合计 | **9分29.943秒** |
| executor启动→最终结果落库，含Zot镜像准备，不含排队 | **11分43.217秒** |

最后一项由Pod内executor startedAt01:58:49Z与最终job updatedAt02:10:32.216931542Z核对；不是将排队/镜像准备并入步骤计时来声称加速。完整PG子命令含fixture启动为4分23.578秒，fixture启动3.291秒；与旧13分1.231秒PG步骤相比主要消除了154次PG初始化/销毁，本轮仍独立建库及保持持久化设置。checkout和Codecov有网络波动，不能把所有跨次时差都归因于PG优化。Codecov本轮仍因protected main缺token拒绝上传，按原fail_ci_if_error=false非阻断；workflow success不表示覆盖率已经上传。

同源image`39fb828f0c3feb09899b9c61`的reliability与build-push均success；原两个push workflows的7个精确job external_id Checks全部completed/success，另有上面的手动PG Check。Zot `latest`、`sha-22b8b32`与原build-push Action输出均为`sha256:f683f162d6bdb3279a75b3c4ecad73299c5c9b8bd50120245eb2e0261a664330`。以专用账号读取该新镜像，在非root/只读根/断网/256Mi/1CPU中验证Atlas SHA/v1.3.0和两套迁移均成功，不连接Tasks生产数据库或启动业务部署。

10:20收尾核对：所有实际executor Pod已自动回收，仅原Go控制面常驻；本机只保留owner的lg-multiarch builder。Web HTTP200、Deployment ready1、Flux仍main@564223da；etcd无告警、leader1，P99 fsync约25.1ms/commit41.3ms。server a28ffec4、Ubuntu supervisor d7ae262a/runner90ad0dc5、节点/CPU/内存/并发预算、授权范围和业务部署保持；托管GitHub Actions已确认enabled=true。

10.6e/10.6g/10.7b及本轮第三批完成：上表九仓库的固定提交CI/发布语义已分别验收，实际业务CD范围为platform-web，先前真实GitOps/Flux健康、失败阻断、回滚/恢复结果保持。不是给其余仓库新增生产部署。旧失败与新成功独立保留。阶段10的远程reusable、动态matrix、完整原版artifact v4等仍未实现，不因本批完成而勾选，具体边界见compatibility。


## 2026-10-29：阶段10交付范围复核与下一步计划

结合当前代码及上述固定提交证据，阶段10的首批9仓库完整CI/原发布语义、Tasks PG18限时与platform-web实际CD目标已完成。原清单所有兼容扩展尚未完成：远程reusable及调用matrix/concurrency、动态matrix、act原版artifact v4/逐step timeout/local pre、私有远程Actions等仍有明确诊断；native与act的hashFiles/artifact能力需分别描述。既有SDK/Atlas/Zot/PG夹具调整属于显式业务修改，不把固定提交验收扩大成任意仓库零修改或完整GitHub云服务兼容。

代码确认的日常使用缺口：StepResult无开始/结束时间、UI仅在终态展示步骤结果，准备阶段与排队/执行耗时未结构化区分；prepareActMirrors遍历全部配置镜像；webhook只处理push/PR，installation生命周期同步仍待6.5实现。Codecov上传缺token，Admin Web历史1CPU失败的唯一根因未确认。Tasks20分钟通过场景Go cache=true，不能视为任意全冷/公网状态的保证。除了platform-web，其余仓库未在本批新增实际业务CD。

已在plan写入阶段11建议顺序：11.1结构化实时进度/计时/警告→11.2 GitHub授权生命周期同步→11.3按需镜像准备及实际缓存开销优化→11.4选择一个无状态应用继续真实CD。每一批随实现做单测/集成/Testcontainers，再独立记录homelab验证；不设spec或独立基线门禁。阶段11仅为计划，本轮未改功能代码、未运行新测试或操作实际集群；没有把计划记作已实现。已修正文档陈旧的阶段10标题、接下来说明和事故恢复待办，历史失败记录保留。


## 2026-10-29：11.1–11.4开始实施，当前11.1自动验证中

owner已授权完整实现11.1至11.4，目标保持不缩减。当前新增结构化进度事件与SQLite v7迁移：事件严格绑定job/Pod UID、连续seq与SHA256去重记录，限制2048条/attempt与64条注解；有界物化进度同job保存，journal随job删除级联清理，列表移除详细steps/annotations。claim协商progress_version，新worker保留未知ACK事件并在取消后独立有界flush，最终result带last_progress_seq核对；最终result/日志ACK继续独立权威，进度不会把任务提前标成功。原生执行器与act已接准备/步骤/收尾事件，act只使用上游结构化stage/stepID/stepResult字段；标准warning/error注解走脱敏与文本展示，不解析任意stdout推断第三方成功。

本轮Go race已通过run/storage/server/executor/actworker，包含HTTP+SQLite运行中观察、乱序/重复/身份/重启/终态不可变与ACK丢失取消后flush；v6→v7迁移保留旧attempt。前端新增时间线、实时step状态与计时、警告、旧历史缺失值处理；37个前端单测、15个浏览器用例通过，桌面/移动截图已检查。最终make verify通过（含Go race/build/lint/generate、37前端、15浏览器35.8秒）；早期import分组lint失败已修正，不算首次全绿。临时k3s act专项PASS718.387秒（用例464.85秒），真实Pod中运行进度/脱敏注解、同UID控制面重启进度保留、冷/热缓存、失败/取消/超时与最终progress/log ACK通过，临时容器已清理；11.1真实仓库复验尚在进行，清单仍未勾选。

原v6快照已私有备份；副本v6→v7迁移保留117 runs/288 jobs/19 settings/20 deliveries/68 events。已将11.1 server及native/两种act supervisor部署homelab，原登录会话/历史可读；入口短暂503后同一新Pod恢复，无重复重启。镜像父层和运行配置精确核对保持，所有runner工具/资源/并发/业务部署不变。源码与二进制摘要和快照保存于本机私有stage11-delivery-20261029目录。11.4只读盘点发现steam-admin-web已有实际部署，upstream为steam-admin-service:8080；后续接GitOps前以当前集群和仓库状态为准，保留现有配置/服务入口，不用旧验收digest覆盖新部署。11.2授权同步、11.3按需准备、11.4实际CD均仍待实现与各自完整验收。


11.1真实仓库复验已创建：platform-server固定ac3f90d/run acd575db577d7de1ee3b3569，admin-web固定4d5cd9d/run5261cc2a10f0f48c46563d7e，Tasks22b8b32/原PG18输入run3becc4bc3e502091c11d5f4a。保持原workflow测试/断言/超时，观测阶段进度及最后progress ACK；三项当前仍在运行/排队，不提前算成功。11.2–11.4目标继续保留，未标记完成。


## 2026-10-29：11.1真实复验完成，11.2/11.3实施中

三项原workflow固定快照均已success：Go ac3f90d/acd575db、Node 4d5cd9d/5261cc2a、Tasks22b8b32/手动PG18 3becc4bc。验收程序实际观察每项执行中的running step，并确认最终result.last_progress_seq等于持久化progress.seq；Tasks原1200秒预算及PG步骤成功保持。最终证据progress-live-complete.json保存于本机私有stage11-delivery-20261029目录；未将本次手动快照复验称为新的真实push或镜像/CD验收。结合先前make verify与718.387秒真实k3s专项，11.1勾选完成。

11.2新增签名安装生命周期提示、持久化幂等/冲突处理、权威快照和可取消后台刷新；接通排队准入、启动/claim及凭据生成前后检查，确认失权走现有取消/ACK/Pod清理，普通同步失败保留最后成功结果。专项race回归通过；完整自动回归、真实k3s及homelab撤权/恢复验收尚未完成，不能称11.2交付。11.3开始增加管理员image_groups和workflow显式选择、固定digest计划快照及旧配置全目录兼容；尚在自动验证，没有部署。11.4仍保持完整目标，未进行新的业务CD操作。


11.2真实k3s首轮暴露旧名称绑定遇installation suspend时丢失安装关联，误显示not_authorized；已增加仅用于身份匹配的持久化关联与race回归。修复后临时k3s授权生命周期9.03秒通过：失败读取保留快照/两个活跃Pod和排队任务、撤权后的凭据403、同UID控制面重启、确认失权全部取消/清理、恢复运行及暂停识别。合并的act场景因重启后尚在确认授权而收到准入503，整轮298.661秒仍记FAIL；测试客户端随后增加仅针对持久化前明确授权pending的有界重试，不重试传输不明结果或已创建任务。最终act分组和全量Testcontainers尚待重跑，11.2/11.3均不勾选。

11.4已本地准备Admin Web image workflow复用同提交CI的needs门禁，并改向Zot；原npm ci/check/32测试/build/生成文件检查全部通过。完整候选YAML的真实SQLite回归确认CI失败时publisher和native CD均skipped、无Pod，CI成功才放行publisher。Zot候选精确路径发布/只读拉取权限Testcontainers通过4.890秒：禁止跨私有路径写入、读取与删除边界保持；新账号/steam命名空间只读pull Secret及Forgelet/GitHub加密仓库secrets已配置，Zot rollout成功，操作前无活跃CI。业务workflow尚未推送，业务Deployment/Service/Gateway与实际GitOps目标尚未修改，不算11.4交付。


11.2/11.3完整回归首轮最终FAIL2835.101秒，不能作为全量通过：自动发布子场景的直接POST夹具未等待重启后的授权确认；真实浏览器手动表单也在确认前允许提交。已分别补齐持久化前503的有界准入等待及UI按钮授权门禁，用户填写的installation不再被历史轮询覆盖；最后成功授权不会因普通刷新失败被降级。新make verify通过42前端单测/17浏览器41秒。新增跨账号真实k3s回归：installation7撤权提示/全量失败不隔离已确认的8，确认7失权取消其任务后8仍能成功执行；重启/权限发放边界继续通过。包含四个真实浏览器场景的专项316.482秒PASS（授权14.73、控制台73.24秒）；更新后的完整回归正在执行，明确加载原Rust工具镜像及Zot权限用例，首轮Rust缺环境SKIP不冒称通过。

11.4真实正向链路已通过：Admin Web业务c5db392仅修改CI复用门禁与Zot发布，原32测试/检查/构建保留。独立GitOps基线eff0719按原digest630820b5完整复制至Zot，Deployment配置仅更换等价registry引用及只读pull Secret；原Service/Gateway/API地址保留。新Flux forgelet-admin-web使用同仓库只读source与限于本Deployment的reconciler/observer权限，Ready/Healthy与HTTP/healthz200通过。实际platform-web由Flux spec.images覆盖的6ee9e663保持，不用其旧GitOps文件c85覆盖当前配置。

真实push的CI b5506506及image内复用CI均通过，但image0049a510的publisher checkout因GitHub TLS/连接超时失败，CD正确skipped，旧失败保留。连接恢复后同SHA/原固定steps/Action pins计划仅重跑一次652df38e，CI/镜像/native CD全部success；投递b604e4a9已deployed，GitOps91715022，实际Zot镜像f29f6ff3，latest/sha-c5db392/Action摘要、OCI revision标签、Deployment和精确Checks一致；此前digest630记录为回滚起点。GitHub托管image workflow在原push期间临时关闭以避免双写标签，已恢复active。后续仍需负向CI、部署健康失败、回滚/恢复和控制面重启幂等，不提前勾选11.4。

## 2026-10-29：11.2/11.3 控制面上线与 11.4 回滚实测

11.2/11.3 控制面镜像 `02ad1e35…` 已部署，配置仅增加显式镜像组，保留资源预算、supervisor、管理设置、登录会话和原任务 UID。真实 GitHub 安装设置仅移除测试仓库 `forgelet-e2e-app`：收到签名生命周期 delivery `61485ba2-d36e-11f1-9045-c8dc4b8dc627` 后，完整权威同步确认 `not_authorized`；探针 `c885cc5f…` 的一个运行中任务及一个排队变体均取消，记录明确失权原因。随后重启控制面，取消历史及本地启用配置保留，新运行 HTTP403；其他12个仓库的授权范围不变。owner操作时确认后已保存恢复该测试仓库，Forgelet自动同步为authorized；核对全部13个仓库ID/安装关联及权限与测试前完全一致。恢复验证新任务 `1c7d5da2…` 已接收，等待现有队列正常调度；旧取消任务未重放，执行成功验收尚未完成。

11.4 的真实 Admin Web 发布 `b604e4a9…` 已完成。回滚 `0a59b6ed…` 提交 `cd007af8…` 在仅暂停新增的 `forgelet-admin-web` Flux Kustomization 时保持 delivered；控制面升级后重复请求返回相同回滚 ID，GitOps HEAD 未变化。已恢复该 Kustomization，baseline digest `630820b5…` 健康；再回滚此回滚得到 `21d228c0…`，GitOps `47fe0142…`，恢复发布 digest `f29f6ff3…`，状态 deployed、HTTP健康检查200。未修改 platform-web 的镜像覆盖设置。CI失败阻断与应用健康失败后的恢复另行验证，未宣称11.4全部完成。

完整 Testcontainers 第二轮最终 FAIL2090.959秒：授权用例通过管理API临时写配置时，意外将空targets持久化成覆盖项，导致之后原native CI→CD目标不可用。已将跨账号测试仓库改为临时 ConfigMap 声明，并在用例结束恢复原配置，避免污染管理覆盖项；这是测试隔离修复，未放宽交付授权。另发现生产边界：claim/credentials 在授权暂待确认时误用永久403，现改为可重试503，确认失权仍403，临时状态不会返回凭据。真实 remote executor + HTTP/SQLite/race 回归验证 claim、claim后领取凭据两个窗口等待确认后，同UID只执行一次并完成进度ACK；make verify通过（42前端单测、17浏览器41.7秒）。包含这两项修复的完整Testcontainers重新运行中，尚未记录为通过；该503修复随后以 server `400978eb…` 上线；资源和管理配置未变，Node冷运行与已创建Pod UID保留，完整回归仍继续。

11.3 管理配置已选择 Go/Node tools空组、构建build组、Tasks postgres组及Rust组，保留未选择工作流的旧完整目录兼容。固定原源码 Go `ac3f90d…`、Node `4d5cd9d…`、Tasks `22b8b32…` 在独立ref缓存范围运行，冷运行完成后重跑相同固定计划；计时、实际缓存命中和完整PG18结果待核对，不提前声明通过。

11.3 实际冷缓存边界：Tasks `8d9933f6…` 在独立ref、Go cache-hit=false 下，原模块/静态检查/构建约6分48秒、原short/race约6分45秒、完整PG18约4分29秒均成功；Atlas安装/迁移校验也通过。后续原Codecov上传步骤运行时触及原20分钟，最终状态timed_out，**不能计作完整workflow成功**，未延长超时或删减步骤。已保留冷运行、最终日志/进度ACK与失效Check，另接收相同22b8源码、步骤、固定Action SHA和PG18输入的main现有缓存对照 `8e6763e1…`；仅ref/cache范围不同，不能宣称它是同一计划的冷→热重跑。Go与Node的冷热对照仍使用完全相同的原计划重跑。与计划既定边界一致，不承诺任意冷缓存都能在20分钟内完成；热对照结果待核对。

补充实际镜像身份验证：独立Testcontainers Docker引擎专项PASS6.354秒（用例5.50秒），验证完整已加载镜像只在固定manifest digest与精确config ID匹配时复用，错误digest被拒绝，临时引擎已清理；原pull/tag/别名身份不符及取消回归也保留。完整k3s回归继续，不用该专项替代全量结论。

最新完整Testcontainers最终 **PASS2466.697秒**，28个k3s必选叶场景通过（包含授权/跨账号/重启14.86秒、act421.08秒、Rust134.62秒、大BuildKit缓存560.33秒、实际SDK51.61秒、Web浏览器78.51秒、登录/授权重启91.21秒、GC18.32秒），另有6个非k3s辅助叶检查通过。共7个环境可选fixture跳过：Action镜像归档、Atlas安装/镜像stage、DuckDB工具、两套私有业务目录综合fixture及工具reaper策略；未将其宣称为本轮执行。真实业务仓库/Atlas/冷热结果另行记录，不替代可选fixture结论。浏览器实际k3s4项51.3秒，官方SHA验证的SDK归档预载不计公网SDK下载性能。所有临时k3s/registry/容器按测试正常清理；首次和第二次失败记录保留，没有改成成功。

授权恢复后新native运行 `1c7d5da2…` 已成功，旧 `c885cc5f…` 两个取消变体未重放；实际线上Connections页面显示CI已启用、授权正常和这次成功运行，最近同步时间正常。再补一次真实GitHub凭据/checkout恢复探针 `6d6d97c0…`，验证相同固定SHA可实际读取，尚待队列执行。Go热缓存 `6bffdc39…` 同计划成功、cache-hit=true，准备70.484秒、实际执行60.454秒；Node热缓存 `a242ebfb…` 也已成功，命中与计时在最终报告汇总。

11.4 真实CI失败负向验收 `b8381832…` 已通过：明确标识的手动快照保留原32个测试、check/build/生成文件校验，全部先成功，再追加exit23失败；publisher与native CD均skipped且未创建delivery，应用HTTP200。该fixture使用旧4d5源码SHA，未伪装为原push或修改业务提交。健康失败fixture `e101d21e…` 保留原CI，仅在独立workspace生成故意退出17的镜像，使用独立测试tag，不写latest或提交tag；已接收，最终健康失败/恢复仍待核对。

11.2额外真实凭据验收已完成：`6d6d97c0…` 成功从恢复授权的GitHub测试仓库领取短期凭据、checkout并核对固定SHA，日志确认restored-private-checkout-ok，完整日志/进度ACK一致；原取消任务的实际Pod已不存在、历史仍取消。本地开关/原13个仓库范围与权限保留，11.2已勾选。


## 阶段11收尾：11.1–11.4完成（2026-10-29）

最终现场核对通过：275个运行源码/测试/前端文件hash与已验证构建一致，server为 `400978eb601091e08c65cdfa42db91f2c78db4dd85c85f35d927aab883bde30c`，SQLite实际快照schema v7；本地私有快照包含两条真实签名授权生命周期delivery及完整13仓库目录。会话、全部管理设置及原执行资源预算保留；配置只增加镜像组及其已记录选择。没有新增常驻执行器或共享Docker数据/宿主socket。

- **11.1**：结构化实时步骤、计时、标准注解、日志/进度/最终结果ACK及历史兼容已验证，Go/Node/完整Tasks PG18真实运行成功；临时k3s覆盖重启、取消与运行中UI，不从任意第三方stdout推断上传成功。
- **11.2**：真实测试仓库签名撤权取消运行中/排队任务，重启后继续拒绝新运行；恢复同一原13仓库授权后，native运行及真实短期凭据checkout均成功，旧取消历史不重放、Pod已回收。UI显示授权正常及最近运行，已核对源码身份/实际固定SHA。
- **11.3**：显式空组/选组/并集、旧完整目录兼容、固定digest及计划快照/重跑检查、缺失与身份不符、取消/重启、Docker Action/BuildKit/Testcontainers均有验证。实际Go/Node不再准备额外镜像，Tasks仅选3个依赖；冷热结果如下，预算和原测试保持。
- **11.4**：Admin Web原CI成功才允许publisher/native CD；真实c5db392 push的原CI及同计划镜像重跑完成GitOps/Flux发布。明确负向fixture保留原全部CI后exit23，正确阻断publisher和CD。另一fixture保留原CI，独立tag发布退出17的坏镜像；delivery `3c308a6d…`、digest `6c538235…` 保持delivered，Flux两次明确HealthCheckFailed、坏Pod退出17，旧副本HTTP200。回滚 `4a555320…` 后恢复健康digest `f29f6ff3…`，最终GitOps `774ef847aa457925b1935bf2de4a20199b0000be`，Flux Ready、Deployment 1/1、HTTP200，坏记录superseded。此前已验证baseline digest回滚/恢复及控制面重启后同ID/同GitOps提交幂等。

健康失败采样说明：Flux报错后立即开始下一次健康检查，Ready=False的瞬时状态被原3秒轮询漏采；改用同一Kustomization UID、当前attempted revision和本次delivery之后的持久HealthCheckFailed事件，另核对真实Pod退出17、目标digest、非deployed状态和HTTP200。原观察器因漏采以exit1退出，日志保留；后续使用同一实际失败运行、上述持久证据和同一个确定性回滚ID完成验证与恢复，没有重跑业务流水线或放松健康要求，未遗留观察器。

| 固定源码/缓存对照 | 准备 | 实际workflow执行 | 实际结果 |
|---|---:|---:|---|
| Server ac3f90d 冷 | 70.317秒 | 197.101秒 | success，Go cache-hit=false |
| Server 同计划热 | 70.484秒 | 60.454秒 | success，Go cache-hit=true |
| Admin Web 4d5cd9d 冷 | 70.224秒 | 283.736秒 | success，14文件/32测试，npm cache-hit=false |
| Admin Web 同计划热 | 71.964秒 | 102.335秒 | success，14文件/32测试，npm cache-hit=true |
| Tasks 22b8b32 独立ref冷 | 82.199秒 | 1200.416秒 | timed_out；原short/race、迁移及完整PG18先成功，coverage步骤达到原20分钟 |
| Tasks 同源码/步骤/Action main热 | 82.508秒 | 789.004秒 | 完整workflow success；Go与Atlas缓存true，完整PG18约4分28秒 |

Tasks的冷热ref不同，热对照复用main原有缓存，不称为同计划重跑或冷超时修复。热流程中的Go安装/缓存恢复约162秒，Codecov步骤约139秒；Codecov依旧因缺token拒绝外部上传，但原fail_ci_if_error=false使Action正常退出。保留这些外部/冷启动边界及计时依据，不保证任意空缓存或公网故障都能在20分钟内完成，也没有删减测试或扩大并发。公共Action固定SHA归档及SDK镜像工具复用继续沿用已验证实现。

最终自动验证：make verify通过（真实构建、Go race/HTTP/SQLite集成、lint/generate、42前端单测、17浏览器41.7秒）；完整Testcontainers PASS2466.697秒（28个k3s核心叶场景、6个辅助叶检查通过，7个额外环境可选fixture跳过，详见上文）；另有真实Docker固定digest/已加载镜像身份专项PASS6.354秒。没有把单测、计划、自动测试和真实部署混为同一结论。

收尾确认 `latest` / `sha-c5db392` 仍为健康 `f29f6ff3…`，负向镜像仅使用专用tag；GitHub托管image workflow处于active，未遗留暂停。platform-web原Kustomization spec及owner镜像覆盖 `6ee9e663…` 精确保留，原应用HTTP200；Admin后端service存在ready endpoint。没有本轮遗留executor Pod，最后etcd无触发告警，5分钟窗口P99 fsync约15ms/commit约38ms（现场时点观察，不是资源基线或未来负载保证）。Forgelet源码改动保留当前工作区，未自动提交；业务workflow与GitOps的已授权提交已实际发布。


## 2026-10-29：日常开发就绪检查与 Codecov 配置

结论：现有能力已足够开始使用，优先以 Admin Web 做普通开发分支→PR→合并→发布的日常试用；不继续把完整 Actions 兼容当作使用前置条件。阶段11的自动测试与真实集群验收保持原结论，本轮配置检查没有重新执行全量 Testcontainers，也没有新的发布/回滚验收。

- **Codecov 配置已完成，上传未成功**：owner提供的 `codecov.token` 是账户 API Token，`codecov-upload.token` 是 `CODECOV_TOKEN=...` 格式的 Global Upload Token。两文件均设为0600，并加入 Git 和 Docker context 排除。安全解析赋值文本，未通过shell执行。仅将上传token作为加密仓库Secret配置到使用Codecov的 `steam-tasks`、`steam-api`；没有全局注入或写入Git。
- **真实配置集成验证**：Tasks固定源码 `22b8b3238b85c2164b248fe88bcf48eddd2f2984`，常规CI入口、原测试/断言/20分钟预算保持，运行 `98b8116fa406cb130ab38770` 最终success。该手动workflow快照只给原coverage Action增加token、slug和关联name，不是新push，也不是原YAML原样验收；本次未打开可选PG18。Codecov create-commit/create-report/do-upload均返回 `Repository not found`，API查询也为404；`fail_ci_if_error: false`使步骤绿色，**不代表上传成功**。需继续确认Codecov侧私有仓库接入/可见性，不能仅凭404判定token错误或缺少某一权限。
- **业务YAML仍需合并引用**：两个远端workflow目前没有 `with.token`，只保存Secret不会自动注入。已在本机私有 `daily-readiness-20261029` 目录准备仅增加token/slug的补丁；未修改/推送业务仓库。证据、完整脱敏日志、配置名称及请求快照也保存在该目录，不含明文token。
- **缓存是真正的日常使用缺口**：缓存命名空间只使用完整ref，没有默认分支/PR base读取回退；新的开发分支可能重复冷编译。此前Tasks独立ref完整冷流程超时不能因main热运行成功而消除。下一批优先实现可信同仓库的回退读取，写入仍限定当前ref。
- **platform-web发布有配置冲突**：Flux `forgelet-platform-web.spec.images`固定外部registry的 `6ee9e663…`，GitOps文件仍为 `c85afdda…`；覆盖项会压过Forgelet的GitOps镜像提交。不能直接删除覆盖项导致回退：应先把GitOps对齐现网digest，再移除覆盖并验证新发布/回滚。本轮未修改该资源。Admin Web没有此覆盖，GitOps/实际运行均为 `f29f6ff3…`，可先使用。
- **CI归属尚未切换**：Admin Web/Tasks的GitHub托管workflow仍active，正常push可能两边执行并重复发布。试用仓库应选定Forgelet为唯一日常CI/镜像发布方，保留可重新启用的GitHub应急路径；本轮没有禁用workflow。当前私有仓库的GitHub合并保护API提示升级套餐，属于外部限制；不阻碍先人工核对Checks后合并。
- **Forgelet自身尚未接入**：App仓库目录和本地启用列表都没有本仓库；本地阶段11变更尚未提交。自身workflow单job预算40分钟，而最新本地完整Testcontainers已用2466.697秒，另有make verify；不能直接声称原预算足够。runner现有预载Go1.26而本项目需Go1.27，嵌套k3s资源也须实测。先形成可追溯提交，再分开快速verify与完整集成预算、验证按需执行环境；不因此阻止业务仓库先使用。

现有业务CD目标仅platform-web和Admin Web；其他仓库已验收CI/镜像不等于已经配置自动CD。历史分页、Pending原因及资源感知排队可随实际使用改进，不作为首个试用仓库的前置工程。


## 2026-10-29：12.1 开发分支缓存阻断修复

**代码和自动验证完成，尚未部署homelab或做业务真实PR验收。** 为解决其他项目每个开发分支重复冷编译，runtime blob GET/lookup现在按当前ref→PR目标分支→目录中的默认分支回退读取；每层保持精确key/restore-keys顺序，只在未命中时回退。目标分支由已验证PR事件持久化计划提供，默认分支来自控制面GitHub安装目录；不从执行器请求取分支、不猜main/master、不添加每请求GitHub I/O。目录缺失时保留当前ref。PUT仍只写当前ref，仓库/版本隔离、原配额和不可变对象约束保持。native和act v1/v2通过原端点共用规则，无新依赖、迁移或常驻服务。

自动验证分别记录：

- 顺序/去重/非main默认分支/PR base/未知目录单测；HTTP+真实SQLite覆盖实际持久化PR计划、lookup、当前ref前缀优先、后备精确/前缀命中、版本/仓库/兄弟分支隔离、默认分支禁止反读开发分支、写入参数不能越界、不可读当前对象不静默回退。
- `make verify` PASS：真实构建、全Go race/HTTP/SQLite、lint 0 issues、generate、42前端单测、17浏览器场景（1.1分钟）。server专项race PASS45.438秒；更新后的完整verify中server race PASS46.707秒。
- **Testcontainers专项 PASS324.970秒**：临时k3s实际安装/执行，原artifact/cache跨Pod与重启26.51秒；新增实际开发分支读取默认分支cache、开发分支保存、两次控制面重启后main仍读原值且不能读开发分支cache43.78秒。只选择这两个叶场景，**不是全量Testcontainers重跑**。临时k3s及构建容器正常清理，未使用用户kubeconfig。

日志保存在本机私有 `daily-readiness-20261029/forgelet-{daily-verify,cache-k3s,cache-race}.log`。12.1整体清单因homelab真实分支/PR验收尚缺保持未勾选；12.2的Flux覆盖与双CI发布归属、12.3的Codecov外部接入和自身CI预算仍未解决。本轮未修改业务仓库、禁用托管workflow、变更实际集群、提交或推送。


12.1交付继续：owner已授权提交/推送本批、部署homelab、真实开发分支与PR缓存验收，以及platform-web镜像覆盖和双CI归属修正。阶段11未提交实现与12.1一起纳入本批可追溯提交；owner自行修改的AGENTS.md保留本地不纳入提交。工作区diff与未跟踪文件已在本机私有stage12-delivery-20261029备份；部署及实际验收结果随后追加，不提前称为完成。


12.1/12.2线上推进：提交fb6e2f4已推送，server69fbb384…上线，SQL备份与管理/资源配置一致性核对通过。Admin Web真实开发分支8b3295f已push并创建PR#1，原setup-node已恢复main的npm缓存（cache-hit=true），全部测试尚未完成；原npm ci正遇npm registry metadata网络重试。原始GitHub签名opened delivery3851030510631419904因merge_commit_sha=null被旧服务400拒绝，不能宣称PR已验收。正在修复异步测试merge解析与先持久化再准入，并补单测/SQLite/重启Testcontainers；不改原PR为head执行。

platform-web GitOps acb5fe1先对齐现网6ee9e663，再移除Flux spec.images覆盖；相同Deployment模板、Ready/Healthy、原digest均确认，尚待新发布/回滚验收。Admin Web两个托管workflow及platform-web check.yml已明确停用，Forgelet入口保留；本轮不声称其他仓库已切换。操作前状态和恢复命令保留在stage12-delivery-20261029。


PR追加实测：f9d2b6d服务器a9a17192…已上线，原运行同Pod UID保留，原opened签名重投成功202并持久化等待。进一步用同一GitHub App比较实际API响应，确认默认旧版本提供merge_commit_sha，而控制面使用的2026-03-10不再返回该字段；原解析实现因此一直not ready，不能计为PR通过。现改为同仓库只读contents+pull_requests令牌，读取merge ref与不可变commit父列表并核对原始base/head；新夹具删除旧字段，补ref不存在与父提交变化回归。之前一次Testcontainers失败是新增夹具误用github容器名，已修正为github-test-api，失败日志保留。修正夹具后的专项825.448秒通过，含真实大BuildKit/自动发布依赖/重启/失败门禁及分支cache；最后只读令牌及旧字段移除专项281.327秒通过，但这两次尚未覆盖新的ref/parents实现，最终版本自动验证继续。


6a52234/merge-ref修复已上线，原事件显式重试后固定2a35cf6（父提交c5db392和8b3295f），PR运行befc785a已创建；原分支0bde91ea最终完整success，npm metadata单次重试约671秒保留实测，不把缓存命中当作网络消除。原始workflow/32测试与构建保持。最新ref/parents实现make verify通过42前端/17浏览器39.8秒，真实k3s签名PR/等待/重启/固定执行专项PASS202.909秒（25.70秒叶场景）。

又发现实际PR展示阻断：gh pr checks只见分支push成功，而PR merge Check写在测试合并SHA上。正在将PR Check关联到持久化事件的原始head，并用PR名称区分，执行计划SHA不变；旧Check需同App/job/精确merge SHA校验后幂等迁移，补单测和真实k3s断言。原PR仍在资源队列，不提前称通过。platform-web真实0d06918 push运行b498a426原48单测、28 Playwright与生产构建/smoke已通过，image/CD尚待结束；两个试用仓库该commit的GitHub hosted run总数均0。


真实PR befc785a已完整success，cache-hit=true、原32测试/check/build/生成文件检查全部通过；GitHub显示独立PR检查SUCCESS，与分支检查区分。核对原始base/head及当前merge ref后，Admin Web PR#1已squash合并为945431dc，等待原main CI/image自动执行与发布，未提前称main CD通过。

platform-web b498a426原build成功（48单测、28浏览器、build/smoke）；image已构建/推送，但GHA cache导出最终503失败，整体failure、CD正确skipped，健康6ee9e663保持。故障实际根因：总cache容量6GiB，现有4342683136B（4.044GiB），旧Store每个PUT无论大小都预留cache对象上限2GiB，因剩余不足2GiB拒绝小layer。现改为有界buffer写入前逐段预留、提交原子转账、失败/取消清理释放；总配额/对象上限/不可变缓存与TTL保持，无需扩大磁盘预算或修改原workflow忽略错误。

配额修复的单测/race/HTTP+SQLite通过，覆盖多个小写满额、并发流、超额失败、取消释放、幂等ACK；make verify PASS（42前端、17浏览器41.5秒，首次新增测试import分组lint失败已修正）。真实Testcontainers专项PASS238.232秒（50.33秒叶场景）：总空闲低于对象上限时两次不同cache保存成功，真实超额任务失败，控制面重启后两份已存cache均可恢复。只执行该专项，非全量重跑。服务器升级及原workflow重跑仍待完成。

公共Node24-alpine与nginx-unprivileged1.27-alpine的实际amd64镜像已缓存到Zot既有公开上游命名空间，未更改ACL或发布业务代码。Node官方根8a32c8cf对应amd64 manifest4eefa1bf，缺失约57MB公共层由本次job自有BuildKit只读提取、SHA256对照官方manifest验证后补齐；初次公网下载约1516秒的成本与早期失败同步日志保留。所有临时port-forward已退出、loopback凭据副本删除；后续构建是否命中镜像源另行实测。


## 阶段12.1/12.2收尾：日常分支、PR与唯一发布来源已验收（2026-10-29）

### 代码与部署

本批源码已提交并推送至 `chore/homelab-reset-20261023`：fb6e2f4（阶段11已验收实现与分支cache）、f9d2b6d/6a52234（PR异步合并SHA、2026 API merge ref和父提交校验）、e392535（PR head Checks与受限幂等迁移）、147129b（按实际流量预留cache配额）；3ff5f92补标准Buildx/cache与原reusable CI门禁夹具。owner本地AGENTS.md修改保留未提交。

实际server为 `zot.k.mygo.rocks/forgelet/server@sha256:ead5fd43a84938d09f73d37402f6db56c5dcd0a8e342a0f339787a28ecde737a`，对应运行代码147129b。每次升级前保存SQL备份，管理设置、安装授权、执行profile、CPU/内存/临时空间与并发预算保持，活跃任务Pod UID未改变。没有新增常驻服务、PVC、CRD或额外controller；缓存总预算仍6GiB，对象上限仍2GiB。最终本批所有执行Pod已回收，现场etcd firing告警为空；这是收尾时点核对，不是独立性能基线或未来负载保证。

### 真实开发分支与PR

Admin Web的真实文档修正分支8b3295f / push运行0bde91ea，从原main npm缓存恢复且原32测试、check/build/生成文件校验全部成功。PR https://github.com/shitamachi/steam-admin-web/pull/1 的初始真实opened事件merge SHA为null，旧400记录保留；修复后原GitHub签名重投202，固定merge `2a35cf6813352a5001166fbf44cbe2536c83656a`，原始base c5db392、head8b3295f均经API/ref父提交核对，运行befc785a完整success、cache-hit=true。执行仍是merge SHA，GitHub `forgelet / PR / admin-web / check`显示原head上的success，与分支push检查区分；旧merge Check经同App/job/精确SHA校验后迁移，未修改别的Check。操作前cache metadata只有main和旧独立验收ref，没有新分支或PR cache，证明不是先自行暖热同ref。

合并前再次核对base/head/当前merge ref及精确PR Check，squash为945431dc；main普通CI683f9369通过。其旧默认builder发布735ea473由操作者明确取消，原因是它不读取profile的BuildKit镜像源，为避免和替代发布交叉写标签；取消ACK与Pod清理已核对，不能把该运行计为发布成功。

### Main发布与GitOps

Admin Web随后提交标准 `docker/setup-buildx-action@v3` 与 `type=gha,scope=admin-web` cache（94f9fc6）；全部原CI命令和 `needs: check` 保留，未设置ignore-error。新真实main CI87dad40a和image6290501b均success，reusable CI原32测试通过，全部精确GitHub Checks成功。镜像 `sha256:5568a75a9bce38952b276e1c087bb4cc9dbf47faef0e385e52d9530154eff479`，latest与sha-94f9fc6一致；delivery `d09b497da28161541eae79794704130b` 已deployed，实际Deployment与GitOps/Flux一致，HTTP /healthz200。这个最终部署来自94f9fc6，**不是声称先前945431dc的已取消发布成功**。

platform-web先以GitOps acb5fe1对齐现网6ee9e663，再移除Kustomization.spec.images覆盖，原Deployment模板不变。真实0d06918/b498a426的48单测、28 Playwright、构建/smoke通过，但image虽推送、cache导出因配额预留错误失败，CD正确skipped；后续真实da701f5/aa52f253在GitHub凭据网络错误时未执行测试即failure，这些失败均保留。确认main仍da701f5后，同一固定源码、原始完整workflow/Action pins计划重跑 `198edac874dc6cd52f86602ef7977f2a`，原48/28测试、构建、镜像、cache导出、native CD全部success。delivery `e5503e10ef7dbb1a38f70130fbc735c1`，GitOps `febab059d92e8aa6e1c8d5cfc59cb965355fad16`，镜像 `sha256:665a04846d4259e2db7a4d44b129bf4553db8aaaf67d5de5646ea922ef45a9f5`，latest/完整提交标签/精确Checks/实际Deployment/Flux均一致，应用HTTP200。**这是原真实push失败后的固定计划重试，不改记第一轮push成功，也没有修改原platform workflow或删减断言。**

对该platform发布执行真实回滚e90fb897，GitOps78e4517恢复原健康6ee9e663；再回滚该回滚2b501322，GitOps `33b330616a1b8d1bc57c69ec8bbb83d59332b46f`恢复665a0484。两次deployed、Deployment可用、Flux Ready、HTTP200；重复原回滚请求返回同ID且GitOps HEAD不变。最终spec.images为空，GitOps文件是唯一镜像来源，Admin Web保持新5568a75a健康版本。

### 唯一CI来源、镜像缓存与边界

只切换两个试用仓库：Admin Web ci.yml/image.yml及platform-web check.yml均为disabled_manually；对应新提交的GitHub hosted run数为0。两个业务README已记录“先停用Forgelet对应workflow，再启用GitHub workflow”的应急步骤，不能两边同时发布。未声称其他7个业务仓库也已切换。Forgelet内部ci.yml与image.yml复用CI仍是原安全门禁，不把它们的多个内部Check冒充第二个发布者。

公共Node/Nginx镜像已存入Zot，匿名只读、平台digest/层校验通过；新增约83.5MB公共层资产，没有业务镜像内容公开。Admin新builder实际解析到Zot对应Node4eefa1bf与Nginx28d91bdc，基础层约5秒完成，原首轮公网约57MB耗时约1516秒的记录保留。标准setup-buildx会采用profile.BuildkitConfig；默认Docker driver不会自动采用该配置，兼容范围明确。浮动上游tag不自动刷新，更新由管理员显式核对。

本轮遇到的GitHub transport故障后来由既有事件退避恢复；容器内探针确认新HTTP/2连接可请求，不足以认定唯一协议根因。未修改生产HTTP/TLS策略，也未保留试验性的连接池代码或增加不明确的写请求重放。所有临时探针、port-forward和临时凭据副本已清理。

### 自动验证与证据

- 最终运行代码与标准Buildx夹具的make verify通过：真实构建、全Go race/HTTP/SQLite、lint/generate、42前端单测、17浏览器38.6秒；后续文档不改变运行代码。
- 本批Testcontainers分别记录：CI/CD/大BuildKit/分支cache/重启失败门禁专项825.448秒；PR merge ref/父提交专项202.909秒；PR head Check专项197.612秒；小cache实际预留、超额失败、重启恢复专项238.232秒。各自对应新增行为，**不是本批重新执行全量**；此前阶段11全量2466.697秒记录保持独立。首轮夹具容器名失败及lint失败日志保留，不改记通过。
- 真实验收与自动测试分开记录：分支/PR原始32测试，两个main CI/镜像，platform原48/28、固定计划重试、实际GitOps/Flux、回滚/恢复、托管CI停用及任务清理均有现场证据。私有 `stage12-delivery-20261029` 保存final-delivery-audit.json、完整脱敏日志、签名delivery、SQL升级备份、镜像/标签/Check匹配及回滚证明，不含明文token回显。

12.1/12.2完成；12.3仍有Codecov私有仓库外部接入（已有上传token但Repository not found）、业务YAML token引用及Forgelet自身CI/完整集成预算/Go1.27环境验证。只把已实际配置的两个业务CD目标计为交付，不扩展为所有仓库已自动部署。


## 阶段13阻断修复（2026-10-29，进行中）

当前在实现自动发布防倒退、凭据签发隔离与缓存容量淘汰。第一轮make verify通过（42前端单测、17浏览器39.7秒）；后续补充限流分类/持久化与终态回归后，blob/delivery/provider/storage race通过。真实k3s尚在验证，未部署，不把代码完成算作线上完成。

测试准备中默认Go代理下载慢，随后TUNA镜像403；已恢复此前验证使用的goproxy.cn与Aliyun构建配置，保留失败日志。未修改业务测试或集群资源额度。owner的AGENTS.md修改保留。


### 阶段13部署与真实验收进展（2026-10-30，完整回归仍在运行）

服务端已更新为 `zot.k.mygo.rocks/forgelet/server@sha256:db32c7c9b16a47dfb16e38b2e12317963c44160d9ed00931ea80bb6d660b077c`，对应本批未提交工作区二进制SHA前缀 `f0645a67c0da`，不是已有Git提交。升级前保存SQLite备份；auth、管理配置、执行profile、并发2与原资源预算保持。未新增常驻服务。owner的AGENTS.md修改原样保留，尚未自动提交/推送本批。

- **旧来源拒绝**：实际Admin Web旧源码945431dc、既有旧digest f29f6ff3，通过明确的诊断native投递运行 `f10cf9813ccb136167b82d8a`，在当前main为94f9fc6时得到 `superseded`。GitOps HEAD和原线上digest均未改变。此项使用真实仓库、提交和镜像，不是新的真实push，也不是重跑整个旧workflow。
- **缓存容量压力/恢复**：测试仓库forgelet-e2e-app用固定源码运行明确诊断workflow；临时将总blob额度设为既有占用+4MiB，写入12MiB cache及小artifact。实际仅淘汰一项cache，artifact保留、使用量未超过临时额度。恢复原6GiB配置并重启后，运行 `a4758de3489c0d86567aded1` 从新Pod恢复成功。测试改变的是配额压力，不冒称新增物理磁盘低水位验收。
- **完整Admin Web发布**：固定原始image workflow和94f9fc6源码重跑 `48b3a3e823b03474168cc7ddea1d5569` 完整success，原32项测试/check/build保持，npm cache-hit=true，Buildx镜像推送和cache导出成功。delivery `de16615f63d102e8b4367c77621b7373` 为deployed，来源身份已持久化；Zot digest `4d8ade69aa62e99acadaf79cb5281a7f917c89c59a410fb2f526a263800ded9d`，GitOps/Flux revision `8c959ceac639613f6dba13042d66563e1bd9f57a`，Deployment可用、healthz HTTP200，精确GitHub Checks成功。此项是原快照重跑，不是新的push。platform-web原digest665a0484保持、无spec.images覆盖、HTTP200。
- **凭据真实复验进行中**：原steam-cm凭据失败运行的同一150a780源码/原固定计划重跑 `a46c97d8a9c8dd887440cdfebb47ce2e` 已取得凭据进入Rust编译；整条test/image workflow尚未完成，不提前记为通过。
- **自动验证**：最终生产代码的 `make verify` PASS（Go race/HTTP/SQLite、构建/lint/generate、42前端单测、17浏览器场景39.0秒）。较早专项Testcontainers PASS1020.956秒，覆盖实际k3s新缓存淘汰/跨Pod/重启、分支隔离、BuildKit/Flux/失败/回滚与凭据暂态恢复/永久拒绝；该次早于最终共享GitOps分支串行及额外对象超限用例，不能代替最终全量回归。最终全量仍在运行。

私有证据目录 `~/.config/forgelet/stage13-reliability-20261029` 保存部署/容量配置恢复、旧来源拒绝、运行/日志、GitOps/Flux/HTTP/Checks与现场etcd告警检查。当前检查etcd firing为空，仅代表现场时点，无额外资源基线门禁。


阶段13追加边界修正：两个target共用GitOps分支时，必须先恢复既有Preparing记录的分支占用，再处理刚完成CI的较早Queued来源；只按创建顺序临时加锁仍可能让后者抢占已有快照。已补尚未生成commit/已生成commit两种重启回归，delivery/storage race通过；更新后make verify完整通过（42前端、17浏览器38.9秒）。最终服务端更新为 `sha256:a05d530e98ff7795998de2f79486fe3d702af10aba34a6008b8beadc336ba5e2`，二进制SHA `18668e5f55705f443b1c2a424d0e25c6763b0709096e404b0c0e5657e60a99c3`，原steam-cm活跃Pod UID `07192b19-a2ad-4146-bf42-b803fea127a8` 保留并继续执行。最终版本重新进行旧来源投递运行 `4bddb678fc07b52c3c4b3a10`，仍superseded，GitOps和现网未变。

steam-cm原test job已完整success，141项通过、0失败、原3项ignored保留；image job继续编译。自动回归安排为已启动完整套件完成后，顺序运行最终代码的BuildKit/Flux发布/重启/失败/回滚Testcontainers专项（最后变动仅涉及发布队列；凭据与缓存由完整套件覆盖），避免同时创建两个临时k3s争用本机资源。两次结果将在完成后分别追加，不把正在运行当作通过。


**真实仓库最终结果（2026-10-30 00:29）**：steam-cm `a46c97d8a9c8dd887440cdfebb47ce2e` /150a780完整success，保留原9步test和原image workflow、原30/45分钟job预算。test合计141通过、0失败、原3 ignored不变；release镜像ID `32935319967b8d27223a0e553b9989d5664361d246fce802199b7b33222d0bd3`，构建6分27.9秒，非root/只读/health/shutdown/redaction smoke成功；managed-test编译3分47.8秒，精确release镜像中两项managed lifecycle测试成功20.6秒。原普通push的publish=false远程发布步骤正确skipped，不连接真实Steam、不部署CM业务。GitHub精确源码的两个Forgelet Checks均success。旧凭据失败 `e45b1bdd8670069a25084539` 保留；本次未复现公网依赖下载超时，但不据此承诺公网依赖永远可用。

收尾现场：Admin仍为4d8ade69、platform-web仍为665a0484，两者Flux Ready/HTTP200；etcd firing为空，forgelet命名空间仅剩控制面Pod，没有遗留本批执行Pod。完整套件和最终补丁专项结果仍待追加。


**完整自动回归通过**：`go test -tags=integration ./test/integration -count=1 -v -timeout=95m` PASS **2642.225秒**。新增缓存满额淘汰/对象超限/重启50.05秒，artifact跨Pod/重启41.17秒，分支缓存隔离41.76秒，真实BuildKit/Flux590.71秒（含旧来源拒绝/自动发布依赖/重启/失败门禁187.04秒），凭据暂态恢复/永久拒绝8.80秒；>1GiB BuildKit cache冷热/重启/失败/取消506.92秒，act435.65秒、Rust156.76秒，真实SDK46.70秒，浏览器71.90秒（4个真实页面场景47.2秒），登录授权/重启83.86秒，日志/历史保留19.08秒均通过。完整命令的7个可选SKIP为：Atlas安装器/构建stage、DuckDB工具、reaper工具、Zot ACL，以及两组需要私有业务clone目录的workflow fixture；没有将它们记为通过，真实业务验收另见上文。SDK通过官方checksum验证的预载归档仍运行真实Action，不把预载计为公网下载验证。

完整套件覆盖最终凭据/缓存代码及首版共享分支串行；它启动后补充的“既有Preparing优先”边界由最终make verify和当前新的BuildKit/Flux专项单独验证，不能声称完整套件已经包含后补代码。首个临时k3s已清理，新专项独立顺序启动。


## 阶段13优先三项交付完成（2026-10-30）

**实现与验证**分别完成：

- GitOps发布前核对持久化run对应的实际来源head；过时来源为superseded，不推进分支。保留丢ACK恢复和显式回滚；同一GitOps分支串行准备/发布，重启先恢复Preparing占用，避免不同应用互相制造旧parent冲突。
- GitHub token网络请求不再持有全局锁；按scope合并、等待可取消、10秒签发预算。永久拒绝明确结束，限流/暂态故障沿原窗口重试，诊断脱敏；没有承诺消除所有外部网络故障。
- 满额时按访问时间淘汰可重建cache，保留artifact、活跃读流、当前producer和在途预留；重启恢复LRU。总额度仍6GiB，没有可回收项或超过单对象限制仍明确失败。

最终代码 `make verify` PASS（Go race/HTTP/SQLite、构建、lint/generate、42前端单测、17浏览器测试38.9秒）。完整Testcontainers **2642.225秒**，k3s **31个通过叶场景**，整个命令7项可选SKIP已逐项列明。最后补充的Preparing优先逻辑另经delivery/storage race、make verify，以及最终代码实际BuildKit/Flux专项 **677.700秒**；CI/CD场景509.14秒，其中自动依赖/重启/旧来源拒绝/失败门禁156.91秒。专项只选择发布相关场景，不冒称第二次全量回归；两个临时k3s顺序执行并正常清理，没有读取用户kubeconfig。

**homelab最终版本**：`zot.k.mygo.rocks/forgelet/server@sha256:a05d530e98ff7795998de2f79486fe3d702af10aba34a6008b8beadc336ba5e2`，二进制18668e5f5570；SQLite已备份，配置/权限/资源预算与并发2保留，升级保留活跃steam-cm Pod UID。Admin Web 48b3a3e8的原workflow完整重跑、缓存恢复/导出及GitOps/Flux部署4d8ade69通过；steam-cm a46c97d8的原workflow完整重跑与GitHub Checks成功。两次都使用原固定源码/计划，不是新增push。最终版本旧来源诊断4bddb678被superseded；测试仓库缓存满额/产物保留/重启恢复通过，临时额度已恢复。真实验证的完整ID、digest、revision、测试结果和边界见上文。

**尚未覆盖的边界**：防倒退针对自动GitOps部署，不为registry的可变latest标签排序；部署继续使用digest。Tasks原20分钟冷缓存完整流程、其余仓库的唯一CI来源和实际业务CD接管仍未完成。CM本次未复现旧公网下载超时，不能因此将外部依赖可用性视为已根治。Codecov外部接入及Forgelet自身CI仍按12.3推进。

本轮源码未自动提交/推送；owner的AGENTS.md修改保持原样。最终补丁、新测试文件、镜像与二进制hash、升级备份、自动验证日志及真实验收证据保存在本机私有 `~/.config/forgelet/stage13-reliability-20261029`，不含对外回显的token。
