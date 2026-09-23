# Contributing to MoreToken

Thanks for your interest in contributing! This document covers the basics.

## Code of Conduct

Be respectful and constructive. All contributors are expected to follow standard open source etiquette.

## Getting Started

### Prerequisites

- Go 1.25+
- Git

### Build

```bash
go build -o bin/moretoken .
```

### Run Tests

```bash
go test ./...
```

With race detector:

```bash
go test -race ./...
```

With coverage:

```bash
go test -cover ./...
```

### Full Test Suite

```bash
./scripts/test-all.sh
```

## Workflow

1. Fork the repository
2. Create a feature branch (`git checkout -b feat/your-feature`)
3. Make changes with tests
4. Run `go test ./...` to ensure everything passes
5. Commit changes (`git commit -am 'feat: add something'`)
6. Push to the branch (`git push origin feat/your-feature`)
7. Create a new Pull Request

## Commit Style

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add new feature
fix: fix bug
docs: update documentation
refactor: restructure code
test: add tests
chore: maintenance tasks
```

## Code Style

- Run `golangci-lint run` before committing
- Keep functions small and focused (max complexity ~15)
- Add comments for non-obvious logic
- Follow existing naming conventions in the codebase

## Testing Requirements

- New features must include tests
- Bug fixes must include regression tests
- Aim for meaningful test coverage, not just percentage

## Reporting Issues

When opening an issue, include:
- What you expected to happen
- What actually happened
- Steps to reproduce
- Your environment (OS, Go version)

## Pull Requests

PRs should:
- Be focused on a single concern
- Include tests for new functionality
- Update documentation if needed
- Pass all CI checks

---

Thanks for helping make MoreToken better!
