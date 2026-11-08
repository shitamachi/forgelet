import { useState, type FormEvent } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Copy, KeyRound, Plus } from 'lucide-react'
import { api } from '../lib/api'
import { date } from '../lib/model'
import { queryClient } from '../lib/queries'
import type { Runner, RunnerCredential } from '../lib/types'
import {
  Confirm,
  Dialog,
  Empty,
  ErrorPanel,
  Loading,
  PageHeader,
  Status,
  useToast,
} from '../components/ui'

const runnersKey = ['runners']

function refreshRunners() {
  void queryClient.invalidateQueries({ queryKey: runnersKey })
}

export default function Runners() {
  const inventory = useQuery({
    queryKey: runnersKey,
    queryFn: ({ signal }) => api.runners(signal),
    refetchInterval: 5000,
  })
  const [registering, setRegistering] = useState(false)
  const [action, setAction] = useState<{ runner: Runner; kind: 'pause' | 'rotate' }>()
  const [credential, setCredential] = useState<RunnerCredential>()
  const toast = useToast()
  const register = (
    <button className="button button-primary" onClick={() => setRegistering(true)}>
      <Plus size={16} /> 登记 Runner
    </button>
  )
  return (
    <>
      <PageHeader
        title="Runners"
        description="查看设备连接和当前任务。在线设备从共享的 macOS 队列接单。"
        actions={register}
      />
      {inventory.isPending ? (
        <Loading />
      ) : inventory.isError ? (
        <ErrorPanel error={inventory.error} retry={() => void inventory.refetch()} />
      ) : inventory.data.length === 0 ? (
        <section className="panel">
          <Empty title="暂无已登记的 Runner">
            登记设备后，在 Mac 应用中填入控制面地址和设备凭据。
          </Empty>
        </section>
      ) : (
        <section className="panel runner-list" aria-label="已登记的 Runner">
          {inventory.data.map((runner) => (
            <article className="runner-row" key={runner.name} aria-label={runner.name}>
              <div>
                <h2>{runner.name}</h2>
                <p>{runner.source === 'yaml' ? '配置文件登记' : 'Web 登记'}</p>
              </div>
              <div>
                <Status state={runner.status} />
                {runner.paused && runner.status === 'busy' && <p>当前任务完成后暂停接单</p>}
                <p>{runner.last_seen ? `最近连接 ${date(runner.last_seen)}` : '尚无连接记录'}</p>
                {runner.run_id && (
                  <Link
                    to="/runs/$runId"
                    params={{ runId: runner.run_id }}
                    search={{ job: runner.job_id }}
                  >
                    查看当前任务
                  </Link>
                )}
              </div>
              <div className="runner-actions">
                <button
                  className="button button-small"
                  onClick={() => setAction({ runner, kind: 'pause' })}
                >
                  {runner.paused ? '恢复接单' : '暂停接单'}
                </button>
                <button
                  className="button button-small"
                  disabled={runner.status === 'busy'}
                  onClick={() => setAction({ runner, kind: 'rotate' })}
                >
                  <KeyRound size={14} /> 轮换凭据
                </button>
              </div>
            </article>
          ))}
        </section>
      )}
      {registering && (
        <Register
          onClose={() => setRegistering(false)}
          onRegistered={(value) => {
            setRegistering(false)
            setCredential(value)
            refreshRunners()
          }}
        />
      )}
      {action && (
        <Confirm
          title={
            action.kind === 'rotate'
              ? '轮换设备凭据'
              : action.runner.paused
                ? '恢复接单'
                : '暂停接单'
          }
          label={
            action.kind === 'rotate' ? '轮换凭据' : action.runner.paused ? '恢复接单' : '暂停接单'
          }
          onClose={() => setAction(undefined)}
          onConfirm={async () => {
            if (action.kind === 'rotate') {
              setCredential(await api.rotateRunner(action.runner.name))
            } else {
              await api.pauseRunner(action.runner.name, !action.runner.paused)
              toast(action.runner.paused ? '已恢复接单' : '已暂停接单')
            }
            refreshRunners()
          }}
        >
          <p>
            <strong>{action.runner.name}</strong>
          </p>
          <p>
            {action.kind === 'rotate'
              ? '轮换后，请在 Mac 应用中替换设备凭据。旧凭据会立即失效。'
              : action.runner.paused
                ? '设备连接后可以继续领取排队任务。'
                : '已领取的任务会继续执行。其余任务等待恢复接单。'}
          </p>
        </Confirm>
      )}
      {credential && !action && (
        <Dialog title="设备凭据" onClose={() => setCredential(undefined)}>
          <div className="dialog-body">
            <p>
              在 Mac 应用中填入 <strong>{credential.name}</strong>{' '}
              的设备凭据。关闭后可通过轮换生成新凭据。
            </p>
            <label className="field-label">
              仅显示一次
              <input readOnly value={credential.token} onFocus={(event) => event.target.select()} />
            </label>
          </div>
          <div className="dialog-footer">
            <button className="button" onClick={() => setCredential(undefined)}>
              关闭
            </button>
            <button
              className="button button-primary"
              onClick={() => {
                void navigator.clipboard.writeText(credential.token).then(
                  () => toast('已复制设备凭据'),
                  () => toast('请从输入框手动复制凭据'),
                )
              }}
            >
              <Copy size={15} /> 复制凭据
            </button>
          </div>
        </Dialog>
      )}
    </>
  )
}

function Register({
  onClose,
  onRegistered,
}: {
  onClose: () => void
  onRegistered: (credential: RunnerCredential) => void
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const name = String(form.get('name') ?? '').trim()
    setPending(true)
    setError(undefined)
    try {
      onRegistered(await api.registerRunner(name))
    } catch (e) {
      setError(e)
      setPending(false)
    }
  }
  return (
    <Dialog
      title="登记 Runner"
      onClose={() => {
        if (!pending) onClose()
      }}
    >
      <form onSubmit={(event) => void submit(event)}>
        <div className="dialog-body">
          <div className="form-grid">
            <label>
              设备名称
              <input
                name="name"
                required
                placeholder="macos-build"
                pattern="[a-z0-9][a-z0-9\-]{0,63}"
              />
            </label>
          </div>
          {error != null && <ErrorPanel error={error} />}
        </div>
        <div className="dialog-footer">
          <button type="button" className="button" onClick={onClose} disabled={pending}>
            返回
          </button>
          <button className="button button-primary" disabled={pending}>
            登记 Runner
          </button>
        </div>
      </form>
    </Dialog>
  )
}
