# FAQ

## How is this different from `act`?

[`act`](https://github.com/nektos/act) re-implements GitHub Actions and runs workflows on your machine, but GitHub never sees the result. LocalCI runs GitHub's own runner, so GitHub dispatches the job, the job runs exactly as Actions would run it, and GitHub writes the check. Branch protection and required checks work unchanged. [More](architecture.md#why-githubs-own-runner).

## Why do I have to change `runs-on`?

GitHub has no way to send a job meant for a hosted runner to a self-hosted one. The `runs-on` line is how GitHub decides, and LocalCI makes it fall back to `ubuntu-latest` whenever you're not running `localci up`.

## Does this change anything for my teammates?

No. The line looks up whoever triggered the run in `LOCALCI_RUNNERS`. Only people running `localci up` are listed, so everyone else's jobs, and yours when you stop, go to hosted runners as before.

## Do I need admin on the repo?

Yes. Registering and removing a runner needs repo admin on GitHub, and no token type gets around that on the Free plan. [Details](architecture.md#auth-and-permissions).

## Is a green check from LocalCI trustworthy?

It's as trustworthy as the person whose Mac ran it. A developer controls their own machine and can make any job report success. That's fine for your own feedback loop, and not fine as proof for someone else. Keep release and deploy jobs on hosted runners. [Security](security.md).

## What happens to secrets?

A routed job gets every secret it references, on your Mac, the same as any self-hosted runner. Don't route jobs that use secrets, an `environment:` or OIDC. `localci up` warns when it finds one on the default branch.

## What happens when my Mac sleeps?

Your jobs queue until it wakes, then run. GitHub fails a job that waits 24 hours. Short sleeps and network drops recover on their own. Stop `localci up` (Ctrl-C) before a long break and your jobs go back to hosted runners.

## Why is my workflow slower locally when every job is faster?

Hosted runs each job on its own machine at the same time, so the workflow takes as long as its slowest job. One local runner runs the jobs one after another, so the workflow takes the sum, plus a few seconds per job to register the next runner. In the ripgrep benchmark, every job was 1.5–4× faster locally, but six of them in a row took 98s against hosted's 79s. With `-runners 3` it took 64s. [Timeline](diagrams.md#one-runner-vs-several).

Rule of thumb: one big job (common for Go and Node repos) wins outright. Many parallel jobs need `-runners N`, and enough CPUs in the VM to go round.

## Can I use Docker, `services:` and `container:` jobs?

Yes, Docker is installed in the VM. Images must have ARM64 builds, because there's no amd64 emulation. Docker state persists between jobs and is pruned when the VM disk passes 80%.

## Why does a tool that's on `ubuntu-latest` fail?

The VM is plain ARM64 Ubuntu 24.04, not GitHub's runner image, which ships hundreds of preinstalled tools. Install what you need in a step, or use a `setup-*` action. Those cache their downloads in the VM, so it costs time only once.

## Is the VM fresh for every job?

No. It persists, which is where the warm-run speedup comes from, but one job can leave state for the next. Each runner slot has its own working directory and `HOME`.

A fresh VM per job (a copy-on-write clone of a stopped VM, deleted afterwards) has been measured but not built:

| | Fresh clone | Shared VM (today) |
|---|---|---|
| `localci up` to runner registered | 12–15s | 3s |
| Go CPU-bound job | 24s | 23–24s |
| Go job with `services: postgres` | 17–20s | 18–19s |

The clone itself is instant on APFS, and jobs run as fast because the clone starts with the stopped VM's caches. The cost is about 10s of boot per job, one VM (6 GiB) per runner slot, and caches that stop building up: anything a job saves is deleted with its clone. That last part is the point. A cache that carries over from one job to the next is also a way for one job to poison the next.

## Does `actions/cache` go to GitHub?

No, it stays on the VM's disk, which is much faster on a home connection. Artifacts still go to GitHub. Jobs that fall back to hosted runners use GitHub's cache, so the two caches don't share entries. Turn the local cache off with `localci setup -cache=false`.

## Can I run it in the background?

Not built in yet. Run `localci up` in a terminal, tmux or similar. To stop idle sleep while it runs, use `caffeinate -i bin/localci up -repo owner/name`.

## Does it work with more than one repo?

One repo at a time for now. Run a single `localci up` per VM: two processes would share runner directories.

## Intel Macs, Linux or Windows?

Not yet. LocalCI targets Apple Silicon Macs with Lima's `vz` VMs.
