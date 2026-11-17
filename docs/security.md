# Security

## Threat model

The developer controls the Mac, the VM, the runner and `localci`. Anything that runs locally can be observed or altered by them. [Diagram](diagrams.md#trust-boundary).

Everything that crosses into the developer's machine is exposed to the developer, and everything that comes out of it is the developer's word. The VM boundary protects the **developer's** host from job code: that's why there are no host mounts, and why the VM can't reach services on the Mac's `127.0.0.1`. It doesn't protect jobs or secrets from the developer.

Everyone who runs `localci up` is a repo admin ([why](architecture.md#auth-and-permissions)), and a repo admin can already read every repo secret. So LocalCI doesn't treat the developer as the adversary. It defends against:

- malicious code inside a job (a compromised dependency, say) reaching the Mac or the repo,
- malware on the Mac, or a stolen laptop, getting a broad GitHub token,
- accidents that put deploy jobs or secrets on a laptop.

## What LocalCI does **not** guarantee

- **Result integrity.** A local runner can report success for a failing commit. GitHub's branch protection can't tell a self-hosted runner's result from a hosted one. A green check from LocalCI means "this developer's machine says it passed".
- **Secret confidentiality.** Every secret a routed job references is delivered to the local runner. A VM doesn't hide it from the host owner.
- **Clean environment.** The VM persists between jobs, so state can leak from one job to the next.
- **Docker inside the VM.** Job code is in the `docker` group, which makes it root inside the VM. That changes nothing for the host, because the VM is the boundary and has no host mounts. It does mean one job can tamper with the VM for the next.

## Invariants

- The VM can't reach services that listen on the Mac's `127.0.0.1`. `setup-vm.sh` sets `networkOpts.hostLoopback: false` on Lima 2.3 and later. Services the Mac binds to all interfaces stay reachable through its LAN IP.
- The local cache server listens on the VM's port 7380, which Lima `--plain` doesn't forward to the Mac. It decodes but doesn't verify job tokens: it only serves the developer's own jobs, and anything that reaches it is already in the VM. It keeps GitHub's branch scoping, which prevents accidental cross-branch overwrites. It isn't a boundary against a malicious job in the VM, which could forge the token's claims or write the cache directory directly.
- Tokens are read at runtime (`GH_TOKEN`, then the repo's Keychain token from `localci auth`, then `gh auth token`) and are never logged or printed. The only copy `localci` stores is the `localci auth` token, in the macOS Keychain, scoped to one repo with Administration and Variables write and Contents read. Without it, `localci` falls back to the `gh` token, which covers every repo, and says so at startup.
- The runner's JIT config is single-use and is passed directly to the runner process over stdin.
- Don't route jobs that use secrets (other than `GITHUB_TOKEN`), an `environment:` or OIDC (`id-token: write`) to local runners. `localci up` warns about them by scanning the default branch's workflows. It's a line scan, not a YAML parser, and it only warns.
- For OIDC deploys, the cloud's trust policy should require `runner_environment: github-hosted`. GitHub sets that claim, not the developer, so a deploy job routed to a Mac by mistake gets no credentials.
- A local job's `GITHUB_TOKEN` is only as narrow as the repo's default. `localci up` warns when the default is read-write, because then a dependency in a routed job could push to the repo.
- **Public repos:** routing by `github.actor` keeps fork PRs on hosted runners. Never give a local runner a label that untrusted actors can target.

## Not covered yet

- One job affecting the next. That needs a fresh VM per job.
- Results others can rely on. Ideas: re-run a random sample of local jobs on hosted runners and flag disagreements, or sign results into a transparency log.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting on this repo rather than a public issue.
