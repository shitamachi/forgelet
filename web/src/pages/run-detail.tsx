import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  ArrowLeft,
  ArrowRight,
  Clock3,
  Download,
  GitBranch,
  GitCommitHorizontal,
  Package,
  RotateCcw,
  Square,
} from 'lucide-react'
import { queries, queryClient } from '../lib/queries'
import { api } from '../lib/api'
import {
  branch,
  bytes,
  date,
  duration,
  elapsed,
  isActive,
  jobLayers,
  shortSHA,
  stepState,
  workflowName,
} from '../lib/model'
import { Confirm, Empty, ErrorPanel, Loading, PageHeader, Status, useToast } from '../components/ui'
import JobLogs from '../components/job-logs'
import JobProgress from '../components/job-progress'
export default function RunDetail() {
  const { runId } = useParams({ from: '/runs/$runId' })
  const search = useSearch({ from: '/runs/$runId' })
  const navigate = useNavigate({ from: '/runs/$runId' })
  const record = useQuery(queries.run(runId))
  const config = useQuery(queries.config)
  const [now, setNow] = useState(() => Date.now())
  const active = !!record.data && isActive(record.data.state)
  useEffect(() => {
    if (!active) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [active])
  const artifacts = useQuery({
    // A fast job may finish before the first poll: fetch once more at terminal state.
    queryKey: ['artifacts', runId, record.data?.state],
    queryFn: ({ signal }) => api.artifacts(runId, signal),
    enabled: config.data?.features.artifacts === true,
    refetchInterval: record.data && isActive(record.data.state) ? 5000 : false,
  })
  const deliveries = useQuery(queries.deliveries)
  const [checkError, setCheckError] = useState<unknown>()
  const [retryingChecks, setRetryingChecks] = useState(false)
  const [confirm, setConfirm] = useState<'cancel' | 'rerun' | null>(null)
  const toast = useToast()
  if (record.isPending) return <Loading />
  if (record.isError) return <ErrorPanel error={record.error} retry={() => void record.refetch()} />
  const run = record.data
  const jobs = run.jobs ?? []
  const layers = jobLayers(jobs)
  const selected = jobs.find((j) => j.id === search.job) || layers[0]?.[0]
  async function action() {
    if (confirm === 'cancel') {
      await api.cancel(runId)
      toast('已请求取消，正在终止任务')
      await queryClient.invalidateQueries({ queryKey: ['run', runId] })
    } else {
      const next = await api.rerun(runId)
      toast('已创建新的运行')
      await navigate({ to: '/runs/$runId', params: { runId: next.id }, search: { job: undefined } })
    }
    await queryClient.invalidateQueries({ queryKey: ['runs'] })
  }
  return (
    <>
      <Link to="/runs" search={{ q: '', state: 'all', repo: 'all' }} className="back-link">
        <ArrowLeft size={15} />
        运行记录
      </Link>
      <PageHeader
        title={workflowName(run)}
        description={
          <>
            {run.repository}
            <span className="inline-id">#{run.id.slice(0, 8)}</span>
          </>
        }
        actions={
          <>
            {isActive(run.state) ? (
              <button
                className="button"
                disabled={jobs.some((j) => j.cancel_requested)}
                onClick={() => setConfirm('cancel')}
              >
                <Square size={15} />
                {jobs.some((j) => j.cancel_requested) ? '正在取消…' : '取消运行'}
              </button>
            ) : (
              <button className="button button-primary" onClick={() => setConfirm('rerun')}>
                <RotateCcw size={16} />
                重新运行
              </button>
            )}
          </>
        }
      />
      {jobs.some((j) => j.check_error) && (
        <section className="alert" role="status">
          <div>
            <strong>GitHub Checks 回写未完成</strong>
            <p>执行结果已保存。{jobs.find((j) => j.check_error)?.check_error}</p>
          </div>
          <button
            className="button button-small"
            disabled={retryingChecks}
            onClick={async () => {
              setRetryingChecks(true)
              setCheckError(undefined)
              try {
                await api.retryChecks(runId)
                await record.refetch()
                toast('已请求重新回写 Checks')
              } catch (error) {
                setCheckError(error)
              } finally {
                setRetryingChecks(false)
              }
            }}
          >
            重新回写 Checks
          </button>
        </section>
      )}
      {checkError != null && <ErrorPanel error={checkError} />}
      <div className="run-meta">
        <Status state={run.state} />
        <span>
          <GitBranch size={15} />
          {branch(run.ref)}
        </span>
        <a
          href={`https://github.com/${run.repository}/commit/${run.sha}`}
          target="_blank"
          rel="noreferrer"
        >
          <GitCommitHorizontal size={16} />
          <code>{shortSHA(run.sha)}</code>
        </a>
        <span>
          <Clock3 size={15} />
          {duration(run, now)}
        </span>
        <time>{date(run.created_at)}</time>
      </div>
      <section className="panel graph-panel">
        <div className="section-header">
          <h2>任务依赖</h2>
          <span className="muted">
            {jobs.filter((j) => j.state === 'success').length} / {jobs.length} 成功
          </span>
        </div>
        <div className="job-graph">
          {layers.map((layer, i) => (
            <div className="graph-group" key={i}>
              {i > 0 && <ArrowRight className="graph-arrow" size={18} aria-hidden="true" />}
              <div className="graph-layer">
                {layer.map((job) => (
                  <button
                    key={job.id}
                    className={`graph-job ${selected?.id === job.id ? 'graph-job-selected' : ''}`}
                    aria-pressed={selected?.id === job.id}
                    onClick={() =>
                      void navigate({ search: { job: job.id }, replace: true, resetScroll: false })
                    }
                  >
                    <strong>{job.name}</strong>
                    <Status state={job.state} small />
                    <span>
                      {job.profile}
                      {job.plan.needs?.length ? ` · needs: ${job.plan.needs.join(', ')}` : ''}
                    </span>
                  </button>
                ))}
              </div>
            </div>
          ))}
        </div>
      </section>
      {selected ? (
        <div className="job-detail-grid">
          <aside className="panel step-panel">
            <div className="section-header">
              <h2>{selected.name}</h2>
            </div>
            <div className="job-spec">
              <span>
                执行环境<strong>{selected.profile}</strong>
              </span>
              <span>
                超时<strong>{selected.plan.timeout_seconds / 60} 分钟</strong>
              </span>
            </div>
            <JobProgress job={selected} now={now} />
            <ol className="steps">
              {selected.plan.steps.map((step, i) => (
                <li key={step.id}>
                  <span className="step-number">{i + 1}</span>
                  <div>
                    <strong>
                      {step.name || step.action?.uses || (step.checkout ? 'Checkout' : step.id)}
                    </strong>
                    <Status state={stepState(selected, step.id)} small />
                    {selected.progress?.steps?.find((s) => s.id === step.id)?.started_at && (
                      <span className="step-elapsed">
                        {elapsed(
                          selected.progress.steps.find((s) => s.id === step.id)?.started_at,
                          selected.progress.steps.find((s) => s.id === step.id)?.finished_at ??
                            (!isActive(selected.state) ? selected.updated_at : undefined),
                          now,
                        )}
                      </span>
                    )}
                    {selected.result?.steps?.find((s) => s.id === step.id)?.outcome === 'failure' &&
                      selected.result?.steps?.find((s) => s.id === step.id)?.state ===
                        'success' && <p className="field-hint">失败已忽略（continue-on-error）</p>}
                  </div>
                </li>
              ))}
            </ol>
            {isActive(selected.state) && (
              <p className="step-note">
                {selected.progress
                  ? '步骤进度实时更新；最终结论以任务结果为准。'
                  : '当前执行器未提供实时进度，步骤结果在任务结束后确认。'}
              </p>
            )}
            {selected.cancel_reason && (
              <p className="step-note" role="status">
                取消原因：{selected.cancel_reason}
              </p>
            )}
            {selected.result?.error && (
              <div role="alert" className="step-error">
                {selected.result.error}
              </div>
            )}
            {selected.result?.outputs && Object.keys(selected.result.outputs).length > 0 && (
              <details className="outputs">
                <summary>任务输出</summary>
                <pre>{JSON.stringify(selected.result.outputs, null, 2)}</pre>
              </details>
            )}
          </aside>
          <JobLogs key={selected.id} job={selected} />
        </div>
      ) : (
        <Empty title="这个运行没有任务" />
      )}
      <section className="panel artifacts-panel">
        <div className="section-header">
          <h2>
            <Package size={18} />
            构建产物
          </h2>
        </div>
        {artifacts.isError ? (
          <ErrorPanel error={artifacts.error} retry={() => void artifacts.refetch()} />
        ) : artifacts.data?.length ? (
          <div className="artifact-list">
            {artifacts.data.map((a) => (
              <a key={a.id} href={api.artifactURL(run.id, a.key)} download className="artifact">
                <Package size={21} />
                <div>
                  <strong>{a.key}</strong>
                  <span>
                    {bytes(a.size)} · 到期 {date(a.expires_at)}
                  </span>
                </div>
                <Download size={17} />
              </a>
            ))}
          </div>
        ) : (
          <p className="section-note">
            {config.data?.features.artifacts === false
              ? '此部署尚未启用产物存储。'
              : '没有可下载的产物，或产物已过期。'}
          </p>
        )}
      </section>
      {deliveries.data
        ?.filter((d) => d.run_id === run.id)
        .map((d) => (
          <Link
            key={d.id}
            to="/deployments/$deploymentId"
            params={{ deploymentId: d.id }}
            className="delivery-link"
          >
            <strong>发布到 {d.target.name}</strong>
            <Status state={d.state} />
            <ArrowRight size={17} />
          </Link>
        ))}
      {confirm && (
        <Confirm
          title={confirm === 'cancel' ? '取消这次运行？' : '重新运行这个 workflow？'}
          label={confirm === 'cancel' ? '确认取消运行' : '确认重新运行'}
          onConfirm={action}
          onClose={() => setConfirm(null)}
        >
          <p>
            {confirm === 'cancel'
              ? '排队的任务将取消，运行中的任务会停止。已经产生的外部副作用不会自动撤销。'
              : '将使用原 commit 和执行计划创建新的运行，当前历史记录保留。Secrets 使用当前仓库设置。'}
          </p>
          <code className="confirm-id">{run.id}</code>
        </Confirm>
      )}
    </>
  )
}
