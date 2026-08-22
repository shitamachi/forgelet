# AGENTS.md

Guidance for AI coding agents (and humans) working on forgelet.

## What this project is

forgelet is a Kubernetes-native CI/CD platform that executes GitHub Actions-compatible
workflows. GitHub is only a source provider (webhooks, git, check runs via GitHub App);
all scheduling and execution happen on Kubernetes. There are **no runners** — Kubernetes
itself is the runner fleet.

Read before doing anything non-trivial:

1. `docs/architecture.md` — the authoritative architecture & module map.
2. `specs/0001-platform-overview/spec.md` — the top-level spec; every feature must trace back to it.
3. `docs/conventions.md` — Go style, error handling, testing rules.

## Development workflow: spec-driven

We develop spec-first. **Do not write implementation code for a feature that has no
approved spec.** The flow is:

```
specs/NNNN-<slug>/
├── spec.md     # WHAT & WHY: requirements, invariants, acceptance criteria
├── plan.md     # HOW: technical design chosen for this spec (created after spec approval)
└── tasks.md    # Implementation checklist with status markers
```

Rules:

- One directory per spec, numbered `NNNN`, e.g. `0002-expression-engine`.
- `spec.md` states requirements in testable terms; it must not prescribe internals.
- Implementation PRs reference the spec number in the branch name (`spec/0002-...`)
  and update `tasks.md` as tasks complete.
- When reality invalidates a spec, update the spec in the same PR — never let docs drift.
- New top-level behaviors need a new spec; refactors don't.

## Repository layout

```
cmd/            one main.go per binary: server, controller, executor
api/            CRD types (api/v1alpha1: RunnerClass, WorkflowRun, JobRun)
internal/       all non-public Go code (provider, workflow, scheduler, executor, ...)
docs/           architecture & conventions
specs/          spec-driven development artifacts
hack/           dev scripts (kind cluster, codegen, lint)
.github/        our own CI (we dogfood forgelet, but until it can bootstrap itself use plain GH Actions)
```

Nothing under `internal/` may be imported from outside the module.

## Tech stack (fixed decisions — do not relitigate in PRs)

| Concern      | Choice |
|--------------|--------|
| Language     | Go (>= 1.27) |
| HTTP         | `net/http` + chi |
| Internal RPC | ConnectRPC |
| Database     | PostgreSQL (history), etcd/CRDs (active state only) |
| K8s          | controller-runtime + client-go |
| YAML         | gopkg.in/yaml.v3 |
| Expression   | own engine, informed by act's exprparser (no hard dependency on act) |
| Logs         | executor → structured JSON → Loki |
| Artifacts/cache | S3 / MinIO |
| Image build  | BuildKit (never the Docker socket) |

Architectural invariants (violations must be flagged in review):

- No Docker socket, no DinD, no GitHub Runner, no ARC, no Tekton on the core execution path.
- One job = one Pod; steps share the container.
- Executor pods get no Kubernetes credentials (`automountServiceAccountToken: false`).
- CRDs never carry secrets or full execution plans — only plan ID + digest.
- `runs-on` means RunnerClass, not a runner machine.

## Commands

```bash
make build      # build all binaries
make test       # unit tests
make lint       # golangci-lint
make generate   # codegen (CRDs, mocks)
make kind-up    # local dev k3s/kind cluster with deps
```

(Commands become real as code lands; until then they are the contract.)

## Coding rules (summary — details in docs/conventions.md)

- Package errors via `fmt.Errorf("...: %w", err)`; no `panic` outside `main`/tests.
- Table-driven tests; target coverage on `internal/workflow/**` and `internal/scheduler/**` ≥ 85%.
- All new behavior lands with tests; bug fixes land with a regression test.
- Public types in `api/` need `+k8s:` deep-copy gen comments and pass CRD schema gen.
- Contexts are passed explicitly; no `context.Background()` inside libraries.
- No `init()` side effects; wire dependencies in `main`.

## Git conventions

- Default branch: `main`.
- Conventional Commits (`feat:`, `fix:`, `docs:`, `spec:`, `refactor:`, `test:`, `chore:`).
- Branches: `spec/NNNN-short-desc` for feature work, `fix/...`, `chore/...`.
- Keep commits small; each must build and pass tests on its own.
