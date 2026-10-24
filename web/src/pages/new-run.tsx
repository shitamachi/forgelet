import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@tanstack/react-query'
import { ArrowLeft, LoaderCircle, Play } from 'lucide-react'
import { queries, queryClient } from '../lib/queries'
import { api } from '../lib/api'
import { ErrorPanel, PageHeader, useToast } from '../components/ui'
import type { ManualRun } from '../lib/types'
const template = `name: Manual check\non: workflow_dispatch\njobs:\n  check:\n    runs-on: homelab\n    steps:\n      - name: Check workspace\n        run: |\n          echo "Hello from forgelet"\n          pwd\n`
export default function NewRun() {
  const config = useQuery(queries.config)
  const runs = useQuery(queries.runs)
  const navigate = useNavigate()
  const toast = useToast()
  const [repository, setRepository] = useState('')
  const [editedWorkflow, setWorkflow] = useState<string>()
  const profile =
    config.data?.profiles.find((p) => p.name === 'homelab')?.name ||
    config.data?.profiles[0]?.name ||
    'homelab'
  const workflow =
    editedWorkflow ?? template.replace('runs-on: homelab', `runs-on: ${JSON.stringify(profile)}`)
  const [error, setError] = useState<unknown>()
  const create = useMutation({
    mutationFn: api.create,
    onSuccess: async (run) => {
      await queryClient.invalidateQueries({ queryKey: ['runs'] })
      toast('运行已加入队列')
      await navigate({ to: '/runs/$runId', params: { runId: run.id } })
    },
  })
  const repo = repository || config.data?.repositories[0] || ''
  const installation =
    runs.data?.find((r) => r.repository === repo && r.installation_id > 0)?.installation_id || 0
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError(undefined)
    const data = new FormData(event.currentTarget)
    try {
      const inputs: unknown = JSON.parse(String(data.get('inputs') || '{}'))
      if (!inputs || Array.isArray(inputs) || typeof inputs !== 'object')
        throw new Error('Inputs 必须是 JSON 对象。')
      const input: ManualRun = {
        repository: repo.trim(),
        sha: String(data.get('sha')).trim(),
        ref: String(data.get('ref')).trim(),
        workflow,
        workflow_path: String(data.get('path')).trim(),
        installation_id: Number(data.get('installation')),
        event_name: String(data.get('event')),
      }
      if (input.event_name === 'workflow_dispatch') input.inputs = inputs as Record<string, unknown>
      await create.mutateAsync(input)
    } catch (e) {
      setError(e)
    }
  }
  return (
    <>
      <Link to="/runs" search={{ q: '', state: 'all', repo: 'all' }} className="back-link">
        <ArrowLeft size={15} />
        运行记录
      </Link>
      <PageHeader
        title="手动运行"
        description="使用固定 commit SHA 创建独立运行，历史记录将完整保留。"
      />
      {config.isError && <ErrorPanel error={config.error} />}
      <form onSubmit={(event) => void submit(event)} className="manual-form">
        <section className="panel form-panel">
          <h2>运行来源</h2>
          <div className="form-grid">
            <label>
              仓库
              {config.data?.repositories.length ? (
                <select value={repo} onChange={(e) => setRepository(e.target.value)} required>
                  {config.data.repositories.map((r) => (
                    <option key={r}>{r}</option>
                  ))}
                </select>
              ) : (
                <input
                  value={repo}
                  onChange={(e) => setRepository(e.target.value)}
                  placeholder="owner/repository"
                  required
                  pattern="[^/]+/[^/]+"
                />
              )}
            </label>
            <label>
              Commit SHA
              <input
                name="sha"
                required
                pattern="[a-f0-9]{40}"
                minLength={40}
                maxLength={40}
                placeholder="完整的 40 位 commit SHA"
                className="mono"
              />
            </label>
            <label>
              Git ref
              <input name="ref" defaultValue="refs/heads/main" required />
            </label>
            <label>
              Workflow 路径
              <input name="path" defaultValue=".github/workflows/manual.yml" required />
            </label>
            <label>
              触发类型
              <select name="event" defaultValue="workflow_dispatch">
                <option value="workflow_dispatch">workflow_dispatch</option>
                <option value="push">push</option>
              </select>
            </label>
            <label>
              GitHub installation ID
              <input
                key={repo + installation}
                name="installation"
                type="number"
                min="0"
                step="1"
                defaultValue={installation}
                required
              />
              <span className="field-hint">
                私有仓库 checkout 与 Checks 需要对应 App installation；纯 shell 可用 0。
              </span>
            </label>
          </div>
          <label className="field-label">
            Inputs（JSON）
            <textarea
              name="inputs"
              rows={3}
              defaultValue="{}"
              spellCheck={false}
              className="code-input"
            />
          </label>
        </section>
        <section className="panel form-panel">
          <div className="section-header">
            <h2>Workflow YAML</h2>
            <span className="muted">
              可用 profile：{config.data?.profiles.map((p) => p.name).join(', ') || '加载中'}
            </span>
          </div>
          <label className="sr-only" htmlFor="workflow">
            Workflow YAML
          </label>
          <textarea
            id="workflow"
            value={workflow}
            onChange={(e) => setWorkflow(e.target.value)}
            required
            spellCheck={false}
            className="code-input workflow-editor"
          />
        </section>
        {error != null && <ErrorPanel error={error} />}
        <div className="form-actions">
          <Link to="/runs" search={{ q: '', state: 'all', repo: 'all' }} className="button">
            返回
          </Link>
          <button
            type="submit"
            className="button button-primary"
            disabled={create.isPending || config.isPending}
          >
            {create.isPending ? <LoaderCircle size={16} className="spin" /> : <Play size={16} />}
            创建运行
          </button>
        </div>
      </form>
    </>
  )
}
