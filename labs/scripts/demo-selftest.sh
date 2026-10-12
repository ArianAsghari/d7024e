#!/usr/bin/env bash
# One-command check that the demo works on this computer. It starts the 50-node
# network, plays the whole demo with demo-auto.exp, checks the key lines of the
# transcript and removes everything again. Takes about 3 minutes.
# Needs: Docker (Swarm is initialised automatically), bash, expect, git.
set -uo pipefail
cd "$(dirname "$0")/.."

LOG="${TMPDIR:-/tmp}/kademlia-demo-selftest.log"
for tool in docker expect git; do
  command -v "$tool" >/dev/null 2>&1 || { echo "missing tool: $tool"; exit 2; }
done
docker info >/dev/null 2>&1 || { echo "Docker is not running"; exit 2; }

echo "== system: $(uname -sm), $(docker --version), expect $(expect -v 2>&1 | head -1)"
scripts/demo-down.sh >/dev/null 2>&1 || true
scripts/demo-up.sh
scripts/demo-auto.exp > "$LOG" 2>&1
echo "== transcript saved to $LOG"

out=$(tr -d '\r' < "$LOG")
fail=0
check() { # description, regular expression, minimum number of matching lines
  local n
  n=$(printf '%s\n' "$out" | grep -c -E "$2")
  if [ "$n" -ge "$3" ]; then echo "PASS  $1 ($n)"; else echo "FAIL  $1 (found $n, need $3)"; fail=1; fi
}
check "ping answers"                         'pong from bootstrap:8000'                      1
check "put prints a key"                     'stored as [0-9a-f]{64}'                        1
check "get fetches from another node"        '\(found on [0-9.]+:8000\)'                     2
check "publish 1.0.0"                        'published example.com:tool:1.0.0'              1
check "install latest gives 2.0.0"           'installed example.com:tool 2.0.0 .*latest'     2
check "old version 1.0.0 still installs"     'installed example.com:tool 1.0.0 .*/out/v1'    1
check "no key for a foreign domain"          'no private key for other.org'                  1
check "keygen refuses to replace an owner"   'already has an owner key'                      1
check "fork refused"                         'update would fork the version history'         2
check "rollback refused"                     'update would roll back'                        1
check "replicas refuse invalid updates"      'refused by [0-9]+ nodes'                       4
check "skipping versions is accepted"        'published example.com:tool:5.0.0'              1
check "demo finished"                        'DEMO FINISHED'                                 1

scripts/demo-down.sh >/dev/null 2>&1 || true
if [ "$fail" -eq 0 ]; then echo "RESULT: all checks passed"; else echo "RESULT: some checks FAILED (see $LOG)"; fi
exit "$fail"
