import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Play } from 'lucide-react'
import { api } from '../lib/api'
import { queryClient } from '../lib/queries'
import type { GitHubPreview, GitHubRepository } from '../lib/types'
import { Dialog, ErrorPanel, Empty } from '../components/ui'

export default function FirstRun({
  repository,
  close,
}: {
  repository: GitHubRepository
  close: () => void
}) {
  const [preview, setPreview] = useState<GitHubPreview>()
  const [event, setEvent] = useState('push')
  const [inputs, setInputs] = useState('{}')
  const [checkedInputs, setCheckedInputs] = useState<Record<string, unknown>>({})
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  const [requestID] = useState(() => crypto.randomUUID())
  const navigate = useNavigate()
  async function inspect() {
    setBusy(true)
    setError(undefined)
    setPreview(undefined)
    try {
      const values: unknown = JSON.parse(inputs)
      if (!values || typeof values !== 'object' || Array.isArray(values))
        throw new Error('Inputs 必须是 JSON 对象')
      setCheckedInputs(values as Record<string, unknown>)
      setPreview(await api.githubPreview(repository.id, event, values as Record<string, unknown>))
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  async function start(path: string) {
    if (!preview) return
    setBusy(true)
    setError(undefined)
    try {
      const run = await api.githubRun({
        repository_id: repository.id,
        sha: preview.sha,
        ref: preview.ref,
        path,
        event_name: event,
        inputs: checkedInputs,
        request_id: requestID,
      })
      await queryClient.invalidateQueries({ queryKey: ['runs'] })
      await navigate({ to: '/runs/$runId', params: { runId: run.id } })
    } catch (e) {
      setError(e)
      setBusy(false)
    }
  }
  return (
    <Dialog
      title="检查 workflow 与首次运行"
      onClose={() => {
        if (!busy) close()
      }}
    >
      <div className="dialog-body form-stack">
        <p>
          <strong>{repository.full_name}</strong> · 自动读取默认分支{' '}
          {repository.default_branch || '（空）'}
        </p>
        <label>
          触发事件
          <select
            aria-label="首次运行事件"
            value={event}
            disabled={busy}
            onChange={(e) => {
              setEvent(e.target.value)
              setPreview(undefined)
            }}
          >
            <option value="push">push</option>
            <option value="workflow_dispatch">workflow_dispatch</option>
          </select>
        </label>
        {event === 'workflow_dispatch' && (
          <label>
            Inputs JSON
            <textarea
              aria-label="首次运行 Inputs"
              value={inputs}
              disabled={busy}
              onChange={(e) => {
                setInputs(e.target.value)
                setPreview(undefined)
              }}
            />
          </label>
        )}
        <button className="button button-primary" disabled={busy} onClick={() => void inspect()}>
          {busy ? '处理中…' : '检查默认分支 workflow'}
        </button>
        {error != null && <ErrorPanel error={error} />}
        {preview && (
          <>
            <p className="section-note">
              本次检查与运行固定到 {preview.sha}，ref {preview.ref}。
            </p>
            {!preview.workflows.length && (
              <Empty title="没有已启用的 workflow">
                <p>
                  在 .github/workflows 添加兼容的 YAML，或在 Workflows 页面配置控制面
                  YAML，再重新检查。runs-on 使用系统配置的 profile。
                </p>
              </Empty>
            )}
            {preview.workflows.map((file) => (
              <article className="onboarding-workflow" key={file.path}>
                <strong>{file.path}</strong>
                <p>{file.runnable ? `检查通过 · ${file.jobs} 个任务` : file.diagnostic}</p>
                <details>
                  <summary>查看本次检查的 YAML</summary>
                  <pre className="code-input">{file.source}</pre>
                </details>
                <button
                  className="button button-primary"
                  disabled={!file.runnable || busy}
                  onClick={() => void start(file.path)}
                >
                  <Play size={14} />
                  运行此 workflow
                </button>
              </article>
            ))}
          </>
        )}
      </div>
    </Dialog>
  )
}
