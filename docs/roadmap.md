# Roadmap

Full goals per phase: `docs/brief.md` §2.

## Phase 1 — Make it work (current)

1. **Spike (manual):** register a runner by hand in a Lima VM. Verify a green job, a red job, the SHA match, `runs-on` routing, and offline queueing. Record timings.
2. **`internal/github`:** JIT config client.
3. **`internal/runner` + `localci up`:** VM lifecycle and the ephemeral runner loop.
4. **End-to-end verification** against a test repo (green and red).

**Done when:** pushing to the test repo produces green or red job checks on the exact SHA, executed on the Mac, with timings logged.

## Phase 2 — Make it secure

Keep protected secrets off local runners (route those jobs to hosted runners). Replace the repo-admin token with a GitHub App or something narrower. Evaluate OIDC.

## Phase 3 — Make it zero-trust

Independent verification, for example a trusted remote re-execution that posts its own required check. Be explicit about what ordinary MacBooks can't attest to.

## Phase 4 — Make it fast

Caches, VM warm start, a VM per job, hosted fallback. Only on the basis of benchmarks collected since Phase 1.
