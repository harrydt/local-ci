#!/usr/bin/env bash
# Creates the LocalCI Lima VM and installs the GitHub Actions runner in it.
# Safe to re-run. See docs/research/001-native-runner-spike.md.
set -euo pipefail

VM=${VM:-localci}
RUNNER_VERSION=2.338.2
RUNNER_SHA256=670adb2f268fe21dcbb58b3db0e0aa9d5b3bddfe2c651481336e561ff86de945

# Lima 2.3+ can stop job code from reaching services that listen on the Mac's
# 127.0.0.1 through host.lima.internal. Older Lima can't, so warn and carry on.
LIMA_VERSION=$(limactl --version | awk '{print $NF}')
NO_HOST_LOOPBACK=
if [ "$(printf '2.3.0\n%s\n' "$LIMA_VERSION" | sort -V | head -1)" = 2.3.0 ]; then
  NO_HOST_LOOPBACK='.networkOpts.hostLoopback = false'
else
  echo "WARNING: Lima $LIMA_VERSION lets job code reach services on this Mac's 127.0.0.1." >&2
  echo "WARNING: upgrade to Lima 2.3 or later and re-run localci setup to block that." >&2
fi

if limactl list -q | grep -qx "$VM"; then
  if [ -n "$NO_HOST_LOOPBACK" ] && ! grep -q 'hostLoopback: false' "$(limactl list --format '{{.Dir}}' "$VM")/lima.yaml"; then
    if [ "$(limactl list --format '{{.Status}}' "$VM")" = Running ]; then limactl stop "$VM"; fi
    limactl edit --tty=false --set "$NO_HOST_LOOPBACK" "$VM"
  fi
  limactl start --tty=false "$VM"
else
  # --plain: no host mounts, so job code cannot read the developer's $HOME.
  limactl start --name="$VM" --plain --cpus=4 --memory=6 --disk=30 --vm-type=vz --tty=false \
    ${NO_HOST_LOOPBACK:+--set="$NO_HOST_LOOPBACK"} template:ubuntu-24.04
fi

limactl shell "$VM" -- bash -ec "set -u
  if [ -x ~/actions-runner/run.sh ]; then echo 'runner already installed'; exit 0; fi
  mkdir -p ~/actions-runner && cd ~/actions-runner
  curl -fsSLo runner.tgz https://github.com/actions/runner/releases/download/v$RUNNER_VERSION/actions-runner-linux-arm64-$RUNNER_VERSION.tar.gz
  echo '$RUNNER_SHA256  runner.tgz' | sha256sum -c -
  tar xzf runner.tgz && rm runner.tgz
  sudo ./bin/installdependencies.sh >/dev/null
  echo 'runner $RUNNER_VERSION installed'
"

# Docker, for services:, container: jobs and Docker actions (GitHub's hosted image ships it too).
# Group membership only applies to new logins, and Lima reuses one SSH connection,
# so the VM is restarted the first time the user is added to the docker group.
if ! limactl shell "$VM" -- id -nG | grep -qw docker; then
  limactl shell "$VM" -- bash -ec "set -u
    sudo apt-get update -qq
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io >/dev/null
    sudo usermod -aG docker \$(whoami)
  "
  limactl stop "$VM" && limactl start --tty=false "$VM"
fi
limactl shell "$VM" -- docker version --format 'docker {{.Server.Version}} installed'

# Local Actions cache (scripts/install-cache.sh): a cache server in the VM and a
# one-byte runner patch, so actions/cache saves and restores on the VM's disk
# instead of over the Mac's internet link. localci setup passes the server
# binary and the in-VM script; CACHE=off turns it back off.
INSTALL_CACHE=${INSTALL_CACHE:-$(cat "$(dirname "$0")/install-cache.sh" 2>/dev/null || true)}
if [ "${CACHE:-on}" = off ]; then
  limactl shell "$VM" -- bash -s off <<<"$INSTALL_CACHE"
elif [ -f "${CACHE_BIN:-}" ]; then
  limactl copy "$CACHE_BIN" "$VM:/tmp/localci-cache"
  limactl shell "$VM" -- bash -s on <<<"$INSTALL_CACHE"
else
  echo "WARNING: ${CACHE_BIN:-localci-cache-linux-arm64} not found (run make build); jobs use GitHub's cache." >&2
fi
