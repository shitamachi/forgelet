import { describe, expect, it } from 'vitest'
import type { GitHubConnection, GitHubRepository, RepositoryConfig } from './types'
import { repositoryList, repositoryState } from './repository-state'
const registered: RepositoryConfig = {
  name: 'owner/app',
  repository_id: 1,
  installation_id: 7,
  enabled: true,
}
const repository: GitHubRepository = {
  id: 1,
  full_name: 'owner/app',
  installation_id: 7,
  default_branch: 'main',
  private: true,
  archived: false,
  disabled: false,
}
function catalog(): GitHubConnection {
  return {
    app: 'test',
    install_url: 'https://github.com/apps/test/installations/new',
    checked_at: '2026-10-24T12:00:00Z',
    installations: [
      {
        id: 7,
        account: { login: 'owner', type: 'User' },
        repository_selection: 'selected',
        suspended_at: null,
        permissions: { contents: 'read', checks: 'write' },
        manage_url: 'https://github.com/settings/installations/7',
      },
    ],
    repositories: [repository],
  }
}
describe('repository access is distinct from the CI enable switch', () => {
  it.each([
    'ready',
    'unbound',
    'missing',
    'suspended',
    'permissions',
    'archived',
    'renamed',
    'wrong-installation',
    'unknown',
    'refresh-failed',
  ])('%s', (mode) => {
    const doc = catalog()
    const repo = { ...registered }
    if (mode === 'unbound') delete repo.repository_id
    if (mode === 'missing') doc.repositories = []
    if (mode === 'suspended') doc.installations[0].suspended_at = '2026-10-24T12:00:00Z'
    if (mode === 'permissions') doc.installations[0].permissions.checks = 'read'
    if (mode === 'archived') doc.repositories = [{ ...repository, archived: true }]
    if (mode === 'renamed') doc.repositories = [{ ...repository, full_name: 'owner/renamed' }]
    if (mode === 'wrong-installation') doc.repositories = [{ ...repository, installation_id: 8 }]
    const state = repositoryState(
      repo,
      mode === 'unknown' ? undefined : doc,
      mode === 'refresh-failed',
    )
    expect(state.key).toBe(
      mode === 'ready'
        ? 'ready'
        : ['unknown', 'refresh-failed'].includes(mode)
          ? 'unknown'
          : 'attention',
    )
    if (mode !== 'ready') expect(state.repository).toBeUndefined()
    expect(repo.enabled).toBe(true)
  })
  it('paused repository may retain valid authorization', () =>
    expect(repositoryState({ ...registered, enabled: false }, catalog()).key).toBe('ready'))
})
describe('focused repository discovery', () => {
  const repos = Array.from({ length: 50 }, (_, i) => ({
    ...repository,
    id: i + 1,
    full_name: `${i % 2 ? 'team' : 'owner'}/app-${String(i).padStart(2, '0')}`,
  }))
  it('shows every repository sorted by full name', () => {
    const rows = repositoryList(repos, '', 'all')
    expect(rows).toHaveLength(50)
    expect(rows[0].full_name).toBe('owner/app-00')
    expect(rows[49].full_name).toBe('team/app-49')
  })
  it('filters by owner and case-insensitive full name without hiding the rest', () => {
    const team = repositoryList(repos, ' TEAM/app-0 ', 'team')
    expect(team).toHaveLength(5)
    expect(team.every((r) => r.full_name.startsWith('team/'))).toBe(true)
    expect(repositoryList(repos, '', 'owner')).toHaveLength(25)
  })
  it('returns an empty list for an absent search', () =>
    expect(repositoryList(repos, 'absent', 'all')).toEqual([]))
})

describe('durable authorization state', () => {
  it('retains confirmed access during a failed ordinary refresh', () => {
    const state = repositoryState(registered, catalog(), true, { state: 'authorized' })
    expect(state.key).toBe('ready')
    expect(state.repository?.id).toBe(1)
  })
  it.each(['pending', 'not_authorized', 'suspended', 'renamed'] as const)(
    'does not let an old catalog override %s',
    (state) => {
      const result = repositoryState(registered, catalog(), false, {
        state,
        remote_name: 'owner/new',
      })
      expect(result.key).toBe('attention')
      expect(result.repository).toBeUndefined()
      expect(registered.enabled).toBe(true)
    },
  )
})
