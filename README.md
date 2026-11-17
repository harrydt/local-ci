# LocalCI

Run your existing GitHub Actions jobs on your own Apple Silicon Mac and get the same green or red check on GitHub.

```mermaid
flowchart LR
  dev([git push]) --> GH[GitHub Actions]
  GH -- "your jobs, while localci up runs" --> VM["Lima VM on your Mac<br/>ephemeral actions/runner"]
  GH -- "everyone else, or you when it's stopped" --> Hosted[GitHub-hosted runner]
  VM -- "logs + result" --> GH --> Check["✅ / ❌ check on the commit"]
```

GitHub still dispatches the job and produces the check. LocalCI only provides the machine, so branch protection behaves as usual. There's no LocalCI server and no GitHub App: one Go binary on your Mac runs GitHub's official runner in a Linux VM.

**Status:** early. Push and PR events, `services:` and `container:` jobs, parallel jobs, a local `actions/cache` and hosted fallback all work. See the [roadmap](docs/roadmap.md).

## Get started

You need a Mac with Apple Silicon and **admin** on the repo you want to run jobs for.

**1. Install and build** (once)

```sh
brew install go lima gh
gh auth login
git clone https://github.com/harrydt/local-ci.git && cd local-ci
make build                  # → bin/localci, bin/localci-cache-linux-arm64
bin/localci setup           # creates the VM, installs the runner, Docker and the local cache (a few minutes the first time)
```

Use Lima 2.3 or later. On older Lima, job code can reach services on your Mac's `127.0.0.1`, and `localci setup` warns.

**2. Opt a job in**

Change `runs-on` on each job that may run locally, and push:

```yaml
jobs:
  test:
    runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
    steps:
      - uses: actions/checkout@v4
      - run: make test
```

Until someone runs `localci up`, this is still `ubuntu-latest`.

**3. Run it**

```sh
bin/localci up -repo owner/name     # leave it running
```

Push a commit. The job's runner name starts with `localci-<your login>`, and the check appears on the commit as usual. Add `-runners 2` to run two jobs at once.

**4. Stop**

Ctrl-C. Your runner is removed and your jobs go back to GitHub-hosted runners.

## How routing works

While it runs, `localci up` adds you to a repo variable, `LOCALCI_RUNNERS`, for example `{"alice":"localci-alice"}`. The `runs-on` expression looks up whoever triggered the run. If they're listed, the job goes to their Mac. Everyone else, fork PRs included, gets `ubuntu-latest`. Details in [architecture](docs/architecture.md#routing).

## Tokens

`localci up` borrows your `gh` token by default, which covers every repo you can see and doesn't expire. To give it a token for one repo only, run `bin/localci auth -repo owner/name` once. It opens GitHub's token page with the permissions filled in, asks you to pick the repo and paste the token, and keeps it in your macOS Keychain. When the token expires (90 days by default), run it again.

`localci up` uses `GH_TOKEN` if it's set, then the repo's `localci auth` token, then your `gh` token, and logs a line when it falls back to `gh`. An expired `localci auth` token stops `up` with a 401 instead of falling back.

## Secrets and deploys

Don't add the line to jobs that use secrets, an `environment:` or OIDC (`id-token: write`), because they would run on your Mac. `localci up` warns about any such job on the default branch. If you deploy to a cloud with OIDC, also require `runner_environment` to be `github-hosted` in the cloud's trust policy. GitHub sets that claim and a developer can't fake it, so a deploy job that lands on a Mac by mistake gets no credentials. See [security](docs/security.md).

## Caching

`actions/cache`, `setup-*` caches and BuildKit `type=gha` save and restore on the VM's disk, not over your internet link. 30–200 MB takes under a second, instead of about 3s to restore and 12–80s to save through GitHub on a home connection. Workflows don't change. Artifacts still go to GitHub.

`localci setup` installs a small cache server in the VM and patches one byte of the runner so jobs use it. `localci setup -cache=false` undoes both. [How it works](docs/architecture.md#local-actions-cache).

## Benchmarks (one sample each)

| | ripgrep CI, 6 jobs, total | Go CPU-bound tests, one job |
|---|---|---|
| GitHub-hosted (2 vCPU x86 per job) | 79s | 57s |
| LocalCI warm, 1 runner (4 vCPU ARM64 VM on an M2) | 98s | 24s |
| LocalCI warm, `-runners 3` | **64s** | — |

Each job runs 1.5–4× faster locally, but hosted runs every job on its own machine at once. With one local runner, a multi-job workflow takes the sum of its jobs, so it needs `-runners N` to beat hosted on total time ([why](docs/faq.md#why-is-my-workflow-slower-locally-when-every-job-is-faster)). `scripts/bench.sh` runs the same comparison on your own repo.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `runner not installed in VM` | `bin/localci setup` |
| `local cache is on but not answering` | `bin/localci setup`, or turn it off with `bin/localci setup -cache=false` |
| `unknown .../Runner.Worker.dll` during setup | The runner version changed: update both checksums in `scripts/install-cache.sh` |
| `HTTP 403` from `generate-jitconfig` | You need admin on the repo |
| `HTTP 401` at startup | The `localci auth` token expired or was revoked: `bin/localci auth -repo owner/name` |
| Your job sits in `queued` | Is `localci up` running and awake? If it was killed with `kill -9`, start it again, or remove yourself from `LOCALCI_RUNNERS` |
| An offline `localci-` runner stays listed in the repo's runner settings | `localci up` was stopped while it was registering the next runner. Delete it in Settings → Actions → Runners. A fix is planned |
| A tool is missing in the job | The VM is ARM64 Ubuntu, not GitHub's image. Install the tool in a step, or use a `setup-*` action |

More in the [FAQ](docs/faq.md).

## Limitations

- Each job needs the one-line `runs-on` change.
- If your Mac sleeps, or `localci up` is killed uncleanly, your jobs wait until it's back (up to 24h).
- ARM64 only: amd64-only images and tools don't work.
- The VM persists between jobs: warm caches, but no clean slate.
- A local green check is your Mac's word, not proof ([security](docs/security.md)).

## Docs

- [Architecture](docs/architecture.md): components, job lifecycle, routing, failure behaviour, the local cache
- [Diagrams](docs/diagrams.md): every diagram in one place
- [FAQ](docs/faq.md)
- [Security](docs/security.md): what a local check does and doesn't prove
- [Roadmap](docs/roadmap.md)

## Contributing

```sh
make check            # vet, tests, gofmt (CI runs this plus golangci-lint and shellcheck)
golangci-lint run
```

Conventions are in [AGENTS.md](AGENTS.md).

## Authors

Harry Tran and Aatish Neupane.
