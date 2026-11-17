#!/usr/bin/env bash
# Runs one commit three times via workflow_dispatch and prints job timings:
#   hosted      GitHub-hosted runner
#   local-cold  LocalCI after wiping the VM's toolcache and Go caches
#   local-warm  LocalCI again, caches kept
#
# Requires `localci up` running for the repo and a workflow_dispatch trigger.
# Temporarily rewrites the LOCALCI_RUNNERS repo variable to force the hosted
# run; the original value is restored on exit.
#
# usage: scripts/bench.sh owner/repo workflow-file [ref]
#   e.g. scripts/bench.sh octo-org/hello ci.yml
#        scripts/bench.sh octo-org/ripgrep bench.yml master
set -euo pipefail

REPO=${1:?usage: bench.sh owner/repo workflow-file [ref]}
WF=${2:?usage: bench.sh owner/repo workflow-file [ref]}
REF=${3:-main}
VM=${VM:-localci}

ORIG=$(gh variable get LOCALCI_RUNNERS -R "$REPO")
trap 'gh variable set LOCALCI_RUNNERS -R "$REPO" --body "$ORIG"' EXIT

# dispatch MODE: trigger the workflow, wait for it, print one TSV row per job.
dispatch() {
  local since id=""
  since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  gh workflow run "$WF" -R "$REPO" --ref "$REF" >/dev/null
  until [ -n "$id" ]; do
    sleep 3
    id=$(gh run list -R "$REPO" -w "$WF" -e workflow_dispatch --created ">=$since" -L1 --json databaseId --jq '.[0].databaseId // empty')
  done
  gh run watch "$id" -R "$REPO" >/dev/null 2>&1 || true
  gh api "repos/$REPO/actions/runs/$id/jobs" --jq ".jobs[] | [\"$1\", .head_sha[0:7], .runner_name, .conclusion,
    ((.started_at|fromdate) - (.created_at|fromdate)), ((.completed_at|fromdate) - (.started_at|fromdate))] | @tsv"
}

printf 'mode\tsha\trunner\tresult\tqueue_s\trun_s\n'

gh variable set LOCALCI_RUNNERS -R "$REPO" --body '{}'
dispatch hosted
gh variable set LOCALCI_RUNNERS -R "$REPO" --body "$ORIG"

# Toolchains and caches in every runner slot (each slot has its own HOME, _home).
# Go's module cache is read-only, hence the chmod before rm.
limactl shell "$VM" -- bash -c 'chmod -R u+w ~/go ~/actions-runner*/_home 2>/dev/null; rm -rf ~/go ~/.cache/go-build ~/.cargo ~/.rustup ~/actions-runner*/_home ~/actions-runner*/_work/_tool'
dispatch local-cold
dispatch local-warm
