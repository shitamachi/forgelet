# 10.6c 原始构建 workflow

`sources.json` 固定 GitHub commit、路径与原始 YAML SHA256；源文件不改字节。
普通单测验证四份 workflow 的来源与纯编译。真实运行需要在仓库外准备干净 Git 快照：

```python
import json, os, pathlib, subprocess
os.umask(0o077)
root = pathlib.Path('/private/path/forgelet-build-snapshots')
root.mkdir(parents=True, exist_ok=False)
sources = json.loads(pathlib.Path('test/fixtures/steam-build/sources.json').read_text())
for repo, commit in sorted({(s['repository'], s['commit']) for s in sources.values()}):
    target = root / repo.split('/')[1]
    subprocess.run(['gh', 'repo', 'clone', repo, str(target), '--', '--no-checkout', '--filter=blob:none'], check=True)
    subprocess.run(['git', '-C', str(target), 'checkout', '--detach', commit], check=True)
```

```sh
FORGELET_TEST_BUILD_BUSINESS_DIR=/private/path/forgelet-build-snapshots \
  go test -tags=integration ./test/integration \
  -run 'TestK3sConcurrentExecution/original_Buildx' -count=1 -v -timeout=95m
```

该可选专项依次执行 steam-admin 原 image、steam-platform-web 原 check 两次和 steam-login 原 docker。
包含实际 pnpm/Node24/Chromium/Playwright/build/smoke、原始 setup-buildx/build-push 与双 registry 发布。
不设置快照目录时显式 skip，不能当作业务运行通过。
可用 `FORGELET_TEST_BUILD_BUSINESS_ONLY=steam-platform-web` 选择单仓库排错；专项子集不冒充完整通过。
steam-api 原 ci 同时保留为编译样本；其原 GitHub 测试失败的业务修复属于 10.6e，不绕过 tests/needs 验证发布。

测试只读获取 gh 源码，在按需镜像中保存同 SHA Git 对象；原 checkout Action 通过 file:// 别名取得原代码。
GitHub API 为 TLS fixture；GitHub App token、GHCR package token 与 registry 密码都是独立测试值。
原始 registry.shitamchi.com / ghcr.io 只在临时 k3s DNS 和测试引擎中路由至隔离 registry。
GHCR 代理校验独立包凭据并放入单独 namespace，避免两个原始发布写成同一个镜像位置；不连接实际 GHCR。
这不是 homelab 部署、实际 GitHub push/Checks 或真实账号 GHCR 授权成功。

原 workflow/Dockerfile 的工具、网络与构建语义保留。管理员 profile 提供执行环境：
setup-buildx 的默认 BuildKit 由已验证镜像目录指定；registry/TLS 默认由 buildkit_config 提供。
本测试的 buildkit_config 显式设置 Docker Hub 只读镜像源 docker.m.daocloud.io；真实版本/内容 digest 仍由 BuildKit 校验。
独立 BuildKit 不自动继承 Docker engine 的预加载镜像，未配置的 Dockerfile 构建源仍需能访问 upstream。
公共 Actions 使用实际 GitHub 固定 SHA 源码；真实安装、测试、构建、缓存与日志走产品正式协议。
仅更新测试构建的 Debian mirror 参数不改变受测 workflow；apt 仍检查发行签名。

2026-10-27 实测：admin 原 image 与 login 原 docker（两处隔离发布）通过；platform-web 的首次原 check
在比较状态用例失败，后续 image 正确 skipped。修复后的源码通过显式补丁模式完成两轮原 check/image，
包缓存 false→true、新 builder 依赖 RUN 层 CACHED、latest/SHA 标签与 Action digest 均核对通过。
源码修复显式记录，未跳过失败测试；详情、运行 ID、digest 与证据路径见 [status](../../../docs/status.md)。

## platform-web hydration 修复验证

`steam-platform-web/hydration.patch` 是业务源码修正与三项状态回归测试，不修改 workflow、
Dockerfile 或原 E2E 断言。原提交在慢 CPU 的独立 Chromium 中 6/6 失败；补丁后相同条件 6/6 通过。
详见 status，不用延长 timeout、增加重试或删除测试作为修复。

对同一份干净快照显式启用补丁，运行两轮原始 check（每轮包含 CI 与 needs:image）：

```sh
FORGELET_TEST_PLATFORM_WEB_FIX=1 \
FORGELET_TEST_BUILD_BUSINESS_ONLY=steam-platform-web \
FORGELET_TEST_BUILD_BUSINESS_DIR=/private/path/forgelet-build-snapshots \
  go test -tags=integration ./test/integration \
  -run 'TestK3sConcurrentExecution/original_Buildx' -count=1 -v -timeout=95m
```

测试只在临时 clone 应用补丁、生成确定性 Git fixture 对象，以供真实 checkout 获取修复后源码；
日志分别标出 GitHub base SHA、执行 fixture SHA 和 patch SHA256。不会更新输入快照或 owner 工作区的
HEAD/index/源码，不创建实际业务提交或推送。未设置该开关仍执行原始源码，不能隐式绕过失败。
这验证原始 workflow 配合修复源码，不宣称原 GitHub SHA 的全部源码字节保持不变。

实际客户端使用 1 MiB blocks；原 128 blocks 上限会在总字节预算仍充足时失败。
现支持最多 256 blocks，保留 256 MiB/对象和 512 MiB/attempt 字节预算，并区分三类超限诊断。
专项逐 job 检查 pnpm cache-hit=false → true、第二轮新 builder 的依赖 RUN 层 CACHED，
并核对 latest/SHA 双标签与 build Action digest。

可显式提供 `FORGELET_TEST_TOOL_CACHE_IMAGE`，从该测试镜像复制
`/opt/forgelet/test-browsers` 和 `/opt/hostedtoolcache`，设置 PLAYWRIGHT_BROWSERS_PATH。
工具镜像可预装官方同版本浏览器与完整 Node24（对应架构的 toolcache 目录和 .complete 标记），
不预填 pnpm/GHA 缓存，不改源代码、setup Actions、原安装命令或任何 E2E 断言；普通工具环境配置须与缓存命中验收分别记录。

默认独立 BuildKit 专项生成 129 MiB 不可压缩层，确保真实 SDK 超过旧 128 块上限，
并继续验证冷热、重启、失败和取消；测试期间 tmpfs 临时扩至 512 MiB，结束恢复 256 MiB。
这属于功能回归数据，不设资源/性能门禁，也不依赖私有业务快照。

最终通过记录：`platform-workflow-reuse-final.log`（1550.930 秒）以及默认大层
`buildcache-large-reuse.log`（720.983 秒），完整命令、运行 ID、来源边界与失败记录见 status。
