# Agent Guide — appkit

Application development kit

## Tooling

This repo uses [mise](https://mise.jdx.dev/) to pin developer tools (`go`, `golangci-lint`, `goreleaser`, `lefthook`). Run `mise install` once, then use the task aliases below.

## Common tasks

| Task                   | Purpose                                           |
| ---------------------- | ------------------------------------------------- |
| `mise run test`        | `go test ./...`                                   |
| `mise run coverage`    | Tests with `-race` + coverage profile             |
| `mise run lint`        | `golangci-lint run ./...`                         |
| `mise run fmt`         | `golangci-lint fmt` (applies gofumpt)             |
| `mise run tidy`        | `go mod tidy`                                     |
| `mise run check`       | `tidy` + `lint` + `test` (run this before a PR)   |

## Conventions

- **Commit messages** follow Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`…). The goreleaser changelog groups depend on this prefix.
- **Formatter** is gofumpt (configured through golangci-lint v2). Do not run `gofmt` separately.
- **Lint** must pass cleanly before merging; `mise run check` is the full gate.
- **Public API** changes require a `godoc` comment on every exported symbol.

## Pre-commit hooks

Lefthook runs `golangci-lint`, `gofumpt`, and `go mod tidy` on staged files. Install once per clone:

```sh
lefthook install
```
