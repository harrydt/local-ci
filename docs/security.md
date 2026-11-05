# Security

## Threat model (Phase 1)

The developer controls the Mac, the VM, the runner and localci. Anything that runs locally can be observed or altered by them.

## What Phase 1 does **not** guarantee

- **Result integrity.** A local runner can report success for a failing commit. GitHub's branch protection can't tell a self-hosted runner's result from a hosted one. A green check from LocalCI means "this developer's machine says it passed".
- **Secret confidentiality.** Every secret a routed job references is delivered to the local runner. A VM doesn't hide it from the host owner.
- **Clean environment.** The VM persists between jobs, so state can leak from one job to the next.

## Invariants

- Tokens are read at runtime (`GH_TOKEN` or `gh auth token`) and are never logged, printed, or written to disk by localci.
- The runner's JIT config is single-use and is passed directly to the runner process.
- Don't route jobs that reference protected secrets to local runners.
- **Public repos:** routing by `github.actor` keeps fork PRs on hosted runners. Never give a local runner a label that untrusted actors can target.

## Deferred

GitHub App or narrower auth (Phase 2). Independent verification (Phase 3).
