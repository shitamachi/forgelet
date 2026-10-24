import { useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  ArrowUpRight,
  Braces,
  GitBranch,
  KeyRound,
  LoaderCircle,
  Pencil,
  Plus,
  Trash2,
} from 'lucide-react'
import { api } from '../lib/api'
import { queries, queryClient } from '../lib/queries'
import { Confirm, Dialog, Empty, ErrorPanel, Loading, PageHeader, useToast } from '../components/ui'
type Setting = { kind: 'vars' | 'secrets'; name: string; value: string; editing?: boolean }
export default function Repositories() {
  const config = useQuery(queries.config)
  const search = useSearch({ from: '/repositories' })
  const navigate = useNavigate({ from: '/repositories' })
  const repo = config.data?.repositories.includes(search.repo || '')
    ? search.repo!
    : config.data?.repositories[0] || ''
  const variables = useQuery({
    queryKey: ['settings', repo, 'vars'],
    queryFn: ({ signal }) => api.vars(repo, signal),
    enabled: Boolean(repo),
  })
  const secrets = useQuery({
    queryKey: ['settings', repo, 'secrets'],
    queryFn: ({ signal }) => api.secrets(repo, signal),
    enabled: Boolean(repo),
  })
  const [editing, setEditing] = useState<Setting | null>(null)
  const [removing, setRemoving] = useState<Setting | null>(null)
  const toast = useToast()
  if (config.isPending) return <Loading />
  if (config.isError) return <ErrorPanel error={config.error} retry={() => void config.refetch()} />
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['settings', repo] })
  return (
    <>
      <PageHeader title="仓库设置" description="管理 workflow 使用的变量与加密 secrets。" />
      {!repo ? (
        <Empty title="尚未配置仓库">在控制面配置中添加可信仓库后，即可管理其运行设置。</Empty>
      ) : (
        <>
          <div className="repository-picker">
            <GitBranch size={20} />
            <label className="sr-only" htmlFor="repository">
              选择仓库
            </label>
            <select
              id="repository"
              value={repo}
              onChange={(e) => {
                setEditing(null)
                setRemoving(null)
                void navigate({ search: { repo: e.target.value } })
              }}
            >
              {config.data.repositories.map((r) => (
                <option key={r}>{r}</option>
              ))}
            </select>
            <a
              href={`https://github.com/${repo}`}
              target="_blank"
              rel="noreferrer"
              className="quiet-link"
            >
              GitHub
              <ArrowUpRight size={15} />
            </a>
          </div>
          <section className="panel settings-panel">
            <div className="section-header">
              <h2>
                <Braces size={18} />
                变量
              </h2>
              <button
                className="button button-small"
                onClick={() => setEditing({ kind: 'vars', name: '', value: '' })}
              >
                <Plus size={15} />
                新增变量
              </button>
            </div>
            <p className="section-note">
              明文值，运行创建时保存快照。Workflow 通过 vars.NAME 引用。
            </p>
            {variables.isPending ? (
              <Loading />
            ) : variables.isError ? (
              <ErrorPanel error={variables.error} retry={() => void variables.refetch()} />
            ) : Object.keys(variables.data).length ? (
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>名称</th>
                      <th>值</th>
                      <th>
                        <span className="sr-only">操作</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {Object.entries(variables.data).map(([name, value]) => (
                      <tr key={name}>
                        <td>
                          <code>{name}</code>
                        </td>
                        <td>
                          <span className="setting-value">{value}</span>
                        </td>
                        <td>
                          <div className="row-actions">
                            <button
                              className="icon-button"
                              aria-label={`编辑变量 ${name}`}
                              onClick={() =>
                                setEditing({ kind: 'vars', name, value, editing: true })
                              }
                            >
                              <Pencil size={15} />
                            </button>
                            <button
                              className="icon-button danger-text"
                              aria-label={`删除变量 ${name}`}
                              onClick={() => setRemoving({ kind: 'vars', name, value })}
                            >
                              <Trash2 size={15} />
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <Empty title="还没有变量">可添加构建参数、环境名称等非敏感值。</Empty>
            )}
          </section>
          <section className="panel settings-panel">
            <div className="section-header">
              <h2>
                <KeyRound size={18} />
                Secrets
              </h2>
              <button
                className="button button-small"
                disabled={!config.data.features.secrets}
                onClick={() => setEditing({ kind: 'secrets', name: '', value: '' })}
              >
                <Plus size={15} />
                新增 secret
              </button>
            </div>
            <p className="section-note">加密保存，仅执行任务时下发。已保存的值不会返回到浏览器。</p>
            {!config.data.features.secrets && (
              <div className="alert alert-warning">
                <p>控制面尚未配置加密密钥，暂时无法保存 secret。</p>
              </div>
            )}
            {secrets.isPending ? (
              <Loading />
            ) : secrets.isError ? (
              <ErrorPanel error={secrets.error} retry={() => void secrets.refetch()} />
            ) : secrets.data.names.length ? (
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>名称</th>
                      <th>值</th>
                      <th>
                        <span className="sr-only">操作</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {secrets.data.names.map((name) => (
                      <tr key={name}>
                        <td>
                          <code>{name}</code>
                        </td>
                        <td className="muted">已加密</td>
                        <td>
                          <div className="row-actions">
                            <button
                              className="button button-small"
                              disabled={!config.data.features.secrets}
                              onClick={() =>
                                setEditing({ kind: 'secrets', name, value: '', editing: true })
                              }
                            >
                              替换值
                            </button>
                            <button
                              className="icon-button danger-text"
                              aria-label={`删除 secret ${name}`}
                              onClick={() => setRemoving({ kind: 'secrets', name, value: '' })}
                            >
                              <Trash2 size={15} />
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <Empty title="还没有 secrets">可添加镜像仓库或外部服务所需的凭证。</Empty>
            )}
          </section>
          {editing && (
            <SettingEditor
              key={repo + editing.kind + editing.name}
              setting={editing}
              repo={repo}
              onClose={() => setEditing(null)}
              onSaved={async () => {
                await refresh()
                setEditing(null)
                toast('仓库设置已保存')
              }}
            />
          )}
          {removing && (
            <Confirm
              title={`删除 ${removing.name}？`}
              label="确认删除"
              onClose={() => setRemoving(null)}
              onConfirm={async () => {
                await api.deleteSetting(repo, removing.kind, removing.name)
                await refresh()
                toast('仓库设置已删除')
              }}
            >
              <p>
                后续任务将无法读取这个{removing.kind === 'secrets' ? ' secret' : '变量'}。请确认相关
                workflow 已完成调整。
              </p>
            </Confirm>
          )}
        </>
      )}
    </>
  )
}
function SettingEditor({
  setting,
  repo,
  onClose,
  onSaved,
}: {
  setting: Setting
  repo: string
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const [error, setError] = useState<unknown>()
  const [pending, setPending] = useState(false)
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    const data = new FormData(e.currentTarget)
    setPending(true)
    setError(undefined)
    try {
      await api.saveSetting(repo, setting.kind, String(data.get('name')), String(data.get('value')))
      await onSaved()
    } catch (err) {
      setError(err)
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog
      title={`${setting.editing ? '编辑' : '新增'}${setting.kind === 'vars' ? '变量' : ' secret'}`}
      onClose={() => {
        if (!pending) onClose()
      }}
    >
      <form onSubmit={(e) => void submit(e)}>
        <div className="dialog-body form-stack">
          <label>
            名称
            <input
              name="name"
              defaultValue={setting.name}
              readOnly={setting.editing}
              required
              pattern="[A-Za-z_][A-Za-z0-9_]{0,63}"
              maxLength={64}
              autoComplete="off"
              placeholder="DEPLOY_ENV"
            />
          </label>
          <label>
            {setting.kind === 'secrets' ? '新的 secret 值' : '值'}
            {setting.kind === 'secrets' ? (
              <textarea
                name="value"
                aria-label="新的 secret 值"
                required
                rows={4}
                autoComplete="off"
                spellCheck={false}
                placeholder="输入新的值，保存后不可读取"
              />
            ) : (
              <textarea name="value" aria-label="值" defaultValue={setting.value} rows={4} />
            )}
          </label>
          {error != null && <ErrorPanel error={error} />}
        </div>
        <div className="dialog-footer">
          <button className="button" type="button" disabled={pending} onClick={onClose}>
            取消
          </button>
          <button className="button button-primary" disabled={pending}>
            {pending && <LoaderCircle className="spin" size={15} />}保存
          </button>
        </div>
      </form>
    </Dialog>
  )
}
