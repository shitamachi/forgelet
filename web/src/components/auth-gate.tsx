import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { LogIn, KeyRound, GitBranch } from 'lucide-react'
import { api } from '../lib/api'
import { queryClient, authQuery } from '../lib/queries'
import { ErrorPanel, Loading } from './ui'

export function AuthGate({ children }: { children: ReactNode }) {
  const session = useQuery(authQuery)
  useEffect(() => {
    const refresh = () => {
      void session.refetch()
    }
    window.addEventListener('forgelet-session-expired', refresh)
    return () => window.removeEventListener('forgelet-session-expired', refresh)
  }, [session.refetch])
  if (session.isPending) return <Loading label="正在确认登录状态…" />
  if (session.isError)
    return <ErrorPanel error={session.error} retry={() => void session.refetch()} />
  if (session.data.mode === 'session' && !session.data.user)
    return <Login github={session.data.github_enabled} />
  return children
}
function Login({ github }: { github: boolean }) {
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  async function signIn(form: HTMLFormElement) {
    setBusy(true)
    setError(undefined)
    const values = new FormData(form)
    try {
      const session = await api.login(
        String(values.get('username')),
        String(values.get('password')),
      )
      await queryClient.cancelQueries({
        predicate: (query) => query.queryKey[0] !== 'auth-session',
      })
      queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== 'auth-session' })
      queryClient.setQueryData(['auth-session'], session)
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <main className="login-shell">
      <div className="login-intro">
        <div className="login-brand">forgelet</div>
        <h1>登录你的 Homelab</h1>
        <p>连接源码，执行 CI，交付到集群。</p>
        <div className="login-pipeline">
          GitHub <span>→</span> CI <span>→</span> GitOps <span>→</span> k3s
        </div>
      </div>
      <section className="panel login-panel">
        <h2>
          <KeyRound size={20} />
          管理员登录
        </h2>
        <p className="section-note">
          首次启动会初始化 admin，初始密码保存在服务端数据目录的 initial-admin-password
          文件，或由安装时的 Secret 指定。
        </p>
        <form
          className="form-stack"
          onSubmit={(e) => {
            e.preventDefault()
            void signIn(e.currentTarget)
          }}
        >
          <label>
            用户名
            <input
              name="username"
              autoComplete="username"
              defaultValue="admin"
              required
              disabled={busy}
            />
          </label>
          <label>
            密码
            <input
              name="password"
              type="password"
              autoComplete="current-password"
              required
              maxLength={72}
              disabled={busy}
            />
          </label>
          <button className="button button-primary" disabled={busy}>
            <LogIn size={16} />
            {busy ? '登录中…' : '登录'}
          </button>
        </form>
        {github && (
          <>
            <div className="login-divider">GitHub 登录</div>
            <button
              className="button"
              disabled={busy}
              onClick={async () => {
                setBusy(true)
                setError(undefined)
                try {
                  window.location.assign((await api.githubStart(false)).url)
                } catch (e) {
                  setError(e)
                  setBusy(false)
                }
              }}
            >
              <GitBranch size={16} />
              使用 GitHub 登录
            </button>
            <p className="section-note">首次关联请先用 admin 登录，再到账号页面连接 GitHub。</p>
          </>
        )}
        {error != null && <ErrorPanel error={error} />}
      </section>
    </main>
  )
}
