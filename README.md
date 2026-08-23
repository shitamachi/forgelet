# forgelet

A Kubernetes-native CI/CD platform that runs **GitHub Actions-compatible workflows**, with GitHub acting only as a source provider.

- Workflows remain in `.github/workflows/*.yml`; forgelet implements an explicitly documented compatible subset.
- No runners, no ARC, no Docker socket, no DinD. **Kubernetes itself is the runner fleet.**
- `runs-on` refers to a `RunnerClass` (infrastructure profile), not a long-lived runner.
- The repository is a modular monorepo: one Go module initially, multiple binaries, strict package boundaries.

## Status

🚧 Pre-implementation. This repository currently contains architecture docs and specs only (spec-driven development). See:

- [docs/architecture.md](docs/architecture.md) — overall architecture & implementation plan
- [docs/module-boundaries.md](docs/module-boundaries.md) — monorepo modules and allowed dependencies
- [specs/](specs/) — formal specs (start with [specs/0001-platform-overview](specs/0001-platform-overview/spec.md))

Spec 0001 is accepted; child specs and their plans must be accepted before product implementation begins.

## Core ideas

1. **Workflow Engine and Kubernetes runtime are separated.** YAML → AST → Workflow IR → Compiled Workflow → Job DAG → JobRun CRD.
2. **One JobRun = one primary execution Pod.** Ordinary/JS/composite/builtin steps share the main container;
   P2 services and Docker Actions may use explicitly modeled auxiliary Pods.
3. **Expression engine is a single Go package** evaluated in two phases: scheduler-time (control plane) and runtime (executor).
4. **CRDs hold active state only** (`RunnerClass`, `WorkflowRun`, `JobRun`); PostgreSQL holds long-term history. etcd is not a history database.
5. **Executor never gets Kubernetes API permission.** It uses a short-lived, audience-bound identity only for
   the forgelet control plane.

## GitHub operating modes

- **Replacement mode (default):** disable native GitHub Actions for the repository and let forgelet handle
  webhook, manual, and scheduled triggers.
- **Coexistence mode (opt-in):** native GitHub Actions stays enabled; duplicate execution is possible and is
  not automatically suppressed by forgelet.

## License

TBD
