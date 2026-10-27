# 开发、部署与恢复

当前版本提供 shell、needs/matrix/并发组、真实 Node/Composite Actions、push/同仓库 PR、filters/manual inputs、
仓库 secrets/vars、日志与历史保留，以及 PVC artifact/cache、按需 BuildKit、GitOps/Flux 部署与 digest 回滚。
executor 镜像含 bash、git、Node 20/24 与解压工具；已验证的 setup-go/setup-node 可按需安装工具链，精确范围见 [compatibility.md](compatibility.md)。

以下命令供管理员在自己的环境执行；普通单测和 Testcontainers 测试不读取用户 kubeconfig。

## 本地开发与隔离集成测试

需要 Go 1.27+、Node 24（验证版本 24.14.0）与 npm；lint 使用仓库 CI 指定的 golangci-lint。镜像与容器测试需要可用的 Docker 服务。
Node 用于前端构建、Vitest/Playwright 和真实 Action 单测，生产 server 不运行 Node。Testcontainers 中 Node 20/24 Action 测试必须执行。

```sh
make build              # 静态 Web + bin/forgelet、bin/forgelet-executor
make test               # race + 单测、真实 SQLite、HTTP 集成测试
make lint
make web-test           # TypeScript、格式、Vitest
make web-e2e            # Chromium + 确定性测试 API 的页面交互
make verify            # 上述构建/测试，加 generate
make test-integration   # Testcontainers 新建临时 k3s，运行后清理
```

首次运行会用 `npm ci` 安装锁定依赖，浏览器测试会下载 Playwright Chromium。Linux 首次需在 `web/` 运行
`npx playwright install --with-deps chromium`；CI 已包含这一步。无前端改动时可单独 `make test` 运行 Go 测试。
直接 `go build` 不生成页面，正式二进制请用 `make build`；Dockerfile 自动在 Node 构建阶段生成并嵌入页面。

开发 UI 时，启动一个自己的开发控制面（`admin_access: open`），再执行：

```sh
make web-install
FORGELET_API_URL=http://127.0.0.1:8080 npm --prefix web run dev
```

浏览器打开 `http://127.0.0.1:5173`；Vite 将 `/api` 代理到指定开发控制面。
只想预览 UI 可执行 `make web-build && npm --prefix web run preview:test`，打开 `http://127.0.0.1:4173`；
这是确定性测试数据，操作仅作用于预览内存，不连接 GitHub 或集群。测试/预览 API 不会编入产品。

容器测试使用 `rancher/k3s:v1.34.4-k3s1`，自行构建 Linux 测试镜像、创建集群并从该容器获取 kubeconfig。
它使用正式 Dockerfile 的 executor 环境和实际 server 二进制，验证调度/身份/恢复/清理、签名 webhook、matrix/并发组、
Node/Composite、加密 secrets/typed inputs，以及公开固定 SHA 的 setup-go/setup-node 实际下载工具并运行程序，**不会连接当前 kubectl context**。
Web 用例启动 Chromium 访问该集群的正式嵌入页面，验证手动运行、needs、日志/产物下载、重跑、运行中取消、仓库设置、备份和重启持久化；浏览器不拦截 API。
GitHub App API 使用测试 HTTPS sidecar；不会创建真实 GitHub Check。公开 Actions/工具链下载仍访问真实外部服务，Go 命令超时上限 95 分钟；临时 k3s 场景默认 context 为 55 分钟，显式启用 10.6c 原业务构建专项时为 90 分钟。
10.6b 可设置 `FORGELET_TEST_BUSINESS_DIR` 验证两个固定提交的原业务 workflow；快照准备、隔离发布和 skip 边界见 [原业务测试说明](../test/fixtures/steam-business/README.md)。
上述 Docker 用于开发测试；native 执行 Pod 不挂载 Docker socket。10.5b 可选 profile 见本文末尾。容器测试结果以实际运行输出为准。

## 镜像与安装文件

在构建机执行；镜像架构必须与目标节点相符。默认构建的是构建机架构，不会自动生成多架构镜像。

```sh
make images
docker save forgelet-server:dev forgelet-executor:dev -o /tmp/forgelet-images.tar
cp deploy/install.yaml deploy/install.local.yaml
```

把镜像归档复制到**每个可能调度到的 k3s 节点**，在各节点执行：

```sh
sudo k3s ctr images import /tmp/forgelet-images.tar
sudo k3s ctr images list
```

也可推送到已有 registry：`make images IMAGE_REGISTRY=ghcr.io/OWNER/forgelet IMAGE_TAG=YOUR_TAG`，
然后推送生成的 `ghcr.io/OWNER/forgelet-server:YOUR_TAG` 和 `.../forgelet-executor:YOUR_TAG`。
同步修改本地安装文件中的 server image 和 profile image；私有 registry 的节点拉取凭证需要自行配置。
同一 `dev` 标签配合 `IfNotPresent` 可能使用旧镜像，日常更新建议使用新标签或 digest。
镜像导入机制见 [k3s 文档](https://docs.k3s.io/add-ons/import-images)。

先检查 `kubectl config current-context`，下面的安装命令会修改该 context 对应的集群。
安装文件包含一个 namespace、两个 ServiceAccount、Pod 操作 Role、TokenReview 权限、10 GiB PVC、
单副本 Recreate Deployment 和 ClusterIP Service；需要默认 StorageClass，不创建 Ingress。

## TLS 与凭证

安装清单默认 `admin_access: session`：首次启动初始化 admin，先用初始密码登录，再在账号页面关联 GitHub。完整配置见本页“管理员与 GitHub 登录”。显式 open 继续作为开发/已有内网部署的兼容模式。
管理入口面向自己的内网/VPN；公网 Ingress 仍只转发 `/webhooks/github`。session/open 模式的管理写请求需要 `Content-Type: application/json`，浏览器必须同源。旧 token 模式需要显式 Bearer；只读 Basic 不能授权写入。
反向代理应保留 Host，`public_url` 填入实际内部管理域名（用于同源判断和 Checks 详情链接）。
已有部署省略此项时仍使用 token 模式；正式 Web 可显式切换 session 并配置正确 public_url，初次启动再初始化 admin。
需要继续 token API 的部署可设 `admin_access: token`，保留至少 16 字符的 `FORGELET_ADMIN_TOKEN`；前端不提供 token 输入；账号登录使用 session 模式。

默认内部地址为 `https://forgelet.forgelet.svc:8443`。以下生成内部 CA 和包含 Service DNS 的服务证书，
还包含 localhost/127.0.0.1，方便安全地使用 port-forward。已有内部 PKI 时可使用其签发的证书替换。

```sh
umask 077
FORGELET_SECRETS_DIR="$HOME/.config/forgelet"
mkdir -p "$FORGELET_SECRETS_DIR"
cat > "$FORGELET_SECRETS_DIR/ca.cnf" <<'EOF'
[req]
distinguished_name=dn
x509_extensions=ca
prompt=no
[dn]
CN=forgelet internal CA
[ca]
basicConstraints=critical,CA:TRUE
keyUsage=critical,keyCertSign,cRLSign
subjectKeyIdentifier=hash
EOF
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
  -config "$FORGELET_SECRETS_DIR/ca.cnf" \
  -keyout "$FORGELET_SECRETS_DIR/ca.key" -out "$FORGELET_SECRETS_DIR/ca.crt"
openssl req -new -newkey rsa:2048 -nodes -subj '/CN=forgelet.forgelet.svc' \
  -keyout "$FORGELET_SECRETS_DIR/tls.key" -out "$FORGELET_SECRETS_DIR/tls.csr"
cat > "$FORGELET_SECRETS_DIR/server.ext" <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:forgelet,DNS:forgelet.forgelet,DNS:forgelet.forgelet.svc,DNS:forgelet.forgelet.svc.cluster.local,DNS:localhost,IP:127.0.0.1
EOF
openssl x509 -req -in "$FORGELET_SECRETS_DIR/tls.csr" \
  -CA "$FORGELET_SECRETS_DIR/ca.crt" -CAkey "$FORGELET_SECRETS_DIR/ca.key" \
  -CAcreateserial -days 365 -extfile "$FORGELET_SECRETS_DIR/server.ext" \
  -out "$FORGELET_SECRETS_DIR/tls.crt"
openssl rand -hex 32 | tr -d '\n' > "$FORGELET_SECRETS_DIR/webhook-secret"

kubectl create namespace forgelet --dry-run=client -o yaml | kubectl apply -f -
kubectl -n forgelet create secret tls forgelet-api-tls \
  --cert="$FORGELET_SECRETS_DIR/tls.crt" --key="$FORGELET_SECRETS_DIR/tls.key" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n forgelet create secret generic forgelet-api-ca \
  --from-file=ca.crt="$FORGELET_SECRETS_DIR/ca.crt" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n forgelet create secret generic forgelet-auth \
  --from-file=webhook-secret="$FORGELET_SECRETS_DIR/webhook-secret" \
  --dry-run=client -o yaml | kubectl apply -f -
```

保管 CA 私钥和 token 文件，不放进 Git。已有部署不要再次运行随机 token 生成命令，除非正在进行密钥轮换。
更换 namespace/Service DNS 时同步修改 SAN、`internal_url` 和清单；证书到期前替换 Secret 并重启控制面。
`runtime_ca_secret: forgelet-api-ca` 把 `ca.crt` 挂载到执行 Pod，执行器验证内部 HTTPS。
`tls_cert_file` 和 `tls_key_file` 必须同时配置；HTTP 仅在显式设置 `allow_insecure_runtime: true` 时允许。

## 配置与 GitHub App

编辑 `deploy/install.local.yaml` 中 ConfigMap 的 `forgelet.yaml`：

| 配置 | 设置 |
|---|---|
| `repositories` | 允许执行的 `owner/repo`，大小写与 GitHub payload 一致 |
| `profiles.homelab.control_plane_timeout` | 临时控制面中断容忍窗口，例如 `5m`；默认 5 分钟，最大 1 小时 |
| `profiles.homelab.image` | 已导入或可以拉取的执行镜像；workflow 使用 `runs-on: homelab` |
| `max_parallel` | 默认 4，按集群余量调整；starting/running 都占额度 |
| `public_url` | 用户打开 Checks 详情时访问的地址；token/open 可省略，此时 Checks 不带详情链接；session 必填 |
| `internal_url` / TLS / CA | 与上节 Service 和证书一致 |
| `github_app_id` / `github_app_key_file` | 手动模式都设为 `''`；接 GitHub 时同时填写 |
| `secret_key_file` | 不启用 secrets 时留空；启用后填 `/encryption/key`，密钥只挂 server |

配置只接受一个 YAML 文档和已知字段。单控制面、单数据目录；不要增加 Deployment 副本数。

接入 GitHub 时：

1. 注册 GitHub App，仓库权限选择 **Contents: Read-only、Checks: Read & write**，保留自动授予的
   Metadata 读取权限；订阅 **Push**。需要 PR 时增加 **Pull requests: Read-only** 并订阅 **Pull request**，只安装到可信仓库。
2. Webhook URL 为 `https://你的公开域名/webhooks/github`，Secret 使用上面 `webhook-secret` 文件的值。
   公开入口需要 GitHub 信任的 TLS 证书；内部 CA 证书不直接作为公网证书。使用现有反向代理接入，
   代理回源到 HTTPS Service 时信任内部 CA。外部通常只开放 webhook，管理界面/API 通过内网或 VPN 访问。
3. 下载 App RSA private key，然后创建 Secret：

```sh
kubectl -n forgelet create secret generic forgelet-github \
  --from-file=private-key.pem=/你的安全路径/github-app-private-key.pem \
  --dry-run=client -o yaml | kubectl apply -f -
```


在配置中写入数值 App ID，例如 `github_app_id: '123456'`，并设置
`github_app_key_file: /github/private-key.pem`。App ID 与 installation ID 是不同的值：
Webhook 自动取得 installation ID，手动调用需要该仓库的 installation ID 才能使用私有 checkout 和回写 Checks。
forgelet 使用短期安装 token，给执行器的 clone token 限定单个仓库、Contents 只读。

被 forgelet 接管的 workflow 应在 GitHub 原生 Actions 中禁用；不要先双跑再假设系统会自动去重。
当前接收 push 与同仓库 PR，fork PR 忽略；PR 必须提供可用 merge SHA，详见兼容文档。
[GitHub PR webhook 权限](https://docs.github.com/en/webhooks/webhook-events-and-payloads#pull_request)。
参考 [注册 GitHub App](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app)
和 [Checks API 权限](https://docs.github.com/en/rest/checks/runs)。

应用配置并等待启动：

```sh
kubectl apply -f deploy/install.local.yaml
kubectl -n forgelet rollout status deployment/forgelet --timeout=180s
kubectl -n forgelet logs deployment/forgelet
```

基础设施配置文件不是热加载；修改并 apply 后运行 `kubectl -n forgelet rollout restart deployment/forgelet`。
执行 Pod 的 SA 没有 Kubernetes API 权限；当前只运行可信代码，安装文件尚不包含 NetworkPolicy。
使用公开 Actions/工具链需允许 executor 访问 codeload.github.com、GitHub release 下载和工具供应商下载域名；不会自动转发 clone token。

## 页面管理仓库、workflow 与发布目标

session 模式登录后可打开 `/connections`、`/workflows`、`/targets`；已有 open 模式仍可直接打开。这些操作即时生效，无需修改 ConfigMap 或重启。

1. **仓库接入**：填入 `owner/repository` 与已有 GitHub App installation ID，启用并保存。点击“检查接入”并填写完整 commit SHA，验证该 App 可以读取 `.github/workflows`。App 安装、权限与 webhook 仍按上一节在 GitHub 授权；纯手动 shell 可用 installation 0。
2. **Workflows**：登记仓库与 `.github/workflows/*.yml/.yaml` 路径。Git 文件模式按事件 SHA 读取；切换“控制面保存 YAML”可编辑并保存独立源码，也可填写 SHA 读取 Git 源码为编辑副本。保存不修改远端 Git。启停决定未来事件是否执行；页面“运行”填写固定 SHA、ref、事件和 inputs，通过现有管理运行链执行。
3. **发布目标**：填写来源仓库/ref、GitOps 仓库/installation/分支/文件、direct 或 PR、镜像映射、Flux 与应用资源字段。必须已配置 GitHub App；Flux controller 和必要 RBAC 仍需安装。保存不会自行发布，workflow 的 forgelet/deploy 请求使用对应目标。

首次打开显示安装文件的初始配置，**第一次保存后 repositories/workflows/targets 以 SQLite 为准**，覆盖这三项的安装文件默认值；其他设置仍来自安装文件。
修改失败不会应用部分配置。若有其他页面先保存，当前保存返回 409，保留未提交表单；关闭表单并点“重新加载”后再编辑。
每仓库没有 workflow 登记规则时继续发现 Git 文件；登记后只执行启用的路径，全部停用时不执行该仓库 workflow。
移除仓库会同时移除其 workflow/target 登记；历史和 vars/secrets 保留。删除 target 后，运行中任务若尚未请求投递，该请求将失败；已创建发布记录不改变。

管理 API 可通过同源 JSON 客户端使用：`GET /api/management` 读取当前完整文档，修改后以原 version `PUT /api/management`；成功返回递增 version。不要把安装文件中的密钥或 token 写入该文档。
数据库备份包括这些设置，跨环境恢复前核对安装授权和目标资源；完整验证结果见 status。

## 手动运行、查看与取消

先在单独终端保持端口转发：

```sh
kubectl -n forgelet port-forward service/forgelet 8443:8443
```

另一个终端准备 curl 包装函数和示例工作流（需要 jq）。`FORGELET_SHA` 应是目标仓库的完整 commit SHA，
`example/project` 必须在 allowlist 中。本例不 checkout，便于先验证执行链路：

```sh
FORGELET_SECRETS_DIR="$HOME/.config/forgelet"
FORGELET_WORK_DIR="$(mktemp -d)"
FORGELET_SHA="$(git rev-parse HEAD)"
forgelet_api() {
  curl --fail-with-body --silent --show-error \
    --cacert "$FORGELET_SECRETS_DIR/ca.crt" \
    -H 'Content-Type: application/json' "$@"
}
cat > "$FORGELET_WORK_DIR/smoke.yml" <<'EOF'
on: push
jobs:
  smoke:
    runs-on: homelab
    steps:
      - id: first
        run: |
          echo hello > greeting.txt
          echo answer=42 >> "$GITHUB_OUTPUT"
      - run: |
          cat greeting.txt
          test '${{ steps.first.outputs.answer }}' = 42
EOF
jq -n --arg sha "$FORGELET_SHA" --rawfile workflow "$FORGELET_WORK_DIR/smoke.yml" \
  '{repository:"example/project",sha:$sha,ref:"refs/heads/main",workflow:$workflow}' |
  forgelet_api -X POST https://localhost:8443/api/runs \
    -H 'Content-Type: application/json' --data-binary @- > "$FORGELET_WORK_DIR/run.json"
FORGELET_RUN_ID="$(jq -r .id "$FORGELET_WORK_DIR/run.json")"
FORGELET_JOB_ID="$(jq -r '.jobs[0].id' "$FORGELET_WORK_DIR/run.json")"

forgelet_api https://localhost:8443/api/runs
forgelet_api "https://localhost:8443/api/runs/$FORGELET_RUN_ID"
forgelet_api "https://localhost:8443/api/jobs/$FORGELET_JOB_ID/logs?after=0"
forgelet_api -N -H 'Accept: text/event-stream' "https://localhost:8443/api/jobs/$FORGELET_JOB_ID/logs?after=0"
forgelet_api -X POST "https://localhost:8443/api/runs/$FORGELET_RUN_ID/cancel"
forgelet_api -X POST "https://localhost:8443/api/runs/$FORGELET_RUN_ID/rerun"
```

重跑只接受终态 run，重新检查仓库启用状态、当前 profile/BuildKit 能力，创建新 run/job ID 并保留固定 SHA。GET /api/runs 为轻量列表，省略执行 payload、steps、变量和详细结果；完整计划与结果通过 GET /api/runs/{id} 获取。日志 JSON 每页最多 256 条，下一页的 `after`
使用上页最后的 `seq`；SSE 也支持该游标。Web 工作台在 `/`，运行详情在 `/runs/<id>`，支持操作与完整日志。
`GET /api/jobs/<id>/logs/download` 下载全部日志，日志过期返回 410。`GET /api/events` 查看 webhook/编译诊断。
以上 curl 示例用于 open 模式；旧 token 模式另加 `Authorization: Bearer <token>`。
私有仓库的手动请求需再填写 `installation_id`；不提供时不会回写 Checks。

## 仓库 secrets、vars 与 manual inputs

启用 secrets 时生成一次 32 bytes **原始二进制**密钥，单独安全备份；已有部署不要覆盖原密钥：

```sh
openssl rand 32 > "$FORGELET_SECRETS_DIR/encryption-key"
kubectl -n forgelet create secret generic forgelet-encryption \
  --from-file=key="$FORGELET_SECRETS_DIR/encryption-key" \
  --dry-run=client -o yaml | kubectl apply -f -
```

安装清单已有 optional Secret volume；将 `secret_key_file` 改为 `/encryption/key` 后 apply 并重启 server。
不配置密钥时 vars 可用，secrets 写入返回 503。换一个随机密钥不能解密已有数据；当前没有在线轮换 API。

```sh
# Secret 文件中只放该值，避免把凭证写入 shell 历史。
jq -n --rawfile value /你的安全路径/deploy-token '{value:$value}' |
  forgelet_api -X PUT https://localhost:8443/api/repositories/example/project/secrets/DEPLOY_TOKEN \
    -H 'Content-Type: application/json' --data-binary @-
forgelet_api -X PUT https://localhost:8443/api/repositories/example/project/vars/TARGET \
  -H 'Content-Type: application/json' --data '{"value":"homelab"}'
forgelet_api https://localhost:8443/api/repositories/example/project/secrets
forgelet_api https://localhost:8443/api/repositories/example/project/vars
# 删除指定项使用 DELETE /api/repositories/example/project/{secrets|vars}/NAME。
```

Secret 列表仅返回 names；vars 返回明文值。每类每仓库最多 100 项，secret 最多 4 KiB、var 最多 16 KiB。
在 workflow 中通过 `${{ secrets.DEPLOY_TOKEN }}`、`${{ vars.TARGET }}` 引用；secrets 不可直接用于 if，
需要条件时先放入 step env。不要把明文凭证直接写进 workflow 或 inputs，后两者会保存在历史中。

声明 `on: workflow_dispatch` 和 inputs 的 workflow 可用同一 POST `/api/runs` 提交：

```json
{
  "repository": "example/project",
  "sha": "目标仓库的40位commit SHA",
  "ref": "refs/heads/main",
  "event_name": "workflow_dispatch",
  "inputs": {"enabled": true, "count": 2, "target": "dev"},
  "workflow": "已声明上述inputs的workflow源码"
}
```

布尔和 number 必须使用 JSON 原生类型；未知 inputs 或不匹配的 choice 会拒绝。
`changed_files` 数组用于手动 push 的 path filters，需要提供完整变更列表。重跑保留 vars 快照，执行时读取最新 secrets。

## 备份与显式恢复

日志目前也在 SQLite 中，备份 API 生成包含事件、计划、记录、CD 投递和日志的一致性快照。
artifact/cache 在 `/data/blobs/`，不包含在这个数据库快照中；需要保留 artifact 时另做 PVC 文件备份，cache 可以重建。
ConfigMap、Kubernetes Secrets、secrets 加密主密钥和 CA 私钥另行备份；同 PVC 上的副本不能代替节点外备份。

```sh
forgelet_api -X POST https://localhost:8443/api/backup > "$FORGELET_WORK_DIR/backup.json"
FORGELET_BACKUP_FILE="$(jq -r .file "$FORGELET_WORK_DIR/backup.json")"
FORGELET_SERVER_POD="$(kubectl -n forgelet get pods -l app=forgelet -o jsonpath='{.items[0].metadata.name}')"
kubectl -n forgelet cp --no-preserve \
  "$FORGELET_SERVER_POD:/data/$FORGELET_BACKUP_FILE" "$FORGELET_WORK_DIR/$FORGELET_BACKUP_FILE"
```

把快照复制到自己的长期备份位置并核对校验值。备份带 `restore_required` 标记，直接作为数据库启动会被拒绝。
恢复时先停止旧控制面和旧执行 Pod；如果节点失联，应先隔离或停止那个节点，不能仅凭 API 对象已删除推断进程停止。
下面用维护 Pod 挂原 PVC，保留旧数据库后再替换：

```sh
kubectl -n forgelet scale deployment/forgelet --replicas=0
kubectl -n forgelet wait --for=delete pod -l app=forgelet --timeout=120s
kubectl -n forgelet delete pods -l forgelet.dev/job --wait=true
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: forgelet-restore
  namespace: forgelet
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  securityContext: {runAsUser: 1000, runAsGroup: 1000, fsGroup: 1000}
  containers:
    - name: restore
      image: forgelet-server:dev
      imagePullPolicy: IfNotPresent
      command: [sleep, '3600']
      volumeMounts: [{name: data, mountPath: /data}]
  volumes:
    - name: data
      persistentVolumeClaim: {claimName: forgelet-data}
EOF
kubectl -n forgelet wait --for=condition=Ready pod/forgelet-restore --timeout=120s
kubectl -n forgelet cp --no-preserve \
  "$FORGELET_WORK_DIR/$FORGELET_BACKUP_FILE" forgelet-restore:/data/restore-candidate.db
kubectl -n forgelet exec forgelet-restore -- \
  /usr/local/bin/forgelet --prepare-restore /data/restore-candidate.db
kubectl -n forgelet exec forgelet-restore -- sh -ec '
  saved="/data/before-restore-$(date +%s)"
  mkdir "$saved"
  for suffix in "" "-wal" "-shm"; do
    if [ -f "/data/forgelet.db$suffix" ]; then mv "/data/forgelet.db$suffix" "$saved/"; fi
  done
  mv /data/restore-candidate.db /data/forgelet.db
'
kubectl -n forgelet delete pod forgelet-restore --wait=true
kubectl -n forgelet scale deployment/forgelet --replicas=1
kubectl -n forgelet rollout status deployment/forgelet --timeout=180s
```

维护 Pod 的 image 也应替换成当前使用的 server 镜像。`--prepare-restore` 不启动服务，它把快照中未完成的
job 标为 interrupted、隔离待处理事件及活跃 CD 投递，再解除恢复标记。已创建的远端 PR 不会自动关闭；核对 Git/Flux 后再发新投递。
确认外部副作用后再人工重跑；备份时间之后的数据
和去重记录可能丢失。运行中的数据库不要直接复制、替换或只恢复 `.db` 却保留旧 WAL。

## 历史、日志和备份保留

安装清单已设置自动清理，修改 ConfigMap 后重启 server 生效：

```yaml
retention:
  history: 720h   # 全部 job 完成满 30 天后清理 run/job/计划/结果
  logs: 168h      # 完成满 7 天后先清理日志
  events: 720h    # 已处理事件的原始 payload；delivery 身份仍保留
  backups: 168h   # 本程序在 data_dir 生成的备份快照
  interval: 10m
```

所有值都是正的 Go duration，例如 `24h`，不接受 `7d`；logs 不得大于 history。每次最多处理 100 个历史 run、
100 个日志 job、100 个事件和 100 个备份，后续周期继续。无需常驻额外清理服务，也不创建 CronJob。

```sh
forgelet_api -X POST https://localhost:8443/api/maintenance
```

该管理员接口立即运行同一策略，返回 `runs/jobs/logs/events/backups/blobs/deployments` 清理计数（logs 是日志块数）。
自动与手动清理都保护整个未完成 run、仍有 Pod 的 run；历史还会等待 GitHub Check 回写完成。
手动运行没有 installation ID 时不需要等待 GitHub。日志过期返回 HTTP 410 和 `logs_expired: true`，历史仍可查看；
历史过期返回 404，不能再重跑该记录。旧 delivery/工作流唯一键保留为精简去重记录，重投不会重新执行。

备份清理只匹配 `backup-<32位小写hex>.db` 的过期普通文件，保留正在写的临时文件和无关文件。
需要长期保留的快照应导出到节点外；本地自动清理不处理外部备份。
新数据库可增量回收空闲页，旧数据库可复用已删除页，升级不会自动 full VACUUM。
约 20 MiB 的单 job 日志配额和年龄清理与下面的磁盘保护共同生效。

## 磁盘低水位保护

安装清单和省略配置时均使用以下默认值；修改 ConfigMap 后重启 server 生效：

```yaml
disk_space:
  pause_below_bytes: 536870912  # 可用空间 < 512 MiB 时暂停新 Pod
  resume_at_bytes: 1073741824  # 可用空间 >= 1 GiB 时恢复
```

两个字段必须是整数且 `0 < pause_below_bytes < resume_at_bytes`。每个调度 tick（正常每秒一次）和新 Pod 创建前检查
`data_dir` 所在文件系统的非特权可用空间。两个阈值之间保留当前暂停/运行状态；初次启动、重启和探测失败均先暂停，
直到成功测得空间达到恢复阈值。因此，重启不会绕过暂停，首次安装也需满足恢复阈值。

暂停时新任务继续 queued，已经预留但还没创建 Pod 的 starting attempt 等待；现有 Pod 继续，完成结果和日志照常接收。
取消、条件/失败传播、GitHub Checks、Pod 回收、CD 协调和自动/手动保留清理照常运行；不会为了恢复而提前删除未到期数据。
释放空间、扩容文件系统或正常保留清理回收空间后自动恢复，无需手动重跑任务，原并发上限继续生效。

```sh
forgelet_api https://localhost:8443/api/status
```

管理员 API 返回 `disk.paused`、`reason`、`available_bytes`、两个阈值、`checked_at` 和探测失败时的 `error`。
`reason` 分别是 `disk_not_checked`（启动后尚未检查）、`disk_low`（尚未达到恢复条件）、`disk_check_failed`；
健康时省略 reason，失败/尚未检查时 available_bytes 为 null，避免误读旧数据。只读首页同步显示暂停提示。
状态反映最近一次调度探测，清理后在下一次检查更新；`/healthz` 保持 200，避免低水位引发重启循环。

测量的是整个文件系统的可用空间，包括 SQLite、blob、备份及同一文件系统上其他程序的占用。
local-path 的 `10Gi` PVC request 通常不是硬容量限制；实际隔离/配额由存储后端提供。
这是新任务调度保护，正在运行的日志/上传、事件接收、备份仍可能继续占用空间；不预留磁盘，也不覆盖其他挂载点或 inode 耗尽。
阈值应为实际并发任务留下余量；到达暂停状态后可先查看状态、执行正常保留清理，再按实际占用释放空间或扩容。

Testcontainers 用独立 256 MiB 文件系统承载实际 local-path PVC，将阈值调为 32/64 MiB，真实写入/截断/删除填充文件，
验证低水位、运行中重启、取消/日志/回收、保留清理、恢复区间再重启和自动并发恢复；不填满宿主磁盘、不访问用户集群。

## Artifact 与 cache 存储

安装清单默认 `blobs: {object_bytes: 268435456, total_bytes: 2147483648, objects: 4096}`。
总量包含并发上传的临时预留量；超过限制返回明确错误，不挤占无限磁盘。artifact 按输入保留 1–90 天（默认 7），cache 固定 7 天。
维护周期清理到期对象；服务重启丢弃未完成上传，不发布半成品。配额独立于 SQLite/日志，仍需监控整个 PVC。

```sh
forgelet_api "https://localhost:8443/api/runs/$FORGELET_RUN_ID/artifacts"
forgelet_api "https://localhost:8443/api/runs/$FORGELET_RUN_ID/artifacts/reports" -o reports.tar
```

这是 forgelet tar 格式；兼容输入、路径、cache 作用域与 GitHub 的差异见 compatibility。

## 按需 BuildKit 与 registry

执行镜像已包含 buildctl v0.20.2。新增 build profile（保留原 homelab profile），将 image repository 改成自己的地址：

```yaml
profiles:
  builder:
    image: forgelet-executor:dev
    build:
      images: [ghcr.io/example/demo]
      registry_secret: forgelet-registry
      insecure_registry: false
      cpu_request: 100m
      memory_request: 256Mi
      cpu_limit: '2'
      memory_limit: 2Gi
```

使用可信 registry 的标准 Docker config JSON 文件创建凭证；该文件只包含所需 registry 的认证，不提交仓库：

```sh
kubectl -n forgelet create secret generic forgelet-registry \
  --type=kubernetes.io/dockerconfigjson \
  --from-file=.dockerconfigjson=/你的安全路径/registry-config.json \
  --dry-run=client -o yaml | kubectl apply -f -
```

build job 才挂载这个 Secret；普通 executor 不挂载。使用内建 `auth`/token，不依赖外部 Docker credential helper。
这份凭证用于构建拉取/推送，不会自动变成应用或 Flux 的拉取凭证；私有应用镜像要在应用 namespace 单独配置 imagePullSecrets。

集群需要原生 sidecar 和节点 user namespace。临时 k3s v1.34.4（arm64）与 homelab k3s v1.33.9（amd64）已实测 rootless；其他节点环境需对应验证。
builder 非 privileged、无 Docker socket/hostPath，但 rootlesskit 要求允许 SETUID/SETGID、提权、Unconfined seccomp/AppArmor；
只在这个按需 sidecar 设置，executor 保持原限制。仅用于可信仓库，restricted Pod Security 策略下需管理员配置相应 namespace 策略。
单 builder 默认 request 100m/256Mi、limit 2 CPU/2Gi，构建 emptyDir 8Gi；多个构建会叠加，不计入常驻控制面资源。
没有持久化层缓存；构建失败、取消后随 job Pod 清理。`insecure_registry` 仅用于显式配置的测试 HTTP registry，正式 registry 使用 TLS。

## GitOps、Flux 观测和回滚

环境先自行准备 Flux GitRepository/Kustomization、应用 Deployment 和就绪探针；forgelet 不自动安装 Flux。
GitRepository 的 URL/branch 必须和 target 相同，Kustomization 包含该 Deployment，建议 `wait: true`。
应用声明使用 Kustomize `images` 的不可变 digest，例如 `name: demo, newName: ghcr.io/example/demo, digest: sha256:...`。
该前置 digest 也是首次回滚的目标；只有 tag、没有前一个 digest 时不能回滚该次交付。

GitHub App 增加 **Contents: Read & write、Pull requests: Read & write**，安装到所需源码和 GitOps 仓库。
控制面为 GitOps 单独申请仅目标仓库的写 token；checkout 仍是源码仓库只读 token，executor 拿不到 GitOps 写权限。
direct 模式要求该 App 能更新目标分支；需要审核的分支使用 pr 模式，由正常 PR 流程合并。

```yaml
targets:
  demo:
    source_repository: example/demo
    source_ref: refs/heads/main
    repository: example/homelab-gitops
    installation_id: 123456
    branch: main
    path: apps/demo/kustomization.yaml
    mode: pr                  # 或 direct；不强推、不绕过分支保护
    image: ghcr.io/example/demo
    image_name: demo
    flux_namespace: flux-system
    kustomization: demo
    namespace: apps
    deployment: demo
    container: demo
```

Flux 默认应使用 `https://github.com/example/homelab-gitops.git`；SSH clone 时在 target 填对应 `git_url: ssh://git@github.com/example/homelab-gitops.git`。
不在 URL 内放凭证。target 的 name 取 map key，最多 32 个；source_repository 也必须在执行仓库 allowlist 内。
不为配置了 target 的 server 提供 GitHub App 时会拒绝启动。修改配置后需重启。

按实际名称调整并应用 [cd-rbac.example.yaml](../deploy/cd-rbac.example.yaml)：仅允许 server GET 指定 GitRepository、Kustomization 和 Deployment，
不授予写权限，executor 的 RBAC 不变。Flux 私有 Git 凭证由 Flux 自己管理。

完整 workflow 见 [cicd.yml](../test/fixtures/cicd.yml)：测试 → artifact → build digest → deploy target。
deploy step 仅提交意图，run 全部成功后异步发布。`awaiting_merge` 表示 PR 未合并，`delivered` 表示已经写入目标分支但尚未确认健康，
`deployed` 表示 revision/digest/健康全部吻合；后续退化可退回 delivered，新投递取代时为 superseded。
等待合并/部署及失败重试的远端轮询间隔为 30 秒，健康部署为 2 分钟；应用实际变化到 API 状态更新之间会有对应观测延迟。
待合并 PR 会阻塞同 target 新投递；关闭后进入 cancelled，head/base 被改动或 direct 分支冲突进入 conflict，需要新 run。

```sh
forgelet_api https://localhost:8443/api/deployments
forgelet_api "https://localhost:8443/api/deployments/$FORGELET_DEPLOYMENT_ID"
forgelet_api -X POST "https://localhost:8443/api/deployments/$FORGELET_DEPLOYMENT_ID/rollback"
```

列表返回最近 100 条，API 包含实际 commit、merge revision、Flux observed_revision、digest 和错误。
回滚按 target 的 direct/pr 模式生成新 commit/PR，仅还原 image digest，保护其他配置；当前 image 已变化时拒绝覆盖。
同一个回滚请求重试返回原记录，不重复创建回滚；没有自动多次回滚或自动合并 PR。
queued 投递保护源 run；终态投递按 history 保留期清理，每 target 最新 delivered/deployed 持续观测并保留。


## GitHub 自助接入

升级 server 后打开 `/connections`：

1. 在 GitHub App 卡片选择现有账号的“选择授权仓库”，或“安装到其他账号”。在 GitHub 保持 Selected repositories，只添加所需可信仓库。
2. 回到 forgelet，点击“我已授权，刷新仓库”。这一步不会开启任何仓库的 CI。列表显示 App 实际可访问的仓库及个人/组织安装状态。
3. 搜索仓库并点击“启用仓库”。服务端绑定 repository ID / installation 并保存 SQLite，页面无需手填 installation ID。已有手动配置仍保留高级入口；旧配置需要通过该按钮确认绑定才进入新向导。
4. 点击“检查默认分支 workflow”。选择 push 或 workflow_dispatch，后者可填写 Inputs JSON。查看固定 SHA、源码与逐文件错误；未知 runs-on 要改成系统已配置的 profile，未知 Actions 能力按 compatibility 文档调整。
5. 对通过检查的文件点击“运行此 workflow”，进入运行详情查看 needs、日志与产物，GitHub Checks 由原链路回写。没有 workflow 时在 GitHub 添加 `.github/workflows/*.yml`，或使用 Workflows 页保存控制面 YAML，再检查。控制面不会自动改写远端文件，也不申请新的 Workflows 写权限。

基础 App 私钥、webhook secret 和 profile 仍由安装配置/Secret 管理。App 需要 Contents 至少读、Checks 写；已有 GitOps 使用 Contents/PR 写不变。GitHub 的 sudo/Mobile 验证需账号 owner 自行完成，与 forgelet auth 无关。

第一批使用新标签授权后手动返回刷新，不要求把管理 API 暴露到公网，也不依赖 GitHub Setup URL 回调。公网保持只暴露 `/webhooks/github`。GitHub 待批准的组织安装在授权通过前不会出现在本 App 权威安装列表中。


2026-10-24 homelab 已部署本流程，第三个私有仓库页面首跑与重启后的真实 push 均成功；运行链接与镜像 digest 见 [status](status.md)。现有安装文件中的 repos/workflows/targets 已由首次页面保存后的 SQLite management 接管，升级时不要通过 ConfigMap 覆盖页面配置。


## 管理员与 GitHub 登录

新安装选用 `admin_access: session`，正确设置 `public_url`（HTTPS；本地开发允许 loopback HTTP）。初次启动初始化用户名 `admin`：

- 若 `auth.bootstrap_password_file` 指向挂载的 Secret 文件，使用其内容作为初始密码（16–72 bytes）。
- 否则随机生成密码，保存至数据目录 `initial-admin-password`，权限 0600。管理员可在自己环境用 `kubectl exec -n forgelet deployment/forgelet -c server -- cat /data/initial-admin-password` 读取；服务日志和 API 不输出密码。
- 登录后到 `/account` 修改密码。修改会撤销其他会话；后续重启不再读取或应用 bootstrap 密码，编辑初始文件不会重置账号。
- 工作台当前只初始化一个管理员，其 GitHub ID 关联到该管理员；没有公开注册或团队系统。首次关联必须先用 admin 密码登录，以后可直接点击“使用 GitHub 登录”。

配置 GitHub 代码源需要现有 GitHub App（执行/集中 webhook/Checks）和一个 GitHub OAuth App（用户登录与账号仓库授权）：

1. 在 GitHub Developer settings → OAuth Apps 创建 OAuth App，Homepage 填工作台地址；Authorization callback URL 填 `https://你的工作台域名/api/auth/github/callback`。
2. 将 Client Secret 存入 Kubernetes Secret 并只挂给 server；ConfigMap 只保存 Client ID 和 Secret 文件路径，不能放 Secret 值。例如：

```yaml
admin_access: session
public_url: https://forgelet.example.com
auth:
  bootstrap_password_file: /auth/initial-password # 可选；只初始化时使用
  github_client_id: YOUR_OAUTH_CLIENT_ID
  github_client_secret_file: /auth/github-client-secret
```

OAuth Secret 使用独立名称，例如 `forgelet-oauth`；已有 `forgelet-auth` 保存 admin-token / webhook-secret，不能用仅含 OAuth 键的新对象覆盖它。Deployment 示例：

```yaml
# spec.template.spec
volumes:
  - name: oauth
    secret:
      secretName: forgelet-oauth
      defaultMode: 0440
# server 容器的 volumeMounts
volumeMounts:
  - name: oauth
    mountPath: /auth
    readOnly: true
```

`/auth` 为管理员在 Deployment 添加的只读 Secret 卷；安装清单保留空值，未配置 OAuth 时本地管理员登录仍可用。`github_web_url` / 原 `github_api_url` 可为受控 Enterprise/测试端点配置，默认 GitHub.com；不跟随外站重定向。

3. 用 admin 登录 → 账号 → 连接 GitHub，确认 `read:user repo` 授权。`repo` 包含私有仓库以及代码、状态、仓库 webhook 等写权限；页面明确显示该范围，不申请组织全局管理权限。
4. 登录授权后回到 Connections；主视图为已接入仓库。点“添加仓库”，输入名称并选择仓库，再点“启用仓库”。若 App 已获该仓库授权，复核后立即启用；若显示“等待 GitHub 授权”，按弹窗链接在 GitHub 为 App 选择该仓库，返回点“检查授权并启用”。刷新页面会保留待接入选择，未完成授权不会登记可信仓库或启动任务。安装缺失、暂停和权限不足有对应指引。

独立 OAuth App 的 `repo` 授权用于仓库发现，不能调用安装仓库管理接口代替 GitHub App 授权。旧版曾错误尝试此调用并导致 HTTP 403；本批修复后不再发出该请求。App 的集中 webhook、短期执行凭据和 Checks 继续使用原链路，不创建重复 webhook，不向执行 Pod 下发用户 OAuth token。

App 第一次进入新账号/组织仍需安装；组织审批/策略不允许时界面会给出诊断，OAuth 不会绕过 GitHub 的组织限制。授权撤销或 refresh token 失效时，用本地 admin 登录，在账号页面重新授权 GitHub。当前不提供公开注册或自助改绑到另一 GitHub ID。

会话通过 Secure/HttpOnly/SameSite Cookie 保存，7 天绝对到期；管理写请求仍要求同源 JSON。旧 `FORGELET_ADMIN_TOKEN` 在 session 模式不能绕过登录；脚本客户端可使用 Cookie jar 登录，不能把 OAuth token 或密码放进 URL。执行器 workload identity 和 GitHub webhook HMAC 始终独立。

认证功能引入 SQLite schema v5（管理员哈希、会话摘要、一次性 flow），当前版本已追加 v6 重试元数据。OAuth token/refresh token 使用主密钥加密；没有配置 `secret_key_file` 时，server 在 PVC 创建 `auth-encryption.key`（0600）。备份 SQLite 之外，需备份该 key 或已配置的 Kubernetes Secret。显式备份恢复会撤销恢复出的所有登录 session 和 OAuth flow。升级前保留当前运行库备份；旧版本 server 不能读取更新的 schema，回退需使用对应的升级前备份。

认证验证命令仍为 `make verify` / `make test-integration`，并可用 `go test -tags=integration ./test/integration -run 'TestK3sConcurrentExecution/administrator' -count=1 -v -timeout 15m` 只跑临时 k3s 的认证专项。测试使用受控 GitHub OAuth/API fixture，真实账号配置与实际集群部署另行记录。

2026-10-24 的 homelab 已按 session 模式部署，真实 OAuth 关联、GitHub 再登录、50 仓库发现和重启后真实 push 通过，镜像/快照/操作证据见 [status.md](status.md)。初始化 admin 密码保存在 `/data/initial-admin-password`，可按本节命令在自己的终端读取；已关联的 shitamachi 账号可直接 GitHub 登录。


## 阶段 9：重试、收尾与升级

本次 schema 升级为 v6，增加事件/Checks 重试元数据。部署前保存升级前 SQLite 备份与原加密主密钥；旧 v5 程序不能直接打开 v6 数据库，回退需使用升级前备份并按原恢复隔离流程处理，不能只回退镜像。已有 GitHub 凭据时丢失或换错 auth-encryption.key 将阻止启动；恢复原密钥后再启动。

2026-10-25 已按此流程部署 homelab：server `sha256:fbc91710...`、executor `sha256:cc230abb...`（linux/amd64）。升级前 v5 快照为 16 runs/34 jobs/1 admin；先在本地副本执行 `PrepareRestore` + `Open` 验证 v6 迁移，再启动线上迁移。部署后 1/1 Ready、0 restarts，v6 快照 integrity ok 且数据计数保持；`/healthz`、session、匿名 401 和嵌入 UI 验证通过。未登录管理界面、未触发真实 CI。

事件拉取/workflow admission、Checks 回写使用 2 秒起步、最多 5 分钟的指数退避，连续失败 12 次后暂停，重启保留退避时间。事件在 GET /api/events 保留最终 error，Checks 在 job.check_error 保留诊断，运行详情页可点击“重新回写 Checks”。管理员可用已登录同源 JSON 客户端请求：

- POST /api/runs/{runID}/checks/retry：重置该 run 的回写预算，不重跑执行。
- POST /api/events/{deliveryID}/retry：重新处理仍保留 payload 的失败事件；已有 run 的 delivery/workflow 唯一键继续防止重复执行。

运行时永久拒绝/所有权冲突立即结束对应 attempt；临时创建、观测或取消删除失败最多重试 12 次，释放并发并留下诊断，不自动重跑 shell。外来 Pod 不删除，需要管理员处理冲突。profile.control_plane_timeout 默认 5m，配置为时长字符串；控制面中断超过该窗口仍会停止执行。最终 flush 只有 20 秒，Pod grace 为 45 秒，不能把 5 分钟中断容忍误读成任务结束后的无限等待。

登录限速按直接连接的 RemoteAddr，每分钟最多 6 次。代理共用源地址时同一地址共享限额；当前不信任转发头，尚无 trusted-proxy 配置。blob 数据目录由程序独占；未知文件会使启动失败，不要在其中写入 Finder 元数据或其他文件。

## 生产形态应用实验（10.5c）

`test/productionlike` 是显式操作真实 homelab 的开发工具，不能替代 `make test-integration`。
后者继续只用 Testcontainers 临时 k3s，不读取用户 kubeconfig。工具只允许专用测试仓库，
执行 namespace 为 `forgelet-lab`，每轮使用 Pod 自有的 privileged Docker/overlay2；
不改正式 server/executor 清单、不接管产品调度，也不向 CI 下发 Kubernetes 或 operator 管理凭据。

先在 `forgelet-demo` 部署 PostgreSQL/任务 API 基线，再验证真实 Actions 和 GitOps/Flux。
PostgreSQL 是被测应用的依赖，控制面继续使用 SQLite；应用数据库 Secret/PVC 独立创建，
其余 Deployment/Service 由测试 GitOps 仓库管理。应用新镜像先执行幂等迁移，再等待 readiness。
发布 target 仍为 `demo`，只投递不可变 digest；故障镜像通过原 rollback API 恢复前一个 digest。

每轮采集结果后删除对应实验 Job/Secret；确认 engine 已退出才允许发布。取消使用 foreground
删除并检查没有剩余 Pod。仅保留单个 local-path cache PVC，不保留空闲 Docker daemon。
该 provisioner 的 PVC 容量不是硬配额；实验 cache 不等同于生产缓存的配额、隔离和清理协议。
命令、凭据路径要求及兼容边界见 [实验说明](../test/productionlike/README.md)，实际结果见 status。


## 显式 act job profile（10.5b）

构建 `server`、`executor` 与可选 `executor-act` 三个 Dockerfile target，部署时固定 digest。
原 native profiles 使用 executor；需要原版 Actions/Docker 的可信仓库使用 executor-act。
示例中的 digest 必须替换为实际构建结果：

```yaml
profiles:
  ubuntu-latest:
    image: registry.example/forgelet/executor-act@sha256:REPLACE
    act:
      registry_secret: forgelet-registry
      data_limit: 12Gi
    cpu_request: 100m
    memory_request: 192Mi
    cpu_limit: '2'
    memory_limit: 1Gi
repository_tokens:
  owner/application:
    PRIVATE_MODULE_TOKEN: owner/private-sdk
```

`act` 存在才启用；engine_image 默认 docker:28.5.1-dind，也允许固定 SHA256 的镜像，启动探针必须确认实际引擎版本 28.5.1。
runner_image 默认等于 profile.image，仅当测试/内部 registry 路径不同时才覆盖。
insecure_registry 默认 false，仅供明确使用 HTTP 的临时测试 registry。
依赖仓库必须属于源 GitHub App installation 授权范围；每个领取 job 动态换取单仓库只读 token，
不存储 PAT，不把 registry pull Secret 挂进 job 容器。普通 workflow secrets 仍通过管理员 API 加密保存。

该 profile 为按需 privileged Docker，有独立 emptyDir/socket，不挂宿主路径/Kubernetes identity。
native sidecar 在主进程结束后退出；空闲时只有原 Go 控制面，无 Docker/cache 服务常驻。
引擎内存/CPU 和 data_limit 见 architecture；不能用于不可信或多租户代码。
CD 使用 native profile 的 forgelet/deploy@v1，通过 needs 的 digest 接续，继续使用既有 Flux 目标。

本批沿用 schema v6，但新 Plan 增加 kernel/call 节点；降级旧二进制前必须恢复升级前的原子 SQLite
快照和匹配的数据/config，不要仅回滚 image。升级前执行 POST /api/backup，并单独备份整个 PVC、
ConfigMap 和 Deployment；快照不包含 artifact/cache 文件。

本批 homelab/test 构建使用 `GOPROXY=https://goproxy.cn` 和
`DEBIAN_MIRROR=https://mirrors.aliyun.com` build args；默认仍为官方源。
act 镜像也使用对应 GOPROXY 环境默认值，workflow 可以覆盖。未关闭 go.sum、CA 或 Debian 签名校验。
容器测试可用 FORGELET_TEST_IMAGES 预载相同版本的 k3s 系统镜像、docker:28.5.1-dind；
预载后重建仍在等待外部拉取的临时系统 Pod，不改变安装清单或跳过功能检查。
公开 Actions 源码采用三次有界下载重试、完整临时目录原子发布，失败不会留下可被误认成完整源码的缓存。

10.6b 的新 act 运行在控制面解析公共 Action tag/branch 到 commit，保存完整依赖锁再调度；
因此控制面需访问 `codeload.github.com` 和 `raw.githubusercontent.com`，executor 仍以固定 SHA 访问 codeload。
默认读取 Git 归档的完整 commit 标识再按 SHA 获取 metadata，不消耗 REST core 的匿名配额，也不携带安装/OAuth token。
限流/网络故障保留事件并退避重试，手动 API 返回可诊断的 503。
管理员可用顶层 `action_api_url` 显式选择受控 REST 公共源代理或测试替身（受对应上游配额约束），默认无需配置；workflow 不能设置该地址。
重复首次运行请求、重跑和重启恢复不会重新解析已保存运行的 tag；新运行重新解析。
普通原生 profile 保留原固定版本限制，未承诺私有 Actions、workspace-local Composite 的新远程依赖扫描或全部 Marketplace 行为。

act job 的 workspace 由 UID 1000 的 supervisor 创建，内层工具以 root 运行；执行镜像仅将
`/workspace/repository` 登记为 Git system safe.directory，使 Go VCS stamping 正常工作。
没有信任任意路径，也没有关闭 Go 的 VCS 校验。Testcontainers 覆盖真实 Git 初始化与状态读取。

每个 attempt 的 Docker 镜像存储为空；节点 containerd 缓存不等于 job 引擎缓存。
若需预加载，必须加载到该 attempt 自有引擎。对固定 manifest digest 的 runner 镜像，supervisor
校验远端原始 manifest 的 SHA 与本地完整 config digest 后可复用；tag、index 或校验失败走标准 pull。
这不提供共享 Docker data 或常驻镜像缓存。真实验证中的手工预加载需在状态文档单独记录。

homelab 后续已将 `ubuntu-latest` 的 image 改用 `zot.k.mygo.rocks/forgelet/executor-act`，
沿用相同 manifest digest，act.registry_secret 使用现有 `forgelet/zot-registry`。
Secret 同时用于主 Pod imagePullSecrets 和 supervisor 拉取内层 runner，不挂入内层任务容器。
实际空引擎自动拉取探针通过，细节见 status；没有关闭 HTTPS 校验或增加常驻 Docker。
新版本必须先把不可变镜像同步到 Zot，再切换 profile。此配置仅改变 runner 镜像来源，
不会自动替换 workflow 的 Docker Hub 依赖、应用镜像构建 registry 或 GitOps image 名称。

10.5d 可为显式 act profile 增加管理员镜像目录，先把所需公开镜像按执行节点架构复制到现有 registry，
再填写原 manifest digest；示例不能直接部署：

```yaml
profiles:
  ubuntu-latest:
    image: zot.example/forgelet/executor-act@sha256:REPLACE
    act:
      registry_secret: zot-registry
      engine_image: zot.example/mirrors/docker-engine@sha256:REPLACE
      image_mirrors:
        postgres:18-alpine: zot.example/mirrors/postgres@sha256:REPLACE
        testcontainers/ryuk:0.14.0: zot.example/mirrors/ryuk@sha256:REPLACE
        golang:1.26.4-bookworm: zot.example/mirrors/golang@sha256:REPLACE
        alpine:3.22: zot.example/mirrors/alpine@sha256:REPLACE
        docker/dockerfile:1.7: zot.example/mirrors/dockerfile@sha256:REPLACE
```

mirror 源必须与 runner 同 registry、固定 SHA256；alias 必须是唯一的 Docker Hub 显式 tag，最多 16 项。
失败阻断步骤，无公网回退。引擎仍必须为 Docker 28.5.1，startup probe 检查实际版本。
目录只供 supervisor，先自动拉取并核对完整 config identity，再在该 job 的空引擎登记原名；
不挂共享 Docker data，不将 pull Secret 挂给 engine/内层任务。更换 registry 时使用对应 Secret，
不要将管理员 registry 密码复制到 workflow。强制 `--pull` 或独立 BuildKit 引擎需自行配置对应 registry。

日志不需要新服务或安装参数：同 step 小写入合并，8 KiB/200ms 刷新，保留原 seq/ACK/SQLite 协议。
10.5d 回退到本批 10.5b 时恢复匹配的 profile 配置与镜像即可；没有 SQLite schema 变更，
不要为该回退用旧备份覆盖新增运行。回退到不认识 kernel/call 计划的更早版本仍遵守前述恢复边界。

10.6b 新计划会将已有 `github_api_url` 配置快照到 job 的 REST API 上下文及 GITHUB_API_URL，
供 metadata 等原版 Actions 使用。默认无需配置，仍访问 GitHub.com；若配置内部代理，地址必须能从执行 job 访问，
对应 CA 也应在管理员执行镜像中受信任。公开 Action ref 的 `action_api_url` 与此独立，不接收 job token。

## 独立 BuildKit 缓存与包发布（10.6c）

act job 启动时 supervisor 在 Pod IP 的随机端口提供 job-local cache v1/v2，端点/能力不进入持久计划。
Pod 内 bridge BuildKit 必须可访问该 Pod IP；不要把端点改成 localhost，也不需要开放公网 ingress。
服务随 attempt 关闭，持久内容复用现有数据 PVC、blob 配额和保留期，不增加空闲 cache/BuildKit 服务。
每 attempt 保留 payload 上限 512 MiB；临时 envelope/assembly 和下载归档可额外占用空间，详见 architecture。
对象超过现有 blob 配额时明确报错；不承诺大型仓库所有层都能放进默认 256 MiB/对象配额。

管理员可给原始 setup-buildx workflow 配置执行环境默认值，例如：

```yaml
profiles:
  ubuntu-latest:
    image: zot.example/forgelet/executor-act@sha256:REPLACE
    act:
      runner_image: zot.example/forgelet/executor-act@sha256:REPLACE
      registry_secret: zot-registry
      image_mirrors:
        moby/buildkit:buildx-stable-1: zot.example/forgelet/mirrors/buildkit@sha256:REPLACE
      buildkit_config: |
        [registry."docker.io"]
          mirrors = ["public-read-mirror.example"]
```

占位符必须替换为真实完整 digest。镜像目录的固定 BuildKit 先由 supervisor 拉取并验证；
顶层 docker/setup-buildx-action 在未显式指定 driver image 时使用该固定源。已有 workflow 的显式 driver/image/config
优先；buildkit_config 是默认 TOML，不是 shell 脚本，不要在其中写账号凭据。
这里没有把 supervisor 的 Zot Secret 交给 job；内层 BuildKit 的 Dockerfile FROM 拉取仍需要可达的 upstream，
或独立配置可读取的 registry mirror/认证。Docker engine 中预加载成功不代表独立 builder 已有该基础镜像。
生产 HTTPS/CA 与测试隔离 registry 的 insecure/http 配置分别管理；测试示例不要求生产关闭 TLS 验证。

原始 `cache-from: type=gha` / `cache-to: type=gha,mode=max` 可使用 job 注入的 ACTIONS_RESULTS_URL、
ACTIONS_RUNTIME_TOKEN 与 ACTIONS_CACHE_SERVICE_V2。固定验证的 BuildKit 版本与实际结果见 status。
CacheService/Azure 风格协议来自 [BuildKit cache 客户端](https://github.com/tonistiigi/go-actions-cache) 与
[Actions toolkit](https://github.com/actions/toolkit/tree/main/packages/cache)，不连接 GitHub 托管的缓存账户。
Build record/artifact v4 不属于这个 cache API；build-push 的非阻断记录上传警告不能算 artifact 上传成功。

GHCR 原始 workflow 的 packages:write 需先登记独立 Secret 引用：

```yaml
package_tokens:
  shitamachi/steam-login: GHCR_TOKEN
```

再通过仓库 Secrets 页面保存 `GHCR_TOKEN`，值由管理员在 GitHub 为所需 packages/仓库范围创建；不要填写源仓库只读 installation token。
Secret 仍加密保存在现有 SQLite；配置和计划只保存名称。运行时由 `secrets.GITHUB_TOKEN` 提供该 job 显式要求的包凭据，
隐式 checkout 继续用独立的源只读 token。只有 act 的 push/manual job 可以要求 packages:write，PR 拒绝；
packages:read 暂不提供可降权 token，其他未实现权限继续诊断。填写 YAML 不等于已获得 GitHub registry 授权。
绑定删除/变更会停止旧计划继续领取包凭据；需要恢复原绑定或创建新运行，不能让旧重跑悄悄换一个授权引用。

原业务构建专项的固定源码、隔离 GHCR/registry 路由、完整运行与子集边界见
[test/fixtures/steam-build](../test/fixtures/steam-build/README.md)。默认 make test-integration 总是运行独立 BuildKit
冷热缓存/恢复/失败/取消场景；原始私有源码专项仍需显式准备 Git 快照目录。


### 可选 Web 工具 profile（10.6c）

Dockerfile 的 `executor-act-web` target 在正式 act executor 上预装官方 Node24.14.0 完整
hosted toolcache 与 Playwright1.63.0 Chromium，浏览器位于 `/opt/forgelet/browsers`。
按目标集群架构构建；Node toolcache 使用 Node 的 x64/arm64 名称，不直接使用 OCI amd64 名称。
用于需要真实 Chromium 的可信 Web 仓库；原 `executor-act` 和 native target 保持独立。
这增加按需执行镜像体积与首次拉取成本，不增加空闲常驻服务；pnpm 和 BuildKit 缓存继续由
Forgelet 管理，预装工具不能算作应用热缓存或在线下载验收。
