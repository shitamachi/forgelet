import { Link } from '@tanstack/react-router'
import { GitBranch, GitCommitHorizontal } from 'lucide-react'
import type { Run } from '../lib/types'
import { branch, date, duration, shortSHA, workflowName } from '../lib/model'
import { Empty, Status } from './ui'

export function RunTable({ runs }: { runs: Run[] }) {
  if (!runs.length) return <Empty title="还没有运行记录">推送仓库代码，或手动创建一次运行。</Empty>
  return (
    <div className="table-scroll">
      <table className="run-table">
        <thead>
          <tr>
            <th>Workflow / 仓库</th>
            <th>状态</th>
            <th>分支 / 提交</th>
            <th>耗时</th>
            <th>创建时间</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.id}>
              <td>
                <Link to="/runs/$runId" params={{ runId: run.id }} className="row-title">
                  {workflowName(run)}
                </Link>
                <div className="row-secondary">{run.repository}</div>
              </td>
              <td>
                <Status state={run.state} />
                <div
                  className="job-dots"
                  role="img"
                  aria-label={`${run.jobs?.filter((j) => j.state === 'success').length || 0} / ${run.jobs?.length || 0} 个任务成功`}
                >
                  {run.jobs?.slice(0, 24).map((j) => (
                    <span
                      key={j.id}
                      className={`job-dot dot-${j.state}`}
                      title={`${j.name}: ${j.state}`}
                    />
                  ))}
                </div>
              </td>
              <td>
                <div className="with-icon">
                  <GitBranch size={14} />
                  <span className="truncate branch-name">{branch(run.ref)}</span>
                </div>
                <div className="row-secondary with-icon">
                  <GitCommitHorizontal size={14} />
                  <code>{shortSHA(run.sha)}</code>
                </div>
              </td>
              <td className="tabular">{duration(run)}</td>
              <td>
                <time title={run.created_at}>{date(run.created_at)}</time>
                <div className="row-secondary">
                  <code>#{run.id.slice(0, 8)}</code>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
