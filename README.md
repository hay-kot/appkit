# appkit

Application development kit

[![Go Reference](https://pkg.go.dev/badge/github.com/hay-kot/appkit.svg)](https://pkg.go.dev/github.com/hay-kot/appkit)
[![CI](https://github.com/hay-kot/appkit/actions/workflows/pr.yml/badge.svg)](https://github.com/hay-kot/appkit/actions/workflows/pr.yml)
[![License](https://img.shields.io/github/license/hay-kot/appkit)](./LICENSE)

## Install

```bash
go get github.com/hay-kot/appkit
```

## Packages

| Package                                                                   | Import                                  | Description                                                         |
| ------------------------------------------------------------------------- | --------------------------------------- | ------------------------------------------------------------------- |
| [`concurrency`](https://pkg.go.dev/github.com/hay-kot/appkit/concurrency) | `github.com/hay-kot/appkit/concurrency` | Bounded-parallel work execution with first-error-wins semantics     |
| [`dockertest`](https://pkg.go.dev/github.com/hay-kot/appkit/dockertest)   | `github.com/hay-kot/appkit/dockertest`  | Zero-dependency Docker container harness for Go tests               |
| [`httpclient`](https://pkg.go.dev/github.com/hay-kot/appkit/httpclient)   | `github.com/hay-kot/appkit/httpclient`  | Context-first HTTP client with composable middleware                |
| [`mapx`](https://pkg.go.dev/github.com/hay-kot/appkit/mapx)               | `github.com/hay-kot/appkit/mapx`        | Generic mapping helpers for type conversion                         |
| [`plugs`](https://pkg.go.dev/github.com/hay-kot/appkit/plugs)             | `github.com/hay-kot/appkit/plugs`       | Service lifecycle manager with graceful shutdown                    |
| [`secret`](https://pkg.go.dev/github.com/hay-kot/appkit/secret)           | `github.com/hay-kot/appkit/secret`      | Secret string type that resolves from env, files, or custom sources |

## LLM Context

This repo includes an [`llmtxt.txt`](./llmtxt.txt) file describing all packages, types, and usage examples for consumption by LLMs and coding agents.

To feed it directly into an agent's context from GitHub:

```bash
curl -sL https://raw.githubusercontent.com/hay-kot/appkit/main/llmtxt.txt
```

Or reference it in a CLAUDE.md / system prompt:

```markdown
Fetch and read https://raw.githubusercontent.com/hay-kot/appkit/main/llmtxt.txt for appkit API reference.
```
