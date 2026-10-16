#!/usr/bin/env bash
# Open an interactive node on the demo network. Usage: scripts/demo-node.sh NAME
# Each NAME keeps its own private keys (/tmp/kademlia-demo/keys-NAME) and output
# folder (/out inside the container), so a node can be reopened with the same keys.
# The demo files are available as /files/file1.txt, file2.txt and file3.txt.
# The node's JSON lookup log is saved under /tmp/kademlia-demo/logs by default;
# failures print the end of that log. LOGS=1 scripts/demo-node.sh A shows it live.
# NAME=X is a rogue node: it has its own private owners file, so it can create a key
# for a domain it does not really own. The replicas still check the shared owners file.
set -euo pipefail
NAME="${1:?usage: demo-node.sh NAME}"
if docker container inspect "demo-$NAME" >/dev/null 2>&1; then
  printf 'Cannot start node %s: container demo-%s already exists.\n' "$NAME" "$NAME" >&2
  echo 'Use its existing terminal. If demo-selftest.sh is running, wait until it finishes before starting the manual demo.' >&2
  exit 1
fi
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
D=/tmp/kademlia-demo
mkdir -p "$D/keys-$NAME" "$D/out-$NAME" "$D/logs"

if [ "$NAME" = "X" ]; then
  mkdir -p "$D/rogue-owners"
  OWNERS=(-e OWNERS_FILE=/rogue/owners.json -v "$D/rogue-owners:/rogue")
else
  OWNERS=(-e OWNERS_FILE=/shared/owners.json -v kademlia_registry_shared:/shared)
fi

ARGS=(--rm --name "demo-$NAME" --network kademlia_kademlia_network
  -e BOOTSTRAP=bootstrap:8000 -e RPC_TIMEOUT="${RPC_TIMEOUT:-2s}" -e KEYS_DIR=/keys
  "${OWNERS[@]}"
  -v "$D/keys-$NAME:/keys" -v "$D/out-$NAME:/out" -v "$ROOT/demo/files:/files:ro"
  kadlab:latest)

if [ "${LOGS:-0}" = "1" ]; then
  exec docker run -it "${ARGS[@]}"
else
  log="$D/logs/node-$NAME.stderr.log"
  if docker run -i "${ARGS[@]}" 2>"$log"; then
    exit 0
  else
    status=$?
    printf 'Node %s exited with status %s. Details: %s\n' "$NAME" "$status" "$log" >&2
    tail -n 15 "$log" >&2
    exit "$status"
  fi
fi
