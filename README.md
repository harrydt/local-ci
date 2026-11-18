# LocalCI

Run your existing GitHub Actions jobs on your own Apple Silicon Mac and get the same green or red check on GitHub.

![localci up running local-ci's own CI on a Mac: the runner picks up the job, it succeeds, and gh shows the green check](docs/demo.gif)

GitHub still dispatches the job and writes the check, so branch protection works as usual. LocalCI only provides the machine: one Go binary that runs GitHub's official runner in a Linux VM. No server, no GitHub App.

Jobs run about 2–4× faster than on a hosted runner, and `actions/cache` stays on your disk. [Benchmarks](docs/faq.md#how-much-faster-is-it).

## Get started

You need an Apple Silicon Mac, Lima 2.3 or later, and admin on the repo.

```sh
brew install go lima gh && gh auth login
git clone https://github.com/harrydt/local-ci.git && cd local-ci
make build
bin/localci setup     # creates the VM (a few minutes, once)
```

Opt a job in by changing its `runs-on`:

```yaml
runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}
```

Then run:

```sh
bin/localci up -repo owner/name     # add -runners 2 to run two jobs at once
```

Your pushes now run on your Mac, and everyone else's still run on `ubuntu-latest`. Ctrl-C sends yours back too.

Don't opt in jobs that use secrets, an `environment:` or OIDC, or they'll run on your Mac. `localci up` warns about them. [Security](docs/security.md).

## Limitations

- Each job needs the `runs-on` change.
- While your Mac sleeps, your jobs wait for it (up to 24h).
- ARM64 only: amd64-only images and tools don't work.
- A local green check is your Mac's word, not proof.

## Docs

- [FAQ](docs/faq.md): tokens, caching, benchmarks, troubleshooting
- [Architecture](docs/architecture.md) and [diagrams](docs/diagrams.md)
- [Security](docs/security.md)
- [Roadmap](docs/roadmap.md)

## Contributing

Run `make check` before opening a PR. Conventions are in [AGENTS.md](AGENTS.md).

## Authors

Harry Tran and Aatish Neupane.

## License

[MIT](LICENSE)
