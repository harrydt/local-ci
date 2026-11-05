# AGENTS.md

LocalCI runs a repo's existing GitHub Actions jobs on the developer's Apple Silicon Mac and lets GitHub report the green or red check. Full product brief: `docs/brief.md`.

## Architecture (Phase 1)

A single Go binary manages a Lima ARM64 Ubuntu VM and keeps one **ephemeral JIT self-hosted runner** (GitHub's official `actions/runner`) registered in it, labelled `localci-<user>`. GitHub sends jobs to it, the runner executes them, and GitHub produces the real job check run. LocalCI has no backend, no GitHub App and no Checks API code. See `docs/architecture.md` and ADR `docs/decisions/0001-native-self-hosted-runner.md`.

Workflows opt in with one line per job:

```yaml
runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
```

## Layout

- `cmd/localci/`: CLI entry point, flags, slog setup
- `internal/`: packages are added only when a vertical slice needs them
- `docs/`: architecture, roadmap, security, ADRs (`decisions/`), research notes (`research/`)

## Requirements

- macOS on Apple Silicon
- Go (version in `go.mod`)
- [Lima](https://lima-vm.io) 2.x: `brew install lima`
- [GitHub CLI](https://cli.github.com): `brew install gh`, then `gh auth login` with admin on the target repo

## Commands

```sh
make build   # bin/localci
make test    # go test ./...
make check   # vet + test + gofmt check; run before committing
```

## Conventions

- Prefer the stdlib. Each new dependency needs a reason in the PR or an ADR.
- Use `log/slog` for all logging. Thread `context.Context` through anything that does I/O or blocks.
- Write table-driven tests. Use `httptest` for GitHub API calls. Never call the real GitHub API in unit tests.
- Add an interface only at a real boundary or test seam.
- Avoid hardcoded absolute paths and machine-specific config.

## Security invariants

- Never log, print, or persist GitHub tokens. Read the token at runtime from `GH_TOKEN` or `gh auth token`.
- Treat the developer machine as untrusted. A local green check proves nothing to a third party (`docs/security.md`).
- Don't route jobs that use protected secrets to local runners.
- Never commit secrets, `.env` files, or runner credentials.

## Status

- [x] Research and architecture decision (ADR 0001)
- [x] Scaffold: CLI, logging, Makefile, docs
- [x] Manual spike: runner in a Lima VM, green and red check verified (`docs/research/001-native-runner-spike.md`)
- [ ] `internal/github`: JIT runner config client
- [ ] `internal/runner`: VM and ephemeral runner loop, `localci up`

## Known limitations

- Existing workflows need a `runs-on` change. Transparent redirection isn't possible.
- If the laptop is offline, routed jobs stay queued for up to 24h and then fail. There's no fallback.
- The VM is ARM64. amd64-only tools and Docker images break or run under emulation.
- The runner image is not GitHub's `ubuntu-latest` image, so preinstalled tools differ.

## Working in parallel

Each workmux worktree owns one task. Only the scaffold task edits `go.mod`. Other tasks rebase onto it rather than adding dependencies concurrently.
