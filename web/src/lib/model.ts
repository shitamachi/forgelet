import type { Job, LogChunk, Run, RunState } from './types'
export const isActive = (state: string) => ['queued', 'starting', 'running'].includes(state)
export const shortSHA = (sha?: string) => (sha ? sha.replace(/^sha256:/, '').slice(0, 8) : '—')
export const branch = (ref: string) => ref.replace(/^refs\/(heads|tags)\//, '')
export const workflowName = (run: Run) =>
  run.jobs?.find((j) => j.plan.workflow_name)?.plan.workflow_name ||
  run.workflow_path.split('/').pop() ||
  'Workflow'
export function bytes(value?: number): string {
  if (value === undefined || !Number.isFinite(value)) return '待检测'
  if (value < 1024) return `${value} B`
  const unit = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 4)
  return `${(value / 1024 ** unit).toLocaleString('zh-CN', { maximumFractionDigits: 1 })} ${['B', 'KiB', 'MiB', 'GiB', 'TiB'][unit]}`
}
export function date(value: string): string {
  return new Date(value).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}
export function duration(run: Run, now = Date.now()): string {
  const end = isActive(run.state)
    ? now
    : Math.max(...(run.jobs ?? []).map((j) => Date.parse(j.updated_at)), Date.parse(run.created_at))
  const seconds = Math.max(0, Math.floor((end - Date.parse(run.created_at)) / 1000))
  return seconds >= 3600
    ? `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
    : seconds >= 60
      ? `${Math.floor(seconds / 60)}m ${seconds % 60}s`
      : `${seconds}s`
}
export function filterRuns(runs: Run[], search: string, state: string, repository: string): Run[] {
  const text = search.trim().toLocaleLowerCase()
  return runs.filter(
    (r) =>
      (!state || state === 'all' || r.state === state) &&
      (!repository || repository === 'all' || r.repository === repository) &&
      (!text ||
        [r.id, r.repository, r.sha, branch(r.ref), workflowName(r)].some((v) =>
          v.toLocaleLowerCase().includes(text),
        )),
  )
}
export function jobLayers(jobs: Job[]): Job[][] {
  const levels = new Map<string, number>()
  const remaining = [...jobs]
  for (let pass = 0; remaining.length && pass <= jobs.length; pass++) {
    let progress = false
    for (let i = remaining.length - 1; i >= 0; i--) {
      const job = remaining[i]
      const needs = jobs.filter((j) => (job.plan.needs ?? []).includes(j.plan.group || j.plan.key))
      if (needs.every((j) => levels.has(j.id))) {
        levels.set(job.id, Math.max(-1, ...needs.map((j) => levels.get(j.id)!)) + 1)
        remaining.splice(i, 1)
        progress = true
      }
    }
    if (!progress) break
  }
  const result: Job[][] = []
  for (const job of jobs) {
    const level = levels.get(job.id) ?? 0
    ;(result[level] ??= []).push(job)
  }
  return result
}
export function stepState(job: Job, stepID: string): RunState | 'pending' {
  const final = job.result?.steps?.find((s) => s.id === stepID)?.state
  if (final) return final
  const observed = job.progress?.steps?.find((s) => s.id === stepID)?.state
  if (!observed) return 'pending'
  if (observed === 'running' && !isActive(job.state)) {
    return job.state === 'success' ? 'pending' : job.state
  }
  return observed
}
export function elapsed(start?: string, end?: string, now = Date.now()): string {
  if (!start) return '—'
  const from = Date.parse(start),
    to = end ? Date.parse(end) : now
  if (!Number.isFinite(from) || !Number.isFinite(to) || to < from) return '—'
  const seconds = Math.floor((to - from) / 1000)
  return seconds >= 60 ? `${Math.floor(seconds / 60)}m ${seconds % 60}s` : `${seconds}s`
}
export function jobStage(job: Job): string {
  if (job.state === 'queued') return '等待调度'
  if (job.state === 'starting') return job.pod_uid ? '等待 Kubernetes 启动' : '正在创建执行环境'
  if (!isActive(job.state)) return '执行已结束'
  return (
    (
      { preparing: '正在准备镜像与工具', executing: '正在执行步骤', cleanup: '正在收尾' } as Record<
        string,
        string
      >
    )[job.progress?.phase ?? ''] ?? '执行器已启动，等待进度'
  )
}

export function mergeLogs(previous: LogChunk[], incoming: LogChunk[]): LogChunk[] {
  const cursor = previous.at(-1)?.seq ?? 0
  return [...previous, ...incoming.filter((x) => x.seq > cursor)].sort((a, b) => a.seq - b.seq)
}
// Logs are rendered as text, never HTML. Remove terminal control sequences.
export function logText(chunks: LogChunk[]): string {
  return chunks
    .map((c) => c.data)
    .join('')
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '')
    .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, '')
}
