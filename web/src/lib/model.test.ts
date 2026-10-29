import { describe, expect, it } from 'vitest'
import { fixtureJob, fixtureRun } from '../../tests/fixtures'
import {
  branch,
  bytes,
  duration,
  elapsed,
  jobStage,
  filterRuns,
  isActive,
  jobLayers,
  logText,
  mergeLogs,
  shortSHA,
  stepState,
} from './model'
describe('run presentation', () => {
  it.each(['queued', 'starting', 'running'])('recognizes active %s', (state) =>
    expect(isActive(state)).toBe(true),
  )
  it.each(['success', 'failure', 'cancelled', 'timed_out', 'interrupted', 'skipped'])(
    'recognizes terminal %s',
    (state) => expect(isActive(state)).toBe(false),
  )
  it('combines search, state and repository filters', () => {
    const success = fixtureRun(),
      fail = fixtureRun('failed', 'failure')
    fail.repository = 'other/repo'
    expect(filterRuns([success, fail], 'gateWay', 'success', 'homelab/gateway')).toEqual([success])
    expect(filterRuns([success, fail], success.sha.slice(0, 6), 'all', 'all')).toHaveLength(2)
    expect(filterRuns([success, fail], 'failed', 'failure', 'other/repo')).toEqual([fail])
    expect(filterRuns([success], 'missing', 'all', 'all')).toEqual([])
  })
  it('formats elapsed time from completion, never from unrelated wall clock', () => {
    const run = fixtureRun()
    run.created_at = '2026-10-23T00:00:00Z'
    for (const job of run.jobs!) job.updated_at = '2026-10-23T00:01:05Z'
    expect(duration(run, Date.parse('2026-10-24T00:00:00Z'))).toBe('1m 5s')
    run.state = 'running'
    expect(duration(run, Date.parse('2026-10-23T01:02:00Z'))).toBe('1h 2m')
    expect(shortSHA('sha256:abcdef123456')).toBe('abcdef12')
    expect(branch('refs/heads/fix/logs')).toBe('fix/logs')
    expect(bytes(1024 ** 3)).toBe('1 GiB')
    expect(bytes(undefined)).toBe('待检测')
    expect(bytes(0)).toBe('0 B')
  })
})
describe('dependency graph and step results', () => {
  it('waits for every matrix variant even when jobs arrive out of order', () => {
    const first = fixtureJob('linux', 'success', 'test-0'),
      second = fixtureJob('arm', 'success', 'test-1')
    first.plan.group = second.plan.group = 'test'
    const deploy = fixtureJob('deploy', 'queued', 'deploy', ['build'])
    const build = fixtureJob('build', 'queued', 'build', ['test'])
    expect(jobLayers([deploy, second, build, first]).map((l) => l.map((j) => j.id))).toEqual([
      ['arm', 'linux'],
      ['build'],
      ['deploy'],
    ])
  })
  it('keeps malformed legacy graphs bounded and preserves every job', () => {
    const a = fixtureJob('a', 'queued', 'a', ['b']),
      b = fixtureJob('b', 'queued', 'b', ['a'])
    expect(jobLayers([a, b]).flat()).toEqual([a, b])
    expect(jobLayers([])).toEqual([])
  })
  it('does not invent live step progress from overall job state', () => {
    expect(stepState(fixtureJob('active', 'running'), 'checkout')).toBe('pending')
    expect(stepState(fixtureJob('failed', 'failure'), 'test')).toBe('failure')
  })
})
describe('log cursor', () => {
  it('merges replayed pages without duplicates and preserves order', () => {
    const chunk = (seq: number) => ({ seq, step_id: 'test', data: `line${seq}\n` })
    expect(mergeLogs([chunk(1), chunk(2)], [chunk(4), chunk(2), chunk(3)])).toEqual([
      chunk(1),
      chunk(2),
      chunk(3),
      chunk(4),
    ])
    expect(mergeLogs([chunk(1)], [])).toEqual([chunk(1)])
  })
  it('removes control sequences across chunks while leaving markup as text', () => {
    expect(
      logText([
        { seq: 1, step_id: 's', data: '\x1b[3' },
        { seq: 2, step_id: 's', data: '2mok\x1b[0m\n<svg>literal</svg>' },
      ]),
    ).toBe('ok\n<svg>literal</svg>')
  })
})

describe('live execution progress', () => {
  it('uses final step results over provisional observations', () => {
    const job = fixtureJob('progress', 'running')
    job.progress = {
      seq: 2,
      phase: 'executing',
      last_at: '2026-10-29T00:00:01Z',
      steps: [
        { id: 'observed', state: 'running' },
        { id: 'failed', state: 'failure' },
      ],
    }
    job.state = 'running'
    expect(stepState(job, 'observed')).toBe('running')
    job.result = {
      state: 'success',
      last_log_seq: 0,
      steps: [{ id: 'failed', state: 'success', outcome: 'failure', exit_code: 1 }],
    }
    expect(stepState(job, 'failed')).toBe('success')
    job.state = 'interrupted'
    expect(stepState(job, 'observed')).toBe('interrupted')
    job.state = 'success'
    expect(stepState(job, 'observed')).toBe('pending')
  })
})

it('keeps missing historical times unknown and identifies execution phases', () => {
  const job = fixtureJob('progress', 'starting')
  expect(jobStage(job)).toBe('正在创建执行环境')
  job.pod_uid = 'owned-pod'
  expect(jobStage(job)).toBe('等待 Kubernetes 启动')
  expect(elapsed(undefined)).toBe('—')
  expect(elapsed('invalid')).toBe('—')
  expect(elapsed('2026-10-29T00:00:00Z', '2026-10-28T00:00:00Z')).toBe('—')
  expect(elapsed('2026-10-29T00:00:00Z', '2026-10-29T00:02:03Z')).toBe('2m 3s')
})
