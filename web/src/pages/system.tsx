import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Archive, Check, Database, HardDrive, LoaderCircle, RefreshCw, Trash2 } from 'lucide-react'
import { api } from '../lib/api'
import { queries, queryClient } from '../lib/queries'
import { bytes, shortSHA } from '../lib/model'
import { Confirm, Empty, ErrorPanel, Loading, PageHeader, useToast } from '../components/ui'
export default function System() {
  const status = useQuery(queries.status)
  const config = useQuery(queries.config)
  const events = useQuery(queries.events)
  const [operation, setOperation] = useState<'backup' | 'maintenance' | null>(null)
  const [backup, setBackup] = useState('')
  const [cleaned, setCleaned] = useState<Record<string, number> | null>(null)
  const toast = useToast()
  const disk = status.data?.disk
  async function operate() {
    if (operation === 'backup') {
      const result = await api.backup()
      setBackup(result.file)
      toast('数据库备份已创建')
    } else {
      setCleaned(await api.maintenance())
      toast('保留清理已执行')
      await queryClient.invalidateQueries()
    }
  }
  return (
    <>
      <PageHeader
        title="系统"
        description="检查磁盘保护、执行环境与事件诊断。"
        actions={
          <button className="button" onClick={() => void queryClient.invalidateQueries()}>
            <RefreshCw size={15} />
            刷新
          </button>
        }
      />
      {status.isPending ? (
        <Loading />
      ) : status.isError ? (
        <ErrorPanel error={status.error} retry={() => void status.refetch()} />
      ) : (
        disk && (
          <section className="panel disk-panel">
            <div className="section-header">
              <h2>
                <HardDrive size={19} />
                数据盘保护
              </h2>
              <span className={`health-badge ${disk.paused ? 'health-warning' : ''}`}>
                {disk.paused ? <LoaderCircle size={15} /> : <Check size={15} />}{' '}
                {disk.paused ? '暂停新任务' : '允许调度'}
              </span>
            </div>
            <div className="disk-body">
              <div>
                <span className="disk-number">{bytes(disk.available_bytes)}</span>
                <p>当前进程可用空间</p>
              </div>
              <dl>
                <dt>低于此值暂停</dt>
                <dd>{bytes(disk.pause_below_bytes)}</dd>
                <dt>达到此值恢复</dt>
                <dd>{bytes(disk.resume_at_bytes)}</dd>
                <dt>最后检测</dt>
                <dd>
                  {disk.checked_at.startsWith('0001')
                    ? '等待首次检测'
                    : new Date(disk.checked_at).toLocaleTimeString('zh-CN', { hour12: false })}
                </dd>
              </dl>
            </div>
            <p className="section-note">
              只暂停新任务；运行中的任务、日志、取消和保留清理继续执行。两个阈值之间保持已有状态，重启后需达到恢复阈值。
            </p>
            {disk.error && (
              <div className="alert alert-warning">
                <p>{disk.error}</p>
              </div>
            )}
          </section>
        )
      )}
      <div className="detail-columns">
        <section className="panel detail-panel">
          <h2>
            <Database size={18} />
            保留与维护
          </h2>
          {config.isError ? (
            <ErrorPanel error={config.error} />
          ) : (
            <>
              <dl>
                {Object.entries({
                  history: '运行历史',
                  logs: '执行日志',
                  events: '事件 payload',
                  backups: '数据库备份',
                }).map(([key, label]) => (
                  <div className="definition-row" key={key}>
                    <dt>{label}</dt>
                    <dd>
                      {config.data
                        ? config.data.retention[key as keyof typeof config.data.retention] / 86400
                        : '—'}{' '}
                      天
                    </dd>
                  </div>
                ))}
              </dl>
              <div className="maintenance-actions">
                <button className="button" onClick={() => setOperation('backup')}>
                  <Archive size={15} />
                  创建数据库备份
                </button>
                <button className="button" onClick={() => setOperation('maintenance')}>
                  <Trash2 size={15} />
                  执行保留清理
                </button>
              </div>
            </>
          )}
          {backup && (
            <div className="operation-result" role="status">
              <strong>备份已保存到数据目录</strong>
              <code>{backup}</code>
              <p>此快照包含数据库；产物文件、配置与加密密钥需另行备份。</p>
            </div>
          )}
          {cleaned && (
            <div className="operation-result" role="status">
              <strong>清理完成</strong>
              <p>
                历史 {cleaned.runs} 条，日志 {cleaned.logs} 块，事件 {cleaned.events} 条，备份{' '}
                {cleaned.backups} 个，产物/缓存 {cleaned.blobs} 个，发布 {cleaned.deployments} 条。
              </p>
            </div>
          )}
        </section>
        <section className="panel detail-panel">
          <h2>执行环境</h2>
          <p className="section-note">全局最多 {config.data?.max_parallel ?? '—'} 个并发任务。</p>
          {config.data?.profiles.map((p) => (
            <div className="profile-item" key={p.name}>
              <div className="split">
                <strong>{p.name}</strong>
                {p.build && <span className="tag">BuildKit</span>}
              </div>
              <code>{p.image}</code>
            </div>
          ))}
        </section>
      </div>
      <section className="panel events-panel">
        <div className="section-header">
          <h2>事件诊断</h2>
          <span className="muted">最近 {events.data?.length ?? 0} 条</span>
        </div>
        {events.isPending ? (
          <Loading />
        ) : events.isError ? (
          <ErrorPanel error={events.error} retry={() => void events.refetch()} />
        ) : !events.data.length ? (
          <Empty title="还没有接收事件">GitHub push / PR 事件的处理结果会显示在这里。</Empty>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>仓库 / 事件</th>
                  <th>提交</th>
                  <th>处理结果</th>
                </tr>
              </thead>
              <tbody>
                {events.data.map((e) => (
                  <tr key={e.delivery_id}>
                    <td>
                      <strong>{e.repository}</strong>
                      <div className="row-secondary">
                        {e.name || 'push'} · {e.delivery_id}
                      </div>
                    </td>
                    <td>
                      <code>{shortSHA(e.sha)}</code>
                    </td>
                    <td>
                      {e.error ? (
                        <span className="event-error">{e.error}</span>
                      ) : (
                        <span className="muted">已接收</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {operation && (
        <Confirm
          title={operation === 'backup' ? '创建数据库备份？' : '执行保留清理？'}
          label={operation === 'backup' ? '创建备份' : '执行清理'}
          onClose={() => setOperation(null)}
          onConfirm={operate}
        >
          <p>
            {operation === 'backup'
              ? '在数据目录生成一致的 SQLite 快照。当前运行可继续，快照会额外占用磁盘空间。'
              : '按当前保留策略删除到期数据，活跃任务会受到保护。未到期的数据会继续保留。'}
          </p>
        </Confirm>
      )}
    </>
  )
}
