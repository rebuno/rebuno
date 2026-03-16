# Contributing to Rebuno

Thanks for your interest in contributing. This guide covers how to set up the project locally and submit changes.

## Prerequisites

- **Go** 1.25+ — kernel
- **Python** 3.10+ — SDK and SDK tests
- **Node.js** 20+ — explorer
- **Docker** and **Docker Compose** — running the full stack
- **PostgreSQL** 16 — required for integration testing (or use the Docker setup)

## Getting Started

### Kernel (Go)

```bash
# Build
make build

# Dev kernel
make dev

# Run tests
make test

# Lint (requires golangci-lint)
make lint
```

### Python SDK

```bash
cd sdk/python

# Install in editable mode with dev dependencies
pip install -e ".[dev]"

# Run tests
python -m pytest tests/ -v
```

### Explorer

```bash
cd explorer
npm install
npm run dev       # Development server on http://localhost:3000
npm run build     # Production build
```

## Submitting Changes

1. Fork the repo and create a branch from `main`.
2. Make your changes. Add tests for new functionality.
3. Run the relevant test suites and make sure they pass.
4. Open a pull request with a clear description of what changed and why.

### Code Style

- **Go**: Follow standard Go conventions. Run `golangci-lint` before submitting.
- **Python**: Follow PEP 8. Keep consistency with the existing codebase.
- **TypeScript**: Follow the existing patterns in the explorer.

## Reporting Issues

Open an issue on GitHub. Include:

- What you expected to happen
- What actually happened
- Steps to reproduce
- Relevant logs or error messages
