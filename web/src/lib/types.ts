export type RunState =
  | 'queued'
  | 'starting'
  | 'running'
  | 'success'
  | 'failure'
  | 'cancelled'
  | 'timed_out'
  | 'interrupted'
  | 'skipped'
export type DeliveryState =
  | 'queued'
  | 'preparing'
  | 'awaiting_merge'
  | 'delivered'
  | 'deployed'
  | 'superseded'
  | 'cancelled'
  | 'conflict'
export interface Step {
  id: string
  name?: string
  run?: string
  action?: { uses: string }
  checkout?: { path?: string }
  if?: string
}
export interface StepResult {
  id: string
  state: RunState
  outcome?: RunState
  exit_code: number
  outputs?: Record<string, string>
}
export interface Job {
  id: string
  run_id: string
  name: string
  profile: string
  pod_name: string
  pod_uid?: string
  state: RunState
  cancel_requested: boolean
  created_at: string
  updated_at: string
  logs_expired?: boolean
  plan: {
    key: string
    group?: string
    workflow_name?: string
    needs?: string[]
    steps: Step[]
    matrix?: Record<string, unknown>
    timeout_seconds: number
  }
  result?: {
    state: RunState
    error?: string
    steps?: StepResult[]
    outputs?: Record<string, string>
    last_log_seq: number
  }
}
export interface Run {
  id: string
  delivery_id: string
  repository: string
  sha: string
  ref: string
  workflow_path: string
  installation_id: number
  state: RunState
  created_at: string
  jobs?: Job[]
}
export interface DiskStatus {
  paused: boolean
  reason?: string
  available_bytes?: number
  pause_below_bytes: number
  resume_at_bytes: number
  checked_at: string
  error?: string
}
export interface Target {
  image_name: string
  git_url?: string
  name: string
  repository: string
  source_repository: string
  source_ref: string
  branch: string
  path: string
  mode: string
  image: string
  namespace: string
  deployment: string
  container: string
  flux_namespace: string
  kustomization: string
  installation_id: number
}
export interface Delivery {
  id: string
  target: Target
  run_id: string
  job_id: string
  digest: string
  state: DeliveryState
  created_at: string
  updated_at: string
  commit?: string
  revision?: string
  observed_revision?: string
  previous_digest?: string
  error?: string
  url?: string
  rollback_of?: string
}
export interface Configuration {
  repositories: string[]
  profiles: { name: string; image: string; build: boolean }[]
  targets: Target[]
  max_parallel: number
  features: { github: boolean; secrets: boolean; artifacts: boolean; delivery: boolean }
  retention: { history: number; logs: number; events: number; backups: number }
}
export interface LogChunk {
  seq: number
  step_id: string
  data: string
}
export interface Artifact {
  id: string
  key: string
  digest: string
  size: number
  created_at: string
  expires_at: string
}
export interface Event {
  delivery_id: string
  repository: string
  sha: string
  ref: string
  name?: string
  error?: string
}
export interface ManualRun {
  repository: string
  sha: string
  ref: string
  workflow: string
  workflow_path: string
  installation_id: number
  event_name?: string
  inputs?: Record<string, unknown>
}

export interface RepositoryConfig {
  repository_id?: number
  name: string
  installation_id: number
  enabled: boolean
}
export interface WorkflowConfig {
  repository: string
  path: string
  enabled: boolean
  source: string
}
export interface Management {
  version: number
  repositories: RepositoryConfig[]
  workflows: WorkflowConfig[]
  targets: Target[]
}

export interface GitHubRepository {
  id: number
  full_name: string
  installation_id: number
  default_branch: string
  private: boolean
  archived: boolean
  disabled: boolean
}
export interface GitHubConnection {
  app: string
  install_url: string
  checked_at: string
  installations: {
    id: number
    account: { login: string; type: string }
    repository_selection: string
    suspended_at: string | null
    permissions: Record<string, string>
    manage_url: string
  }[]
  repositories: GitHubRepository[]
}
export interface GitHubPreview {
  repository: GitHubRepository
  sha: string
  ref: string
  workflows: {
    path: string
    source: string
    diagnostic: string
    runnable: boolean
    jobs: number
  }[]
}

export interface AuthSession {
  mode: 'open' | 'token' | 'session'
  github_enabled: boolean
  user: { id: number; username: string; github_id?: number; github_login?: string } | null
}
export interface GitHubUserRepository extends GitHubRepository {
  owner: { login: string }
  permissions: { admin: boolean }
}

export interface RepositoryAuthorization {
  status: 'authorization_required'
  reason:
    | 'repository_not_granted'
    | 'installation_required'
    | 'installation_suspended'
    | 'installation_permissions'
  repository_id: number
  repository: string
  authorize_url: string
}
