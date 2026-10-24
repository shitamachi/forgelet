import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { GitBranch, LogOut } from 'lucide-react'
import { api } from '../lib/api'
import { queryClient, authQuery } from '../lib/queries'
import { ErrorPanel, PageHeader } from '../components/ui'

export default function Account() {
  const session = useQuery(authQuery)
  const [error, setError] = useState<unknown>()
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  if (!session.data || session.data.mode !== 'session')
    return (
      <>
        <PageHeader title="账号" />
        <p>
          当前部署仍使用 {session.data?.mode || '旧版'} 管理模式。启用 session 模式后使用账号登录。
        </p>
      </>
    )
  return (
    <>
      <PageHeader
        title="账号"
        description="管理员密码、GitHub 登录与代码源授权。"
        actions={
          <button
            className="button"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError(undefined)
              try {
                await api.logout()
                await queryClient.cancelQueries({
                  predicate: (query) => query.queryKey[0] !== 'auth-session',
                })
                queryClient.removeQueries({
                  predicate: (query) => query.queryKey[0] !== 'auth-session',
                })
                queryClient.setQueryData(['auth-session'], { ...session.data, user: null })
              } catch (e) {
                setError(e)
                setBusy(false)
              }
            }}
          >
            <LogOut size={16} />
            退出登录
          </button>
        }
      />
      <div className="account-grid">
        <section className="panel account-panel">
          <h2>管理员</h2>
          <p>{session.data.user?.username}</p>
          <form
            className="form-stack"
            onSubmit={async (e) => {
              e.preventDefault()
              const form = e.currentTarget
              const values = new FormData(form)
              if (values.get('new') !== values.get('confirm')) {
                setError(new Error('两次新密码不一致'))
                return
              }
              setBusy(true)
              setError(undefined)
              setNotice('')
              try {
                await api.password(String(values.get('current')), String(values.get('new')))
                form.reset()
                setNotice('密码已更新，其他登录会话已撤销。')
              } catch (e) {
                setError(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            <label>
              当前密码
              <input
                name="current"
                type="password"
                autoComplete="current-password"
                required
                disabled={busy}
              />
            </label>
            <label>
              新密码
              <input
                name="new"
                type="password"
                autoComplete="new-password"
                minLength={16}
                maxLength={72}
                required
                disabled={busy}
              />
            </label>
            <label>
              确认新密码
              <input
                name="confirm"
                type="password"
                autoComplete="new-password"
                minLength={16}
                maxLength={72}
                required
                disabled={busy}
              />
            </label>
            <button className="button" disabled={busy}>
              更新密码
            </button>
          </form>
        </section>
        <section className="panel account-panel">
          <h2>GitHub</h2>
          <p>
            {session.data.user?.github_login
              ? `已关联 @${session.data.user.github_login}`
              : '尚未关联 GitHub'}
          </p>
          <p className="section-note">
            授权读取个人及组织仓库；启用仓库时，使用你的管理权限将仓库接入现有 GitHub App。CI
            继续使用 App 的 webhook、仓库凭据和 Checks。
          </p>
          <p className="section-note">
            GitHub 会请求 read:user 和 repo 权限；repo
            包含私有仓库及写操作。这里只关联当前管理员，不开放 GitHub 自助注册。
          </p>
          <button
            className="button button-primary"
            disabled={busy || !session.data.github_enabled}
            onClick={async () => {
              setBusy(true)
              setError(undefined)
              try {
                window.location.assign((await api.githubStart(true)).url)
              } catch (e) {
                setError(e)
                setBusy(false)
              }
            }}
          >
            <GitBranch size={16} />
            {session.data.user?.github_id ? '重新授权 GitHub' : '连接 GitHub'}
          </button>
          {!session.data.github_enabled && (
            <p className="section-note">
              请在服务端配置 GitHub OAuth App 的 Client ID 与 Secret 文件；回调地址为本工作台
              /api/auth/github/callback。
            </p>
          )}
        </section>
      </div>
      {error != null && <ErrorPanel error={error} />}{' '}
      {notice && (
        <p role="status" className="alert">
          {notice}
        </p>
      )}
    </>
  )
}
