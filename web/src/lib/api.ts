import type {
  Artifact,
  Configuration,
  Delivery,
  DiskStatus,
  Event,
  LogChunk,
  ManualRun,
  Run,
} from './types'

export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
    this.name = 'APIError'
  }
}
export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`/api${path}`, {
    ...init,
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      ...(init.method && init.method !== 'GET' ? { 'Content-Type': 'application/json' } : {}),
      ...init.headers,
    },
  })
  if (!response.ok) {
    if (response.status === 401 && !path.startsWith('/auth/') && typeof window !== 'undefined')
      window.dispatchEvent(new Event('forgelet-session-expired'))
    const detail = (await response.text()).trim().slice(0, 1500)
    throw new APIError(response.status, detail || `请求失败（${response.status}）`)
  }
  return response.json() as Promise<T>
}
const part = encodeURIComponent
export const api = {
  retryChecks: (id: string) =>
    request(`/runs/${part(id)}/checks/retry`, { method: 'POST', body: '{}' }),
  session: (signal?: AbortSignal) =>
    request<import('./types').AuthSession>('/auth/session', { signal }),
  login: (username: string, password: string) =>
    request<import('./types').AuthSession>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  logout: () => request('/auth/logout', { method: 'POST', body: '{}' }),
  password: (current_password: string, new_password: string) =>
    request('/auth/password', {
      method: 'POST',
      body: JSON.stringify({ current_password, new_password }),
    }),
  githubStart: (link: boolean) =>
    request<{ url: string }>('/auth/github/start', {
      method: 'POST',
      body: JSON.stringify({ link }),
    }),
  githubUserRepositories: (signal?: AbortSignal) =>
    request<import('./types').GitHubUserRepository[]>('/github/user/repositories', { signal }),
  githubUserEnable: (repository_id: number, version: number) =>
    request<import('./types').Management | import('./types').RepositoryAuthorization>(
      '/github/user/enable',
      {
        method: 'POST',
        body: JSON.stringify({ repository_id, version }),
      },
    ),
  githubConnection: (signal?: AbortSignal) =>
    request<import('./types').GitHubConnection>('/github/connection', { signal }),
  githubEnable: (repository_id: number, version: number) =>
    request<import('./types').Management>('/github/enable', {
      method: 'POST',
      body: JSON.stringify({ repository_id, version }),
    }),
  githubPreview: (repository_id: number, event_name: string, inputs: Record<string, unknown>) =>
    request<import('./types').GitHubPreview>('/github/preview', {
      method: 'POST',
      body: JSON.stringify({ repository_id, event_name, inputs }),
    }),
  githubRun: (value: {
    repository_id: number
    sha: string
    ref: string
    path: string
    event_name: string
    inputs: Record<string, unknown>
    request_id: string
  }) => request<Run>('/github/run', { method: 'POST', body: JSON.stringify(value) }),
  management: (signal?: AbortSignal) =>
    request<import('./types').Management>('/management', { signal }),
  saveManagement: (value: import('./types').Management) =>
    request<import('./types').Management>('/management', {
      method: 'PUT',
      body: JSON.stringify(value),
    }),
  inspect: (repository: string, sha: string) =>
    request<import('./types').WorkflowConfig[]>('/management/inspect', {
      method: 'POST',
      body: JSON.stringify({ repository, sha }),
    }),
  config: (signal?: AbortSignal) => request<Configuration>('/config', { signal }),
  status: (signal?: AbortSignal) => request<{ disk: DiskStatus }>('/status', { signal }),
  runs: async (signal?: AbortSignal) => (await request<Run[] | null>('/runs', { signal })) ?? [],
  run: (id: string, signal?: AbortSignal) => request<Run>(`/runs/${part(id)}`, { signal }),
  create: (input: ManualRun) =>
    request<Run>('/runs', { method: 'POST', body: JSON.stringify(input) }),
  cancel: (id: string) =>
    request<{ cancel_requested: boolean }>(`/runs/${part(id)}/cancel`, {
      method: 'POST',
      body: '{}',
    }),
  rerun: (id: string) => request<Run>(`/runs/${part(id)}/rerun`, { method: 'POST', body: '{}' }),
  logs: async (id: string, after: number, signal?: AbortSignal) =>
    (await request<LogChunk[] | null>(`/jobs/${part(id)}/logs?after=${after}`, { signal })) ?? [],
  artifacts: async (id: string, signal?: AbortSignal) =>
    (await request<Artifact[] | null>(`/runs/${part(id)}/artifacts`, { signal })) ?? [],
  artifactURL: (id: string, name: string) => `/api/runs/${part(id)}/artifacts/${part(name)}`,
  deliveries: async (signal?: AbortSignal) =>
    (await request<Delivery[] | null>('/deployments', { signal })) ?? [],
  delivery: (id: string, signal?: AbortSignal) =>
    request<Delivery>(`/deployments/${part(id)}`, { signal }),
  rollback: (id: string) =>
    request<Delivery>(`/deployments/${part(id)}/rollback`, { method: 'POST', body: '{}' }),
  events: async (signal?: AbortSignal) =>
    (await request<Event[] | null>('/events', { signal })) ?? [],
  vars: (repo: string, signal?: AbortSignal) =>
    request<Record<string, string>>(`/repositories/${repo.split('/').map(part).join('/')}/vars`, {
      signal,
    }),
  secrets: (repo: string, signal?: AbortSignal) =>
    request<{ names: string[] }>(`/repositories/${repo.split('/').map(part).join('/')}/secrets`, {
      signal,
    }),
  saveSetting: (repo: string, kind: 'vars' | 'secrets', name: string, value: string) =>
    request(`/repositories/${repo.split('/').map(part).join('/')}/${kind}/${part(name)}`, {
      method: 'PUT',
      body: JSON.stringify({ value }),
    }),
  deleteSetting: (repo: string, kind: 'vars' | 'secrets', name: string) =>
    request(`/repositories/${repo.split('/').map(part).join('/')}/${kind}/${part(name)}`, {
      method: 'DELETE',
      body: '{}',
    }),
  backup: () =>
    request<{ file: string; restore: string }>('/backup', { method: 'POST', body: '{}' }),
  maintenance: () =>
    request<Record<string, number>>('/maintenance', { method: 'POST', body: '{}' }),
}
export function errorMessage(error: unknown): string {
  if (
    error instanceof APIError &&
    error.status === 401 &&
    error.message === 'invalid username or password'
  )
    return '用户名或密码不正确。'
  if (error instanceof APIError && error.status === 401) return '登录已失效，请重新登录。'
  if (error instanceof APIError && error.status === 410) return '记录已根据保留策略清理。'
  if (error instanceof APIError && error.status === 404) return '记录不存在，或已被清理。'
  if (error instanceof TypeError) return '无法连接控制面，请检查网络后重试。'
  return error instanceof Error ? error.message : '请求未完成，请重试。'
}
