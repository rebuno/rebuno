# Contributing to Rebuno

Thanks for your interest in contributing. This guide covers how to set up the project locally and submit changes.

## Prerequisites

- **Go** 1.26+
- **Postgres**, to run `rebuno server` or the store and stream tests

## Getting Started

```bash
make build   # build bin/rebuno
make dev     # build + run the in-memory dev kernel
make test    # go test -race ./...
make lint    # golangci-lint (pinned version, run via go run)
make fmt     # gofmt -s -w .
make tidy    # go mod tidy
```

Set `DATABASE_URL` to run the tests that need Postgres; they skip without it.
Running the production kernel is covered in [docs/deployment.md](docs/deployment.md).

Documentation lives in [docs/](docs/). If you change kernel behavior, API
surface, events, or the policy format, update the corresponding doc.

## Submitting Changes

1. Fork the repo and create a branch from `main`.
2. Make your changes. Add tests for new functionality.
3. Run `make fmt`, then make sure `make test` and `make lint` pass. CI runs those two.
4. Open a pull request with a clear description of what changed and why.

## Reporting Issues

Open an issue on GitHub. Include:

- What you expected to happen
- What actually happened
- Steps to reproduce
- Relevant logs or error messages

## License

By submitting a contribution, you agree that it is licensed under the
[MIT License](LICENSE), the same terms that cover the rest of the project.
