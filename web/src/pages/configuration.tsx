import { useState } from 'react'
import type { FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { Plus, Pencil, Trash2, Play, RefreshCw } from 'lucide-react'
import { api } from '../lib/api'
import { queryClient } from '../lib/queries'
import type { Management, RepositoryConfig, WorkflowConfig, Target } from '../lib/types'
import { Confirm, Dialog, Empty, ErrorPanel, Loading, PageHeader, useToast } from '../components/ui'

import RepositoryConnections from './repository-connections'

type Section = 'connections' | 'workflows' | 'targets'
type Edit = { section: Section; index: number }
const titles = { connections: '仓库接入', workflows: 'Workflows', targets: '发布目标' }
const template =
  'name: Managed check\non: push\njobs:\n  check:\n    runs-on: homelab\n    steps:\n      - run: echo workflow-ready\n'
export default function Configuration({ section }: { section: Section }) {
  const query = useQuery({
    queryKey: ['management'],
    queryFn: ({ signal }) => api.management(signal),
  })
  const [edit, setEdit] = useState<Edit | null>(null)
  const [remove, setRemove] = useState<Edit | null>(null)
  const [inspect, setInspect] = useState<string | null>(null)
  const [dispatch, setDispatch] = useState<WorkflowConfig | null>(null)
  const toast = useToast()
  async function save(next: Management) {
    const result = await api.saveManagement(next)
    queryClient.setQueryData(['management'], result)
    await queryClient.invalidateQueries({ queryKey: ['config'] })
    toast('配置已保存并生效')
  }
  if (query.isPending) return <Loading />
  if (query.isError) return <ErrorPanel error={query.error} retry={() => void query.refetch()} />
  const doc = query.data
  const rows =
    section === 'connections'
      ? doc.repositories
      : section === 'workflows'
        ? doc.workflows
        : doc.targets
  async function deleteEntry() {
    if (!remove) return
    const next = structuredClone(doc)
    if (remove.section === 'connections') {
      const name = next.repositories[remove.index].name
      next.repositories.splice(remove.index, 1)
      next.workflows = next.workflows.filter((w) => w.repository !== name)
      next.targets = next.targets.filter((t) => t.source_repository !== name)
    } else if (remove.section === 'workflows') next.workflows.splice(remove.index, 1)
    else next.targets.splice(remove.index, 1)
    await save(next)
  }
  const legacy = (
    <>
      <PageHeader
        title={titles[section]}
        description={
          section === 'connections'
            ? '授权 GitHub 仓库，启用 CI，检查并运行第一个 workflow。'
            : section === 'workflows'
              ? '管理触发路径、启停与 YAML 来源；使用固定提交手动运行。'
              : '配置 GitOps 投递与 Flux 观测目标。'
        }
        actions={
          <>
            <button className="button" onClick={() => void query.refetch()}>
              <RefreshCw size={15} />
              重新加载
            </button>
            <button
              className="button button-primary"
              onClick={() => setEdit({ section, index: -1 })}
            >
              <Plus size={15} />
              {section === 'connections' ? '手动登记（高级）' : `新增${titles[section]}`}
            </button>
          </>
        }
      />
      <nav className="configuration-tabs" aria-label="配置导航">
        {(['connections', 'workflows', 'targets'] as const).map((s) => (
          <Link to={`/${s}`} key={s} className={s === section ? 'button button-primary' : 'button'}>
            {titles[s]}
          </Link>
        ))}
      </nav>
      <section className="panel">
        <p className="section-note">
          保存后即时生效并持久化，版本 {doc.version}。
          {section === 'connections'
            ? 'GitHub App 安装与 webhook 需在 GitHub 授权；这里登记已有 installation，不会授予新权限。'
            : section === 'workflows'
              ? '未登记规则的仓库继续读取 Git 文件；有规则后只执行已启用的登记路径。控制面 YAML 不会写回 Git。'
              : '修改用于后续投递；已创建的发布记录保留自己的目标快照。'}
        </p>
        {!rows.length ? (
          <Empty title="尚无配置" />
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>名称 / 路径</th>
                  <th>来源 / 状态</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((_, index) => {
                  const repo = section === 'connections' ? doc.repositories[index] : undefined
                  const workflow = section === 'workflows' ? doc.workflows[index] : undefined
                  const target = section === 'targets' ? doc.targets[index] : undefined
                  return (
                    <tr key={index}>
                      <td>
                        <strong>{repo?.name || workflow?.path || target?.name}</strong>
                        <div className="row-secondary">
                          {workflow?.repository ||
                            target?.repository ||
                            `installation ${repo?.installation_id}`}
                        </div>
                      </td>
                      <td>
                        {repo
                          ? repo.enabled
                            ? '已启用'
                            : '已停用'
                          : workflow
                            ? `${workflow.enabled ? '已启用' : '已停用'} · ${workflow.source ? '控制面 YAML' : 'Git 文件'}`
                            : `${target?.mode} · ${target?.namespace}/${target?.deployment}`}
                      </td>
                      <td>
                        <div className="row-actions">
                          {repo && (
                            <button
                              className="button button-small"
                              onClick={() => setInspect(repo.name)}
                            >
                              检查接入
                            </button>
                          )}
                          {workflow && (
                            <button
                              className="button button-small"
                              disabled={
                                !workflow.enabled ||
                                !doc.repositories.find((r) => r.name === workflow.repository)
                                  ?.enabled
                              }
                              onClick={() => setDispatch(workflow)}
                            >
                              <Play size={14} />
                              运行
                            </button>
                          )}
                          <button
                            className="icon-button"
                            aria-label={`编辑 ${repo?.name || workflow?.path || target?.name}`}
                            onClick={() => setEdit({ section, index })}
                          >
                            <Pencil size={15} />
                          </button>
                          <button
                            className="icon-button danger-text"
                            aria-label={`删除 ${repo?.name || workflow?.path || target?.name}`}
                            onClick={() => setRemove({ section, index })}
                          >
                            <Trash2 size={15} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
  )
  return (
    <>
      {section === 'connections' ? (
        <RepositoryConnections
          doc={doc}
          reload={() => void query.refetch()}
          edit={(index) => setEdit({ section, index })}
          remove={(index) => setRemove({ section, index })}
          inspect={setInspect}
          save={save}
        />
      ) : (
        legacy
      )}
      {edit && (
        <Editor
          key={edit.section + edit.index}
          edit={edit}
          doc={doc}
          close={() => setEdit(null)}
          save={async (next) => {
            await save(next)
            setEdit(null)
          }}
        />
      )}
      {remove && (
        <Confirm
          title="删除配置？"
          label="确认删除配置"
          onClose={() => setRemove(null)}
          onConfirm={deleteEntry}
        >
          <p>
            {remove.section === 'connections'
              ? '同时移除该仓库的 workflow 与发布目标配置。'
              : '后续事件或投递将使用更新后的配置。'}
            运行历史与已创建发布不删除。移除目标后，尚未发起投递的任务将无法使用该目标。
          </p>
        </Confirm>
      )}
      {inspect && <Inspect repo={inspect} close={() => setInspect(null)} />}
      {dispatch && (
        <Dispatch
          workflow={dispatch}
          repository={doc.repositories.find((r) => r.name === dispatch.repository)!}
          close={() => setDispatch(null)}
        />
      )}
    </>
  )
}
function Editor({
  edit,
  doc,
  close,
  save,
}: {
  edit: Edit
  doc: Management
  close: () => void
  save: (m: Management) => Promise<void>
}) {
  const repository = doc.repositories[edit.index],
    workflow = doc.workflows[edit.index],
    target = doc.targets[edit.index]
  const [source, setSource] = useState(edit.section === 'workflows' ? workflow?.source || '' : '')
  const [mode, setMode] = useState(workflow?.source ? 'managed' : 'git')
  const [error, setError] = useState<unknown>()
  const [pending, setPending] = useState(false)
  const [preview, setPreview] = useState('')
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    setPending(true)
    setError(undefined)
    const data = new FormData(e.currentTarget)
    const value = (k: string) => String(data.get(k) || '').trim()
    const next = structuredClone(doc)
    try {
      if (edit.section === 'connections') {
        const r: RepositoryConfig = {
          repository_id: repository?.name === value('name') ? repository.repository_id : undefined,
          name: value('name'),
          installation_id: Number(value('installation_id')),
          enabled: data.has('enabled'),
        }
        if (!Number.isSafeInteger(r.installation_id))
          throw new Error('Installation ID 必须是安全整数')
        if (edit.index < 0) next.repositories.push(r)
        else {
          const old = next.repositories[edit.index].name
          next.repositories[edit.index] = r
          for (const w of next.workflows) if (w.repository === old) w.repository = r.name
          for (const t of next.targets)
            if (t.source_repository === old) t.source_repository = r.name
        }
      } else if (edit.section === 'workflows') {
        const w: WorkflowConfig = {
          repository: value('repository'),
          path: value('path'),
          enabled: data.has('enabled'),
          source: mode === 'managed' ? source : '',
        }
        if (edit.index < 0) next.workflows.push(w)
        else next.workflows[edit.index] = w
      } else {
        const t = Object.fromEntries(
          targetFields.map(([key]) => [key, value(key)]),
        ) as unknown as Target
        t.installation_id = Number(value('installation_id'))
        if (!Number.isSafeInteger(t.installation_id))
          throw new Error('Installation ID 必须是安全整数')
        if (edit.index < 0) next.targets.push(t)
        else next.targets[edit.index] = t
      }
      await save(next)
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog
      title={`${edit.index < 0 ? '新增' : '编辑'}${titles[edit.section]}`}
      onClose={() => {
        if (!pending) close()
      }}
    >
      <form onSubmit={(e) => void submit(e)}>
        <div className="dialog-body form-stack">
          {edit.section === 'connections' ? (
            <>
              <label>
                仓库全名
                <input
                  name="name"
                  required
                  pattern="[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"
                  defaultValue={repository?.name}
                  placeholder="owner/repository"
                />
              </label>
              <label>
                Installation ID
                <input
                  name="installation_id"
                  required
                  type="number"
                  min="0"
                  step="1"
                  defaultValue={repository?.installation_id || 0}
                />
              </label>
              <label className="check-label">
                <input
                  name="enabled"
                  type="checkbox"
                  defaultChecked={repository?.enabled ?? true}
                />
                启用仓库
              </label>
            </>
          ) : edit.section === 'workflows' ? (
            <>
              <label>
                仓库
                <select
                  aria-label="仓库"
                  name="repository"
                  required
                  defaultValue={workflow?.repository}
                >
                  {doc.repositories.map((r) => (
                    <option key={r.name}>{r.name}</option>
                  ))}
                </select>
              </label>
              <label>
                Workflow 路径
                <input
                  name="path"
                  required
                  defaultValue={workflow?.path || '.github/workflows/ci.yml'}
                />
              </label>
              <label>
                来源
                <select
                  aria-label="Workflow 来源"
                  value={mode}
                  onChange={(e) => setMode(e.target.value)}
                >
                  <option value="git">Git 文件（事件 SHA）</option>
                  <option value="managed">控制面保存 YAML</option>
                </select>
              </label>
              {mode === 'managed' && (
                <label>
                  Workflow YAML
                  <textarea
                    aria-label="Workflow YAML"
                    rows={12}
                    className="code-input"
                    required
                    value={source || ''}
                    onChange={(e) => setSource(e.target.value)}
                  />
                  <button type="button" className="button" onClick={() => setSource(template)}>
                    填入示例
                  </button>
                </label>
              )}
              <label className="check-label">
                <input name="enabled" type="checkbox" defaultChecked={workflow?.enabled ?? true} />
                启用 workflow
              </label>
              <label>
                查看 Git 固定 SHA
                <input
                  aria-label="查看 Git 固定 SHA"
                  placeholder="40 位 commit SHA"
                  onChange={(e) => setPreview(e.target.value)}
                />
              </label>
              <button
                className="button"
                type="button"
                onClick={async (e) => {
                  const form = e.currentTarget.form!
                  setPending(true)
                  setError(undefined)
                  try {
                    const files = await api.inspect(
                      String(new FormData(form).get('repository')),
                      preview,
                    )
                    const found = files.find(
                      (w) => w.path === String(new FormData(form).get('path')),
                    )
                    if (!found) throw new Error('该提交没有这个 workflow')
                    setSource(found.source)
                    setMode('managed')
                  } catch (e) {
                    setError(e)
                  } finally {
                    setPending(false)
                  }
                }}
              >
                读取 Git 源码为编辑副本
              </button>
            </>
          ) : (
            <>
              {targetFields.map(([key, label]) => (
                <label key={key}>
                  {label}
                  {key === 'mode' ? (
                    <select name={key} defaultValue={target?.mode || 'direct'}>
                      <option value="direct">direct</option>
                      <option value="pr">pr</option>
                    </select>
                  ) : (
                    <input
                      name={key}
                      required={key !== 'git_url'}
                      defaultValue={target?.[key] ?? targetDefaults[key] ?? ''}
                      type={key === 'installation_id' ? 'number' : 'text'}
                      min={key === 'installation_id' ? 1 : undefined}
                    />
                  )}
                </label>
              ))}
            </>
          )}
          {error != null && <ErrorPanel error={error} />}
        </div>
        <div className="dialog-footer">
          <button type="button" className="button" disabled={pending} onClick={close}>
            返回
          </button>
          <button className="button button-primary" disabled={pending}>
            {pending ? '保存中…' : '保存配置'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}
const targetFields = [
  ['name', '目标名称'],
  ['source_repository', '来源仓库'],
  ['source_ref', '来源 ref'],
  ['repository', 'GitOps 仓库'],
  ['installation_id', 'Installation ID'],
  ['branch', 'GitOps 分支'],
  ['path', 'Kustomize 文件'],
  ['mode', '交付方式'],
  ['image', '镜像地址'],
  ['image_name', 'Kustomize image name'],
  ['git_url', 'Git URL（可选）'],
  ['flux_namespace', 'Flux namespace'],
  ['kustomization', 'Kustomization 名称'],
  ['namespace', '应用 namespace'],
  ['deployment', 'Deployment 名称'],
  ['container', '容器名称'],
] as const
const targetDefaults: Partial<Record<keyof Target, string>> = {
  source_ref: 'refs/heads/main',
  branch: 'main',
  path: 'apps/demo/kustomization.yaml',
  flux_namespace: 'flux-system',
  container: 'app',
}
function Inspect({ repo, close }: { repo: string; close: () => void }) {
  const [sha, setSHA] = useState('')
  const [files, setFiles] = useState<WorkflowConfig[]>()
  const [error, setError] = useState<unknown>()
  const [pending, setPending] = useState(false)
  return (
    <Dialog title="检查仓库接入" onClose={close}>
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          setPending(true)
          setError(undefined)
          setFiles(undefined)
          try {
            setFiles(await api.inspect(repo, sha))
          } catch (e) {
            setError(e)
          } finally {
            setPending(false)
          }
        }}
      >
        <div className="dialog-body form-stack">
          <p>{repo}：使用配置的 App installation 读取固定提交，验证访问权限。</p>
          <label>
            Commit SHA
            <input
              required
              pattern="[a-fA-F0-9]{40}"
              value={sha}
              onChange={(e) => setSHA(e.target.value)}
            />
          </label>
          {files && (
            <div role="status">
              接入检查通过，发现 {files.length} 个 workflow。
              {files.map((f) => (
                <div key={f.path}>
                  <code>{f.path}</code>
                </div>
              ))}
            </div>
          )}
          {error != null && <ErrorPanel error={error} />}
        </div>
        <div className="dialog-footer">
          <button className="button button-primary" disabled={pending}>
            检查访问
          </button>
        </div>
      </form>
    </Dialog>
  )
}
function Dispatch({
  workflow,
  repository,
  close,
}: {
  workflow: WorkflowConfig
  repository: RepositoryConfig
  close: () => void
}) {
  const navigate = useNavigate()
  const [error, setError] = useState<unknown>()
  const [pending, setPending] = useState(false)
  return (
    <Dialog
      title="运行 workflow"
      onClose={() => {
        if (!pending) close()
      }}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          setPending(true)
          setError(undefined)
          const data = new FormData(e.currentTarget)
          try {
            const sha = String(data.get('sha'))
            const source =
              workflow.source ||
              (await api.inspect(workflow.repository, sha)).find((w) => w.path === workflow.path)
                ?.source
            if (!source) throw new Error('指定提交没有该 workflow')
            const inputs: unknown = JSON.parse(String(data.get('inputs')))
            if (!inputs || Array.isArray(inputs) || typeof inputs !== 'object')
              throw new Error('Inputs 必须是 JSON 对象')
            const r = await api.create({
              repository: workflow.repository,
              sha,
              ref: String(data.get('ref')),
              workflow_path: workflow.path,
              workflow: source,
              installation_id: repository.installation_id,
              event_name: String(data.get('event')),
              inputs: inputs as Record<string, unknown>,
            })
            await queryClient.invalidateQueries({ queryKey: ['runs'] })
            await navigate({ to: '/runs/$runId', params: { runId: r.id } })
          } catch (e) {
            setError(e)
          } finally {
            setPending(false)
          }
        }}
      >
        <div className="dialog-body form-stack">
          <p>
            {workflow.repository} / {workflow.path}
          </p>
          <label>
            Commit SHA
            <input name="sha" required pattern="[a-fA-F0-9]{40}" />
          </label>
          <label>
            Git ref
            <input name="ref" required defaultValue="refs/heads/main" />
          </label>
          <label>
            事件
            <select name="event">
              <option value="push">push</option>
              <option value="workflow_dispatch">workflow_dispatch</option>
            </select>
          </label>
          <label>
            Inputs JSON
            <textarea name="inputs" defaultValue="{}" />
          </label>
          {error != null && <ErrorPanel error={error} />}
        </div>
        <div className="dialog-footer">
          <button className="button button-primary" disabled={pending}>
            创建运行
          </button>
        </div>
      </form>
    </Dialog>
  )
}
