# 原始业务 workflow 验收

这里的 3 份 YAML 按 `sources.json` 的 commit/path 从 GitHub 读取，保留原始字节和 SHA256。
普通单测只编译样本，不需要账号；真实代码不复制进本项目。

完整运行需要在仓库外用已经授权的 `gh` 准备两个干净快照。以下 Python 只读获取 Git 对象，
不提交/推送代码，不把 gh 凭据带进测试镜像；目录必须是新目录。

```python
import json, os, pathlib, subprocess

os.umask(0o077)
destination = pathlib.Path('/private/path/forgelet-business-snapshots')
destination.mkdir(parents=True, exist_ok=False)
sources = json.loads(pathlib.Path('test/fixtures/steam-business/sources.json').read_text())
for repository, commit in sorted({(s['repository'], s['commit']) for s in sources.values()}):
    target = destination / repository.split('/')[1]
    subprocess.run(['gh', 'repo', 'clone', repository, str(target), '--', '--no-checkout', '--filter=blob:none'], check=True)
    subprocess.run(['git', '-C', str(target), 'checkout', '--detach', commit], check=True)
```

```sh
FORGELET_TEST_BUSINESS_DIR=/private/path/forgelet-business-snapshots make test-integration
```

仅运行原业务场景时可使用：

```sh
FORGELET_TEST_BUSINESS_DIR=/private/path/forgelet-business-snapshots \
  go test -tags=integration ./test/integration \
  -run 'TestK3sConcurrentExecution/original_Go_and_Node_business_workflows' \
  -count=1 -v -timeout=60m
```

测试创建临时 k3s，使用实际 server/executor/act、SQLite/PVC 和 Pod 自有 Docker，不读取用户 kubeconfig。
会检查快照 HEAD、干净状态及 workflow 字节；原始 checkout Action 从镜像内的 file:// Git 对象取得原 SHA。
公共 Actions 使用真实 GitHub ref/metadata/固定 SHA archive，Go/npm 使用实际工具与依赖下载。
Go 与 Node 的 CI 各执行冷/热两次，全部原步骤和 cache-hit 必须符合预期；image.yml 实际执行
login、metadata、build-push，并对照 manifest 与 Action 输出 digest。

仅测试引擎将 `registry.shitamchi.com` 指向临时 registry，使用独立测试凭据；不修改主机/homelab DNS，
不写业务镜像仓库或真实 GitHub Checks，不等于实际账号接入、push 验证或 homelab 部署。
该场景的源码授权、镜像路由是测试替身，执行、缓存、构建、日志与结果均走产品正式协议。
默认测试不设置环境变量时显式 skip 此场景，不把 skip 当成原业务通过。

steam-admin-web 的 ci.yml 与 image.yml 原本是独立触发入口。本测试按顺序验收两者，
不会由此为产品增加原文中不存在的跨 workflow 依赖。若后续要求 CI 失败禁止发布/部署，
需显式加入调用/needs 或发布策略，再分别验证阻断与回滚。
