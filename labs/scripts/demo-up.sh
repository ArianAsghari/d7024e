#!/usr/bin/env bash
# Start the 50-node demo network: one bootstrap node plus 49 workers on Docker Swarm.
# Usage: scripts/demo-up.sh            (starts or reuses the demo network)
#        RPC_TIMEOUT=500ms scripts/demo-up.sh   (override the per-attempt RPC deadline)
# Run demo-down.sh first to rebuild an existing demo or change its settings.
set -euo pipefail
cd "$(dirname "$0")/.."

STACK=kademlia
export RPC_TIMEOUT="${RPC_TIMEOUT:-2s}"   # 2 s: slow registry validation can outlast the 500 ms default

if [ "$(docker info --format '{{.Swarm.LocalNodeState}}')" != "active" ]; then
  docker swarm init >/dev/null
fi

existing=$(docker service ls --filter "label=com.docker.stack.namespace=$STACK" --format '{{.Name}}')
if [ -n "$existing" ]; then
  boot=$(docker service ls --filter "name=${STACK}_bootstrap" --format '{{.Replicas}}')
  work=$(docker service ls --filter "name=${STACK}_kademliaNodes" --format '{{.Replicas}}')
  if [ "$boot" = "1/1" ] && [ "$work" = "49/49" ]; then
    echo 'Demo network is already running: reusing its 50 background nodes and current settings.'
    echo 'Open interactive nodes with: scripts/demo-node.sh A   (and B, C, D, X, P)'
    echo 'To rebuild or start fresh, finish the current rehearsal, then run scripts/demo-down.sh followed by scripts/demo-up.sh.'
    exit 0
  fi
  printf 'Demo network already exists but is not ready (bootstrap=%s, workers=%s).\n' "$boot" "$work" >&2
  echo 'Wait for the current startup to finish. To reset a failed rehearsal, run scripts/demo-down.sh first.' >&2
  exit 1
fi

echo "code version: $(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
docker build -q -t kadlab:latest . >/dev/null
docker stack deploy --resolve-image never -c docker-compose.yml "$STACK" >/dev/null 2> >(grep -v -- "--detach" >&2 || true)
docker service scale --detach=true "${STACK}_kademliaNodes=49" >/dev/null 2> >(grep -v -- "--detach" >&2 || true)

printf 'waiting for 50 containers'
for _ in $(seq 1 90); do
  boot=$(docker service ls --filter "name=${STACK}_bootstrap" --format '{{.Replicas}}')
  work=$(docker service ls --filter "name=${STACK}_kademliaNodes" --format '{{.Replicas}}')
  if [ "$boot" = "1/1" ] && [ "$work" = "49/49" ]; then break; fi
  printf '.'; sleep 2
done
echo
if [ "$boot" != "1/1" ] || [ "$work" != "49/49" ]; then
  printf 'Startup timed out: expected bootstrap=1/1 and workers=49/49, got bootstrap=%s and workers=%s.\n' "$boot" "$work" >&2
  exit 1
fi
sleep 15   # let the workers finish joining and refreshing their routing tables

mkdir -p /tmp/kademlia-demo
echo "ready: $(docker ps --filter "name=${STACK}_" --format '{{.Names}}' | wc -l | tr -d ' ') node containers, RPC_TIMEOUT=$RPC_TIMEOUT"
echo "open interactive nodes with: scripts/demo-node.sh A   (and B, C, D ...)"
