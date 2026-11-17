# AGENTS.md

LocalCI runs a repo's existing GitHub Actions jobs on the developer's Apple Silicon Mac and lets GitHub report the green or red check.

## Architecture

A single Go binary manages a Lima ARM64 Ubuntu VM and keeps N (default 1) **ephemeral JIT self-hosted runners** (GitHub's official `actions/runner`) registered in it, labelled `localci-<login>`. While it runs, it maps the user's login to that label in the `LOCALCI_RUNNERS` repo variable. GitHub sends jobs to the runner, the runner executes them, and GitHub produces the real job check run. LocalCI has no backend, no GitHub App and no Checks API code. See `docs/architecture.md`.

Workflows opt in with one line per job:

```yaml
runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
```

## Layout

- `cmd/localci/`: CLI entry point, flags, slog setup
- `internal/github/`: REST client for JIT runner config, deleting runners and the `LOCALCI_RUNNERS` variable (stdlib `net/http`)
- `internal/workflow/`: line scan of workflow files for routed jobs that use secrets, an environment or OIDC
- `internal/runner/`: `Agent` loop: start the VM, register a JIT runner, run one job, clean up, repeat
- `internal/cache/`, `cmd/localci-cache/`: local stand-in for GitHub's Actions cache API, run in the VM as a systemd service (`make build` cross-compiles `bin/localci-cache-linux-arm64`) ([architecture](docs/architecture.md#local-actions-cache))
- `scripts/install-cache.sh`: run in the VM by `localci setup` to turn the cache on or off: installs the server, patches one byte of `Runner.Worker.dll` (checksums pinned), writes the job-started hook
- `scripts/setup-vm.sh`: one-time VM and runner install (pinned version and SHA256), embedded in the binary as `localci setup`
- `scripts/bench.sh`: hosted vs local-cold vs local-warm timings for one commit
- `docs/`: architecture, diagrams, FAQ, security, roadmap

## Requirements

- macOS on Apple Silicon
- Go (version in `go.mod`)
- [Lima](https://lima-vm.io) 2.3 or later: `brew install lima`. On older Lima, job code can reach services on the Mac's `127.0.0.1`, and `localci setup` warns
- [GitHub CLI](https://cli.github.com): `brew install gh`, then `gh auth login` with admin on the target repo

## Commands

```sh
make build   # bin/localci
make test    # go test ./...
make check   # vet + test + gofmt check; run before committing
golangci-lint run   # also runs in CI with shellcheck (.github/workflows/ci.yml)
```

## Running it

Setup and first run: [README → Get started](README.md#get-started). Use a test repo you admin. Useful checks:

- Which runner took a job: `gh api repos/<owner/repo>/actions/runs/<id>/jobs --jq '.jobs[] | "\(.conclusion) \(.runner_name)"'`. Local ones start with `localci-`.
- Red check: make a test fail and push. Expect `failure`.
- Benchmark: `scripts/bench.sh <owner/repo> <workflow.yml>` (needs `localci up` running and a `workflow_dispatch` trigger) prints hosted vs local-cold vs local-warm timings for one commit.

## Conventions

- Prefer the stdlib. Each new dependency needs a reason in the PR.
- Use `log/slog` for all logging. Thread `context.Context` through anything that does I/O or blocks.
- Write table-driven tests. Use `httptest` for GitHub API calls. Never call the real GitHub API in unit tests.
- Add an interface only at a real boundary or test seam.
- Avoid hardcoded absolute paths and machine-specific config.

## Security invariants

- Never log, print, or persist GitHub tokens, except `localci auth`'s one-repo token in the macOS Keychain. Read the token at runtime: `GH_TOKEN`, then the Keychain, then `gh auth token`.
- Treat the developer machine as untrusted. A local green check proves nothing to a third party (`docs/security.md`).
- Don't route jobs that use secrets, an `environment:` or `id-token: write` to local runners. `localci up` warns about them (`internal/workflow`).
- Never commit secrets, `.env` files, or runner credentials.

## Known limitations

- Existing workflows need a `runs-on` change. Transparent redirection isn't possible.
- Jobs fall back to hosted runners only when `localci up` exits cleanly. After SIGKILL, a crash or power loss, or while the Mac sleeps with `localci up` running, the user's jobs queue (up to 24h) until it's back. Short sleeps and network drops recover on their own; real lid-close sleep is untested.
- The VM is ARM64. amd64-only tools and Docker images break or run under emulation.
- The runner image is not GitHub's `ubuntu-latest` image, so preinstalled tools differ.
- The VM persists between jobs (warm caches, but no clean slate). One repo per `localci up` process, and one `localci up` per VM.
- `actions/cache` uses the cache server in the VM (on by default). It relies on a one-byte patch to the pinned runner: a runner self-update silently sends caches back to GitHub, and a runner version bump needs new checksums in `scripts/install-cache.sh`. Jobs that fall back to hosted runners use GitHub's cache.
- If `localci up` is killed mid-job, GitHub fails that job. The next session starts fresh.
- A job running during a network drop fails if a step needs the network, including the `docker pull` the runner always does for `services:`.
- `-runners N` runs N jobs at once (default 1), each from its own runner directory (`~/actions-runner`, `~/actions-runner-2`, ...) with its own `HOME` (`<dir>/_home`) and tool cache, like separate hosted machines. They share the VM's CPU, memory and Docker.
- Docker is installed in the VM. `services:`, `container:` jobs and Docker container actions work. Images need ARM64 builds: amd64-only images fail, because there's no emulation. Docker state persists between jobs and is pruned when the VM disk passes 80%.
