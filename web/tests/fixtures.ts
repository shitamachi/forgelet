import type { Configuration, Delivery, Job, LogChunk, Run, RunState } from '../src/lib/types.ts'

const now = Date.now()
const at = (seconds: number) => new Date(now - seconds * 1000).toISOString()
export function fixtureJob(id: string, state: RunState, key = 'test', needs: string[] = []): Job {
  return {
    id,
    run_id: 'run-success',
    name: key,
    profile: 'homelab',
    pod_name: `forgelet-${id}`,
    state,
    cancel_requested: false,
    created_at: at(360),
    updated_at: at(120),
    plan: {
      key,
      group: key,
      workflow_name: 'Gateway · CI / CD',
      needs,
      timeout_seconds: 1200,
      steps: [
        { id: 'checkout', name: 'Checkout', checkout: {} },
        { id: 'test', name: 'Run tests', run: 'go test ./...' },
      ],
    },
    ...(['success', 'failure'].includes(state)
      ? {
          result: {
            state,
            last_log_seq: 260,
            steps: [
              { id: 'checkout', state: 'success' as const, exit_code: 0 },
              { id: 'test', state, exit_code: state === 'failure' ? 1 : 0 },
            ],
          },
        }
      : {}),
  }
}
export function fixtureRun(id = 'run-success', state: RunState = 'success'): Run {
  const jobs = [
    fixtureJob(`${id}-lint`, 'success', 'lint'),
    fixtureJob(`${id}-test`, state, 'test'),
    fixtureJob(`${id}-build`, state === 'running' ? 'queued' : state, 'build', ['lint', 'test']),
    fixtureJob(
      `${id}-deploy`,
      state === 'running' ? 'queued' : state === 'failure' ? 'skipped' : state,
      'deploy',
      ['build'],
    ),
  ]
  for (const job of jobs) job.run_id = id
  return {
    id,
    delivery_id: `github-${id}`,
    repository: 'homelab/gateway',
    sha: 'f83d205c4141b9d3a514677bf103d81f5ec192f1',
    ref: 'refs/heads/main',
    workflow_path: '.github/workflows/ci.yml',
    installation_id: 12,
    state,
    created_at: at(360),
    jobs,
  }
}
export function fixtureState() {
  const target = {
    name: 'homelab',
    image_name: 'app',
    source_repository: 'homelab/gateway',
    source_ref: 'refs/heads/main',
    repository: 'homelab/gitops',
    branch: 'main',
    path: 'apps/gateway/kustomization.yaml',
    mode: 'direct',
    image: 'registry.homelab/gateway',
    namespace: 'apps',
    deployment: 'gateway',
    container: 'app',
    flux_namespace: 'flux-system',
    kustomization: 'gateway',
    installation_id: 12,
  }
  const config: Configuration = {
    repositories: ['homelab/gateway', 'homelab/notes'],
    profiles: [{ name: 'homelab', image: 'registry.homelab/forgelet-executor:dev', build: true }],
    targets: [target],
    max_parallel: 4,
    features: { github: true, secrets: true, artifacts: true, delivery: true },
    retention: { history: 2592000, logs: 604800, events: 2592000, backups: 604800 },
  }
  const runs = [
    fixtureRun('run-live', 'running'),
    fixtureRun(),
    fixtureRun('run-failure', 'failure'),
    fixtureRun('run-cancelled', 'cancelled'),
    fixtureRun('run-expired'),
  ]
  runs[0].created_at = at(31)
  runs[2].ref = 'refs/heads/feat/health-check'
  for (const job of runs[4].jobs!) job.logs_expired = true
  const docs = fixtureRun('run-docs')
  docs.repository = 'homelab/notes'
  docs.jobs = [fixtureJob('docs', 'success')]
  docs.jobs[0].plan.workflow_name = 'Notes · Publish'
  runs.push(docs)
  const logs: Record<string, LogChunk[]> = {}
  for (const run of runs)
    for (const job of run.jobs!)
      logs[job.id] = Array.from({ length: 260 }, (_, i) => ({
        seq: i + 1,
        step_id: i < 5 ? 'checkout' : 'test',
        data:
          i === 259
            ? 'PASS · 260 chunks persisted\n<svg onload=alert(1)>literal log text</svg>\n'
            : `\x1b[32m${i < 5 ? 'checkout' : 'ok'}\x1b[0m  github.com/homelab/gateway/internal/package_${i}  0.0${i % 9}s\n`,
      }))
  const delivery: Delivery = {
    id: 'delivery-current',
    target,
    run_id: 'run-success',
    job_id: 'run-success-deploy',
    digest: `sha256:${'b'.repeat(64)}`,
    previous_digest: `sha256:${'a'.repeat(64)}`,
    state: 'deployed',
    created_at: at(120),
    updated_at: at(30),
    commit: 'e984f12b' + 'a'.repeat(32),
    revision: 'e984f12b' + 'a'.repeat(32),
    observed_revision: 'e984f12b' + 'a'.repeat(32),
  }
  return {
    config,
    runs,
    logs,
    deliveries: [delivery],
    status: {
      disk: {
        paused: false,
        available_bytes: 8.6 * 1024 ** 3,
        pause_below_bytes: 512 * 1024 ** 2,
        resume_at_bytes: 1024 ** 3,
        checked_at: at(1),
      },
    },
    variables: { DEPLOY_ENV: 'homelab' } as Record<string, string>,
    secrets: ['REGISTRY_TOKEN'],
    events: [
      {
        delivery_id: 'push-f83d205c',
        repository: 'homelab/gateway',
        name: 'push',
        sha: runs[0].sha,
        ref: 'refs/heads/main',
        installation_id: 12,
      },
    ],
    calls: [] as { method: string; path: string; body: unknown }[],
    errors: {} as Record<string, { status: number; body: string }>,
  }
}
