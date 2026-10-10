# Understanding and running the project

## What we are building

The assignment is a decentralized package repository: package files live on multiple computers instead of one central server. Part 1 implements the distributed storage underneath it. Part 2 will add package names, owners, signatures, and version history.

One running copy of the Go program is one **node**. A node has:

- An address, such as `127.0.0.1:8001`, and an ID equal to `SHA-256(IP:port)`.
- A **routing table**, which is a contact book for other nodes.
- A **data store**, which holds some file contents in memory.
- A UDP listener, an interactive CLI or headless loop, and replication and bucket-refresh workers.

A file's **key** is `SHA-256(file contents)`. The same bytes always have the same key. When receiving bytes for a key, nodes check that the hash matches. This detects incorrect content; Part 2 adds proof of who published it.

Kademlia decides where to store a key using `distance = nodeID XOR key`, interpreted as a number. Smaller numbers are closer. This is distance between identifiers, unrelated to physical location. A lookup asks known peers for contacts closer to the target, then asks those contacts, continuing until it has queried the closest candidates it has found. The initiating node controls the iteration.

The parameter `k` is both the bucket capacity and desired replication factor, defaulting to 10. In a three-node network, only three distinct nodes can hold replicas. `alpha` controls concurrent lookup probes and defaults to 3. Both node and value lookups query up to alpha closest unqueried candidates per round, wait for the batch, and then incorporate referrals. Set `ALPHA=1` to compare against sequential lookups. Failed peers are excluded, each peer is queried at most once, and lookup ends when the k closest remaining candidates have been queried (or a valid value is found).

```mermaid
flowchart LR
    CLI["put file / get key"] --> Core["Kademlia core"]
    Core --> RT["Routing table: known peers"]
    Core --> DS["Local key-value store"]
    Core --> UDP["UDP RPCs"]
    UDP <--> Peers["Other nodes"]
    Timer["Replication timer"] --> Core
```

The four network operations are:

| RPC | Meaning |
| --- | --- |
| PING | Are you alive? |
| FIND_NODE | Which contacts do you know closest to this ID? |
| FIND_VALUE | Do you have these bytes? If not, which contacts are closest to the key? |
| STORE | Keep these bytes under this key, after verifying the hash. |

`put` hashes the file, finds the closest nodes, and sends them STORE requests. `get` first checks the local store, then follows FIND_VALUE responses until it finds valid bytes or exhausts its candidates. A successful remote retrieval also saves a local copy.

## Jerald's work and status

The initial core PR, [PR #1](https://github.com/ArianAsghari/d7024e/pull/1), was already merged into Sprint-1. Sprint-2 was created after all three Sprint-1 PRs merged.

| Assigned item | Current status |
| --- | --- |
| Merge the core PR | Already merged |
| PING-based full-bucket eviction | Implemented, with concurrent refresh protection |
| Bootstrap contact and self-lookup | Implemented through `BOOTSTRAP` and `join` |
| Refresh buckets farther than the closest neighbor | Implemented during join |
| Periodic replication without expiration | Implemented; interval defaults to one hour |
| Alpha-parallel node and value lookup | Implemented; `ALPHA` / `SetAlpha`, default 3 |
| Periodic refresh of idle buckets | Implemented; `REFRESH_INTERVAL`, default one hour |
| Explicit value-size limit | Implemented; 4 KiB (4096 bytes), enforced at store and retrieval boundaries |
| Probe and outcome logging | Implemented; JSON events with per-lookup probe/round counts |
| Report thread-safety section | Written in `../REPORT.md` |
| Mutable-pointer core hook | Implemented: `ValueValidator`, `SetValueValidator`, `StoreAt`, and key-preserving replication |
| Simulated network, 1000-node test, and experiments | Integrated from the team's Sprint-2 work; see `experiment/` and `experiments/results/` |
| Signed version records, ownership, no forks/rollback, and registry CLI | Kim's remaining registry integration; the validator can fetch missing records through `LookupData` |

Joining works as follows:

1. Contact the bootstrap node and insert its contact into the routing table.
2. Run the normal node lookup with **our own ID** as the target. We are discovering neighbors close to where we belong in the ID space.
3. Pick a random target in each farther bucket's range and look it up. This fills out contacts in other parts of the network and introduces us to peers.

The bootstrap is an entry point, not a central database. Once peers have discovered each other, they can communicate directly. New nodes still need a reachable entry point to join. This sequence follows the [original Kademlia paper](https://www.scs.stanford.edu/~dm/home/papers/kpos.pdf), adapted to the lab's 256-bit IDs.

A bucket keeps contacts in recency order. When full, it PINGs the least-recently-seen contact. If that contact responds, it stays; otherwise the newcomer replaces it. This keeps established live peers while making room when peers disappear.

**Churn** means nodes joining and leaving. Periodic replication looks up the current closest peers and sends fresh copies of locally held values. It never deletes values or attaches a TTL. If every copy disappears before another replica is made, the data is lost; replication cannot reconstruct bytes that no node still has.

## Branch workflow

Run Git commands from the repository root. The team repository is called `arian`; `origin` is the course's starter repository.

This checkout is already on Sprint-2. Before starting work with a clean working tree:

```sh
git switch Sprint-2
git pull --ff-only arian Sprint-2
```

For another checkout that does not yet have a local Sprint-2 branch:

```sh
git fetch arian
git switch --track arian/Sprint-2
```

After making and testing a change, stage the specific files you changed, commit, and push to the shared branch. If a teammate pushed first, commit your own changes, run `git pull --rebase arian Sprint-2`, resolve any conflicts, rerun the relevant tests, and then run `git push arian Sprint-2`. Do not force-push the shared branch.

## Run two nodes locally

Go 1.22 or newer is required. Work inside `labs/`, where `go.mod` lives. No Docker is needed for this demonstration.

In terminal A, starting from the repository root:

```sh
cd labs
printf 'hello from Kademlia\n' > /tmp/kademlia-demo.txt
BIND_IP=127.0.0.1 PORT=8001 REPLICATION_INTERVAL=5s go run .
```

In terminal B, also starting from the repository root:

```sh
cd labs
BIND_IP=127.0.0.1 PORT=8002 BOOTSTRAP=127.0.0.1:8001 REPLICATION_INTERVAL=5s go run .
```

B prints `Joined via 127.0.0.1:8001` before its prompt. Type these commands **inside A's node prompt**, not your regular shell:

```text
show rt
put /tmp/kademlia-demo.txt
show ds
```

Copy the full 64-character key printed by `put`. Inside B's prompt, replace `KEY` with that key:

```text
show rt
get KEY
get KEY /tmp/kademlia-downloaded.txt
```

The value should be `hello from Kademlia`. Seeing `(found locally)` is expected if A already replicated the value to B. In a separate shell, compare the files:

```sh
cmp /tmp/kademlia-demo.txt /tmp/kademlia-downloaded.txt
```

No output and exit status 0 means the bytes match. Type `exit` in each node prompt to stop it.

If you start a node without `BOOTSTRAP`, it starts its own empty network. You can introduce it later with:

```text
join 127.0.0.1:8001
```

Use `ping 127.0.0.1:8001` to check reachability. A PING now also records the responsive peer locally; it does not perform the full self-lookup and refresh sequence.

## See periodic replication handle a departing node

1. Start A with `REPLICATION_INTERVAL=5s`, and put the demo file **before starting B**.
2. Start B with `BOOTSTRAP=127.0.0.1:8001`.
3. On B, use `show ds` until the file's key appears. Do not use `get` yet, because `get` itself downloads and caches a copy. Allow at least one replication interval.
4. Exit A. Run `get KEY` on B. The surviving replica remains available.

Stopping and restarting all nodes loses their in-memory stores. Disk persistence is optional in the lab; TTL/expiration is forbidden.

## Configuration

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `BIND_IP` | First local non-loopback IPv4 address | Concrete local address to listen on and advertise. Use `127.0.0.1` for local demos. |
| `PORT` | `8000` | UDP listener port; use different ports for nodes on one machine. |
| `BOOTSTRAP` | Empty | Existing peer's IP:port or hostname:port. Empty starts the first node. |
| `REPLICATION_INTERVAL` | `1h` | Positive duration such as `5s`, `30s`, or `1h`. |
| `REFRESH_INTERVAL` | `1h` | Scan interval and idle threshold for bucket refresh; must be positive. |
| `ALPHA` | `3` | Maximum concurrent probes per lookup round; positive integer. |
| `HEADLESS` | Empty | Set to `1` to run without a CLI until SIGINT/SIGTERM. Used for service containers. |

Hostnames are resolved before the bootstrap ID is hashed. A service name must resolve to the actual node address, which is why the Docker bootstrap uses DNS round-robin instead of a virtual service IP. `k` is adjustable through `NewRoutingTable(me, k)`; runtime flags for it are not implemented.

## Idle bucket refresh

Each bucket records the last outgoing lookup targeting its XOR distance range. A remote node or value lookup updates that timestamp; a local value-cache hit, a self lookup, and referrals alone do not. Every `REFRESH_INTERVAL`, the worker looks up a random ID in each idle range, including empty buckets. It skips a node with no peers. The timestamp advances even on a failed attempt, so failures are retried after another interval instead of in a tight loop. Buckets start with the node's creation time, preventing an immediate 256-bucket startup sweep.

Refresh passes run sequentially, with no bucket lock held during network calls. Both maintenance workers stop on CLI exit or process cancellation and are joined before the listener closes. Cancellation takes effect between buckets/values; an active lookup finishes under the RPC timeout/retry policy. A scan may take time on an unresponsive network.

For a short local refresh demonstration, set `REFRESH_INTERVAL=5s`. In simulator code, call `RefreshIdleBuckets(duration)` for a pass, or run `RunBucketRefresher(ctx, duration)` as a goroutine when maintenance belongs in the experiment.

## Value size

The maximum value is **4096 bytes (4 KiB)**, inclusive. Empty values are allowed. Values use a single JSON-encoded UDP datagram: 4096 bytes become 5464 base64 characters, with additional RPC metadata. This conservative limit accommodates smaller local UDP send limits as well as the protocol's 65,507-byte IPv4 payload ceiling. A 32 KiB candidate limit failed a real loopback test on the development Mac with `message too long`; the 4 KiB boundary is tested with STORE and FIND_VALUE. Large datagrams can still fragment and be lost; the existing retry policy applies. Larger packages would require chunking or a separate TCP value-transfer plane.

`Store`, `StoreAt`, `StoreValue` (including incoming STORE handlers), outgoing UDP STORE, and acceptance of FIND_VALUE bytes enforce the same limit. Oversized values return `ErrValueTooLarge` and are never cached or partially stored. CLI `put` reads at most 4097 bytes before validation, and reports rejection as `put failed`.

## Lookup instrumentation and experiment integration

The existing `RPCClient`, `LookupContact`, and `LookupData` signatures are unchanged. **The simulated RPC client must support concurrent calls**, including any counters, maps, and random-number generators. Configure experiments with `node.SetAlpha(1)` or `node.SetAlpha(3)`; invalid values return an error. A lookup snapshots alpha when it starts. Alpha is a per-lookup limit, so simultaneous lookups can collectively issue more probes.

Lookup events are JSON lines on stderr by default. Redirect them to a separate file with a shared standard logger:

```go
logger := slog.New(slog.NewJSONHandler(file, nil)) // file is an open io.Writer
node.SetLookupLogger(logger)
if err := node.SetAlpha(3); err != nil {
    return err
}
```

`SetLookupLogger(nil)` disables events explicitly, for example during topology setup. Use a shared handler when multiple simulated nodes write to one file. Other diagnostic messages on stderr are plain text, so a dedicated lookup logger is preferable for analysis.

| Event / field | Meaning |
| --- | --- |
| `event=lookup_probe` | One logical FIND_NODE/FIND_VALUE call dispatched; includes `peer_id`, `peer_address`, and `round`. Transport retransmissions are not additional logical probes. |
| `event=lookup_complete` | Exactly one outcome per call, including input failures and local cache hits. |
| `node_id`, `lookup_id` | Correlation pair; lookup IDs increase within one node instance. Separate process runs should use separate files or run labels. |
| `rpc`, `target`, `alpha` | Operation, target ID/key, and configured parallelism. |
| `probes`, `rounds`, `elapsed_us` | Total logical probes, completed batches, and elapsed microseconds. |
| `success`, `error` | FIND_NODE succeeds when at least one responsive contact is returned; FIND_VALUE succeeds only with verified bytes. Empty routing-table node lookups retain the existing empty-result/nil-error API but log `success=false`. |
| `found_target` | FIND_NODE returned the exact target among its contacts; use this when measuring exact-node discovery. |
| `local` | FIND_VALUE was served from the local store, with zero probes. |

Parallel rounds wait for their slowest RPC, including the final value-lookup round, so no lookup RPC is left running after return. Alpha changes concurrency and latency; it does **not** guarantee fewer total probes or a flat probes/log2(N) ratio. Compare alpha 1 and 3 with the same topology/seeds, `k`, retry policy, and cache state. Exclude join/refresh/replication setup lookups from query measurements by controlling worker lifetime and the logging window. The team's simulated network and experiments are now integrated. The normal test suite includes `TestThousandNodeNetwork`; do not use `-short` when checking the 1000-node requirement.

From `labs/`, run the complete experiments and external analysis with:

```sh
go run ./cmd/experiments -out experiments/results
python3 scripts/analyze.py experiments/results
```

For a smoke test that leaves the committed full results intact:

```sh
go run ./cmd/experiments -quick -seeds 2 -out /tmp/kademlia-experiment-smoke
python3 scripts/analyze.py /tmp/kademlia-experiment-smoke
```

The full default run uses ten seeds per configuration. The analysis writes CSV tables, SVG plots, and a Markdown summary. See [the report](../REPORT.md#experimental-evaluation) for methodology, expectations, results, and limitations.

## Docker

From `labs/`, build the image:

```sh
docker build -t kadlab:latest .
```

For one interactive node:

```sh
docker run --rm -it kadlab:latest
```

For the Swarm configuration, initialize Swarm if this Docker engine is not already in one:

```sh
docker swarm init
docker stack deploy --resolve-image never -c docker-compose.yml kademlia
docker service ls
docker service logs kademlia_kademliaNodes
```

The configuration starts one bootstrap and five joining nodes. They run headlessly so a closed stdin does not terminate the service. Bootstrap startup can race with joining containers; failed joins exit with an error and the service's restart policy retries them.

For 50 nodes total:

```sh
docker service scale kademlia_kademliaNodes=49
```

To open an additional interactive node on that network and make the files in `labs/` available inside it:

```sh
docker run --rm -it --network kademlia_kademlia_network \
  -e BOOTSTRAP=bootstrap:8000 \
  -v "$PWD:/files:ro" kadlab:latest
```

You can then run `put /files/README.md`. With no shared volume, filenames refer to files **inside the container**, not your Mac.

To remove this project's services:

```sh
docker stack rm kademlia
```

## Tests and code map

From `labs/`:

```sh
go test -race -coverpkg=./... -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go vet ./...
go build -o /tmp/kadlab .
```

The `total:` line from `go tool cover` is the combined statement coverage. The individual test-package percentages with `-coverpkg=./...` should not be read as that combined total.

| File | Responsibility |
| --- | --- |
| `main.go` | Startup configuration, joining, worker lifetime, and CLI |
| `kademlia/kademlia.go`, `kademlia/lookup.go` | Parallel node/value lookup, JSON instrumentation, replication, size/hash verification, and local values |
| `kademlia/value.go` | Mutable-value validation, copied callback arguments, and revalidation of concurrent updates |
| `kademlia/maintenance.go` | Join, idle bucket refresh, refresh targets, and periodic replication |
| `kademlia/routingtable.go` | Assign contacts to buckets and select nearest peers |
| `kademlia/bucket.go` | Contact recency, PING eviction, and per-bucket locking |
| `kademlia/network.go` | UDP serialization, RPC correlation/timeouts/retries, and request handlers |
| `kademlia/simnetwork.go` | Simulated RPC transport with seeded latency, loss, retries, and node removal |
| `experiment/`, `cmd/experiments/`, `scripts/analyze.py` | Network generation, measurements, 1000-node test, experiment CLI, and external analysis |
| `LAB-SPEC.md` | Required behavior and grading requirements |
| `PART2-DESIGN.md` | Future package registry and version-history design |

The rest of the repository contains course lectures and tutorials; the runnable project is under `labs/`.

## Part 2 integration handoff

The core is ready for Kim's registry package. Before serving RPCs, register the registry callback with `node.SetValueValidator(validator)`, using the signature `func(key *KademliaID, current, incoming []byte) error`. Return nil only after validating the key, ownership, signature, and history. When records are missing, fetch and verify them by hash through this node's `LookupData` inside the callback. An error rejects the pointer update. The callback runs outside core locks and must handle concurrent calls, revalidation after competing writes, and identical updates from retries or replication.

Publish immutable blobs and version records with `Store`; publish the signed pointer at its namespace key with `StoreAt`. Each destination validates independently. `StoreAt` does not prevalidate remote updates, allowing `publish --force` to send attempts that receivers must reject. `FIND_VALUE` returns the last accepted value, and replication preserves its original key. `LookupData` uses a local accepted value when present; it does not poll for a newer global head. The registry and CLI are not wired into `main.go` yet.

A package name such as `example.org:calculator` will have a signed latest pointer pointing to a signed version record. Each version record links to its package blob and to the previous version record:

```text
latest -> record v3 -> record v2 -> record v1
             |            |            |
           blob v3      blob v2      blob v1
```

Versions must have a total order: any two can be compared consistently. Nodes must reject rollback to an older version and reject a different history that does not extend the one they accepted. If a node knows v1 and receives a valid v3 update, it must fetch and validate the missing v2 link before advancing. Signatures tie publications to the domain owner's key. The latest pointer is the controlled exception to ordinary immutable, content-addressed storage. Those registry policies remain Kim's work; the core hook supports their enforcement.

After integration, exercise valid publication and installation, wrong-owner/signature rejection, forks, rollback, missing-history catch-up, and both `--force` and `--prev`. Check `show DOMAIN:PACKAGE`, `show dns DOMAIN`, and decoded `show ds`. Rerun the race/coverage suite with at least 80% combined statement coverage, prepare the 50-container demonstration, finish the report's registry sections, and upload the final report to Canvas before the review.
