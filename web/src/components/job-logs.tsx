import { useEffect, useRef, useState } from 'react'
import { ChevronLeft, ChevronRight, Download, Search, Terminal } from 'lucide-react'
import { api, APIError } from '../lib/api'
import { isActive, logText, mergeLogs } from '../lib/model'
import type { Job, LogChunk } from '../lib/types'
import { ErrorPanel } from './ui'
const PAGE_SIZE = 1000
export default function JobLogs({ job }: { job: Job }) {
  const [chunks, setChunks] = useState<LogChunk[]>([])
  const [error, setError] = useState<unknown>()
  const [expired, setExpired] = useState(Boolean(job.logs_expired))
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [step, setStep] = useState('all')
  const [follow, setFollow] = useState(true)
  const [page, setPage] = useState(0)
  const current = useRef(job)
  const viewer = useRef<HTMLPreElement>(null)
  useEffect(() => {
    current.current = job
  }, [job])
  useEffect(() => {
    if (job.logs_expired) return
    const controller = new AbortController()
    let accumulated: LogChunk[] = []
    let timer: ReturnType<typeof setTimeout> | undefined
    const pause = (ms: number) =>
      new Promise<void>((resolve) => {
        const finish = () => {
          clearTimeout(timer)
          controller.signal.removeEventListener('abort', finish)
          resolve()
        }
        timer = setTimeout(finish, ms)
        controller.signal.addEventListener('abort', finish, { once: true })
        if (controller.signal.aborted) finish()
      })
    async function read() {
      while (!controller.signal.aborted) {
        try {
          const batch = await api.logs(job.id, accumulated.at(-1)?.seq ?? 0, controller.signal)
          if (controller.signal.aborted) return
          accumulated = mergeLogs(accumulated, batch)
          setChunks(accumulated)
          setLoading(false)
          setError(undefined)
          if (batch.length >= 256) continue
          if (!isActive(current.current.state) && batch.length === 0) return
        } catch (e) {
          if (controller.signal.aborted) return
          setLoading(false)
          if (e instanceof APIError && e.status === 410) {
            setExpired(true)
            return
          }
          setError(e)
          if (e instanceof APIError && e.status < 500) return
        }
        await pause(document.visibilityState === 'hidden' ? 10_000 : 1500)
      }
    }
    void read()
    return () => {
      controller.abort()
      clearTimeout(timer)
    }
  }, [job.id, job.logs_expired])
  const content = logText(step === 'all' ? chunks : chunks.filter((c) => c.step_id === step))
  const lines = content
    .split('\n')
    .map((text, index) => ({ text, number: index + 1 }))
    .filter((line) => !search || line.text.toLocaleLowerCase().includes(search.toLocaleLowerCase()))
  const pages = Math.max(1, Math.ceil(lines.length / PAGE_SIZE))
  const effectivePage = follow ? pages - 1 : Math.min(page, pages - 1)
  const visible = lines.slice(effectivePage * PAGE_SIZE, (effectivePage + 1) * PAGE_SIZE)
  useEffect(() => {
    if (follow && viewer.current) viewer.current.scrollTop = viewer.current.scrollHeight
  }, [chunks, follow, step, search])
  return (
    <section className="log-section" aria-label="任务日志">
      <div className="log-toolbar">
        <div className="with-icon">
          <Terminal size={16} />
          <strong>日志</strong>
          <span className="log-live">{isActive(job.state) ? '实时更新' : '执行记录'}</span>
        </div>
        <a
          className="log-download"
          href={`/api/jobs/${encodeURIComponent(job.id)}/logs/download`}
          download
        >
          <Download size={15} />
          下载完整日志
        </a>
      </div>
      <div className="log-filters">
        <label className="log-search">
          <Search size={15} />
          <input
            aria-label="搜索日志"
            placeholder="搜索日志"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </label>
        <select
          aria-label="筛选日志步骤"
          value={step}
          onChange={(e) => {
            setStep(e.target.value)
            setPage(0)
          }}
        >
          <option value="all">全部步骤</option>
          {job.plan.steps.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name || s.action?.uses || s.id}
            </option>
          ))}
        </select>
        <label className="check-label">
          <input
            type="checkbox"
            checked={follow}
            onChange={(e) => {
              setFollow(e.target.checked)
              setPage(effectivePage)
            }}
          />
          跟随输出
        </label>
      </div>
      {error != null && <ErrorPanel error={error} />}
      {expired ? (
        <div className="log-empty" role="status">
          日志已根据保留策略清理，任务结果仍保留。
        </div>
      ) : loading ? (
        <div className="log-empty" role="status">
          正在读取日志…
        </div>
      ) : !content ? (
        <div className="log-empty">
          {isActive(job.state) ? '等待任务输出…' : '这个任务没有日志。'}
        </div>
      ) : (
        <pre ref={viewer} className="log-viewer" tabIndex={0} role="region" aria-label="日志内容">
          {visible.map((line) => (
            <span className="log-line" key={line.number}>
              <span className="line-number" aria-hidden="true">
                {line.number}
              </span>
              <span>{line.text || ' '}</span>
            </span>
          ))}
          {search && !visible.length && <span>没有匹配的日志</span>}
        </pre>
      )}
      <div className="log-footer">
        <span>
          {lines.length.toLocaleString()} 行{search ? '匹配' : ''} / 第 {effectivePage + 1} 页
        </span>
        <div>
          <button
            aria-label="上一页日志"
            disabled={effectivePage === 0}
            onClick={() => {
              setFollow(false)
              setPage(effectivePage - 1)
            }}
          >
            <ChevronLeft size={16} />
          </button>
          <button
            aria-label="下一页日志"
            disabled={effectivePage >= pages - 1}
            onClick={() => {
              setFollow(false)
              setPage(effectivePage + 1)
            }}
          >
            <ChevronRight size={16} />
          </button>
        </div>
      </div>
    </section>
  )
}
