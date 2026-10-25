import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Plus,
  RefreshCw,
  GitBranch,
  ExternalLink,
  Search,
  CircleCheck,
  CircleAlert,
  Pause,
  Play,
  Settings2,
  LockKeyhole,
  Globe2,
} from 'lucide-react'
import { api } from '../lib/api'
import { authQuery, queries, queryClient } from '../lib/queries'
import type { Management, GitHubRepository, RepositoryAuthorization } from '../lib/types'
import { REPOSITORY_BATCH, repositoryList, repositoryState } from '../lib/repository-state'
import { Dialog, Empty, ErrorPanel, Loading, PageHeader, Status, useToast } from '../components/ui'
import FirstRun from './github-onboarding'

type Props = {
  doc: Management
  reload: () => void
  edit: (index: number) => void
  remove: (index: number) => void
  inspect: (name: string) => void
  save: (doc: Management) => Promise<void>
}
const pendingKey = 'forgelet-pending-repository'
function pendingRepository() {
  try {
    return Number(sessionStorage.getItem(pendingKey)) || null
  } catch {
    return null
  }
}
export default function RepositoryConnections({ doc, reload, edit, remove, inspect, save }: Props) {
  const session = useQuery(authQuery)
  const connection = useQuery({
    queryKey: ['github-connection'],
    queryFn: ({ signal }) => api.githubConnection(signal),
    retry: false,
    staleTime: 60_000,
  })
  const runs = useQuery(queries.runs)
  const connected =
    session.data?.mode === 'session' &&
    !!session.data.user?.github_id &&
    !!session.data.github_enabled
  const [pendingID, setPendingID] = useState<number | null>(pendingRepository)
  const [adding, setAdding] = useState(() => pendingRepository() !== null)
  const [search, setSearch] = useState('')
  const [owner, setOwner] = useState('all')
  const [visibleCount, setVisibleCount] = useState(REPOSITORY_BATCH)
  const [filter, setFilter] = useState('all')
  const [listSearch, setListSearch] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<unknown>()
  const [operationError, setOperationError] = useState<unknown>()
  const [selected, setSelected] = useState<GitHubRepository | null>(null)
  const [authorization, setAuthorization] = useState<RepositoryAuthorization | null>(null)
  const [firstRun, setFirstRun] = useState<GitHubRepository | null>(null)
  const toast = useToast()
  const userRepos = useQuery({
    queryKey: ['github-user-repositories'],
    queryFn: ({ signal }) => api.githubUserRepositories(signal),
    enabled: !!connected && adding,
    retry: false,
    staleTime: 60_000,
  })
  const catalog = connection.data
  const repos = connected ? userRepos.data || [] : catalog?.repositories || []
  const choice = selected || repos.find((r) => r.id === pendingID) || null
  const matches = repositoryList(repos, search, owner)
  const shownRepos = matches.slice(0, visibleCount)
  const hasMore = shownRepos.length < matches.length
  const owners = [...new Set(repos.map((r) => r.full_name.split('/')[0]))].sort()
  const rows = doc.repositories.map((repo, index) => ({
    repo,
    index,
    access: repositoryState(repo, catalog, connection.isError),
  }))
  const attention = rows.filter((r) => r.access.key === 'attention').length
  const visible = rows.filter(
    ({ repo, access }) =>
      repo.name.toLowerCase().includes(listSearch.toLowerCase()) &&
      (filter === 'all' ||
        (filter === 'enabled' && repo.enabled) ||
        (filter === 'paused' && !repo.enabled) ||
        (filter === 'attention' && access.key === 'attention')),
  )
  function remember(id: number | null) {
    setPendingID(id)
    try {
      if (id) sessionStorage.setItem(pendingKey, String(id))
      else sessionStorage.removeItem(pendingKey)
    } catch {
      /* Storage is optional; server always rechecks authority. */
    }
  }
  function closeAdd() {
    if (busy) return
    setAdding(false)
    setSelected(null)
    setAuthorization(null)
    setError(undefined)
    setSearch('')
    setOwner('all')
    setVisibleCount(REPOSITORY_BATCH)
    remember(null)
  }
  function openAdd(repo?: GitHubRepository) {
    setSelected(repo || null)
    setAdding(true)
    setError(undefined)
    setSearch('')
    setOwner('all')
    setVisibleCount(REPOSITORY_BATCH)
  }
  function resetList() {
    setVisibleCount(REPOSITORY_BATCH)
  }
  function loadMore() {
    setVisibleCount((count) => Math.min(count + REPOSITORY_BATCH, matches.length))
  }
  async function refresh() {
    reload()
    await connection.refetch()
    if (adding && connected) await userRepos.refetch()
  }
  async function enable(repo: GitHubRepository) {
    setBusy(repo.full_name)
    setError(undefined)
    try {
      const next = await (connected
        ? api.githubUserEnable(repo.id, doc.version)
        : api.githubEnable(repo.id, doc.version))
      if ('status' in next) {
        setAuthorization(next)
        remember(repo.id)
        return
      }
      queryClient.setQueryData(['management'], next)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['config'] }),
        queryClient.invalidateQueries({ queryKey: ['github-connection'] }),
      ])
      remember(null)
      setAuthorization(null)
      setSelected(null)
      setAdding(false)
      setFirstRun(repo)
      toast('仓库已启用，可以检查 workflow')
    } catch (e) {
      setError(e)
    } finally {
      setBusy(null)
    }
  }
  async function toggle(index: number) {
    const next = structuredClone(doc)
    const repo = next.repositories[index]
    setBusy(repo.name)
    setOperationError(undefined)
    try {
      if (repo.enabled) {
        repo.enabled = false
        await save(next)
      } else if (repo.repository_id) {
        const updated = await api.githubEnable(repo.repository_id, doc.version)
        queryClient.setQueryData(['management'], updated)
        await queryClient.invalidateQueries({ queryKey: ['config'] })
        toast('CI 已启用')
      }
    } catch (e) {
      setOperationError(e)
    } finally {
      setBusy(null)
    }
  }
  return (
    <>
      <PageHeader
        title="仓库接入"
        description="管理已接入仓库，确认授权和 CI 状态。"
        actions={
          <>
            <button
              className="button"
              onClick={() => void refresh()}
              disabled={connection.isFetching}
            >
              <RefreshCw size={15} />
              重新加载
            </button>
            <button className="button button-primary" onClick={() => openAdd()}>
              <Plus size={16} />
              添加仓库
            </button>
          </>
        }
      />
      <section className="repository-source" aria-label="GitHub 连接">
        <div className="repository-source-icon">
          <GitBranch size={23} />
        </div>
        <div className="repository-source-copy">
          <strong>GitHub</strong>
          <span>
            {connected
              ? `已关联 @${session.data?.user?.github_login}`
              : '使用 GitHub App 接收事件和执行 CI'}
          </span>
        </div>
        <span
          className={`repository-badge ${connection.isError ? 'tone-warning' : catalog?.installations.some((i) => !i.suspended_at) ? 'tone-ready' : 'tone-neutral'}`}
        >
          <CircleCheck size={14} />
          {connection.isError
            ? '连接检查失败'
            : connection.isPending
              ? '检查连接中'
              : catalog?.installations.some((i) => !i.suspended_at)
                ? 'App 已连接'
                : '待安装 App'}
        </span>
        <details className="repository-source-settings">
          <summary>连接设置</summary>
          <div>
            {session.data?.mode === 'session' && (
              <Link to="/account">{connected ? '账号与重新授权' : '连接 GitHub 账号'}</Link>
            )}
            {catalog && (
              <a href={catalog.install_url} target="_blank" rel="noreferrer">
                安装到其他账号 <ExternalLink size={13} />
              </a>
            )}
            {catalog?.installations.map((i) => (
              <a key={i.id} href={i.manage_url} target="_blank" rel="noreferrer">
                {i.account.login} 的授权设置 <ExternalLink size={13} />
              </a>
            ))}
          </div>
        </details>
      </section>
      {connection.isError && (
        <ErrorPanel
          error={new Error('暂时无法检查 GitHub 授权。已保存的仓库和启用设置保留，请重新加载。')}
          retry={() => void connection.refetch()}
        />
      )}
      {operationError != null && <ErrorPanel error={operationError} />}
      <section className="repository-workspace" aria-label="已接入仓库">
        <div className="repository-toolbar">
          <div className="repository-tabs" role="group" aria-label="筛选接入状态">
            {[
              ['all', '全部', rows.length],
              ['enabled', 'CI 已启用', rows.filter((r) => r.repo.enabled).length],
              ['paused', '已停用', rows.filter((r) => !r.repo.enabled).length],
              ['attention', '需要处理', attention],
            ].map(([key, label, count]) => (
              <button
                key={key}
                aria-pressed={filter === key}
                onClick={() => setFilter(String(key))}
              >
                {label}
                <span>{count}</span>
              </button>
            ))}
          </div>
          <label className="repository-search">
            <Search size={16} />
            <input
              aria-label="搜索已接入仓库"
              placeholder="搜索已接入仓库"
              value={listSearch}
              onChange={(e) => setListSearch(e.target.value)}
            />
          </label>
        </div>
        <div className="repository-list-heading">
          <h2>已接入仓库</h2>
          <span>{visible.length} 个仓库</span>
        </div>
        {!visible.length ? (
          <Empty title={rows.length ? '没有匹配的仓库' : '从第一个仓库开始'}>
            {rows.length ? '调整搜索内容或接入状态筛选。' : '选择 GitHub 仓库，完成授权后启用 CI。'}
            {!rows.length && (
              <button className="button button-primary" onClick={() => openAdd()}>
                添加仓库
              </button>
            )}
          </Empty>
        ) : (
          <div className="repository-list">
            {visible.map(({ repo, index, access }) => {
              const recent = runs.data?.find((r) => r.repository === repo.name)
              const workflowCount = doc.workflows.filter(
                (w) => w.repository === repo.name && w.enabled,
              ).length
              return (
                <article className="repository-row" key={repo.name} aria-label={repo.name}>
                  <div className="repository-row-title">
                    <div className="repository-mark">
                      <GitBranch size={19} />
                    </div>
                    <div>
                      <a href={`https://github.com/${repo.name}`} target="_blank" rel="noreferrer">
                        <strong>{repo.name}</strong>
                        <ExternalLink size={12} />
                      </a>
                      <p>
                        {workflowCount
                          ? `${workflowCount} 个已配置 workflow`
                          : '自动发现 Git 中的 workflow'}
                        {access.repository?.default_branch
                          ? ` · ${access.repository.default_branch}`
                          : ''}
                      </p>
                    </div>
                  </div>
                  <div className="repository-row-state">
                    <span
                      className={`repository-badge ${repo.enabled ? 'tone-ready' : 'tone-neutral'}`}
                    >
                      {repo.enabled ? <CircleCheck size={14} /> : <Pause size={14} />}
                      {repo.enabled ? 'CI 已启用' : 'CI 已停用'}
                    </span>
                    <span
                      title={access.detail}
                      className={`repository-access ${access.key === 'ready' ? '' : 'attention-text'}`}
                    >
                      {access.label}
                    </span>
                  </div>
                  <div className="repository-row-run">
                    <span>最近运行</span>
                    {runs.isError ? (
                      <span>暂时无法读取</span>
                    ) : recent ? (
                      <Link to="/runs/$runId" params={{ runId: recent.id }}>
                        <Status state={recent.state} small />
                        <small>{recent.sha.slice(0, 7)}</small>
                      </Link>
                    ) : (
                      <span>暂无近期运行</span>
                    )}
                  </div>
                  <div className="repository-row-actions">
                    {access.repository && repo.enabled ? (
                      <button
                        className="button button-small"
                        onClick={() => setFirstRun(access.repository!)}
                      >
                        检查与运行
                      </button>
                    ) : access.key === 'attention' ? (
                      <button
                        className="button button-small"
                        onClick={() => openAdd(repos.find((r) => r.id === repo.repository_id))}
                      >
                        处理授权
                      </button>
                    ) : null}
                    <details className="repository-menu">
                      <summary aria-label={`管理 ${repo.name}`}>
                        <Settings2 size={17} />
                      </summary>
                      <div>
                        <button
                          onClick={() => void toggle(index)}
                          disabled={!!busy || (!repo.enabled && access.key !== 'ready')}
                        >
                          {repo.enabled ? <Pause size={14} /> : <Play size={14} />}
                          {repo.enabled ? '停用 CI' : '启用 CI'}
                        </button>
                        <button onClick={() => inspect(repo.name)}>检查接入</button>
                        <button onClick={() => edit(index)}>编辑仓库配置</button>
                        <button className="danger-text" onClick={() => remove(index)}>
                          移除仓库
                        </button>
                      </div>
                    </details>
                  </div>
                </article>
              )
            })}
          </div>
        )}
        <footer className="repository-footer">
          <span>
            {catalog
              ? `授权检查于 ${new Date(catalog.checked_at).toLocaleTimeString()}`
              : '等待授权检查'}
            ；启用和运行前再次核验。
          </span>
          <button onClick={() => edit(-1)}>手动登记（高级）</button>
        </footer>
      </section>
      {adding && (
        <Dialog title="添加 GitHub 仓库" onClose={closeAdd}>
          <div className="dialog-body repository-onboarding-picker" aria-label="GitHub 自动接入">
            <p className="section-note">
              默认展示全部可发现仓库，滚动列表或点击“加载更多”继续；GitHub 登录用于发现仓库， 执行
              CI 还需要将仓库授权给 GitHub App。
            </p>
            {connected && userRepos.isPending ? (
              <Loading label="正在读取账号仓库…" />
            ) : userRepos.isError ? (
              <ErrorPanel error={userRepos.error} retry={() => void userRepos.refetch()} />
            ) : !choice ? (
              <>
                <div className="repository-picker-search">
                  <label className="repository-search">
                    <Search size={18} />
                    <input
                      autoFocus
                      aria-label="搜索 GitHub 仓库"
                      placeholder="输入仓库名，例如 my-app"
                      value={search}
                      onChange={(e) => {
                        setSearch(e.target.value)
                        resetList()
                      }}
                    />
                  </label>
                  <select
                    aria-label="筛选仓库所属账号"
                    value={owner}
                    onChange={(e) => {
                      setOwner(e.target.value)
                      resetList()
                    }}
                  >
                    <option value="all">所有账号</option>
                    {owners.map((o) => (
                      <option key={o}>{o}</option>
                    ))}
                  </select>
                </div>
                {!matches.length ? (
                  <Empty title={search.trim() ? '没有找到匹配仓库' : '账号下没有可接入的仓库'}>
                    {search.trim()
                      ? '检查名称，或在连接设置中确认 GitHub 账号和 App 授权。'
                      : '在 GitHub App 的安装设置中授权仓库后，重新打开此窗口。'}
                  </Empty>
                ) : (
                  <>
                    <div
                      className="repository-results"
                      role="list"
                      aria-label="仓库列表"
                      tabIndex={0}
                      onScroll={(event) => {
                        const list = event.currentTarget
                        if (hasMore && list.scrollHeight - list.scrollTop - list.clientHeight < 160)
                          loadMore()
                      }}
                    >
                      {shownRepos.map((repo) => {
                        const registered = doc.repositories.find((r) => r.repository_id === repo.id)
                        const unavailable =
                          repo.archived ||
                          repo.disabled ||
                          ('permissions' in repo &&
                            !(repo as import('../lib/types').GitHubUserRepository).permissions
                              .admin)
                        return (
                          <div role="listitem" className="repository-result" key={repo.id}>
                            <div>
                              <strong>{repo.full_name}</strong>
                              <p>
                                {repo.private ? <LockKeyhole size={12} /> : <Globe2 size={12} />}
                                {repo.private ? '私有' : '公开'}
                                <span>{repo.default_branch || '空仓库'}</span>
                              </p>
                            </div>
                            <button
                              className="button button-small"
                              disabled={!!busy || unavailable}
                              onClick={() => {
                                setSelected(repo)
                                setAuthorization(null)
                                setError(undefined)
                              }}
                            >
                              {unavailable
                                ? repo.archived || repo.disabled
                                  ? '仓库不可用'
                                  : '需要管理员权限'
                                : registered
                                  ? '查看接入'
                                  : '选择仓库'}
                            </button>
                          </div>
                        )
                      })}
                    </div>
                    <div className="repository-list-footer">
                      <span>
                        {search.trim() ? `${matches.length} 个匹配` : `${matches.length} 个仓库`}
                        {' · '}已显示 {shownRepos.length}
                      </span>
                      {hasMore ? (
                        <button type="button" className="button button-small" onClick={loadMore}>
                          加载更多仓库
                        </button>
                      ) : (
                        <span>已全部显示</span>
                      )}
                    </div>
                  </>
                )}
              </>
            ) : (
              <section className="repository-selection">
                <button
                  className="text-button"
                  disabled={!!busy}
                  onClick={() => {
                    setSelected(null)
                    setAuthorization(null)
                    setError(undefined)
                    resetList()
                    remember(null)
                  }}
                >
                  重新选择仓库
                </button>
                <h3>
                  <GitBranch size={19} />
                  {choice.full_name}
                </h3>
                {authorization ? (
                  <div className="repository-consent" role="status">
                    <CircleAlert size={20} />
                    <div>
                      <strong>
                        {authorization.reason === 'installation_suspended'
                          ? 'GitHub App 安装已暂停'
                          : authorization.reason === 'installation_permissions'
                            ? '需要更新 App 权限'
                            : '等待 GitHub 授权'}
                      </strong>
                      <p>
                        {authorization.reason === 'installation_required'
                          ? '在此账号安装 Forgelet App，并选择这个仓库。'
                          : `在 GitHub 中将 ${choice.full_name} 加入 Forgelet App 的仓库授权范围。完成后返回这里检查。`}
                      </p>
                      <a
                        className="button"
                        href={authorization.authorize_url}
                        target="_blank"
                        rel="noreferrer"
                      >
                        前往 GitHub 授权
                        <ExternalLink size={14} />
                      </a>
                    </div>
                  </div>
                ) : (
                  <p>
                    启用后接收此仓库的事件。授权尚未完成时会先引导你前往
                    GitHub，确认前不会启动任务。
                  </p>
                )}
                {error != null && <ErrorPanel error={error} />}
                <button
                  className="button button-primary"
                  disabled={!!busy || connection.isError || !catalog}
                  onClick={() => void enable(choice)}
                >
                  {busy ? '正在检查授权…' : authorization ? '检查授权并启用' : '启用仓库'}
                </button>
              </section>
            )}
          </div>
        </Dialog>
      )}
      {firstRun && <FirstRun repository={firstRun} close={() => setFirstRun(null)} />}
    </>
  )
}
