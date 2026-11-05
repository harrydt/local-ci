# Architecture

## Phase 1

```
GitHub (repo, workflow, Actions service, check runs)
   ^  outbound HTTPS long-poll only (no inbound ports, no webhook)
   |
Mac ── localci (Go binary)
        ├─ gets a JIT runner config: POST /repos/{o}/{r}/actions/runners/generate-jitconfig
        ├─ ensures the Lima VM "localci" (Ubuntu, ARM64) is running
        └─ loop: limactl shell localci -- actions-runner/run.sh --jitconfig <cfg>
                 (one job, then the runner deregisters and the loop repeats)
```

### Responsibilities

| Concern | Owner |
|---|---|
| Event detection, job dispatch, commit identity | GitHub Actions (runner long-poll) |
| Workflow parsing and execution | official `actions/runner` |
| Check status and logs | GitHub Actions (the job's own check run) |
| VM lifecycle, runner registration loop | localci |
| Auth | `gh auth token` (repo admin) at runtime, never persisted |

### Routing

`runs-on` picks the runner when the job is queued. Opt-in jobs use:

```yaml
runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
```

`LOCALCI_RUNNERS` is a repository variable mapping a GitHub login to a runner label, for example `{"alice":"localci-alice"}`. Actors not in the map, including fork PR authors, fall back to hosted runners. Each job runs exactly once, so duplicate checks can't happen.

### Failure behaviour

- **Laptop offline or asleep:** the job stays queued and fails after 24h. No fallback in Phase 1.
- **Agent killed mid-job:** GitHub marks the job failed when the runner stops responding. The next loop iteration registers a fresh runner.
- **Cancellation and timeouts:** handled by the runner itself (`timeout-minutes`, cancel button).

## Not in Phase 1

- Checks API and GitHub App (Phase 2/3)
- A fresh VM per job (Phase 4)
- Hosted fallback when offline
