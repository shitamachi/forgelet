import type { Job } from '../lib/types'
import { elapsed, isActive, jobStage } from '../lib/model'

const phases: Record<string, string> = {
  preparing: '镜像与工具准备',
  executing: '步骤执行',
  cleanup: '收尾',
}
export default function JobProgress({ job, now = Date.now() }: { job: Job; now?: number }) {
  if (job.plan.control) return <p className="step-note">这是工作流调度节点，不创建执行 Pod。</p>
  const end = isActive(job.state) ? undefined : job.updated_at
  return (
    <section className="execution-progress" aria-label="执行时间线">
      <p className="execution-current" role="status">
        {jobStage(job)}
      </p>
      <dl className="execution-timeline">
        <div>
          <dt>等待调度</dt>
          <dd>
            {job.reserved_at
              ? elapsed(job.created_at, job.reserved_at, now)
              : job.state === 'queued'
                ? elapsed(job.created_at, undefined, now)
                : '—'}
          </dd>
        </div>
        {job.reserved_at && (
          <div>
            <dt>Kubernetes 启动</dt>
            <dd>{elapsed(job.reserved_at, job.claimed_at ?? end, now)}</dd>
          </div>
        )}
        {job.progress?.phases?.map((phase) => (
          <div key={phase.name} aria-current={!phase.finished_at && !end ? 'step' : undefined}>
            <dt>{phases[phase.name] ?? phase.name}</dt>
            <dd>{elapsed(phase.started_at, phase.finished_at ?? end, now)}</dd>
          </div>
        ))}
      </dl>
      {!job.progress && !isActive(job.state) && (
        <p className="field-hint">此历史任务没有分阶段计时。</p>
      )}
      {!!job.progress?.annotations?.length && (
        <details className="execution-annotations" open>
          <summary>执行提示（{job.progress.annotations.length}）</summary>
          <ul>
            {job.progress.annotations.map((item, i) => (
              <li key={i} className={`annotation-${item.level}`}>
                <strong>
                  {item.level === 'error' ? '错误提示' : item.level === 'warning' ? '警告' : '提示'}
                  {item.step_id ? ` · ${item.step_id}` : ''}
                </strong>
                <p>{item.message}</p>
              </li>
            ))}
          </ul>
          <p className="field-hint">
            提示不改变任务结论。非阻断步骤结束不代表第三方服务已接收结果。
          </p>
        </details>
      )}
    </section>
  )
}
