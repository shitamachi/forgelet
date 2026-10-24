import { describe, expect, it } from 'vitest'
import type { GitHubConnection, GitHubRepository, RepositoryConfig } from './types'
import { repositoryPage, repositoryState } from './repository-state'
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
  it('does not show the whole account before searching', () =>
    expect(repositoryPage(repos, '', 'all', 0).rows).toEqual([]))
  it('bounds results and clamps an obsolete page after filters change', () => {
    const p = repositoryPage(repos, 'APP', 'owner', 99)
    expect(p.total).toBe(25)
    expect(p.pages).toBe(5)
    expect(p.current).toBe(4)
    expect(p.rows).toHaveLength(1)
  })
  it('respects owner and case-insensitive full name search', () => {
    const p = repositoryPage(repos, ' TEAM/app-0 ', 'team', 0)
    expect(p.total).toBe(5)
    expect(p.rows.every((r) => r.full_name.startsWith('team/'))).toBe(true)
    expect(repositoryPage(repos, 'absent', 'all', 0).total).toBe(0)
  })
})
