# Architecture

## Phase 1: components

```mermaid
flowchart LR
  dev([Developer]) -- git push / PR --> repo

  subgraph GH["GitHub (source of truth)"]
    repo[(Repo + workflows)]
    actions["Actions service<br/>job queue"]
    checks["Check runs<br/>app: github-actions"]
    repo --> actions --> checks
  end

  subgraph Mac["Developer's Mac"]
    cli["localci up<br/>(Go binary)"]
    gh["gh auth token"]
    subgraph VM["Lima VM 'localci'<br/>Ubuntu 24.04 ARM64, no host mounts"]
      runner["actions/runner<br/>ephemeral JIT, one job"]
    end
    gh -. token in memory .-> cli
    cli -- "limactl start / shell" --> VM
  end

  cli -- "REST: generate-jitconfig,<br/>delete runner" --> actions
  runner -- "outbound HTTPS long-poll:<br/>fetch job, stream logs, report result" --> actions
```

| Concern | Owner |
|---|---|
| Event detection, job dispatch, commit identity | GitHub Actions (runner long-poll) |
| Workflow parsing and execution | official `actions/runner` |
| Check status and logs | GitHub Actions (the job's own check run) |
| VM lifecycle, runner registration loop | `localci` (`internal/runner`) |
| GitHub API calls | `localci` (`internal/github`) |
| Auth | `GH_TOKEN` or `gh auth token` (repo admin), in memory only |

Everything goes over outbound HTTPS. There are no inbound ports, no webhooks and no LocalCI backend.

## Job lifecycle

```mermaid
sequenceDiagram
  autonumber
  participant L as localci up
  participant API as GitHub REST API
  participant R as runner (in VM)
  participant A as GitHub Actions
  participant D as Developer

  L->>L: limactl start (no-op if already running)
  loop until Ctrl-C
    L->>API: POST generate-jitconfig (label localci-user)
    API-->>L: runner id + single-use JIT config
    L->>R: limactl shell, run.sh --jitconfig (via stdin)
    R->>A: long-poll for a job
    D->>A: push / open PR
    A->>A: evaluate runs-on, queue job for label localci-user
    A-->>R: job (exact SHA, workflow, token)
    R->>A: stream logs, step results
    A->>A: complete check run (success / failure)
    R-->>L: exit (runner self-deregisters)
    L->>R: pkill stray listener
    L->>API: DELETE runner (404 = already gone)
  end
```

## Routing

```mermaid
flowchart TD
  ev[push / pull_request / workflow_dispatch] --> eval{"fromJSON(vars.LOCALCI_RUNNERS || '{}')<br/>[github.actor]"}
  eval -- "actor listed, e.g. localci-alice" --> online{localci up running<br/>for that label?}
  eval -- "not listed (incl. fork PRs)" --> hosted[GitHub-hosted ubuntu-latest]
  online -- yes --> local[Runs on the developer's Mac]
  online -- no --> queued["Job stays queued<br/>(fails after 24h)"]
```

`runs-on` is evaluated when the job is queued. Opt-in jobs use:

```yaml
runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
```

`LOCALCI_RUNNERS` is a repository variable mapping a GitHub login to a runner label, for example `{"alice":"localci-alice"}`. Each job runs exactly once, on either a hosted or a local runner, so duplicate or conflicting checks can't happen.

For `pull_request`, the runner tests GitHub's merge ref (`refs/pull/N/merge`), and the check appears on the PR head SHA. That's the same as hosted runners (research 002).

## Failure behaviour

- **Laptop offline or asleep:** the job stays queued and fails after 24h. No fallback in Phase 1.
- **Agent killed mid-job:** GitHub fails the job. On shutdown, cleanup kills the listener in the VM and deletes the registration.
- **Registration fails** (network, API): logged, then retried every 10s.
- **Cancellation and timeouts:** handled by the runner itself (`timeout-minutes`, cancel button).

## Not in Phase 1

- Checks API and GitHub App (Phase 2/3)
- A fresh VM per job (Phase 4)
- Hosted fallback when offline
- Docker inside the VM (pending decision)
- Parallel runners per `localci up`
