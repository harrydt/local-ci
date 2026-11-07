# AGENTS.md

LocalCI runs a repo's existing GitHub Actions jobs on the developer's Apple Silicon Mac and lets GitHub report the green or red check. Full product brief: `docs/brief.md`.

**Start here:** `docs/progress.md` has the current state, open decisions and next tasks. Add an entry to it at the end of every work session. Entry headings are `### YYYY-MM-DD: <topic>` only: no author, no "with Claude Code", no AI or tool attribution.

## Architecture (Phase 1)

A single Go binary manages a Lima ARM64 Ubuntu VM and keeps N (default 1) **ephemeral JIT self-hosted runners** (GitHub's official `actions/runner`) registered in it, labelled `localci-<login>`. While it runs, it maps the user's login to that label in the `LOCALCI_RUNNERS` repo variable. GitHub sends jobs to the runner, the runner executes them, and GitHub produces the real job check run. LocalCI has no backend, no GitHub App and no Checks API code. See `docs/architecture.md` and ADR `docs/decisions/0001-native-self-hosted-runner.md`.

Workflows opt in with one line per job:

```yaml
runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
```

## Layout

- `cmd/localci/`: CLI entry point, flags, slog setup
- `internal/github/`: REST client for JIT runner config, deleting runners and the `LOCALCI_RUNNERS` variable (stdlib `net/http`)
- `internal/runner/`: `Agent` loop: start the VM, register a JIT runner, run one job, clean up, repeat
- `scripts/setup-vm.sh`: one-time VM and runner install (pinned version and SHA256), embedded in the binary as `localci setup`
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
golangci-lint run   # also runs in CI with shellcheck (.github/workflows/ci.yml)
```

## Running it

Setup and first run: [README → Get started](README.md#get-started). The reference target is `TeepLabs/localci-bench`. Useful checks:

- Which runner took a job: `gh api repos/<owner/repo>/actions/runs/<id>/jobs --jq '.jobs[] | "\(.conclusion) \(.runner_name)"'`. Local ones start with `localci-`.
- Red check: in `localci-bench`, change `want` to 5 in `cases/go-cpu/hello_test.go` and push. Expect `failure`.
- Benchmark: `scripts/bench.sh <owner/repo> <workflow.yml>` (needs `localci up` running and a `workflow_dispatch` trigger) prints hosted vs local-cold vs local-warm timings for one commit.

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

Phase 1 is done and on `main`. Next tasks are in `docs/progress.md`.

## Known limitations

- Existing workflows need a `runs-on` change. Transparent redirection isn't possible.
- Jobs fall back to hosted runners only when `localci up` exits cleanly. After SIGKILL, a crash or power loss, or while the Mac sleeps with `localci up` running, the user's jobs queue (up to 24h) until it's back. Short sleeps and network drops recover on their own; real lid-close sleep is untested (`docs/research/004-sleep-and-network.md`).
- The VM is ARM64. amd64-only tools and Docker images break or run under emulation.
- The runner image is not GitHub's `ubuntu-latest` image, so preinstalled tools differ.
- The VM persists between jobs (warm caches, but no clean slate). One repo per `localci up` process.
- If `localci up` is killed mid-job, GitHub fails that job. The next session starts fresh.
- A job running during a network drop fails if a step needs the network, including the `docker pull` the runner always does for `services:`.
- `-runners N` runs N jobs at once (default 1), each from its own runner directory (`~/actions-runner`, `~/actions-runner-2`, ...) with its own tool cache. They share the VM's CPU, memory and Docker.
- Docker is installed in the VM. `services:`, `container:` jobs and Docker container actions are verified (bench cases `go-postgres`, `docker-compat`). Images need ARM64 builds: amd64-only images fail, because there's no emulation. Docker state persists between jobs and is pruned when the VM disk passes 80%.

## Working in parallel

Each workmux worktree owns one task from `docs/progress.md`. The task table lists the files each task touches and its likely conflicts. Don't add dependencies to `go.mod` in parallel sessions. Each machine has its own VM, so two sessions on one Mac share the `localci` VM: only one should run `localci up` at a time.
