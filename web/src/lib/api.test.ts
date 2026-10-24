import { afterEach, expect, it, vi } from 'vitest'
import { api, APIError, errorMessage } from './api'
afterEach(() => vi.unstubAllGlobals())
it('sends same-origin JSON mutations and encodes identifiers', async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response('{}', { status: 200 }))
  vi.stubGlobal('fetch', fetcher)
  await api.deleteSetting('org/repo', 'secrets', 'A/B')
  expect(fetcher).toHaveBeenCalledWith(
    '/api/repositories/org/repo/secrets/A%2FB',
    expect.objectContaining({
      method: 'DELETE',
      body: '{}',
      credentials: 'same-origin',
      headers: expect.objectContaining({ 'Content-Type': 'application/json' }),
    }),
  )
})
it('passes cancellation to log reads and normalizes empty collections', async () => {
  const fetcher = vi.fn().mockImplementation(() => Promise.resolve(new Response('null')))
  vi.stubGlobal('fetch', fetcher)
  const signal = new AbortController().signal
  expect(await api.logs('job/id', 256, signal)).toEqual([])
  expect(fetcher).toHaveBeenCalledWith(
    '/api/jobs/job%2Fid/logs?after=256',
    expect.objectContaining({ signal }),
  )
  expect(await api.runs()).toEqual([])
})
it('preserves conflict errors without pretending a mutation succeeded', async () => {
  const fetcher = vi
    .fn()
    .mockImplementation(() =>
      Promise.resolve(new Response('GitOps branch changed', { status: 409 })),
    )
  vi.stubGlobal('fetch', fetcher)
  await expect(api.rollback('d')).rejects.toMatchObject({
    status: 409,
    message: 'GitOps branch changed',
  })
  expect(fetcher).toHaveBeenCalledTimes(1)
  expect(errorMessage(new APIError(410, 'expired'))).toContain('清理')
  expect(errorMessage(new TypeError('fetch failed'))).toContain('无法连接')
})

it('saves a versioned management document without retrying stale updates', async () => {
  const fetcher = vi
    .fn()
    .mockImplementation(() =>
      Promise.resolve(new Response('configuration changed', { status: 409 })),
    )
  vi.stubGlobal('fetch', fetcher)
  const doc = { version: 3, repositories: [], workflows: [], targets: [] }
  await expect(api.saveManagement(doc)).rejects.toMatchObject({ status: 409 })
  expect(fetcher).toHaveBeenCalledTimes(1)
  expect(fetcher).toHaveBeenCalledWith(
    '/api/management',
    expect.objectContaining({ method: 'PUT', body: JSON.stringify(doc) }),
  )
})

it('onboarding sends a repository identity and preserves the previewed SHA for first run', async () => {
  const fetcher = vi.fn().mockImplementation(() => Promise.resolve(new Response('{}')))
  vi.stubGlobal('fetch', fetcher)
  await api.githubEnable(123, 5)
  expect(fetcher).toHaveBeenLastCalledWith(
    '/api/github/enable',
    expect.objectContaining({ method: 'POST', body: '{"repository_id":123,"version":5}' }),
  )
  const input = {
    repository_id: 123,
    sha: 'a'.repeat(40),
    ref: 'refs/heads/main',
    path: '.github/workflows/ci.yml',
    event_name: 'push',
    inputs: {},
    request_id: 'unique-request-12345',
  }
  await api.githubRun(input)
  expect(fetcher).toHaveBeenLastCalledWith(
    '/api/github/run',
    expect.objectContaining({ method: 'POST', body: JSON.stringify(input) }),
  )
})
