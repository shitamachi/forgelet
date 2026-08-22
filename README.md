# forgelet

A Kubernetes-native CI/CD platform that runs **GitHub Actions-compatible workflows**, with GitHub acting only as a source provider.

- Workflows live in `.github/workflows/*.yml` — zero migration cost from GitHub Actions.
- No runners, no ARC, no Docker socket, no DinD. **Kubernetes itself is the runner fleet.**
- `runs-on` refers to a `RunnerClass` (infrastructure profile), not a long-lived runner.

## Status

🚧 Pre-implementation. This repository currently contains architecture docs and specs only (spec-driven development). See:

- [docs/architecture.md](docs/architecture.md) — overall architecture & implementation plan
- [specs/](specs/) — formal specs (start with [specs/0001-platform-overview](specs/0001-platform-overview/spec.md))

## Core ideas

1. **Workflow Engine and Kubernetes runtime are separated.** YAML → AST → Workflow IR → Compiled Workflow → Job DAG → JobRun CRD.
2. **One job = one Pod.** All steps of a job share the same container (filesystem, PATH, ENV, tool cache).
3. **Expression engine is a single Go package** evaluated in two phases: scheduler-time (control plane) and runtime (executor).
4. **CRDs hold active state only** (`RunnerClass`, `WorkflowRun`, `JobRun`); PostgreSQL holds long-term history. etcd is not a history database.
5. **Executor never gets Kubernetes credentials.** Only `ci-controller` talks to the Kubernetes API.

## License

TBD
