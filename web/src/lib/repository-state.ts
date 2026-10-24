import type { GitHubConnection, GitHubRepository, RepositoryConfig } from './types'

export function repositoryState(
  repo: RepositoryConfig,
  catalog?: GitHubConnection,
  failed = false,
) {
  if (!catalog || failed)
    return {
      key: 'unknown',
      label: '授权待检查',
      detail: '未能确认当前 GitHub 授权，刷新后重试。',
      repository: undefined,
    }
  const installation = catalog.installations.find((i) => i.id === repo.installation_id)
  const match = catalog.repositories.find(
    (r) =>
      r.id === repo.repository_id &&
      r.full_name === repo.name &&
      r.installation_id === repo.installation_id,
  )
  if (installation?.suspended_at)
    return {
      key: 'attention',
      label: '安装已暂停',
      detail: '在 GitHub 恢复安装后重新检查。',
      repository: undefined,
    }
  if (!repo.repository_id)
    return {
      key: 'attention',
      label: '待确认绑定',
      detail: '从添加仓库中选择同名仓库，确认 GitHub 身份。',
      repository: undefined,
    }
  if (!match || !installation)
    return {
      key: 'attention',
      label: '待 GitHub 授权',
      detail: 'GitHub App 当前无法访问此仓库。',
      repository: undefined,
    }
  if (
    !['read', 'write'].includes(installation.permissions.contents) ||
    installation.permissions.checks !== 'write'
  )
    return {
      key: 'attention',
      label: '安装权限不足',
      detail: '需要更新 GitHub App 的代码读取及 Checks 权限。',
      repository: undefined,
    }
  if (match.archived || match.disabled)
    return {
      key: 'attention',
      label: '仓库不可用',
      detail: '仓库已归档或被 GitHub 禁用。',
      repository: undefined,
    }
  return {
    key: 'ready',
    label: '授权正常',
    detail: 'GitHub App 可以读取仓库并回写 Checks。',
    repository: match,
  }
}

export function repositoryPage<T extends GitHubRepository>(
  repos: T[],
  search: string,
  owner: string,
  page: number,
  size = 6,
) {
  const text = search.trim().toLowerCase()
  const filtered = text
    ? repos
        .filter(
          (r) =>
            r.full_name.toLowerCase().includes(text) &&
            (owner === 'all' || r.full_name.split('/')[0] === owner),
        )
        .sort((a, b) => a.full_name.localeCompare(b.full_name))
    : []
  const pages = Math.max(1, Math.ceil(filtered.length / size))
  const current = Math.max(0, Math.min(page, pages - 1))
  return {
    rows: filtered.slice(current * size, (current + 1) * size),
    total: filtered.length,
    pages,
    current,
  }
}
