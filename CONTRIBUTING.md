# Contributing to Rebuno

Thanks for your interest in contributing. This guide covers how to set up the project locally and submit changes.

## Prerequisites

- **Go** 1.25+

## Getting Started

### Kernel (Go)

```bash
make build              # build bin/rebuno
make dev                # build + run the in-memory dev kernel
make test               # unit tests (go test -race ./...)
make test-integration   # integration tests (requires Docker)
make lint               # golangci-lint run ./...
make fmt                # gofmt -s -w .
```

Documentation lives in [docs/](docs/). If you change kernel behavior, API
surface, events, or the policy format, update the corresponding doc.

## Submitting Changes

1. Fork the repo and create a branch from `main`.
2. Make your changes. Add tests for new functionality.
3. Run the relevant test suites and make sure they pass.
4. Open a pull request with a clear description of what changed and why.

### Code Style

- **Go**: Follow standard Go conventions. Run `golangci-lint` before submitting.

## Reporting Issues

Open an issue on GitHub. Include:

- What you expected to happen
- What actually happened
- Steps to reproduce
- Relevant logs or error messages
