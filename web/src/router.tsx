import { lazy, Suspense } from 'react'
import { createRootRoute, createRoute, createRouter, Link } from '@tanstack/react-router'
import Layout from './components/layout'
import { Empty, ErrorPanel, Loading } from './components/ui'
const Overview = lazy(() => import('./pages/overview'))
const Runs = lazy(() => import('./pages/runs'))
const RunDetail = lazy(() => import('./pages/run-detail'))
const NewRun = lazy(() => import('./pages/new-run'))
const Deployments = lazy(() => import('./pages/deployments'))
const DeploymentDetail = lazy(() => import('./pages/deployment-detail'))
const Repositories = lazy(() => import('./pages/repositories'))
const Configuration = lazy(() => import('./pages/configuration'))
const Account = lazy(() => import('./pages/account'))
const System = lazy(() => import('./pages/system'))
const root = createRootRoute({
  component: Layout,
  errorComponent: ({ error, reset }) => <ErrorPanel error={error} retry={reset} />,
  notFoundComponent: () => (
    <Empty
      title="页面不存在"
      action={
        <Link to="/" className="button">
          返回工作台
        </Link>
      }
    />
  ),
})
const routes = [
  createRoute({
    getParentRoute: () => root,
    path: '/account',
    component: () => (
      <Suspense fallback={<Loading />}>
        <Account />
      </Suspense>
    ),
  }),
  ...(['connections', 'workflows', 'targets'] as const).map((section) =>
    createRoute({
      getParentRoute: () => root,
      path: `/${section}`,
      component: () => (
        <Suspense fallback={<Loading />}>
          <Configuration section={section} />
        </Suspense>
      ),
    }),
  ),
  createRoute({
    getParentRoute: () => root,
    path: '/',
    component: () => (
      <Suspense fallback={<Loading />}>
        <Overview />
      </Suspense>
    ),
  }),
  createRoute({
    getParentRoute: () => root,
    path: '/runs',
    validateSearch: (s: Record<string, unknown>) => ({
      q: typeof s.q === 'string' ? s.q : '',
      state: typeof s.state === 'string' ? s.state : 'all',
      repo: typeof s.repo === 'string' ? s.repo : 'all',
    }),
    component: () => (
      <Suspense fallback={<Loading />}>
        <Runs />
      </Suspense>
    ),
  }),
  createRoute({
    getParentRoute: () => root,
    path: '/runs/new',
    component: () => (
      <Suspense fallback={<Loading />}>
        <NewRun />
      </Suspense>
    ),
  }),
  createRoute({
    getParentRoute: () => root,
    path: '/runs/$runId',
    validateSearch: (s: Record<string, unknown>): { job?: string } => ({
      job: typeof s.job === 'string' ? s.job : undefined,
    }),
    component: () => (
      <Suspense fallback={<Loading />}>
        <RunDetail />
      </Suspense>
    ),
  }),
  createRoute({
    getParentRoute: () => root,
    path: '/deployments',
    component: () => (
      <Suspense fallback={<Loading />}>
        <Deployments />
      </Suspense>
    ),
  }),
  createRoute({
    getParentRoute: () => root,
    path: '/deployments/$deploymentId',
    component: () => (
      <Suspense fallback={<Loading />}>
        <DeploymentDetail />
      </Suspense>
    ),
  }),
  createRoute({
    getParentRoute: () => root,
    path: '/repositories',
    validateSearch: (s: Record<string, unknown>): { repo?: string } => ({
      repo: typeof s.repo === 'string' ? s.repo : undefined,
    }),
    component: () => (
      <Suspense fallback={<Loading />}>
        <Repositories />
      </Suspense>
    ),
  }),
  createRoute({
    getParentRoute: () => root,
    path: '/system',
    component: () => (
      <Suspense fallback={<Loading />}>
        <System />
      </Suspense>
    ),
  }),
]
export const router = createRouter({
  routeTree: root.addChildren(routes),
  defaultPreload: 'intent',
  scrollRestoration: true,
})
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
