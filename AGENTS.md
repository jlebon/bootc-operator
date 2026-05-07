# Agents

## Context

Read these docs before making changes:

- [docs/PRD.md](docs/PRD.md) — motivation, user stories, requirements
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — CRDs, components, design principles
- [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md) — milestones and validation steps

## Building and Testing

- `make build` — build all binaries
- `make unit` — run unit tests (includes envtest setup, CRD generation)
- `make e2e` — run e2e tests (requires bink; uses `BINK_PATH` env var or PATH). `V=1` for verbose streaming output.
- `make fmt` — run go fmt
- `make vet` — run go vet
- `make lint` — run golangci-lint
