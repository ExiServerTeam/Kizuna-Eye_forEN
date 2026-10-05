# Contributing to Kizuna-Eye

Thank you for your interest in Kizuna-Eye.

## Current Contribution Scope (important)

Kizuna-Eye is currently maintained by a single developer with limited time and
health constraints. To keep the project sustainable, **the scope of accepted
contributions is intentionally narrow**. Please read this before opening a PR.

### Accepted without prior discussion

- **Documentation** (`README.md`, `README.ja.md`, `docs/`, `CHANGELOG.md`)
- **Translations / i18n** (`web/static/i18n.js`, `data-i18n*` attributes)
- **Bug reports** with a clear reproduction (see below)

These areas do not touch the runtime and can be reviewed quickly.

### Please open an Issue first

For anything else — new features, refactors, changes to `pkg/module`,
`pkg/status`, `internal/auth`, or the plugin interface — please open an Issue
and **wait for a reply before writing code**. Because Go plugins require the
host and every `.so` to be rebuilt from identical sources, changes to shared
types are high-impact and cannot be merged casually.

### Not accepted right now

- Large feature PRs (the maintainer cannot commit to reviewing them promptly)
- Changes to build/release/update scripts (`*.sh`) without prior agreement

We would rather decline clearly than leave a PR unreviewed. Thank you for
understanding.

## Development Setup

1. Fork the repository
2. Clone your fork
3. Install Go 1.27.1 or later
4. Run `go mod download`
5. Build with `./build.sh`

## Pull Request Process

1. Create a feature branch (`git checkout -b docs/amazing-fix`)
2. Commit your changes (`git commit -m 'docs: fix ...'`)
3. Push to the branch (`git push origin docs/amazing-fix`)
4. Open a Pull Request

## Code Style

- Follow `gofmt` and `go vet`
- Write tests for new features
- Update documentation as needed
- Do not hardcode a version; read it from the repo-root `VERSION` file

## Reporting Bugs

Please use the GitHub Issues page and include:

- Your OS and version
- Go version
- Steps to reproduce
- Expected vs actual behavior
