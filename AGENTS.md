# AGENTS.md

LocalCI runs a repo's existing GitHub Actions jobs on the developer's Apple Silicon Mac and lets GitHub report the green or red check. Full product brief: `docs/brief.md`.

**Start here:** `docs/progress.md` has the current state, open decisions and next tasks. Add an entry to it at the end of every work session.

## Architecture (Phase 1)

A single Go binary manages a Lima ARM64 Ubuntu VM and keeps one **ephemeral JIT self-hosted runner** (GitHub's official `actions/runner`) registered in it, labelled `localci-<user>`. GitHub sends jobs to it, the runner executes them, and GitHub produces the real job check run. LocalCI has no backend, no GitHub App and no Checks API code. See `docs/architecture.md` and ADR `docs/decisions/0001-native-self-hosted-runner.md`.

Workflows opt in with one line per job:

```yaml
runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
```

## Layout

- `cmd/localci/`: CLI entry point, flags, slog setup
- `internal/github/`: REST client for JIT runner config and deleting runners (stdlib `net/http`)
- `internal/runner/`: `Agent` loop: start the VM, register a JIT runner, run one job, clean up, repeat
- `scripts/setup-vm.sh`: one-time VM and runner install (pinned version and SHA256)
- `scripts/bench.sh`: hosted vs local-cold vs local-warm timings for one commit
- Test cases and benchmark results live in the private repo [`TeepLabs/localci-bench`](https://github.com/TeepLabs/localci-bench)
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

## Running the POC

1. `./scripts/setup-vm.sh`: creates the `localci` Lima VM and installs the runner. Safe to re-run.
2. In the target repo, set `runs-on` as shown above and create a repo variable:
   `gh variable set LOCALCI_RUNNERS -R <owner/repo> --body '{"<your-login>":"localci-<your-login>"}'`
3. `bin/localci up -repo <owner/repo> -label localci-<your-login>`. Ctrl-C deregisters the runner.
4. Push, then verify:
   `gh api repos/<owner/repo>/commits/$(git rev-parse HEAD)/check-runs --jq '.check_runs[] | "\(.name) \(.conclusion)"'`
   The job's `runner_name` (from `gh api repos/<owner/repo>/actions/runs/<id>/jobs`) should start with `localci-`.
5. To see a red check: break a test (in `localci-bench`, change `want` to 5 in `cases/go-cpu/hello_test.go`), push, and run step 4 again. Expect `failure`.
6. To benchmark: `scripts/bench.sh <owner/repo> <workflow.yml>` (needs `localci up` running and a `workflow_dispatch` trigger) prints hosted vs local-cold vs local-warm timings for the same commit.

The reference target is `TeepLabs/localci-bench`, which already has `LOCALCI_RUNNERS` set for `harrydt`. Add your own login to it.

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

Phase 1 POC works end to end. Details and next tasks are in `docs/progress.md`.

## Known limitations

- Existing workflows need a `runs-on` change. Transparent redirection isn't possible.
- If the laptop is offline, routed jobs stay queued for up to 24h and then fail. There's no fallback.
- The VM is ARM64. amd64-only tools and Docker images break or run under emulation.
- The runner image is not GitHub's `ubuntu-latest` image, so preinstalled tools differ.
- The VM persists between jobs (warm caches, but no clean slate). One repo per `localci up` process.
- If `localci up` is killed mid-job, GitHub fails that job. The next session starts fresh.
- The laptop must be awake and online. Sleep/wake and network drops are untested (`docs/research/002-phase1-validation.md`).
- One job at a time per `localci up`. Concurrent jobs queue behind each other.
- No Docker in the VM: `services:`, `container:` and Docker container actions fail.

## Working in parallel

Each workmux worktree owns one task from `docs/progress.md`. The task table lists the files each task touches and its likely conflicts. Don't add dependencies to `go.mod` in parallel sessions. Each machine has its own VM, so two sessions on one Mac share the `localci` VM: only one should run `localci up` at a time.
