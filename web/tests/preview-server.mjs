// Deterministic API fixtures, served only by browser tests and local UI previews.
// This file is never embedded in the Go binary.
import { createServer } from 'node:http'
import { readFile, stat } from 'node:fs/promises'
import { resolve, extname, sep } from 'node:path'
import { fixtureRun, fixtureState } from './fixtures.ts'

let state = fixtureState()
const root = resolve(import.meta.dirname, '../../internal/webui/dist')
const mime = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'application/javascript',
  '.css': 'text/css',
  '.woff2': 'font/woff2',
}
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost')
  const path = decodeURIComponent(url.pathname)
  const json = (value, status = 200) => {
    res.writeHead(status, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify(value))
  }
  const plain = (value, status = 200, headers = {}) => {
    res.writeHead(status, { 'Content-Type': 'text/plain', ...headers })
    res.end(value)
  }
  let raw = ''
  for await (const chunk of req) raw += chunk
  let body
  try {
    body = raw ? JSON.parse(raw) : undefined
  } catch {
    return plain('Invalid JSON', 400)
  }
  if (path === '/__test/reset') {
    state = fixtureState()
    return json({ ok: true })
  }
  if (path === '/__test/state') {
    if (req.method === 'POST') Object.assign(state, body)
    return json(state)
  }
  if (path.startsWith('/api/')) {
    state.calls.push({ method: req.method, path, body })
    const failure = state.errors[`${req.method} ${path}`]
    if (failure) return plain(failure.body, failure.status)
    const auth = state.auth || { mode: 'open', github_enabled: false, user: null }
    if (path === '/api/auth/session') return json(auth)
    if (path === '/api/auth/login') {
      if (
        body.username !== 'admin' ||
        body.password !== (state.password || 'fixture-admin-password')
      )
        return plain('invalid username or password', 401)
      state.auth = { ...auth, user: { id: 1, username: 'admin', ...state.linkedGitHub } }
      res.setHeader(
        'Set-Cookie',
        'forgelet_fixture_session=test-cookie; HttpOnly; SameSite=Lax; Path=/',
      )
      return json(state.auth)
    }
    if (path === '/api/auth/github/start')
      return json({
        url: 'https://github.com/login/oauth/authorize?fixture=' + (body.link ? 'link' : 'login'),
      })
    if (auth.mode === 'session' && !auth.user) return plain('authentication required', 401)
    if (path === '/api/auth/logout') {
      state.auth = { ...auth, user: null }
      res.setHeader('Set-Cookie', 'forgelet_fixture_session=; Max-Age=0; Path=/')
      return json({ logged_out: true })
    }
    if (path === '/api/auth/password') {
      if (body.current_password !== (state.password || 'fixture-admin-password'))
        return plain('current password is incorrect', 401)
      state.password = body.new_password
      return json({ changed: true })
    }
    if (path === '/api/github/user/repositories')
      return json(
        state.githubUserRepositories || [
          {
            id: 123,
            full_name: 'team/new-project',
            installation_id: 0,
            default_branch: 'main',
            private: true,
            archived: false,
            disabled: false,
            owner: { login: 'team' },
            permissions: { admin: true },
          },
        ],
      )
    if (path === '/api/github/user/enable') {
      if (body.version !== state.management?.version) return plain('configuration changed', 409)
      if (state.githubNeedsAuthorization && !state.githubConsent)
        return json(
          {
            status: 'authorization_required',
            reason: 'repository_not_granted',
            repository_id: body.repository_id,
            repository: 'team/new-project',
            authorize_url: 'https://github.com/organizations/team/settings/installations/12',
          },
          202,
        )

      state.management.repositories.push({
        repository_id: 123,
        name: 'team/new-project',
        installation_id: 12,
        enabled: true,
      })
      state.management.version++
      return json(state.management)
    }
    if (path === '/api/github/authorization')
      return json(
        state.githubAuthorization || {
          repositories: Object.fromEntries(
            state.config.repositories.map((name) => [name, { state: 'authorized' }]),
          ),
        },
      )
    if (path === '/api/github/connection')
      return json({
        app: 'forgelet-test',
        install_url: 'https://github.com/apps/forgelet-test/installations/new',
        checked_at: new Date().toISOString(),
        installations: [
          {
            id: 12,
            account: { login: 'team', type: 'Organization' },
            permissions: { contents: 'read', checks: 'write' },
            repository_selection: 'selected',
            suspended_at: null,
            manage_url: 'https://github.com/organizations/team/settings/installations/12',
          },
        ],
        repositories:
          state.githubNeedsAuthorization && !state.githubConsent
            ? []
            : [
                {
                  id: 123,
                  full_name: 'team/new-project',
                  installation_id: 12,
                  default_branch: 'main',
                  private: true,
                  archived: false,
                  disabled: false,
                },
              ],
      })
    if (path === '/api/github/enable') {
      if (!state.management || body.version !== state.management.version)
        return plain('configuration changed; refresh', 409)
      state.management.repositories.push({
        repository_id: 123,
        name: 'team/new-project',
        installation_id: 12,
        enabled: true,
      })
      state.management.version++
      return json(state.management)
    }
    if (path === '/api/github/preview')
      return json({
        repository: { id: 123, full_name: 'team/new-project' },
        sha: 'b'.repeat(40),
        ref: 'refs/heads/main',
        workflows: [
          {
            path: '.github/workflows/ci.yml',
            source: 'on: push',
            diagnostic: '',
            runnable: true,
            jobs: 1,
          },
          {
            path: '.github/workflows/unsupported.yml',
            source: 'on: push',
            diagnostic: 'unknown execution profile ubuntu-latest',
            runnable: false,
            jobs: 0,
          },
        ],
      })
    if (path === '/api/github/run') {
      const run = fixtureRun('onboarding-run', 'queued')
      Object.assign(run, { repository: 'team/new-project', sha: body.sha, ref: body.ref })
      state.runs.unshift(run)
      return json(run, 201)
    }
    if (path === '/api/management') {
      state.management ??= {
        version: 0,
        repositories: state.config.repositories.map((name) => ({
          name,
          installation_id: 12,
          enabled: true,
        })),
        workflows: [],
        targets: state.config.targets,
      }
      if (req.method === 'PUT') {
        if (body.version !== state.management.version)
          return plain('configuration changed; reload before saving', 409)
        state.management = { ...body, version: body.version + 1 }
        state.config.repositories = body.repositories.filter((r) => r.enabled).map((r) => r.name)
        state.config.targets = body.targets
      }
      return json(state.management)
    }
    if (path === '/api/management/inspect')
      return json([
        {
          repository: body.repository,
          path: '.github/workflows/ci.yml',
          enabled: true,
          source:
            'name: Git workflow\non: push\njobs:\n  check:\n    runs-on: homelab\n    steps:\n      - run: echo git-verified\n',
        },
      ])
    if (path === '/api/config') return json(state.config)
    if (path === '/api/status') return json(state.status)
    if (path === '/api/events') return json(state.events)
    if (path === '/api/runs') {
      if (req.method === 'POST') {
        const run = fixtureRun(`manual-${state.runs.length}`, 'queued')
        Object.assign(run, { repository: body.repository, sha: body.sha, ref: body.ref })
        state.runs.unshift(run)
        return json(run, 201)
      }
      return json(state.runs)
    }
    let match = path.match(/^\/api\/runs\/([^/]+)(?:\/(.*))?$/)
    if (match) {
      const run = state.runs.find((r) => r.id === match[1])
      if (!run) return plain('run not found', 404)
      if (match[2] === 'checks/retry') {
        for (const job of run.jobs) delete job.check_error
        return json({ retried: true })
      }
      if (match[2] === 'cancel') {
        run.state = 'cancelled'
        for (const job of run.jobs) job.state = 'cancelled'
        return json({ cancel_requested: true })
      }
      if (match[2] === 'rerun') {
        const next = fixtureRun(`rerun-${state.runs.length}`, 'queued')
        state.runs.unshift(next)
        return json(next, 201)
      }
      if (match[2] === 'artifacts' && ['queued', 'starting', 'running'].includes(run.state))
        return json([])
      if (match[2] === 'artifacts')
        return json([
          {
            id: 'artifact',
            key: 'test-report',
            digest: 'sha256:' + 'c'.repeat(64),
            size: 14208,
            created_at: run.created_at,
            expires_at: new Date(Date.now() + 86400000).toISOString(),
          },
        ])
      if (match[2]?.startsWith('artifacts/'))
        return plain('fixture artifact', 200, {
          'Content-Disposition': 'attachment; filename="test-report.tar"',
        })
      return json(run)
    }
    match = path.match(/^\/api\/jobs\/([^/]+)\/logs(\/download)?$/)
    if (match) {
      const job = state.runs.flatMap((r) => r.jobs).find((j) => j.id === match[1])
      if (job?.logs_expired) return plain('logs expired', 410)
      const entries = state.logs[match[1]] ?? []
      if (match[2])
        return plain(entries.map((c) => c.data).join(''), 200, {
          'Content-Disposition': `attachment; filename="forgelet-${match[1]}.log"`,
        })
      return json(
        entries.filter((c) => c.seq > Number(url.searchParams.get('after') || 0)).slice(0, 256),
      )
    }
    if (path === '/api/deployments') return json(state.deliveries)
    match = path.match(/^\/api\/deployments\/([^/]+)(\/rollback)?$/)
    if (match) {
      const delivery = state.deliveries.find((d) => d.id === match[1])
      if (!delivery) return plain('delivery not found', 404)
      if (match[2]) {
        const rollback = {
          ...delivery,
          id: `rollback-${state.deliveries.length}`,
          state: 'queued',
          rollback_of: delivery.id,
          digest: delivery.previous_digest,
          revision: '',
          observed_revision: '',
          commit: '',
        }
        state.deliveries.unshift(rollback)
        return json(rollback)
      }
      return json(delivery)
    }
    match = path.match(/^\/api\/repositories\/[^/]+\/[^/]+\/(vars|secrets)(?:\/(.*))?$/)
    if (match) {
      const [, kind, name] = match
      if (req.method === 'PUT') {
        if (kind === 'vars') state.variables[name] = body.value
        else if (!state.secrets.includes(name)) state.secrets.push(name)
        return json({ saved: true })
      }
      if (req.method === 'DELETE') {
        if (kind === 'vars') delete state.variables[name]
        else state.secrets = state.secrets.filter((n) => n !== name)
        return json({ deleted: true })
      }
      return json(kind === 'vars' ? state.variables : { names: state.secrets })
    }
    if (path === '/api/backup')
      return json(
        { file: '/data/backups/backup-fixture.db', restore: 'stop, restore snapshot' },
        201,
      )
    if (path === '/api/maintenance')
      return json({ runs: 2, jobs: 4, logs: 12, events: 2, deployments: 0, backups: 1, blobs: 0 })
    return plain('API not found', 404)
  }
  try {
    const file = resolve(root, '.' + path)
    if (!file.startsWith(root + sep)) {
      if (path !== '/') return plain('not found', 404)
    } else if (extname(path)) {
      if (!(await stat(file)).isFile()) return plain('not found', 404)
      res.writeHead(200, { 'Content-Type': mime[extname(file)] || 'application/octet-stream' })
      return res.end(await readFile(file))
    }
    res.writeHead(200, { 'Content-Type': mime['.html'] })
    res.end(await readFile(resolve(root, 'index.html')))
  } catch {
    plain('asset not found; run npm run build', 404)
  }
})
const port = Number(process.env.FORGELET_PREVIEW_PORT || 4173)
server.listen(port, '127.0.0.1', () => console.log(`Fixture preview: http://127.0.0.1:${port}`))
