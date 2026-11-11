# Contributing to Spoon

Thank you for contributing to Spoon!

## Development Setup

```sh
go test ./...
go vet ./...
go build ./cmd/...
```

## Architecture Decisions

Zoekt evaluated for fork similarity — see `docs/adr/0005-zoekt-integration-evaluation.md` — not integrated due to category error (file-level vs fork-level).
Glean evaluated for fork similarity — see `docs/adr/0006-glean-integration-evaluation.md` — not integrated because Glean's file-level code intelligence does not match Spoon's fork-level semantic similarity.

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Tests live beside the package they exercise
- Integration tests opt-in to external credentials, paid services, and native runtimes

## Pull Requests

Project-specific Codex subagents live in `.codex/agents/`. See the
[development agent team](docs/development/agents.md) for routing, ownership,
verification rules, and the Git/PR evidence behind each specialty.

- Keep PRs focused; one logical change per PR
- Update documentation for user-facing changes
- Run the full test suite before requesting review
