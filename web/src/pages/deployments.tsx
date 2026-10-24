import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { ArrowUpRight, GitBranch, Rocket, Search } from 'lucide-react'
import { queries } from '../lib/queries'
import { date, shortSHA } from '../lib/model'
import { Empty, ErrorPanel, Loading, PageHeader, Status } from '../components/ui'
export default function Deployments() {
  const records = useQuery(queries.deliveries)
  const config = useQuery(queries.config)
  const [search, setSearch] = useState('')
  const [state, setState] = useState('all')
  const shown =
    records.data?.filter(
      (d) =>
        (state === 'all' || d.state === state) &&
        `${d.target.name} ${d.target.repository} ${d.digest} ${d.commit || ''}`
          .toLowerCase()
          .includes(search.toLowerCase()),
    ) ?? []
  return (
    <>
      <PageHeader title="发布" description="关联 GitOps 提交、镜像 digest 与集群实际部署结果。" />
      {config.data?.targets.length ? (
        <div className="targets-strip">
          {config.data.targets.map((t) => (
            <div key={t.name}>
              <Rocket size={17} />
              <div>
                <strong>{t.name}</strong>
                <span>
                  {t.namespace} / {t.deployment}
                </span>
              </div>
              <span className="tag">{t.mode === 'pr' ? 'Pull request' : 'Direct'}</span>
            </div>
          ))}
        </div>
      ) : null}
      <section className="panel">
        <div className="filter-bar">
          <label className="search-field">
            <Search size={17} />
            <input
              aria-label="搜索发布"
              placeholder="搜索目标、仓库、digest 或 commit"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </label>
          <select
            aria-label="筛选发布状态"
            value={state}
            onChange={(e) => setState(e.target.value)}
          >
            <option value="all">全部状态</option>
            {Object.entries({
              queued: '排队中',
              preparing: '准备发布',
              awaiting_merge: '等待合并',
              delivered: '等待部署',
              deployed: '已部署',
              superseded: '已被替代',
              conflict: '发布冲突',
              cancelled: '已取消',
            }).map(([v, name]) => (
              <option key={v} value={v}>
                {name}
              </option>
            ))}
          </select>
        </div>
        {records.isPending ? (
          <Loading />
        ) : records.isError ? (
          <ErrorPanel error={records.error} retry={() => void records.refetch()} />
        ) : !shown.length ? (
          <Empty title={records.data.length ? '没有匹配的发布' : '还没有发布记录'}>
            {config.data?.features.delivery
              ? '在 workflow 中使用 forgelet/deploy@v1，成功运行后会生成发布记录。'
              : '配置 GitOps target 与 Flux 观测后，即可在这里跟踪发布。'}
          </Empty>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>发布目标</th>
                  <th>状态</th>
                  <th>镜像 / GitOps</th>
                  <th>创建时间</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((d) => (
                  <tr key={d.id}>
                    <td>
                      <Link
                        to="/deployments/$deploymentId"
                        params={{ deploymentId: d.id }}
                        className="row-title"
                      >
                        {d.target.name}
                        {d.rollback_of && <span className="tag">回滚</span>}
                      </Link>
                      <div className="row-secondary">
                        {d.target.namespace} / {d.target.deployment}
                      </div>
                    </td>
                    <td>
                      <Status state={d.state} />
                      {d.error && (
                        <div className="row-error" title={d.error}>
                          {d.error}
                        </div>
                      )}
                    </td>
                    <td>
                      <code>{shortSHA(d.digest)}</code>
                      <div className="row-secondary with-icon">
                        <GitBranch size={14} />
                        {d.commit ? (
                          <a
                            href={`https://github.com/${d.target.repository}/commit/${d.commit}`}
                            target="_blank"
                            rel="noreferrer"
                          >
                            {shortSHA(d.commit)}
                            <ArrowUpRight size={12} />
                          </a>
                        ) : (
                          '等待 GitOps 提交'
                        )}
                      </div>
                    </td>
                    <td>
                      {date(d.created_at)}
                      <div className="row-secondary">
                        <code>#{d.id.slice(0, 8)}</code>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
  )
}
