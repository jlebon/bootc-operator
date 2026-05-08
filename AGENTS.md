# Agents

## Context

Read these docs before making changes:

- [docs/PRD.md](docs/PRD.md) — motivation, user stories, requirements
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — CRDs, components, design principles
- [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md) — milestones and validation steps

## Building and Testing

- `make build` — build all binaries
- `make test` — run tests (includes envtest setup, CRD generation)
- `make fmt` — run go fmt
- `make vet` — run go vet
- `make lint` — run golangci-lint
