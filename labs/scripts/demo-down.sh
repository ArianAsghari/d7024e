#!/usr/bin/env bash
# Remove the demo network and reset all demo state (owners file, keys, outputs).
set -euo pipefail
docker stack rm kademlia >/dev/null 2>&1 || true
printf 'removing containers'
for _ in $(seq 1 60); do
  [ -z "$(docker ps -q --filter name=kademlia_)" ] && break
  printf '.'; sleep 2
done
echo
docker ps -q --filter name=demo- | xargs -r docker rm -f >/dev/null 2>&1 || true
sleep 3
docker volume rm kademlia_registry_shared >/dev/null 2>&1 || true
if [ -d /tmp/kademlia-demo ]; then
  # On Linux the containers create root-owned key files; remove those through Docker if needed.
  rm -rf /tmp/kademlia-demo 2>/dev/null || \
    docker run --rm -v /tmp/kademlia-demo:/d alpine sh -c 'rm -rf /d/* /d/.[!.]*' >/dev/null 2>&1 || true
  rm -rf /tmp/kademlia-demo 2>/dev/null || true
fi
echo "demo network removed and state reset"
