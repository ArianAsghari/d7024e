# Demo tools for the sprint review

These scripts start the 50-container network and play the sprint-review demo. They are
demo tools only: they add no application code, and the report and tests are unaffected.

**Needs:** Docker (a single-node Swarm is initialised automatically), bash, git, and `expect`
(only for `demo-auto.exp` and the self-test). Tested on macOS with Docker Desktop; not yet
tested on Linux or Windows (WSL).

## Check that it works on this computer (about 3 minutes)

```bash
cd labs
scripts/demo-selftest.sh
```

It starts the network, plays the whole demo, prints PASS or FAIL for the key lines, and
removes everything again.

## Run the demo by hand

```bash
cd labs
scripts/demo-up.sh            # 1 bootstrap + 49 workers, RPC_TIMEOUT=2s, about 40 seconds
```

Open six terminals and start one node in each, **before typing any demo command**:
`scripts/demo-node.sh A` (publisher), `B`, `C`, `D` (readers), `X` (rogue node with its own
owners file) and `P` (Part 1 publisher). Do not close and reopen nodes: a node that has left
stays in the routing tables as a dead contact and slows later joins.

| Step | Node | Commands |
|---|---|---|
| Part 1 | P | `ping bootstrap:8000`, `put /files/file1.txt` (copy the key) |
| | B | `show ds` (empty), `get KEY /out/got.txt` (found on another node) |
| | P, C | `exit` on P, then on C `get KEY` (still found) |
| Publish | A | `keygen example.com`, `publish example.com:tool:1.0.0 /files/file1.txt` |
| | B | `install example.com:tool:latest /out/latest.txt` |
| | A | `publish example.com:tool:2.0.0 /files/file2.txt` |
| | C, D | `install example.com:tool:latest /out/latest.txt`; on D `install example.com:tool:1.0.0 /out/v1.txt` |
| Safety | B | `publish other.org:tool:1.0.0 /files/file1.txt` (no key), `keygen example.com` (refused) |
| | X | `keygen example.com`, `publish example.com:evil:1.0.0 /files/file1.txt` (refused by the replicas) |
| | A | `publish example.com:tool:2.0.0 /files/file3.txt`, then the same with `--force` (fork) |
| | A | `publish --force example.com:tool:1.0.1 /files/file1.txt` (rollback), `publish --force --prev=1.0.0 example.com:tool:2.1.0 /files/file3.txt` (fork) |
| Skip ahead | A | `publish --force --prev=2.0.0 example.com:tool:5.0.0 /files/file1.txt` (accepted: numbers may be skipped) |

`scripts/demo-auto.exp` plays all of this in about 10 seconds. `LOGS=1 scripts/demo-node.sh A`
shows the node's JSON lookup log. Reset with `scripts/demo-down.sh`, then `scripts/demo-up.sh`.
Files inside the nodes: `/files/file1.txt`, `file2.txt`, `file3.txt`; installed files go to
`/out/` and appear on the host under `/tmp/kademlia-demo/out-NAME/`.
