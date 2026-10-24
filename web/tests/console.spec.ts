import { expect, test } from '@playwright/test'
import { readFile } from 'node:fs/promises'

test.beforeEach(async ({ request }) => {
  await request.post('/__test/reset')
})

test('overview, URL filters, deep-link refresh and responsive navigation', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto('/')
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible()
  await page.getByRole('navigation').getByRole('link', { name: '运行记录' }).click()
  await page.getByLabel('筛选状态').selectOption('failure')
  await page.getByLabel('搜索运行').fill('health-check')
  await expect(page.locator('tbody tr')).toHaveCount(1)
  await expect(page).toHaveURL(/state=failure/)
  await page.reload()
  await expect(page.getByLabel('搜索运行')).toHaveValue('health-check')
  await expect(page.locator('tbody tr')).toHaveCount(1)
  await page.getByLabel('搜索运行').fill('nonexistent')
  await expect(page.getByText('没有匹配的运行')).toBeVisible()
  await page.getByRole('button', { name: '清除筛选' }).click()
  await expect(page.locator('tbody tr')).toHaveCount(6)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByRole('button', { name: '打开导航' }).click()
  await page.getByRole('navigation').getByRole('link', { name: '系统', exact: true }).click()
  await expect(page.getByRole('heading', { name: '数据盘保护' })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  expect(errors).toEqual([])
})

test('job graph, cursor log pages, text safety, filtering, downloads and expiration', async ({
  page,
}) => {
  await page.goto('/runs/run-success?job=run-success-test')
  await expect(page.getByRole('button', { name: /^test 成功/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  )
  await expect(page.getByLabel('日志内容')).toContainText('PASS · 260 chunks persisted')
  await expect(page.getByLabel('日志内容')).toContainText(
    '<svg onload=alert(1)>literal log text</svg>',
  )
  expect(await page.getByLabel('日志内容').locator('svg').count()).toBe(0)
  await page.getByLabel('搜索日志').fill('package_255')
  await expect(page.locator('.log-line')).toHaveCount(1)
  await page.getByLabel('搜索日志').fill('')
  await page.getByLabel('筛选日志步骤').selectOption('checkout')
  await expect(page.getByLabel('日志内容')).not.toContainText('PASS')
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('link', { name: '下载完整日志' }).click()
  const download = await downloadPromise
  expect(await readFile((await download.path())!, 'utf8')).toContain('260 chunks persisted')
  const artifactPromise = page.waitForEvent('download')
  await page.getByRole('link', { name: /test-report/ }).click()
  expect((await artifactPromise).suggestedFilename()).toBe('test-report.tar')
  await page.goto('/runs/run-expired')
  await expect(page.getByText('日志已根据保留策略清理，任务结果仍保留。')).toBeVisible()
})

test('cancel, rerun and validated manual dispatch have durable visible outcomes', async ({
  page,
  request,
}) => {
  await page.goto('/runs/run-live')
  await page.getByRole('button', { name: '取消运行', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '确认取消运行' }).click()
  await expect(page.getByRole('button', { name: '重新运行', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '重新运行', exact: true }).click()
  await page.getByRole('button', { name: '确认重新运行' }).click()
  await expect(page).toHaveURL(/\/runs\/rerun-/)
  await page.goto('/runs/new')
  await page.getByLabel('Commit SHA').fill('a'.repeat(40))
  await page.getByLabel('Inputs（JSON）').fill('[]')
  await page.getByRole('button', { name: '创建运行', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('Inputs 必须是 JSON 对象')
  await page.getByLabel('Inputs（JSON）').fill('{"environment":"test"}')
  await page.getByRole('button', { name: '创建运行', exact: true }).click()
  await expect(page).toHaveURL(/\/runs\/manual-/)
  const state = await (await request.get('/__test/state')).json()
  expect(
    state.calls.filter(
      (c: { method: string; path: string }) => c.method === 'POST' && c.path === '/api/runs',
    ),
  ).toHaveLength(1)
  expect(
    state.calls.find(
      (c: { method: string; path: string }) => c.method === 'POST' && c.path === '/api/runs',
    ).body.inputs,
  ).toEqual({ environment: 'test' })
})

test('variables and write-only secrets can be added, edited and removed', async ({ page }) => {
  await page.goto('/repositories')
  await page.getByRole('button', { name: '新增变量' }).click()
  await page.getByRole('dialog').getByLabel('名称').fill('WEB_TEST')
  await page.getByRole('dialog').getByLabel('值', { exact: true }).fill('first')
  await page.getByRole('dialog').getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('row', { name: /WEB_TEST first/ })).toBeVisible()
  await page.getByLabel('编辑变量 WEB_TEST').click()
  await page.getByRole('dialog').getByLabel('值', { exact: true }).fill('updated')
  await page.getByRole('dialog').getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('row', { name: /WEB_TEST updated/ })).toBeVisible()
  await page.getByLabel('删除变量 WEB_TEST').click()
  await page.getByRole('button', { name: '确认删除' }).click()
  await expect(page.getByText('WEB_TEST', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '新增 secret', exact: true }).click()
  await page.getByRole('dialog').getByLabel('名称').fill('TEST_SECRET')
  await page.getByLabel('新的 secret 值').fill('never-return-this-secret')
  await page.getByRole('dialog').getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('row', { name: /TEST_SECRET 已加密/ })).toBeVisible()
  await expect(page.getByText('never-return-this-secret')).toHaveCount(0)
  await page.getByLabel('删除 secret TEST_SECRET').click()
  await page.getByRole('button', { name: '确认删除' }).click()
  await expect(page.getByText('TEST_SECRET', { exact: true })).toHaveCount(0)
})

test('artifacts refresh when a fast running job becomes terminal', async ({ page, request }) => {
  await page.goto('/runs/run-live')
  await expect(page.getByText('没有可下载的产物，或产物已过期。')).toBeVisible()
  const state = await (await request.get('/__test/state')).json()
  const run = state.runs.find((r: { id: string }) => r.id === 'run-live')
  run.state = 'success'
  for (const job of run.jobs) job.state = 'success'
  await request.post('/__test/state', { data: { runs: state.runs } })
  await expect(page.getByRole('link', { name: /test-report/ })).toBeVisible()
})

test('rollback conflicts stay in the dialog, successful retry links to the new delivery', async ({
  page,
  request,
}) => {
  await request.post('/__test/state', {
    data: {
      errors: {
        'POST /api/deployments/delivery-current/rollback': {
          status: 409,
          body: 'GitOps branch changed',
        },
      },
    },
  })
  await page.goto('/deployments/delivery-current')
  await expect(page.getByText('应用就绪', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '回滚到上一版本' }).click()
  await page.getByRole('button', { name: '创建回滚发布', exact: true }).click()
  await expect(page.getByRole('dialog').getByRole('alert')).toContainText('GitOps branch changed')
  await expect(page).toHaveURL(/delivery-current$/)
  await request.post('/__test/state', { data: { errors: {} } })
  await page.getByRole('button', { name: '创建回滚发布', exact: true }).click()
  await expect(page).toHaveURL(/rollback-1$/)
  await expect(page.getByRole('heading', { name: 'homelab / 回滚' })).toBeVisible()
})

test('disk pause, maintenance, backup and missing-record errors', async ({ page, request }) => {
  const state = await (await request.get('/__test/state')).json()
  state.status.disk.paused = true
  state.status.disk.available_bytes = 100 * 1024 ** 2
  state.status.disk.reason = 'low_space'
  await request.post('/__test/state', { data: { status: state.status } })
  await page.goto('/system')
  await expect(page.locator('.disk-banner')).toBeVisible()
  await page.getByRole('button', { name: '创建数据库备份', exact: true }).click()
  await page.getByRole('button', { name: '创建备份', exact: true }).click()
  await expect(page.getByText('/data/backups/backup-fixture.db')).toBeVisible()
  await page.getByRole('button', { name: '执行保留清理', exact: true }).click()
  await page.getByRole('button', { name: '执行清理', exact: true }).click()
  await expect(page.getByText('清理完成', { exact: true })).toBeVisible()
  await page.goto('/runs/missing')
  await expect(page.getByRole('alert').filter({ hasText: '记录不存在' })).toBeVisible()
  await request.post('/__test/state', {
    data: { errors: { 'GET /api/runs': { status: 503, body: 'SQLite unavailable' } } },
  })
  await page.goto('/runs')
  await expect(page.getByRole('alert').filter({ hasText: 'SQLite unavailable' })).toBeVisible()
  await request.post('/__test/state', { data: { errors: {} } })
  await page.getByRole('button', { name: '重试' }).click()
  await expect(page.locator('tbody tr')).toHaveCount(6)
})

test('repository onboarding, workflow editing/dispatch and target forms persist their changes', async ({
  page,
  request,
}) => {
  await page.goto('/connections')
  await page.getByRole('button', { name: '手动登记（高级）' }).click()
  await page.getByLabel('仓库全名').fill('test/new-repo')
  await page.getByRole('dialog').getByLabel('Installation ID', { exact: true }).fill('42')
  await page.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(page.getByRole('article', { name: 'test/new-repo', exact: true })).toBeVisible()
  await page
    .getByRole('article', { name: 'test/new-repo', exact: true })
    .getByLabel('管理 test/new-repo')
    .click()
  await page.getByRole('button', { name: '检查接入', exact: true }).click()
  await page.getByRole('dialog').getByLabel('Commit SHA').fill('a'.repeat(40))
  await page.getByRole('button', { name: '检查访问', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: '接入检查通过' })).toBeVisible()
  await page.getByRole('button', { name: '关闭对话框' }).click()
  await page.goto('/workflows')
  await page.getByRole('button', { name: '新增Workflows' }).click()
  await page.getByRole('dialog').getByLabel('仓库', { exact: true }).selectOption('test/new-repo')
  await page.getByLabel('Workflow 来源').selectOption('managed')
  await page.getByRole('button', { name: '填入示例' }).click()
  await page.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(page.getByRole('row', { name: /test\/new-repo.*控制面 YAML/ })).toBeVisible()
  await page.reload()
  await page
    .getByRole('row', { name: /test\/new-repo/ })
    .getByRole('button', { name: '运行', exact: true })
    .click()
  await page.getByRole('dialog').getByLabel('Commit SHA').fill('a'.repeat(40))
  await page.getByRole('dialog').getByRole('button', { name: '创建运行', exact: true }).click()
  await expect(page).toHaveURL(/\/runs\/manual-/)
  await page.goto('/targets')
  await page.getByLabel('编辑 homelab', { exact: true }).click()
  await page.getByLabel('目标名称', { exact: true }).fill('edited-target')
  await page.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(page.getByText('edited-target', { exact: true })).toBeVisible()
  await page.getByLabel('删除 edited-target', { exact: true }).click()
  await page.getByRole('button', { name: '确认删除配置' }).click()
  await expect(page.getByText('edited-target', { exact: true })).toHaveCount(0)
  const state = await (await request.get('/__test/state')).json()
  expect(state.management.workflows[0].source).toContain('workflow-ready')
  expect(state.management.targets).toEqual([])
})

test('configuration conflict keeps unsaved edits visible until explicitly reloaded', async ({
  page,
  request,
}) => {
  await page.goto('/connections')
  await page.getByLabel('管理 homelab/gateway', { exact: true }).click()
  await page.getByRole('button', { name: '编辑仓库配置', exact: true }).click()
  await page.getByLabel('仓库全名').fill('homelab/changed')
  await request.post('/__test/state', {
    data: {
      errors: {
        'PUT /api/management': { status: 409, body: 'configuration changed; reload before saving' },
      },
    },
  })
  await page.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(page.getByRole('dialog').getByRole('alert')).toContainText('configuration changed')
  await expect(page.getByLabel('仓库全名')).toHaveValue('homelab/changed')
  await page.getByRole('button', { name: '返回', exact: true }).click()
  await page.getByRole('button', { name: '重新加载', exact: true }).click()
  await expect(page.getByText('homelab/gateway', { exact: true })).toBeVisible()
})

test('GitHub authorization discovery enables a selected repository and runs its checked SHA', async ({
  page,
  request,
}) => {
  await page.goto('/connections?installation_id=999&setup_action=install')
  await page.getByRole('button', { name: '添加仓库', exact: true }).first().click()
  const onboarding = page.getByRole('dialog', { name: '添加 GitHub 仓库' })
  await page.getByLabel('搜索 GitHub 仓库').fill('missing')
  await expect(onboarding.getByText('team/new-project', { exact: true })).toHaveCount(0)
  await page.getByLabel('搜索 GitHub 仓库').fill('new-project')
  await onboarding.getByRole('button', { name: '选择仓库', exact: true }).click()
  await onboarding.getByRole('button', { name: '启用仓库', exact: true }).click()
  await page.getByRole('button', { name: '检查默认分支 workflow', exact: true }).click()
  await expect(page.getByRole('dialog')).toContainText('b'.repeat(40))
  const bad = page.locator('article').filter({ hasText: '.github/workflows/unsupported.yml' })
  await expect(bad).toContainText('unknown execution profile')
  await expect(bad.getByRole('button')).toBeDisabled()
  await page
    .locator('article')
    .filter({ hasText: '.github/workflows/ci.yml' })
    .getByRole('button')
    .click()
  await expect(page).toHaveURL(/\/runs\/onboarding-run$/)
  const state = await (await request.get('/__test/state')).json()
  const call = state.calls.find((c: { path: string }) => c.path === '/api/github/run')
  expect(call.body).toMatchObject({
    repository_id: 123,
    sha: 'b'.repeat(40),
    ref: 'refs/heads/main',
  })
  expect(
    state.calls.find((c: { path: string }) => c.path === '/api/github/enable').body,
  ).not.toHaveProperty('installation_id')
})

test('administrator login, account security and GitHub account repositories', async ({
  page,
  request,
}) => {
  await request.post('/__test/state', {
    data: {
      auth: { mode: 'session', github_enabled: true, user: null },
      linkedGitHub: { github_id: 1234, github_login: 'owner' },
    },
  })
  await page.goto('/connections')
  await expect(page.getByRole('heading', { name: '登录你的 Homelab', exact: true })).toBeVisible()
  await expect(page.getByRole('navigation', { name: '主导航' })).toHaveCount(0)
  await page.getByLabel('密码', { exact: true }).fill('wrong')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('用户名或密码不正确')
  await page.getByLabel('密码', { exact: true }).fill('fixture-admin-password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '仓库接入', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '添加仓库', exact: true }).first().click()
  await page.getByLabel('搜索 GitHub 仓库').fill('new-project')
  await page.getByRole('button', { name: '选择仓库', exact: true }).click()
  await page
    .getByRole('dialog', { name: '添加 GitHub 仓库' })
    .getByRole('button', { name: '启用仓库', exact: true })
    .click()
  await expect(page.getByRole('dialog', { name: '检查 workflow 与首次运行' })).toBeVisible()
  await page.getByRole('button', { name: '关闭对话框' }).click()
  const calls = (await (await request.get('/__test/state')).json()).calls
  expect(calls.some((call: { path: string }) => call.path === '/api/github/user/enable')).toBe(true)
  await page
    .getByRole('navigation', { name: '主导航' })
    .getByRole('link', { name: '账号', exact: true })
    .click()
  await expect(page.getByText('已关联 @owner')).toBeVisible()
  await page.getByLabel('当前密码', { exact: true }).fill('fixture-admin-password')
  await page.getByLabel('新密码', { exact: true }).fill('updated-fixture-password')
  await page.getByLabel('确认新密码', { exact: true }).fill('updated-fixture-password')
  await page.getByRole('button', { name: '更新密码', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: '密码已更新' })).toBeVisible()
  await page.getByRole('button', { name: '退出登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '登录你的 Homelab', exact: true })).toBeVisible()
  expect(await page.evaluate(() => Object.keys(localStorage))).toEqual([])
})

test('adding a repository requires focused search, bounds results and preserves GitHub consent across reload', async ({
  page,
  request,
}) => {
  await request.post('/__test/state', {
    data: {
      auth: {
        mode: 'session',
        github_enabled: true,
        user: { id: 1, username: 'admin', github_id: 1234, github_login: 'owner' },
      },
      githubNeedsAuthorization: true,
    },
  })
  await page.goto('/connections')
  await expect(page.getByRole('region', { name: '已接入仓库' })).toBeVisible()
  await expect(page.getByText('team/new-project', { exact: true })).toHaveCount(0)
  let state = await (await request.get('/__test/state')).json()
  expect(
    state.calls.some((c: { path: string }) => c.path === '/api/github/user/repositories'),
  ).toBe(false)
  await page.getByRole('button', { name: '添加仓库', exact: true }).click()
  await expect(page.getByText('搜索要添加的仓库', { exact: true })).toBeVisible()
  await expect(page.getByRole('listitem')).toHaveCount(0)
  await page.getByLabel('搜索 GitHub 仓库').fill('new-project')
  await page.getByRole('button', { name: '选择仓库', exact: true }).click()
  await page.getByRole('button', { name: '启用仓库', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: '等待 GitHub 授权' })).toBeVisible()
  await expect(page.getByRole('link', { name: '前往 GitHub 授权' })).toHaveAttribute(
    'href',
    'https://github.com/organizations/team/settings/installations/12',
  )
  state = await (await request.get('/__test/state')).json()
  expect(
    state.management.repositories.some((r: { name: string }) => r.name === 'team/new-project'),
  ).toBe(false)
  await page.reload()
  await expect(page.getByRole('dialog', { name: '添加 GitHub 仓库' })).toContainText(
    'team/new-project',
  )
  await page.getByRole('button', { name: '启用仓库', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: '等待 GitHub 授权' })).toBeVisible()
  await request.post('/__test/state', { data: { githubConsent: true } })
  await page.getByRole('button', { name: '检查授权并启用', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '检查 workflow 与首次运行' })).toBeVisible()
  await page.getByRole('button', { name: '关闭对话框', exact: true }).click()
  const row = page.getByRole('article', { name: 'team/new-project', exact: true })
  await expect(row).toContainText('CI 已启用')
  await expect(row).toContainText('授权正常')
  await page.getByLabel('管理 team/new-project').click()
  await page.getByRole('button', { name: '停用 CI', exact: true }).click()
  await expect(row).toContainText('CI 已停用')
  await page.getByRole('button', { name: /已停用/ }).click()
  await expect(page.getByRole('article')).toHaveCount(1)
  await page.getByRole('button', { name: '添加仓库', exact: true }).click()
  await page.getByRole('button', { name: '关闭对话框', exact: true }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('repository picker paginates a large account and handles missing access without misleading CI status', async ({
  page,
  request,
}) => {
  const repositories = Array.from({ length: 50 }, (_, i) => ({
    id: 500 + i,
    full_name: `owner/app-${String(i).padStart(2, '0')}`,
    installation_id: 0,
    default_branch: 'main',
    private: true,
    archived: false,
    disabled: false,
    owner: { login: 'owner' },
    permissions: { admin: i !== 0 },
  }))
  await request.post('/__test/state', {
    data: {
      auth: {
        mode: 'session',
        github_enabled: true,
        user: { id: 1, username: 'admin', github_id: 1234, github_login: 'owner' },
      },
      githubUserRepositories: repositories,
    },
  })
  await page.goto('/connections')
  await page.getByRole('button', { name: '添加仓库', exact: true }).click()
  await page.getByLabel('搜索 GitHub 仓库').fill('app-')
  await expect(page.getByRole('listitem')).toHaveCount(6)
  await expect(page.getByRole('button', { name: '需要管理员权限', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '下一页仓库', exact: true }).click()
  await expect(page.getByRole('listitem')).toHaveCount(6)
  await expect(page.getByText('owner/app-06', { exact: true })).toBeVisible()
  await page.getByLabel('搜索 GitHub 仓库').fill('app-49')
  await expect(page.getByRole('listitem')).toHaveCount(1)
  await expect(page.getByRole('button', { name: '上一页仓库', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '关闭对话框', exact: true }).click()
  await request.post('/__test/state', {
    data: { errors: { 'GET /api/github/connection': { status: 502, body: 'unavailable' } } },
  })
  await page.getByRole('button', { name: '重新加载', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('已保存的仓库和启用设置保留')
  await expect(page.getByRole('article', { name: 'homelab/gateway', exact: true })).toContainText(
    '授权待检查',
  )
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})
