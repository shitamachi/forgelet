import { expect, test } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import type { Run } from '../src/lib/types'

test('embedded console controls real k3s runs, logs, artifacts and cancellation without a login', async ({
  page,
  request,
}) => {
  if (!process.env.FORGELET_E2E_BASE_URL)
    throw new Error('Run through make test-integration with a disposable k3s endpoint')
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto('/runs/new')
  await expect(page.getByRole('heading', { name: '手动运行', exact: true })).toBeVisible()
  await page.getByLabel('Commit SHA').fill('a'.repeat(40))
  await page.getByLabel('GitHub installation ID').fill('0')
  await page.getByLabel('Workflow YAML', { exact: true }).fill(`name: Browser integration
on: workflow_dispatch
jobs:
  test:
    runs-on: homelab
    steps:
      - id: check
        run: |
          echo web-console-real-k3s
          mkdir -p result
          echo artifact-from-browser > result/report
      - uses: actions/upload-artifact@v4
        with: {name: browser-report, path: result}
  downstream:
    needs: test
    runs-on: homelab
    steps:
      - run: echo needs-passed
`)
  await page.getByRole('button', { name: '创建运行', exact: true }).click()
  await expect(page).toHaveURL(/\/runs\/[a-f0-9]+$/)
  const id = new URL(page.url()).pathname.split('/').at(-1)!
  await expect
    .poll(async () => ((await (await request.get(`/api/runs/${id}`)).json()) as Run).state)
    .toBe('success')
  await expect(page.getByRole('button', { name: '重新运行', exact: true })).toBeVisible()
  await page.getByRole('button', { name: /\/ test 成功/ }).click()
  await expect(page.getByLabel('日志内容')).toContainText('web-console-real-k3s')
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('link', { name: '下载完整日志' }).click()
  expect(await readFile((await (await downloadPromise).path())!, 'utf8')).toContain(
    'web-console-real-k3s',
  )
  const artifactPromise = page.waitForEvent('download')
  await page.getByRole('link', { name: /browser-report/ }).click()
  expect(
    (await readFile((await (await artifactPromise).path())!)).includes(
      Buffer.from('artifact-from-browser'),
    ),
  ).toBe(true)
  await page.getByRole('button', { name: /downstream 成功/ }).click()
  await expect(page.getByLabel('日志内容')).toContainText('needs-passed')
  await page.reload()
  await expect(page.getByRole('button', { name: /downstream 成功/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  )
  await page.getByRole('button', { name: '重新运行', exact: true }).click()
  await page.getByRole('button', { name: '确认重新运行' }).click()
  await expect(page).not.toHaveURL(new RegExp(id))
  const rerunID = new URL(page.url()).pathname.split('/').at(-1)!
  await expect
    .poll(async () => ((await (await request.get(`/api/runs/${rerunID}`)).json()) as Run).state)
    .toBe('success')
  await page.goto('/runs/new')
  await page.getByLabel('Commit SHA').fill('a'.repeat(40))
  await page.getByLabel('GitHub installation ID').fill('0')
  await page
    .getByLabel('Workflow YAML', { exact: true })
    .fill(
      'name: Browser cancellation\non: workflow_dispatch\njobs:\n  wait:\n    runs-on: homelab\n    steps:\n      - run: echo browser-cancel-started; seq 1 300; sleep 180\n',
    )
  await page.getByRole('button', { name: '创建运行', exact: true }).click()
  await expect(page).toHaveURL(/\/runs\/[a-f0-9]+$/)
  const cancelID = new URL(page.url()).pathname.split('/').at(-1)!
  await expect(page.getByLabel('日志内容')).toContainText('browser-cancel-started')
  await page.getByRole('button', { name: '取消运行', exact: true }).click()
  await page.getByRole('button', { name: '确认取消运行' }).click()
  await expect
    .poll(async () => ((await (await request.get(`/api/runs/${cancelID}`)).json()) as Run).state)
    .toBe('cancelled')
  expect(errors).toEqual([])
})

test('repository settings, delivery records and SQLite backup use real management APIs', async ({
  page,
  request,
}) => {
  await page.goto('/repositories')
  await page.getByRole('button', { name: '新增变量' }).click()
  await page.getByRole('dialog').getByLabel('名称').fill('BROWSER_TEST')
  await page.getByRole('dialog').getByLabel('值', { exact: true }).fill('persisted-in-sqlite')
  await page.getByRole('dialog').getByRole('button', { name: '保存', exact: true }).click()
  expect(await (await request.get('/api/repositories/test/project/vars')).json()).toMatchObject({
    BROWSER_TEST: 'persisted-in-sqlite',
  })
  await page.getByLabel('删除变量 BROWSER_TEST').click()
  await page.getByRole('button', { name: '确认删除' }).click()
  await expect(page.getByText('BROWSER_TEST', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '新增 secret', exact: true }).click()
  await page.getByRole('dialog').getByLabel('名称').fill('BROWSER_SECRET')
  await page.getByLabel('新的 secret 值').fill('test-only-secret')
  await page.getByRole('dialog').getByRole('button', { name: '保存', exact: true }).click()
  const secrets = await (await request.get('/api/repositories/test/project/secrets')).text()
  expect(secrets).toContain('BROWSER_SECRET')
  expect(secrets).not.toContain('test-only-secret')
  await page.getByLabel('删除 secret BROWSER_SECRET').click()
  await page.getByRole('button', { name: '确认删除' }).click()
  await expect(page.getByText('BROWSER_SECRET', { exact: true })).toHaveCount(0)
  await page.goto('/deployments')
  await expect(page.getByRole('heading', { name: '发布', exact: true })).toBeVisible()
  await page.goto('/system')
  await page.getByRole('button', { name: '创建数据库备份', exact: true }).click()
  await page.getByRole('button', { name: '创建备份', exact: true }).click()
  await expect(page.getByText('备份已保存到数据目录')).toBeVisible()
})

test('configuration pages persist repository, managed workflow and delivery target through the real API', async ({
  page,
  request,
}) => {
  await page.goto('/connections')
  await page.getByRole('button', { name: '手动登记（高级）' }).click()
  await page.getByLabel('仓库全名').fill('test/console')
  await page.getByRole('dialog').getByLabel('Installation ID', { exact: true }).fill('7')
  await page.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(page.getByRole('article', { name: 'test/console', exact: true })).toBeVisible()
  await page
    .getByRole('article', { name: 'test/console', exact: true })
    .getByLabel('管理 test/console')
    .click()
  await page.getByRole('button', { name: '检查接入', exact: true }).click()
  await page.getByRole('dialog').getByLabel('Commit SHA').fill('a'.repeat(40))
  await page.getByRole('button', { name: '检查访问', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: '接入检查通过' })).toBeVisible()
  await page.getByRole('button', { name: '关闭对话框' }).click()
  await page.goto('/workflows')
  await page.getByRole('button', { name: '新增Workflows' }).click()
  await page.getByRole('dialog').getByLabel('仓库', { exact: true }).selectOption('test/console')
  await page.getByLabel('Workflow 路径', { exact: true }).fill('.github/workflows/console.yml')
  await page.getByLabel('Workflow 来源').selectOption('managed')
  await page
    .getByLabel('Workflow YAML', { exact: true })
    .fill(
      'name: Configured workflow\non: push\njobs:\n  verify:\n    runs-on: homelab\n    steps:\n      - run: echo configured-workflow-real-pod\n',
    )
  await page.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(page.getByRole('row', { name: /console.yml/ })).toBeVisible()
  await page.reload()
  await page
    .getByRole('row', { name: /console.yml/ })
    .getByRole('button', { name: '运行', exact: true })
    .click()
  await page.getByRole('dialog').getByLabel('Commit SHA').fill('a'.repeat(40))
  await page.getByRole('dialog').getByRole('button', { name: '创建运行', exact: true }).click()
  await expect(page).toHaveURL(/\/runs\/[a-f0-9]+$/)
  const id = new URL(page.url()).pathname.split('/').at(-1)!
  await expect
    .poll(async () => ((await (await request.get(`/api/runs/${id}`)).json()) as Run).state)
    .toBe('success')
  await expect(page.getByLabel('日志内容')).toContainText('configured-workflow-real-pod')
  await page.goto('/targets')
  await page.getByRole('button', { name: '新增发布目标' }).click()
  const fields = {
    目标名称: 'browser-target',
    来源仓库: 'test/console',
    '来源 ref': 'refs/heads/main',
    'GitOps 仓库': 'test/gitops',
    'Installation ID': '7',
    'GitOps 分支': 'main',
    'Kustomize 文件': 'apps/kustomization.yaml',
    镜像地址: 'registry.test/app',
    'Kustomize image name': 'app',
    'Flux namespace': 'forgelet',
    'Kustomization 名称': 'phase3',
    '应用 namespace': 'forgelet',
    'Deployment 名称': 'phase3-app',
    容器名称: 'app',
  }
  for (const [label, value] of Object.entries(fields))
    await page.getByRole('dialog').getByLabel(label, { exact: true }).fill(value)
  await page.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(page.getByText('browser-target', { exact: true })).toBeVisible()
  await page.reload()
  await expect(page.getByText('browser-target', { exact: true })).toBeVisible()
  const doc = await (await request.get('/api/management')).json()
  expect(doc.repositories).toContainEqual({
    name: 'test/console',
    installation_id: 7,
    enabled: true,
  })
  expect(
    doc.workflows.find((w: { repository: string }) => w.repository === 'test/console').source,
  ).toContain('configured-workflow-real-pod')
})

test('GitHub discovery and first-run wizard use real Go, SQLite and a k3s execution Pod', async ({
  page,
  request,
}) => {
  await page.goto('/connections?installation_id=999')
  await page.getByRole('button', { name: '添加仓库', exact: true }).first().click()
  await page.getByLabel('搜索 GitHub 仓库').fill('test/onboarding')
  const row = page.getByRole('listitem').filter({ hasText: 'test/onboarding' })
  await row.getByRole('button', { name: '选择仓库', exact: true }).click()
  await page.getByRole('button', { name: '启用仓库', exact: true }).click()
  await page.getByRole('button', { name: '检查默认分支 workflow', exact: true }).click()
  await expect(page.getByRole('dialog')).toContainText('b'.repeat(40))
  await expect(page.getByRole('dialog')).toContainText('检查通过')
  await page.getByRole('button', { name: '运行此 workflow', exact: true }).click()
  await expect(page).toHaveURL(/\/runs\/[a-f0-9]+$/)
  const id = new URL(page.url()).pathname.split('/').at(-1)!
  await expect
    .poll(async () => (await (await request.get(`/api/runs/${id}`)).json()).state)
    .toBe('success')
  await expect(page.getByLabel('日志内容')).toContainText('onboarding-real-k3s')
  const doc = await (await request.get('/api/management')).json()
  expect(
    doc.repositories.find((r: { name: string }) => r.name === 'test/onboarding'),
  ).toMatchObject({ repository_id: 13, installation_id: 7, enabled: true })
})
