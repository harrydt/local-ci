#!/usr/bin/env bash
# Creates the LocalCI Lima VM and installs the GitHub Actions runner in it.
# Safe to re-run. See docs/research/001-native-runner-spike.md.
set -euo pipefail

VM=${VM:-localci}
RUNNER_VERSION=2.338.2
RUNNER_SHA256=670adb2f268fe21dcbb58b3db0e0aa9d5b3bddfe2c651481336e561ff86de945

if limactl list -q | grep -qx "$VM"; then
  limactl start --tty=false "$VM"
else
  # --plain: no host mounts, so job code cannot read the developer's $HOME.
  limactl start --name="$VM" --plain --cpus=4 --memory=6 --disk=30 --vm-type=vz --tty=false template:ubuntu-24.04
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
