import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  ArrowRight,
  CheckCheck,
  CirclePlay,
  GitBranch,
  Plus,
  Rocket,
  TriangleAlert,
} from 'lucide-react'
import { queries } from '../lib/queries'
import { isActive, date, shortSHA } from '../lib/model'
import { RunTable } from '../components/run-table'
import { Empty, ErrorPanel, Loading, PageHeader, Status } from '../components/ui'

export default function Overview() {
  const runs = useQuery(queries.runs)
  const deployments = useQuery(queries.deliveries)
  const events = useQuery(queries.events)
  const config = useQuery(queries.config)
  if (runs.isPending) return <Loading />
  if (runs.isError) return <ErrorPanel error={runs.error} retry={() => void runs.refetch()} />
  const active = runs.data.filter((r) => isActive(r.state))
  const complete = runs.data.filter((r) => !isActive(r.state))
  const success = complete.filter((r) => r.state === 'success').length
  const latest = [
    ...new Map(
      [...(deployments.data ?? [])]
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
        .map((d) => [d.target.name, d] as const)
        .reverse(),
    ).values(),
  ]
  const problems = events.data?.filter((e) => e.error) ?? []
  return (
    <>
      <PageHeader
        title="工作台"
        description="查看 CI 运行、GitOps 发布与集群调度状态。"
        actions={
          <Link to="/runs/new" className="button button-primary">
            <Plus size={16} />
            手动运行
          </Link>
        }
      />
      <section className="summary-strip" aria-label="运行概况">
        <div>
          <span className="summary-icon">
            <CirclePlay size={20} />
          </span>
          <div>
            <span className="summary-value">{active.length}</span>
            <span className="summary-label">进行中的运行</span>
          </div>
        </div>
        <div>
          <span className="summary-icon">
            <CheckCheck size={20} />
          </span>
          <div>
            <span className="summary-value">
              {complete.length ? `${Math.round((success / complete.length) * 100)}%` : '—'}
            </span>
            <span className="summary-label">最近 {complete.length} 次完成的成功率</span>
          </div>
        </div>
        <div>
          <span className="summary-icon">
            <GitBranch size={20} />
          </span>
          <div>
            <span className="summary-value">{config.data?.repositories.length ?? '—'}</span>
            <span className="summary-label">已接入仓库</span>
          </div>
        </div>
      </section>
      {problems.length > 0 && (
        <div role="status" className="alert alert-warning">
          <TriangleAlert size={18} />
          <div>
            <strong>{problems.length} 个事件需要处理</strong>
            <p>
              {problems[0].repository}：{problems[0].error}
            </p>
          </div>
          <Link to="/system" className="text-link">
            查看事件
          </Link>
        </div>
      )}
      <div className="overview-grid">
        <section className="panel">
          <div className="section-header">
            <h2>最近运行</h2>
            <Link to="/runs" search={{ q: '', state: 'all', repo: 'all' }} className="text-link">
              全部运行
              <ArrowRight size={15} />
            </Link>
          </div>
          <RunTable runs={runs.data.slice(0, 8)} />
        </section>
        <section className="panel deployment-summary">
          <div className="section-header">
            <h2>部署目标</h2>
            <Rocket size={18} />
          </div>
          {deployments.isError ? (
            <ErrorPanel error={deployments.error} retry={() => void deployments.refetch()} />
          ) : latest.length ? (
            latest.map((d) => (
              <Link
                key={d.id}
                to="/deployments/$deploymentId"
                params={{ deploymentId: d.id }}
                className="target-summary"
              >
                <div className="split">
                  <strong>{d.target.name}</strong>
                  <Status state={d.state} small />
                </div>
                <p>
                  {d.target.namespace} / {d.target.deployment}
                </p>
                <code>{shortSHA(d.digest)}</code>
                <time>{date(d.updated_at)}</time>
              </Link>
            ))
          ) : (
            <Empty title="还没有发布">成功运行中的 deploy step 会在这里生成发布记录。</Empty>
          )}
          <div className="panel-bottom">
            <Link to="/deployments" className="text-link">
              查看发布记录
              <ArrowRight size={15} />
            </Link>
          </div>
        </section>
      </div>
    </>
  )
}
