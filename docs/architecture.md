# Architecture

LocalCI is one Go binary. It manages a Lima ARM64 Ubuntu VM on the Mac and keeps N (default 1) ephemeral, just-in-time (JIT) registered copies of GitHub's official `actions/runner` waiting in it, labelled `localci-<login>`. GitHub sends jobs to those runners, the runners execute them, and GitHub produces the job's own check run. LocalCI has no server, no GitHub App and no Checks API code.

Diagrams: [components](diagrams.md#components), [job lifecycle](diagrams.md#job-lifecycle), [routing](diagrams.md#routing), [where your jobs go](diagrams.md#where-your-jobs-go), [local cache](diagrams.md#local-actions-cache).

## Why GitHub's own runner

The other way to run Actions locally is [`nektos/act`](https://github.com/nektos/act), with results posted back through the Checks API. LocalCI doesn't do that, because:

- `act` re-implements Actions and has documented gaps. The official runner is Actions.
- Only GitHub Apps can write check runs, so the Checks API route needs an App, and its checks come from that App rather than `github-actions`. A required check from GitHub Actions can't be satisfied that way.
- You'd still need workflow changes to stop the hosted run from happening as well.

With the official runner, the job runs once, GitHub writes the check, and branch protection works unchanged. The cost is one `runs-on` line per job: GitHub has no way to redirect a hosted job transparently.

## Components

| Concern | Owner |
|---|---|
| Event detection, job dispatch, commit identity | GitHub Actions (runner long-poll) |
| Workflow parsing and execution | official `actions/runner` |
| Check status and logs | GitHub Actions (the job's own check run) |
| VM lifecycle, runner registration loop, routing on/off | `localci` (`internal/runner`) |
| GitHub API calls | `localci` (`internal/github`) |
| Warnings about risky routed jobs | `localci` (`internal/workflow`) |
| `actions/cache` storage | `localci-cache` in the VM (`internal/cache`, `cmd/localci-cache`) |
| VM and runner install | `scripts/setup-vm.sh` and `scripts/install-cache.sh`, embedded in the binary and run by `localci setup` |

Everything goes over outbound HTTPS. There are no inbound ports, no webhooks and no LocalCI backend.

## Job lifecycle

[Diagram](diagrams.md#job-lifecycle).

1. `localci up` starts the VM if it isn't running and adds `login → localci-<login>` to the `LOCALCI_RUNNERS` repo variable.
2. Each slot asks GitHub for a single-use JIT runner config (`generate-jitconfig`) and starts the runner in the VM with it, passed over stdin.
3. The runner long-polls GitHub. When a routed job is queued, GitHub hands it to the runner, which runs it, streams logs and reports the result. GitHub completes the check run.
4. The runner exits after one job and deregisters itself. `localci` kills any stray listener, deletes the registration (a 404 means it's already gone) and loops back to step 2.
5. On Ctrl-C, SIGTERM or SIGHUP, `localci up` removes the login from `LOCALCI_RUNNERS`, so new jobs go to hosted runners again.

With `-runners N`, N slots run this loop at once. Each slot has its own runner directory in the VM (`~/actions-runner`, `~/actions-runner-2`, …), because a runner keeps its credentials and `_work` there, and its own `HOME` (`<dir>/_home`), because tools like `rustup` aren't safe to run concurrently against one home directory. Slots share the VM's CPU, memory and Docker.

### How the work reaches the VM

`localci` never watches pushes and never touches code. GitHub decides which jobs go to which runner, and the runner in the VM fetches everything else itself:

| What | How it gets there | Where it lives in the VM |
|---|---|---|
| Job definition | Part of the job message: steps already parsed from the YAML, the SHA, variables, the secrets the job references, a short-lived `GITHUB_TOKEN` scoped to the repo and run | runner memory |
| Actions (`uses:`) | Downloaded by the runner, pinned to a commit | `~/actions-runner/_work/_actions` |
| Source code | The `actions/checkout` step fetches the exact SHA using `GITHUB_TOKEN`. Without that step there is no code | `~/actions-runner/_work/<repo>/<repo>` |
| Toolchain | `setup-go` and similar actions download it | `~/actions-runner/_work/_tool`, kept between jobs (the warm-run speedup) |
| Results | The runner streams the log, and the step's exit code decides whether the check is green or red | — |

The VM has no host mounts, so files on the Mac never take part.

## Routing

[Diagram](diagrams.md#routing). Opt-in jobs use:

```yaml
runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
```

`runs-on` is evaluated when the job is queued. `LOCALCI_RUNNERS` maps a GitHub login to a runner label, for example `{"alice":"localci-alice"}`, and `localci up` manages its own user's entry. The `|| '{}'` guard matters: `fromJSON` fails when the variable is unset.

Each job runs exactly once, on a hosted or a local runner, so duplicate checks can't happen. JIT runners get only the labels LocalCI passes (no `self-hosted`, `Linux` or `ARM64`), so nothing else in the repo lands on them by accident.

For `pull_request`, the runner tests GitHub's merge ref (`refs/pull/N/merge`) and the check appears on the PR head SHA, the same as on hosted runners.

## Failure behaviour

[Diagram](diagrams.md#where-your-jobs-go).

| Event | What happens |
|---|---|
| Short sleep or network drop | Recovers on its own. The runner reconnects, or GitHub drops the dead registration (about 30s after it goes offline holding a job, then re-queues the job) and `localci` registers a new one. Queued jobs run after wake |
| Long sleep | `localci up` is still running, so the user stays in `LOCALCI_RUNNERS` and their jobs queue. They run on wake, or fail after 24h |
| Network drop mid-job | The job fails if a step needs the network. That includes `services:` and `container:` jobs: the runner always runs `docker pull`, even when the image is cached |
| `localci up` killed mid-job | GitHub fails that job. The next `localci up` starts fresh |
| Registration fails (network, API) | Logged, retried every 10s |
| Cancel or `timeout-minutes` | Handled by the runner, as on hosted |

Sleep and network drops were tested by freezing the VM and blocking port 443 inside it. A real lid-close hasn't been tested yet. `caffeinate -i localci up …` prevents idle sleep for the session, but not lid-close sleep on battery.

## Local Actions cache

[Diagram](diagrams.md#local-actions-cache).

On a local runner, `actions/cache` would still save to and restore from GitHub over the developer's internet link. Saves are the slow part: on a home connection, a 30 MB save took 12–16s and 200 MB took 80s. So `localci setup` installs `localci-cache`, a small server in the VM (Go stdlib only, run by systemd), and points jobs at it.

- **The runner patch.** `Runner.Worker` sets `ACTIONS_RESULTS_URL` from the job message on every step, so a value LocalCI exports is overwritten. `scripts/install-cache.sh` renames that one UTF-16 string literal to `ACTIONS_RESULTS_ORL`, a one-byte change, so the worker writes a variable nobody reads. The DLL is patched only if its SHA256 matches the known stock build, and checked against the known patched SHA256 afterwards. The stock copy is kept, so `localci setup -cache=false` restores it.
- **Getting the URL to jobs.** `localci up` exports `ACTIONS_RESULTS_URL` for host steps. `container:` steps don't inherit the runner's environment, so a job-started hook also writes the VM's IP URL to `$GITHUB_ENV`.
- **The server** speaks the cache API v2 (Twirp JSON for create, finalize and lookup, Azure Blob block uploads, range downloads) and reverse-proxies every other path, such as artifacts, to GitHub.
- **Scoping** follows GitHub's rules, read from the job token's claims: save to the job's own branch; restore from its own branch, then the default branch. Per scope it tries the exact key, then the key as a prefix, then each restore key, newest first. The version (paths and compression) must match, and entries are immutable.
- **Limits.** LRU eviction above 5 GiB (`-max-size-mb`). Unfinished uploads are dropped after an hour.
- **Safety.** `localci up` refuses to start if the cache is on but `/healthz` doesn't answer, because artifact uploads would fail.

Caveats: if GitHub tells the runner to update itself, the stock DLL comes back and caching silently returns to GitHub. A runner version bump needs both checksums in `install-cache.sh` updated, and setup refuses an unknown DLL. Jobs that fall back to hosted runners use GitHub's cache, so the two caches are separate.

[falcondev-oss/github-actions-cache-server](https://github.com/falcondev-oss/github-actions-cache-server) solves the same problem for fleets, with cloud storage and a runner fork. LocalCI needs one developer, one VM and a local disk, so it uses its own small server.

## Auth and permissions

Registering a runner (`generate-jitconfig`) and deleting one need **repo admin**. On GitHub Free no token type gives a user more than their own role, and the roles that could delegate runner management are Enterprise Cloud only. Editing `LOCALCI_RUNNERS` needs only write. So every developer who runs `localci up` is a repo admin, and LocalCI stays a CLI with no backend.

If someone who can't be an admin needs LocalCI, the options are an admin-issued registration for a persistent runner, or a small backend that holds a GitHub App key and mints JIT configs. Neither is built.

Token lookup order: `GH_TOKEN`, then a one-repo fine-grained token from `localci auth` in the macOS Keychain (Administration and Variables write, Contents read), then `gh auth token`. Tokens stay in memory while `localci up` runs and are never logged.

## Not built yet

- Independent verification of local results
- A fresh VM per job
- Emulating amd64 images (Rosetta or qemu)

See the [roadmap](roadmap.md).
