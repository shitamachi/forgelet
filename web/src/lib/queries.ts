import { queryOptions, QueryClient } from '@tanstack/react-query'
import { api, APIError } from './api'
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 2000,
      refetchOnWindowFocus: true,
      retry: (count, error) => count < 2 && !(error instanceof APIError && error.status < 500),
    },
    mutations: { retry: false },
  },
})
export const queries = {
  config: queryOptions({
    queryKey: ['config'],
    queryFn: ({ signal }) => api.config(signal),
    staleTime: 60_000,
  }),
  status: queryOptions({
    queryKey: ['status'],
    queryFn: ({ signal }) => api.status(signal),
    refetchInterval: 5000,
  }),
  runs: queryOptions({
    queryKey: ['runs'],
    queryFn: ({ signal }) => api.runs(signal),
    refetchInterval: 5000,
  }),
  run: (id: string) =>
    queryOptions({
      queryKey: ['run', id],
      queryFn: ({ signal }) => api.run(id, signal),
      refetchInterval: 2000,
    }),
  deliveries: queryOptions({
    queryKey: ['deployments'],
    queryFn: ({ signal }) => api.deliveries(signal),
    refetchInterval: 5000,
  }),
  delivery: (id: string) =>
    queryOptions({
      queryKey: ['deployment', id],
      queryFn: ({ signal }) => api.delivery(id, signal),
      refetchInterval: 5000,
    }),
  events: queryOptions({
    queryKey: ['events'],
    queryFn: ({ signal }) => api.events(signal),
    refetchInterval: 10_000,
  }),
}

export const authQuery = {
  queryKey: ['auth-session'],
  queryFn: ({ signal }: { signal: AbortSignal }) => api.session(signal),
  retry: false,
  staleTime: 5000,
  refetchInterval: 30_000,
}
