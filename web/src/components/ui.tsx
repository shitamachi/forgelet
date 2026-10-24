import { useEffect, useRef, useState, createContext, useContext } from 'react'
import type { ReactNode } from 'react'
import {
  Check,
  Circle,
  CircleCheck,
  CircleX,
  Clock3,
  GitPullRequest,
  LoaderCircle,
  Minus,
  RotateCcw,
  TriangleAlert,
  X,
} from 'lucide-react'
import { errorMessage } from '../lib/api'

const statusLabels: Record<string, string> = {
  queued: '排队中',
  starting: '启动中',
  running: '运行中',
  success: '成功',
  failure: '失败',
  cancelled: '已取消',
  timed_out: '已超时',
  interrupted: '已中断',
  skipped: '已跳过',
  preparing: '准备发布',
  awaiting_merge: '等待合并',
  delivered: '等待部署',
  deployed: '已部署',
  superseded: '已被替代',
  conflict: '发布冲突',
  pending: '未开始',
}
export function Status({ state, small = false }: { state: string; small?: boolean }) {
  const Icon = ['success', 'deployed'].includes(state)
    ? CircleCheck
    : ['failure', 'conflict', 'interrupted', 'timed_out'].includes(state)
      ? CircleX
      : ['starting', 'running', 'preparing', 'delivered'].includes(state)
        ? LoaderCircle
        : state === 'awaiting_merge'
          ? GitPullRequest
          : state === 'queued'
            ? Clock3
            : state === 'pending'
              ? Circle
              : Minus
  return (
    <span className={`status status-${state} ${small ? 'status-small' : ''}`}>
      <Icon
        size={small ? 14 : 15}
        className={['running', 'starting', 'preparing'].includes(state) ? 'spin' : ''}
      />
      <span>{statusLabels[state] || state}</span>
    </span>
  )
}
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string
  description?: ReactNode
  actions?: ReactNode
}) {
  return (
    <header className="page-header">
      <div>
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  )
}
export function Loading({ label = '正在加载…' }: { label?: string }) {
  return (
    <div role="status" className="empty loading">
      <LoaderCircle className="spin" size={22} />
      <p>{label}</p>
    </div>
  )
}
export function ErrorPanel({ error, retry }: { error: unknown; retry?: () => void }) {
  return (
    <div role="alert" className="alert alert-error">
      <TriangleAlert size={19} />
      <div>
        <strong>暂时无法完成请求</strong>
        <p>{errorMessage(error)}</p>
      </div>
      {retry && (
        <button className="button button-small" onClick={retry}>
          <RotateCcw size={14} />
          重试
        </button>
      )}
    </div>
  )
}
export function Empty({
  title,
  children,
  action,
}: {
  title: string
  children?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="empty">
      <div className="empty-mark">
        <Circle size={24} />
      </div>
      <h3>{title}</h3>
      {children && <p>{children}</p>}
      {action}
    </div>
  )
}
export function Dialog({
  title,
  children,
  onClose,
}: {
  title: string
  children: ReactNode
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const dialog = ref.current!
    dialog.showModal()
    return () => dialog.close()
  }, [])
  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-label={title}
      onCancel={(event) => {
        event.preventDefault()
        onClose()
      }}
    >
      <div className="dialog-header">
        <h2>{title}</h2>
        <button aria-label="关闭对话框" className="icon-button" onClick={onClose}>
          <X size={20} />
        </button>
      </div>
      {children}
    </dialog>
  )
}
export function Confirm({
  title,
  children,
  label,
  onConfirm,
  onClose,
}: {
  title: string
  children: ReactNode
  label: string
  onConfirm: () => Promise<unknown>
  onClose: () => void
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  async function submit() {
    setPending(true)
    setError(undefined)
    try {
      await onConfirm()
      onClose()
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog
      title={title}
      onClose={() => {
        if (!pending) onClose()
      }}
    >
      <div className="dialog-body">
        {children}
        {error != null && <ErrorPanel error={error} />}
      </div>
      <div className="dialog-footer">
        <button className="button" disabled={pending} onClick={onClose}>
          返回
        </button>
        <button className="button button-danger" disabled={pending} onClick={() => void submit()}>
          {pending && <LoaderCircle size={15} className="spin" />}
          {label}
        </button>
      </div>
    </Dialog>
  )
}
const ToastContext = createContext<(message: string) => void>(() => {})
export function ToastProvider({ children }: { children: ReactNode }) {
  const [message, setMessage] = useState('')
  useEffect(() => {
    if (!message) return
    const timer = setTimeout(() => setMessage(''), 6000)
    return () => clearTimeout(timer)
  }, [message])
  return (
    <ToastContext value={setMessage}>
      {children}
      <div role="status" aria-live="polite" className={`toast ${message ? 'toast-visible' : ''}`}>
        {message && (
          <>
            <Check size={17} />
            {message}
            <button aria-label="关闭通知" onClick={() => setMessage('')}>
              <X size={15} />
            </button>
          </>
        )}
      </div>
    </ToastContext>
  )
}
export const useToast = () => useContext(ToastContext)
