# Diagrams

Every LocalCI diagram in one place. The prose behind them is in [architecture](architecture.md) and [security](security.md).

- [Components](#components)
- [Job lifecycle](#job-lifecycle)
- [Routing](#routing)
- [Where your jobs go](#where-your-jobs-go)
- [Local Actions cache](#local-actions-cache)
- [Trust boundary](#trust-boundary)
- [One runner vs several](#one-runner-vs-several)

## Components

What runs where. All traffic is outbound HTTPS from the Mac: no inbound ports, no webhooks, no LocalCI server.

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
    tok["Keychain token (localci auth)<br/>or gh auth token"]
    subgraph VM["Lima VM 'localci'<br/>Ubuntu 24.04 ARM64, no host mounts,<br/>no host loopback"]
      runner["actions/runner × N slots<br/>ephemeral JIT, one job each"]
      cache["localci-cache :7380<br/>actions/cache on the VM's disk"]
      runner -- "cache API" --> cache
    end
    tok -. token in memory .-> cli
    cli -- "limactl start / shell" --> VM
  end

  cli -- "REST: generate-jitconfig, delete runner,<br/>LOCALCI_RUNNERS add/remove" --> actions
  runner -- "outbound HTTPS long-poll:<br/>fetch job, stream logs, report result" --> actions
```

## Job lifecycle

One runner slot, from `localci up` to Ctrl-C. With `-runners N`, N slots run the loop at once.

```mermaid
sequenceDiagram
  autonumber
  participant L as localci up
  participant API as GitHub REST API
  participant R as runner (in VM)
  participant A as GitHub Actions
  participant D as Developer

  L->>L: limactl start (no-op if already running)
  L->>API: add login → localci-user to LOCALCI_RUNNERS
  loop each slot, until Ctrl-C
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
  L->>API: remove login from LOCALCI_RUNNERS (on Ctrl-C, SIGTERM, SIGHUP)
```

## Routing

How GitHub picks a runner for a job that has the LocalCI `runs-on` line.

```mermaid
flowchart TD
  ev[push / pull_request / workflow_dispatch] --> eval{"fromJSON(vars.LOCALCI_RUNNERS || '{}')<br/>[github.actor]"}
  eval -- "actor listed, e.g. localci-alice<br/>(only while their localci up runs)" --> online{runner online<br/>for that label?}
  eval -- "not listed (incl. fork PRs)" --> hosted[GitHub-hosted ubuntu-latest]
  online -- yes --> local[Runs on the developer's Mac]
  online -- "no (asleep, or killed uncleanly)" --> queued["Job stays queued<br/>(fails after 24h)"]
```

## Where your jobs go

Depending on what `localci up` is doing.

```mermaid
stateDiagram-v2
  direction LR
  [*] --> Hosted
  Hosted --> Local: localci up starts (adds entry)
  Local --> Hosted: Ctrl-C, SIGTERM, SIGHUP (removes entry)
  Local --> Waiting: Mac asleep or offline, kill -9, crash
  Waiting --> Local: wake, or next localci up
  Waiting --> Failed: 24h in the queue
```

## Local Actions cache

Cache calls stay in the VM. Everything else, such as artifacts, goes on to GitHub.

```mermaid
flowchart LR
  step["job step<br/>actions/cache, setup-* cache,<br/>BuildKit type=gha"]
  subgraph VM["Lima VM"]
    srv["localci-cache :7380"]
    disk[("~/.cache/localci-cache<br/>LRU, 5 GiB")]
  end
  gh["GitHub results service<br/>(artifacts)"]
  step -- "ACTIONS_RESULTS_URL<br/>127.0.0.1:7380, or the VM IP for container: steps" --> srv
  srv -- "cache API" --> disk
  srv -- "any other path, proxied" --> gh
```

## Trust boundary

What crosses into the developer's control, and what comes out of it.

```mermaid
flowchart LR
  subgraph Trusted["Trusted: GitHub"]
    A[Actions service]
    S[(Repo secrets)]
    C[Check runs]
  end
  subgraph Untrusted["Untrusted: developer-controlled"]
    subgraph Mac[Mac host]
      L[localci + one-repo token]
      subgraph VM["Lima VM (no host mounts,<br/>no host loopback)"]
        R[runner + job code]
      end
    end
  end
  S -- "secrets the job references" --> R
  R -- "self-reported result" --> A --> C
  L -- "repo-admin token" --> A
```

## One runner vs several

ripgrep's six CI jobs, in seconds after the run was queued. Hosted gives each job its own machine, so the workflow takes as long as its slowest job. One local runner does the jobs one after another. Three runners close the gap.

```mermaid
gantt
  dateFormat X
  axisFormat %M:%S
  section Hosted, 6 machines
    test         :3, 75
    fuzz_testing :3, 63
    wasm         :3, 33
    benches      :3, 31
    docs         :3, 26
    rustfmt      :3, 11
  section Local, 1 runner
    docs         :3, 13
    fuzz_testing :16, 30
    benches      :33, 42
    rustfmt      :45, 50
    test         :54, 84
    wasm         :87, 97
  section Local, 3 runners
    rustfmt      :2, 13
    docs         :2, 15
    wasm         :2, 19
    benches      :17, 50
    test         :19, 63
    fuzz_testing :23, 49
```
