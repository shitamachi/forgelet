import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Check, Copy, KeyRound, MoreHorizontal, Trash2 } from 'lucide-react'
import { api } from '../lib/api'
import { date } from '../lib/model'
import { queryClient } from '../lib/queries'
import type { Runner, RunnerCredential, RunnerInfo, RunnerPairing } from '../lib/types'
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
const pairingsKey = ['runner-pairings']

const osNames: Record<string, string> = { windows: 'Windows', darwin: 'macOS', linux: 'Linux' }

const statusNames: Record<Runner['status'], string> = {
  online: '空闲',
  busy: '运行中',
  paused: '已暂停',
  offline: '离线',
}

function systemSummary(info: RunnerInfo, live: boolean): string {
  const parts: string[] = []
  if (info.os) {
    parts.push([osNames[info.os] ?? info.os, info.arch].filter(Boolean).join(' '))
  }
  if (info.backend === 'wslc') {
    parts.push(info.engine ? `${info.engine} 容器` : 'WSLC 容器')
  }
  if (info.cpus) {
    parts.push(`${info.cpus} 核`)
  }
  if (info.memory_total_bytes) {
    const total = (info.memory_total_bytes / 2 ** 30).toFixed(1)
    parts.push(
      live && info.memory_available_bytes
        ? `内存 ${(info.memory_available_bytes / 2 ** 30).toFixed(1)} / ${total} GiB`
        : `内存 ${total} GiB`,
    )
  }
  if (live && info.load1 !== undefined) {
    parts.push(`负载 ${info.load1.toFixed(2)}`)
  }
  return parts.join(' · ')
}

function duration(started: string | undefined, seconds: number | undefined, now: number): string {
  let total = seconds ?? 0
  if (started) {
    const at = new Date(started).getTime()
    if (Number.isFinite(at)) {
      total = Math.max(0, Math.floor((now - at) / 1000))
    }
  }
  if (total >= 3600) {
    return `${Math.floor(total / 3600)} 小时 ${Math.floor((total % 3600) / 60)} 分`
  }
  if (total >= 60) {
    return `${Math.floor(total / 60)} 分 ${String(total % 60).padStart(2, '0')} 秒`
  }
  return `${total} 秒`
}

export default function Runners() {
  const devices = useQuery({
    queryKey: runnersKey,
    queryFn: ({ signal }) => api.runners(signal),
    refetchInterval: 5000,
  })
  const pending = useQuery({
    queryKey: pairingsKey,
    queryFn: ({ signal }) => api.pairings(signal),
    refetchInterval: 5000,
  })
  const [action, setAction] = useState<{ runner: Runner; kind: 'pause' | 'rotate' | 'delete' }>()
  const [approving, setApproving] = useState<RunnerPairing>()
  const [credential, setCredential] = useState<RunnerCredential>()
  const [openMenu, setOpenMenu] = useState<string>()
  const [clock, setClock] = useState(() => Date.now())
  const toast = useToast()
  const running = devices.data?.some((runner) => runner.status === 'busy') ?? false
  useEffect(() => {
    if (!running) return
    const timer = setInterval(() => setClock(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [running])
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: runnersKey })
    void queryClient.invalidateQueries({ queryKey: pairingsKey })
  }

  return (
    <>
      <PageHeader
        title="Runners"
        description="每台机器是一个 Runner。开始后在本页批准它的配对码。"
      />
      {pending.data && pending.data.length > 0 && (
        <section className="panel pairing-panel" aria-label="等待批准">
          <h2>等待批准</h2>
          {pending.data.map((pairing) => (
            <article className="pairing-row" key={pairing.id}>
              <div>
                <p className="pairing-code">{pairing.code}</p>
                <p>
                  {pairing.hostname}
                  {pairing.os
                    ? ` · ${osNames[pairing.os] ?? pairing.os} ${pairing.arch ?? ''}`
                    : ''}
                </p>
                <p>有效期至 {date(pairing.expires_at)}</p>
              </div>
              <div className="pairing-actions">
                <button
                  className="button button-primary button-small"
                  onClick={() => setApproving(pairing)}
                >
                  <Check size={14} /> 批准
                </button>
                <button
                  className="button button-small"
                  onClick={async () => {
                    await api.rejectPairing(pairing.id)
                    toast('已忽略该配对码')
                    refresh()
                  }}
                >
                  忽略
                </button>
              </div>
            </article>
          ))}
        </section>
      )}
      {devices.isPending ? (
        <Loading />
      ) : devices.isError ? (
        <ErrorPanel error={devices.error} retry={() => void devices.refetch()} />
      ) : devices.data.length === 0 ? (
        <section className="panel">
          <Empty title="还没有 Runner">在任意一台机器上开始 Runner，配对码会出现在这里。</Empty>
        </section>
      ) : (
        <section className="panel runner-list" aria-label="Runner">
          {devices.data.map((runner) => (
            <article className="runner-row" key={runner.name} aria-label={runner.name}>
              <div>
                <h2>{runner.name}</h2>
                {runner.info?.hostname && runner.info.hostname !== runner.name && (
                  <p>{runner.info.hostname}</p>
                )}
                {runner.labels && runner.labels.length > 0 && (
                  <p className="runner-chips">
                    {runner.labels.map((label) => (
                      <code className="chip" key={label}>
                        {label}
                      </code>
                    ))}
                  </p>
                )}
              </div>
              <div>
                <Status state={runner.status} label={statusNames[runner.status]} />
                {runner.paused && runner.status === 'busy' && <p>当前任务结束后暂停</p>}
                {runner.job_name ? (
                  <p>
                    <strong>{runner.job_name}</strong>
                    {runner.repository ? ` · ${runner.repository}` : ''} · 已运行{' '}
                    {duration(runner.started_at, runner.duration_seconds, clock)}
                  </p>
                ) : null}
                {runner.info ? (
                  <p>{systemSummary(runner.info, runner.status !== 'offline')}</p>
                ) : null}
                {runner.last_seen ? <p>最近在线 {date(runner.last_seen)}</p> : null}
                {runner.run_id && (
                  <Link
                    to="/runs/$runId"
                    params={{ runId: runner.run_id }}
                    search={{ job: runner.job_id }}
                  >
                    查看任务
                  </Link>
                )}
              </div>
              <div className="runner-actions">
                <button
                  className="button button-small"
                  onClick={() => setAction({ runner, kind: 'pause' })}
                  disabled={runner.status === 'busy' && !runner.paused}
                >
                  {runner.paused ? '开始' : '暂停'}
                </button>
                <RunnerMenu
                  open={openMenu === runner.name}
                  onToggle={() => setOpenMenu(openMenu === runner.name ? undefined : runner.name)}
                  onClose={() => setOpenMenu(undefined)}
                  items={[
                    {
                      label: '轮换凭据',
                      icon: <KeyRound size={14} />,
                      disabled: runner.status === 'busy',
                      run: () => setAction({ runner, kind: 'rotate' }),
                    },
                    {
                      label: '移除',
                      icon: <Trash2 size={14} />,
                      disabled: runner.status === 'busy',
                      run: () => setAction({ runner, kind: 'delete' }),
                    },
                  ]}
                />
              </div>
            </article>
          ))}
        </section>
      )}
      {approving && (
        <Approve
          pairing={approving}
          onClose={() => setApproving(undefined)}
          onApproved={() => {
            setApproving(undefined)
            toast('已批准，设备将自动开始')
            refresh()
          }}
        />
      )}
      {action && (
        <Confirm
          title={
            action.kind === 'rotate'
              ? '轮换凭据'
              : action.kind === 'delete'
                ? '移除 Runner'
                : action.runner.paused
                  ? '开始'
                  : '暂停'
          }
          label={
            action.kind === 'rotate'
              ? '轮换凭据'
              : action.kind === 'delete'
                ? '移除'
                : action.runner.paused
                  ? '开始'
                  : '暂停'
          }
          onClose={() => setAction(undefined)}
          onConfirm={async () => {
            if (action.kind === 'rotate') {
              setCredential(await api.rotateRunner(action.runner.name))
            } else if (action.kind === 'delete') {
              await api.deleteRunner(action.runner.name)
              toast('已移除')
            } else {
              await api.pauseRunner(action.runner.name, !action.runner.paused)
              toast(action.runner.paused ? '已开始' : '已暂停')
            }
            refresh()
          }}
        >
          <p>
            <strong>{action.runner.name}</strong>
          </p>
          <p>
            {action.kind === 'rotate'
              ? '轮换后设备需要重新配对。'
              : action.kind === 'delete'
                ? '移除后该设备需要重新配对才能接任务，历史运行记录保留。'
                : action.runner.paused
                  ? '设备可以继续领取任务。'
                  : '当前任务会继续执行，之后不再领取新任务。'}
          </p>
        </Confirm>
      )}
      {credential && !action && (
        <Dialog title="设备凭据" onClose={() => setCredential(undefined)}>
          <div className="dialog-body">
            <p>
              设备将用它继续上报 <strong>{credential.name}</strong>
              。仅在无法自动重新配对时手动填入。
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
                  () => toast('已复制凭据'),
                  () => toast('请从输入框手动复制凭据'),
                )
              }}
            >
              <Copy size={15} /> 复制
            </button>
          </div>
        </Dialog>
      )}
    </>
  )
}

function RunnerMenu({
  open,
  onToggle,
  onClose,
  items,
}: {
  open: boolean
  onToggle: () => void
  onClose: () => void
  items: { label: string; icon: ReactNode; disabled?: boolean; run: () => void }[]
}) {
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const close = (event: MouseEvent) => {
      if (!box.current?.contains(event.target as Node)) onClose()
    }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [open, onClose])
  return (
    <div className="runner-menu" ref={box}>
      <button
        className="button button-small"
        aria-label="更多操作"
        aria-expanded={open}
        onClick={onToggle}
      >
        <MoreHorizontal size={16} />
      </button>
      {open && (
        <div className="runner-menu-panel" role="menu">
          {items.map((item) => (
            <button
              key={item.label}
              role="menuitem"
              disabled={item.disabled}
              onClick={() => {
                onClose()
                item.run()
              }}
            >
              {item.icon}
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function Approve({
  pairing,
  onClose,
  onApproved,
}: {
  pairing: RunnerPairing
  onClose: () => void
  onApproved: () => void
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  const suggested = (pairing.hostname || 'device')
    .replace(/[^a-z0-9-]/g, '-')
    .replace(/^-+|-+$/g, '')
  return (
    <Dialog title="批准设备" onClose={() => !pending && onClose()}>
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          const name = String(new FormData(event.currentTarget).get('name') ?? '').trim()
          setPending(true)
          setError(undefined)
          try {
            await api.approvePairing(pairing.id, name)
            onApproved()
          } catch (e) {
            setError(e)
            setPending(false)
          }
        }}
      >
        <div className="dialog-body">
          <p>
            配对码 <strong className="pairing-code">{pairing.code}</strong> · {pairing.hostname}
          </p>
          <div className="form-grid">
            <label>
              设备名称
              <input
                name="name"
                required
                defaultValue={suggested}
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
            批准
          </button>
        </div>
      </form>
    </Dialog>
  )
}
