import type {
  GitHubConnection,
  GitHubRepository,
  GitHubRepositoryAccess,
  RepositoryConfig,
} from './types'

export function repositoryState(
  repo: RepositoryConfig,
  catalog?: GitHubConnection,
  failed = false,
  authority?: GitHubRepositoryAccess,
) {
  if (authority) {
    const labels: Record<GitHubRepositoryAccess['state'], [string, string]> = {
      authorized: ['授权正常', 'GitHub App 可以读取仓库并回写 Checks。'],
      unknown: ['授权待确认', '正在确认 GitHub 授权，新任务暂时等待。'],
      pending: ['授权变更确认中', '新任务和凭据领取已暂停，确认完成后自动更新。'],
      not_authorized: ['GitHub 授权已移除', '在 GitHub 恢复仓库授权后刷新；历史记录保留。'],
      suspended: ['安装已暂停', '在 GitHub 恢复安装后刷新。'],
      insufficient_permissions: ['安装权限不足', '需要代码读取和 Checks 写入权限。'],
      archived: ['仓库已归档', '取消归档并刷新后才能运行。'],
      disabled: ['GitHub 已禁用仓库', '请先在 GitHub 恢复仓库。'],
      renamed: [
        '仓库已改名',
        `当前名称：${authority.remote_name || '请检查 GitHub'}。请更新仓库、workflow 和发布目标绑定。`,
      ],
    }
    const [label, detail] = labels[authority.state]
    return {
      key:
        authority.state === 'authorized'
          ? 'ready'
          : authority.state === 'unknown'
            ? 'unknown'
            : 'attention',
      label,
      detail,
      repository:
        authority.state === 'authorized'
          ? catalog?.repositories.find((r) => r.full_name === repo.name)
          : undefined,
    }
  }
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

export const REPOSITORY_BATCH = 20

// The picker always lists the account, with search and owner as filters.
// Rendering is windowed so a large account can load more on scroll.
export function repositoryList<T extends GitHubRepository>(
  repos: T[],
  search: string,
  owner: string,
) {
  const text = search.trim().toLowerCase()
  return repos
    .filter(
      (r) =>
        (owner === 'all' || r.full_name.split('/')[0] === owner) &&
        (!text || r.full_name.toLowerCase().includes(text)),
    )
    .sort((a, b) => a.full_name.localeCompare(b.full_name))
}
