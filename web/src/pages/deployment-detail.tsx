import { useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  ArrowLeft,
  ArrowUpRight,
  Check,
  Circle,
  GitCommitHorizontal,
  RotateCcw,
} from 'lucide-react'
import { api } from '../lib/api'
import { queries, queryClient } from '../lib/queries'
import { date, shortSHA } from '../lib/model'
import { Confirm, ErrorPanel, Loading, PageHeader, Status, useToast } from '../components/ui'
export default function DeploymentDetail() {
  const { deploymentId } = useParams({ from: '/deployments/$deploymentId' })
  const record = useQuery(queries.delivery(deploymentId))
  const navigate = useNavigate()
  const [confirm, setConfirm] = useState(false)
  const toast = useToast()
  if (record.isPending) return <Loading />
  if (record.isError) return <ErrorPanel error={record.error} retry={() => void record.refetch()} />
  const d = record.data
  const canRollback = Boolean(d.previous_digest && ['delivered', 'deployed'].includes(d.state))
  const stages = [
    { name: '运行成功', done: !['queued', 'cancelled'].includes(d.state) },
    { name: d.target.mode === 'pr' ? 'GitOps 合并' : 'GitOps 提交', done: Boolean(d.revision) },
    {
      name: 'Flux 已应用',
      done: Boolean(d.observed_revision && d.observed_revision === d.revision),
    },
    { name: '应用就绪', done: d.state === 'deployed' },
  ]
  async function rollback() {
    const result = await api.rollback(deploymentId)
    toast('已创建回滚发布')
    await queryClient.invalidateQueries({ queryKey: ['deployments'] })
    await navigate({ to: '/deployments/$deploymentId', params: { deploymentId: result.id } })
  }
  return (
    <>
      <Link to="/deployments" className="back-link">
        <ArrowLeft size={15} />
        发布记录
      </Link>
      <PageHeader
        title={`${d.target.name}${d.rollback_of ? ' / 回滚' : ''}`}
        description={`发布 #${d.id.slice(0, 8)} · ${date(d.created_at)}`}
        actions={
          <button
            className="button"
            disabled={!canRollback}
            title={canRollback ? '创建新的 GitOps 回滚提交' : '当前记录没有可回滚的已发布版本'}
            onClick={() => setConfirm(true)}
          >
            <RotateCcw size={16} />
            回滚到上一版本
          </button>
        }
      />
      <div className="run-meta">
        <Status state={d.state} />
        <Link to="/runs/$runId" params={{ runId: d.run_id }}>
          来源运行 #{d.run_id.slice(0, 8)}
          <ArrowUpRight size={14} />
        </Link>
        <span>最后更新 {date(d.updated_at)}</span>
      </div>
      {d.error && (
        <div className="alert alert-warning" role="alert">
          <div>
            <strong>发布仍需处理</strong>
            <p>{d.error}</p>
          </div>
        </div>
      )}
      <section className="panel delivery-progress">
        <h2>发布进度</h2>
        <ol>
          {stages.map((s) => (
            <li key={s.name} className={s.done ? 'stage-complete' : ''}>
              <span>{s.done ? <Check size={18} /> : <Circle size={16} />}</span>
              <strong>{s.name}</strong>
            </li>
          ))}
        </ol>
        {d.state === 'awaiting_merge' && (
          <p className="section-note">等待 GitOps pull request 合并，合并后自动跟踪 Flux 部署。</p>
        )}
        {d.state === 'delivered' && (
          <p className="section-note">
            GitOps 已提交；应用镜像、revision 和就绪副本全部吻合后才会显示“已部署”。
          </p>
        )}
      </section>
      <div className="detail-columns">
        <section className="panel detail-panel">
          <h2>镜像与目标</h2>
          <dl>
            <dt>目标</dt>
            <dd>
              {d.target.namespace} / {d.target.deployment}
            </dd>
            <dt>容器</dt>
            <dd>{d.target.container}</dd>
            <dt>镜像</dt>
            <dd className="break-all">{d.target.image}</dd>
            <dt>当前 digest</dt>
            <dd>
              <code className="break-all">{d.digest}</code>
            </dd>
            <dt>上一 digest</dt>
            <dd>
              <code className="break-all">{d.previous_digest || '暂无'}</code>
            </dd>
            <dt>Flux Kustomization</dt>
            <dd>
              {d.target.flux_namespace} / {d.target.kustomization}
            </dd>
          </dl>
        </section>
        <section className="panel detail-panel">
          <h2>GitOps</h2>
          <dl>
            <dt>仓库</dt>
            <dd>
              <a
                href={`https://github.com/${d.target.repository}`}
                target="_blank"
                rel="noreferrer"
              >
                {d.target.repository}
                <ArrowUpRight size={13} />
              </a>
            </dd>
            <dt>分支</dt>
            <dd>{d.target.branch}</dd>
            <dt>文件</dt>
            <dd className="break-all">{d.target.path}</dd>
            <dt>提交</dt>
            <dd>
              {d.commit ? (
                <a
                  href={`https://github.com/${d.target.repository}/commit/${d.commit}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  <GitCommitHorizontal size={15} />
                  <code>{shortSHA(d.commit)}</code>
                </a>
              ) : (
                '等待创建'
              )}
            </dd>
            <dt>目标 revision</dt>
            <dd>
              <code className="break-all">{d.revision || '等待发布'}</code>
            </dd>
            <dt>已观测 revision</dt>
            <dd>
              <code className="break-all">{d.observed_revision || '等待集群反馈'}</code>
            </dd>
            {d.url && /^https:\/\//.test(d.url) && (
              <>
                <dt>Pull request</dt>
                <dd>
                  <a href={d.url} target="_blank" rel="noreferrer">
                    打开 GitHub
                    <ArrowUpRight size={13} />
                  </a>
                </dd>
              </>
            )}
          </dl>
        </section>
      </div>
      {confirm && (
        <Confirm
          title="回滚到上一版本？"
          label="创建回滚发布"
          onConfirm={rollback}
          onClose={() => setConfirm(false)}
        >
          <p>
            将为 <strong>{d.target.name}</strong> 创建新的 GitOps 提交，只恢复上一镜像
            digest。发布结果仍由 Flux 和应用就绪状态确认。
          </p>
          <code className="confirm-id">{d.previous_digest}</code>
        </Confirm>
      )}
    </>
  )
}
