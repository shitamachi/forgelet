import { expect, test } from '@playwright/test'

test('admin initializes, links and logs in with GitHub, imports a repository and executes a real Pod', async ({
  page,
  request,
}) => {
  const base = process.env.FORGELET_E2E_BASE_URL
  if (!base) throw new Error('Run via Testcontainers')
  // This route is the external GitHub authorization fixture. All forgelet
  // pages, OAuth token exchange, repository API, SQLite and Pods are real.
  let usedState = ''
  await page.route('https://localhost:9443/login/oauth/authorize**', async (route) => {
    const url = new URL(route.request().url())
    expect(url.searchParams.get('code_challenge_method')).toBe('S256')
    expect(url.searchParams.get('scope')).toBe('read:user repo')
    usedState = url.searchParams.get('state')!
    const callback = new URL(url.searchParams.get('redirect_uri')!)
    expect(callback.origin).toBe(new URL(base).origin)
    callback.searchParams.set('code', 'integration-oauth-code')
    callback.searchParams.set('state', usedState)
    await route.fulfill({ status: 302, headers: { location: callback.href } })
  })
  await page.goto('/connections')
  await expect(page.getByRole('heading', { name: '登录你的 Homelab', exact: true })).toBeVisible()
  await page.getByLabel('密码', { exact: true }).fill('integration-admin-password-12345')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '仓库接入', exact: true })).toBeVisible()
  const cookie = (await page.context().cookies()).find((c) => c.name === 'forgelet_session')
  expect(cookie).toMatchObject({ httpOnly: true, secure: true, sameSite: 'Lax' })
  await page
    .getByRole('navigation', { name: '主导航' })
    .getByRole('link', { name: '账号', exact: true })
    .click()
  await page.getByRole('button', { name: '连接 GitHub', exact: true }).click()
  await expect(page).toHaveURL(/\/connections\?github=connected$/)
  await page.getByRole('button', { name: '添加仓库', exact: true }).first().click()
  await page.getByLabel('搜索 GitHub 仓库').fill('test/oauth')
  await page
    .getByRole('listitem')
    .filter({ hasText: 'test/oauth' })
    .getByRole('button', { name: '选择仓库', exact: true })
    .click()
  await page.getByRole('button', { name: '启用仓库', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: '等待 GitHub 授权' })).toBeVisible()
  expect(
    (await (await page.request.get('/api/management')).json()).repositories.some(
      (r: { name: string }) => r.name === 'test/oauth',
    ),
  ).toBe(false)
  await page.getByRole('button', { name: '检查授权并启用', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: '等待 GitHub 授权' })).toBeVisible()
  const consent = process.env.FORGELET_E2E_CONSENT_URL
  if (!consent) throw new Error('Testcontainers consent bridge required')
  expect((await request.post(consent)).status()).toBe(204)
  await page.getByRole('button', { name: '检查授权并启用', exact: true }).click()
  await page.getByRole('button', { name: '检查默认分支 workflow', exact: true }).click()
  await expect(page.getByRole('dialog')).toContainText('检查通过')
  await page.getByRole('button', { name: '运行此 workflow', exact: true }).click()
  await expect(page).toHaveURL(/\/runs\/[a-f0-9]+$/)
  const id = new URL(page.url()).pathname.split('/').at(-1)!
  await expect
    .poll(async () => (await (await page.request.get(`/api/runs/${id}`)).json()).state)
    .toBe('success')
  await expect(page.getByLabel('日志内容')).toContainText('oauth-first-run-real-k3s')
  const session = await (await page.request.get('/api/auth/session')).json()
  expect(session.user).toMatchObject({ username: 'admin', github_id: 987654, github_login: 'test' })
  expect(JSON.stringify(session)).not.toContain('integration-user-token')
  await page
    .getByRole('navigation', { name: '主导航' })
    .getByRole('link', { name: '账号', exact: true })
    .click()
  await page.getByRole('button', { name: '退出登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '登录你的 Homelab', exact: true })).toBeVisible()
  expect((await page.request.get('/api/config')).status()).toBe(401)
  await page.getByRole('button', { name: '使用 GitHub 登录', exact: true }).click()
  await expect(page).toHaveURL(/\/connections\?github=connected$/)
  await expect(page.getByRole('heading', { name: '仓库接入', exact: true })).toBeVisible()
  expect(
    (
      await page.request.get(
        `/api/auth/github/callback?code=integration-oauth-code&state=${usedState}`,
      )
    ).status(),
  ).toBe(400)
  expect((await request.get('/api/config')).status()).toBe(401)
})
