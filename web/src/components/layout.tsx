import { useState } from 'react'
import { Link, Outlet, useRouterState } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  ArrowUpRight,
  Boxes,
  ChevronRight,
  CirclePlay,
  HardDrive,
  LayoutDashboard,
  Menu,
  Rocket,
  Settings2,
  X,
} from 'lucide-react'
import { queries } from '../lib/queries'
import { bytes } from '../lib/model'
import { ToastProvider } from './ui'

const links = [
  { to: '/', label: '工作台', icon: LayoutDashboard },
  { to: '/runs', label: '运行记录', icon: CirclePlay },
  { to: '/deployments', label: '发布', icon: Rocket },
  { to: '/connections', label: '仓库接入', icon: Boxes },
  { to: '/workflows', label: 'Workflows', icon: CirclePlay },
  { to: '/targets', label: '发布目标', icon: Rocket },
  { to: '/repositories', label: '仓库设置', icon: Boxes },
  { to: '/system', label: '系统', icon: Settings2 },
  { to: '/account', label: '账号', icon: Settings2 },
] as const
function Brand() {
  return (
    <Link to="/" className="brand" aria-label="forgelet 工作台">
      <svg width="32" height="32" viewBox="0 0 32 32" fill="none" aria-hidden="true">
        <rect width="32" height="32" rx="9" fill="currentColor" />
        <path d="M10 23V9h13M10 16h9" stroke="white" strokeWidth="3" strokeLinecap="round" />
        <circle cx="22" cy="16" r="2" fill="#a7b5ff" />
        <circle cx="23" cy="9" r="2" fill="white" />
      </svg>
      <span>forgelet</span>
    </Link>
  )
}
export default function Layout() {
  const [open, setOpen] = useState(false)
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const status = useQuery(queries.status)
  const config = useQuery(queries.config)
  const disk = status.data?.disk
  const current = links.find((l) => l.to !== '/' && pathname.startsWith(l.to)) || links[0]
  return (
    <ToastProvider>
      <a className="skip-link" href="#main">
        跳到主要内容
      </a>
      <div className="app-shell">
        {open && (
          <button aria-label="关闭导航" className="nav-scrim" onClick={() => setOpen(false)} />
        )}
        <aside className={`sidebar ${open ? 'sidebar-open' : ''}`}>
          <div className="brand-row">
            <Brand />
            <button
              className="icon-button mobile-only"
              aria-label="关闭导航"
              onClick={() => setOpen(false)}
            >
              <X size={18} />
            </button>
          </div>
          <div className="workspace-name">
            <span className="workspace-dot" />
            <span>Homelab</span>
            <span className="workspace-tag">CI/CD</span>
          </div>
          <nav aria-label="主导航">
            {links.map(({ to, label, icon: Icon }) => (
              <Link
                key={to}
                to={to}
                activeOptions={{ exact: to === '/' }}
                onClick={() => setOpen(false)}
                className="nav-link"
                activeProps={{ className: 'nav-link nav-link-active' }}
              >
                <Icon size={18} />
                <span>{label}</span>
                {to === '/runs' && <span className="nav-hint">CI</span>}
                {to === '/deployments' && <span className="nav-hint">CD</span>}
              </Link>
            ))}
          </nav>
          <div className="sidebar-bottom">
            <div className="engine-line">
              <span
                className={`live-dot ${status.isError ? 'live-offline' : disk?.paused ? 'live-warning' : ''}`}
              />
              <span>
                {status.isError
                  ? '连接中断'
                  : disk?.paused
                    ? '新任务已暂停'
                    : status.isPending
                      ? '连接中…'
                      : '调度正常'}
              </span>
            </div>
            <div className="sidebar-detail">
              <HardDrive size={14} />
              {bytes(disk?.available_bytes)} 可用
            </div>
            <div className="sidebar-detail">
              <Activity size={14} />
              {config.data?.max_parallel ?? '—'} 个并发任务
            </div>
          </div>
        </aside>
        <div className="main-shell">
          <div className="topbar">
            <div className="breadcrumb">
              <button
                className="icon-button mobile-only"
                aria-label="打开导航"
                onClick={() => setOpen(true)}
              >
                <Menu size={20} />
              </button>
              <span>Homelab</span>
              <ChevronRight size={14} />
              <strong>{current.label}</strong>
            </div>
            <a
              href="https://github.com/shitamachi/forgelet"
              target="_blank"
              rel="noreferrer"
              className="quiet-link"
            >
              forgelet
              <ArrowUpRight size={14} />
            </a>
          </div>
          <main id="main" className="main-content">
            {disk?.paused && (
              <div className="disk-banner" role="alert">
                <HardDrive size={20} />
                <div>
                  <strong>新任务调度已暂停</strong>
                  <p>
                    {disk.reason === 'disk_check_failed'
                      ? '无法检测数据盘空间。'
                      : `当前可用 ${bytes(disk.available_bytes)}。`}
                    空间达到 {bytes(disk.resume_at_bytes)} 后自动恢复，运行中的任务继续执行。
                  </p>
                </div>
                <Link to="/system">
                  查看磁盘状态
                  <ChevronRight size={16} />
                </Link>
              </div>
            )}
            <Outlet />
          </main>
          <footer className="footer">
            <span>forgelet / homelab</span>
            <span>{status.isError ? '连接中断，正在重试' : '运行状态自动更新'}</span>
          </footer>
        </div>
      </div>
    </ToastProvider>
  )
}
