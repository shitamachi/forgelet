import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Plus, Search, X } from 'lucide-react'
import { queries } from '../lib/queries'
import { filterRuns } from '../lib/model'
import { RunTable } from '../components/run-table'
import { Empty, ErrorPanel, Loading, PageHeader } from '../components/ui'
export default function Runs() {
  const search = useSearch({ from: '/runs' })
  const navigate = useNavigate({ from: '/runs' })
  const runs = useQuery(queries.runs)
  const config = useQuery(queries.config)
  function update(values: Partial<typeof search>) {
    void navigate({ search: { ...search, ...values }, replace: true })
  }
  const shown = filterRuns(runs.data ?? [], search.q, search.state, search.repo)
  const repositories = [
    ...new Set([
      ...(config.data?.repositories ?? []),
      ...(runs.data?.map((r) => r.repository) ?? []),
    ]),
  ]
  return (
    <>
      <PageHeader
        title="运行记录"
        description="跟踪每次提交，查看任务、日志与产物。"
        actions={
          <Link to="/runs/new" className="button button-primary">
            <Plus size={16} />
            手动运行
          </Link>
        }
      />
      <section className="panel">
        <div className="filter-bar">
          <label className="search-field">
            <Search size={17} />
            <input
              aria-label="搜索运行"
              placeholder="搜索 workflow、仓库、分支或 SHA"
              value={search.q}
              onChange={(e) => update({ q: e.target.value })}
            />
            {search.q && (
              <button aria-label="清空搜索" onClick={() => update({ q: '' })}>
                <X size={15} />
              </button>
            )}
          </label>
          <select
            aria-label="筛选状态"
            value={search.state}
            onChange={(e) => update({ state: e.target.value })}
          >
            <option value="all">全部状态</option>
            {Object.entries({
              queued: '排队中',
              running: '运行中',
              starting: '启动中',
              success: '成功',
              failure: '失败',
              cancelled: '已取消',
              timed_out: '已超时',
              interrupted: '已中断',
              skipped: '已跳过',
            }).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
          <select
            aria-label="筛选仓库"
            value={search.repo}
            onChange={(e) => update({ repo: e.target.value })}
          >
            <option value="all">全部仓库</option>
            {repositories.map((repo) => (
              <option key={repo}>{repo}</option>
            ))}
          </select>
        </div>
        {runs.isPending ? (
          <Loading />
        ) : runs.isError ? (
          <ErrorPanel error={runs.error} retry={() => void runs.refetch()} />
        ) : shown.length || !runs.data.length ? (
          <RunTable runs={shown} />
        ) : (
          <Empty
            title="没有匹配的运行"
            action={
              <button
                className="button"
                onClick={() => update({ q: '', state: 'all', repo: 'all' })}
              >
                清除筛选
              </button>
            }
          >
            试试其他关键词或状态。
          </Empty>
        )}
        <div className="table-footer">
          显示 {shown.length} 条 / 最近 {runs.data?.length ?? 0} 条记录
          <span>最多显示最近 100 次运行</span>
        </div>
      </section>
    </>
  )
}
