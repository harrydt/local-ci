#!/usr/bin/env bash
# Runs inside the VM: turns the local Actions cache on or off (internal/cache).
#   on:  install /tmp/localci-cache as a systemd service on :7380, patch each
#        runner's Runner.Worker.dll so jobs keep the ACTIONS_RESULTS_URL that
#        localci sets, and write ~/.localci/cache-url (the marker localci up
#        reads) and ~/.localci/job-started.sh (a runner hook).
#   off: stop the service, restore the stock DLLs, remove the marker and hook.
# The patch renames the one ACTIONS_RESULTS_URL string literal (UTF-16) the
# worker uses to overwrite that variable on every step, to ACTIONS_RESULTS_ORL:
# a one-byte change. Only a DLL with a known checksum is touched. Safe to re-run.
# usage: install-cache.sh on|off
set -euo pipefail

PORT=7380
# Runner.Worker.dll of runner 2.338.2 linux-arm64, before and after the patch.
# Bump both with RUNNER_VERSION in setup-vm.sh.
STOCK=98dfbf564515ab65263168bc98dc80d1c86f512beb6266cc8e749b7500f87c1e
PATCHED=a3107de73d53f01c2425ffc12c08d713795679bb88ff737616436afb8368c130
FROM='A\x00C\x00T\x00I\x00O\x00N\x00S\x00_\x00R\x00E\x00S\x00U\x00L\x00T\x00S\x00_\x00U\x00R\x00L\x00'
TO='A\x00C\x00T\x00I\x00O\x00N\x00S\x00_\x00R\x00E\x00S\x00U\x00L\x00T\x00S\x00_\x00O\x00R\x00L\x00'

sum() { sha256sum "$1" | cut -d' ' -f1; }

case ${1:-} in
on)
  sudo install -m 755 /tmp/localci-cache /usr/local/bin/localci-cache
  rm -f /tmp/localci-cache
  sudo tee /etc/systemd/system/localci-cache.service >/dev/null <<EOF
[Unit]
Description=LocalCI Actions cache
After=network.target

[Service]
User=$(whoami)
ExecStart=/usr/local/bin/localci-cache -addr :$PORT
Restart=always

[Install]
WantedBy=multi-user.target
EOF
  sudo systemctl daemon-reload
  sudo systemctl enable -q localci-cache
  sudo systemctl restart localci-cache
  for _ in $(seq 40); do curl -fs "localhost:$PORT/healthz" >/dev/null && break; sleep 0.25; done
  curl -fs "localhost:$PORT/healthz" >/dev/null || { echo "localci-cache did not start" >&2; exit 1; }

  for dll in ~/actions-runner*/bin/Runner.Worker.dll; do
    case $(sum "$dll") in
    "$PATCHED") ;;
    "$STOCK")
      cp "$dll" "$dll.stock"
      sed -i "s/$FROM/$TO/" "$dll"
      [ "$(sum "$dll")" = "$PATCHED" ] || { cp "$dll.stock" "$dll"; echo "patch of $dll failed" >&2; exit 1; }
      ;;
    *) echo "unknown $dll (runner version changed?); update the checksums in install-cache.sh" >&2; exit 1 ;;
    esac
  done
  mkdir -p ~/.localci
  # container: steps get only the job's environment, not the runner's, so the
  # job-started hook adds the URL there. Containers reach the VM by its IP;
  # Lima's --plain mode doesn't forward the port to the Mac.
  cat > ~/.localci/job-started.sh <<EOF
#!/bin/sh
echo "ACTIONS_RESULTS_URL=http://\$(hostname -I | cut -d' ' -f1):$PORT/" >> "\$GITHUB_ENV"
echo "localci: actions/cache uses the cache in this VM"
EOF
  chmod +x ~/.localci/job-started.sh
  echo "http://127.0.0.1:$PORT/" > ~/.localci/cache-url
  echo "local cache on (:$PORT)"
  ;;
off)
  # Marker first: a runner started from here on uses GitHub's cache again.
  rm -f ~/.localci/cache-url ~/.localci/job-started.sh
  for dll in ~/actions-runner*/bin/Runner.Worker.dll; do
    if [ -f "$dll.stock" ]; then mv "$dll.stock" "$dll"; fi
  done
  sudo systemctl disable -q --now localci-cache 2>/dev/null || true
  echo "local cache off"
  ;;
*)
  echo "usage: install-cache.sh on|off" >&2
  exit 2
  ;;
esac
