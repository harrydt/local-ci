# LocalCI

Run your existing GitHub Actions jobs on your own Apple Silicon Mac and get the same green or red check on GitHub.

```mermaid
flowchart LR
  dev([git push]) --> GH[GitHub Actions]
  GH -- "your jobs, while localci up runs" --> VM["Lima VM on your Mac<br/>ephemeral actions/runner"]
  GH -- "everyone else, or you when it's stopped" --> Hosted[GitHub-hosted runner]
  VM -- "logs + result" --> GH --> Check["✅ / ❌ check on the commit"]
```

GitHub still dispatches the job and produces the check. LocalCI only provides the machine, so branch protection behaves as usual.

**Status:** Phase 1 works end to end (push, PRs, `services:`, parallel jobs, hosted fallback). See [progress](docs/progress.md).

## Get started

You need a Mac with Apple Silicon and **admin** on the repo you want to run jobs for.

**1. Install and build** (once)

```sh
brew install go lima gh
gh auth login
git clone git@github.com:TeepLabs/localci.git && cd localci
make build                  # → bin/localci, bin/localci-cache-linux-arm64
bin/localci setup           # creates the VM, installs the runner, Docker and the local cache (a few minutes the first time)
```

**2. Try it on the bench repo**

```sh
bin/localci up -repo TeepLabs/localci-bench       # leave it running
```

In a second terminal:

```sh
gh workflow run go-cpu.yml -R TeepLabs/localci-bench
gh run watch -R TeepLabs/localci-bench            # pick the run
```

The job's runner name starts with `localci-<your login>`. To see a red check, clone `localci-bench`, change `want` to 5 in `cases/go-cpu/hello_test.go` and push.

**3. Stop**

Ctrl-C. Your runner is removed and your jobs go back to GitHub-hosted runners.

## Use it on your own repo

Change `runs-on` on each job that may run locally:

```yaml
runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
```

Then `bin/localci up -repo owner/name`. Add `-runners 2` to run two jobs at once. `localci up` adds you to the `LOCALCI_RUNNERS` repo variable while it runs, so only your jobs come to your Mac.

`localci up` borrows your `gh` token, which covers every repo you can see and doesn't expire. To give it a token for this repo only, run `bin/localci auth -repo owner/name` once. It opens GitHub's token page with the permissions filled in, asks you to pick the repo and paste the token, and keeps the token in your macOS Keychain. When the token expires (90 days by default), run it again.

`localci up` uses `GH_TOKEN` if it's set, then the repo's `localci auth` token, then your `gh` token, and logs a line when it falls back to `gh`. An expired `localci auth` token stops `up` with a 401 instead of falling back.

Don't add the line to jobs that use secrets, an `environment:` or OIDC (`id-token: write`), because they would run on your Mac. `localci up` warns about any such job on the default branch. If you deploy to a cloud with OIDC, also require `runner_environment` to be `github-hosted` in the cloud's trust policy. GitHub sets that claim and a developer can't fake it, so a deploy job that lands on a Mac by mistake gets no credentials.

## Caching

`actions/cache`, `setup-*` caches and BuildKit `type=gha` save and restore in the VM, not over your internet link. 30–200 MB takes under a second instead of 3s to restore and 12–80s to save ([research 007](docs/research/007-local-actions-cache.md)). Workflows don't change. Artifacts still go to GitHub.

`localci setup` installs a small cache server in the VM and patches one byte of the runner so jobs use it. `localci setup -cache=false` undoes both.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `runner not installed in VM` | `bin/localci setup` |
| `local cache is on but not answering` | `bin/localci setup`, or turn it off with `bin/localci setup -cache=false` |
| `unknown .../Runner.Worker.dll` during setup | The runner version changed: update both checksums in `scripts/install-cache.sh` |
| `HTTP 403` from `generate-jitconfig` | You need admin on the repo |
| `HTTP 401` at startup | The `localci auth` token expired or was revoked: `bin/localci auth -repo owner/name` |
| Your job sits in `queued` | Is `localci up` running and awake? If it was killed with `kill -9`, start it again, or remove yourself from `LOCALCI_RUNNERS` |
| A tool is missing in the job | The VM is ARM64 Ubuntu, not GitHub's image. Install the tool in a step, or use a `setup-*` action |

## Working on LocalCI

```sh
make check            # vet, tests, gofmt (CI runs this plus golangci-lint and shellcheck)
golangci-lint run
```

Read [AGENTS.md](AGENTS.md) for conventions, and [progress](docs/progress.md) for what's next.

## Benchmarks (one sample each)

| | ripgrep CI, 6 jobs, total | Go CPU-bound tests, job |
|---|---|---|
| GitHub-hosted (2 vCPU x86 per job) | 79s | 57s |
| LocalCI warm, 1 runner (4 vCPU ARM64 VM on an M2) | 98s | 24s |
| LocalCI warm, `-runners 3` | **64s** | — |

Each job runs 1.5–4× faster locally, but hosted runs every job on its own machine at once. With one local runner, a multi-job workflow takes the sum of its jobs, so it needs `-runners N` to beat hosted on total time ([why](docs/research/006-ripgrep-bench.md#why-faster-jobs-can-still-mean-a-slower-workflow)). Details: [ripgrep](docs/research/006-ripgrep-bench.md), [Go](docs/research/002-phase1-validation.md).

## Docs

- [Architecture](docs/architecture.md): components, job lifecycle, routing
- [Security](docs/security.md): what a local check does and doesn't prove
- [Roadmap](docs/roadmap.md) and [progress](docs/progress.md)
- [Decisions](docs/decisions/) and [research](docs/research/)
- [Product brief](docs/brief.md)

## Limitations

- Each job needs the one-line `runs-on` change.
- If your Mac sleeps, or `localci up` is killed uncleanly, your jobs wait until it's back (up to 24h).
- ARM64 only: amd64-only images and tools don't work.
- A local green check is your Mac's word, not proof ([security](docs/security.md)).

The full list is in [AGENTS.md](AGENTS.md#known-limitations).
