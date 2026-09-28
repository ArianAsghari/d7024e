# D7024E Lab Report — Kademlia DHT

**Group number:** 9
**Group members:** Sungtae Kim, Jerald Jia Wei Lim, Arian Asghari
**Repository:** https://github.com/ArianAsghari/d7024e (shared development branch: `Sprint-2`)

## Frameworks and tools

- Go 1.22 — standard library only so far, no external dependencies
- Docker + Docker Swarm for containerization
- `crypto/sha256` for content hashing and node ID derivation

## System architecture overview

We are implementing the Kademlia DHT as specified in the course's `LAB-SPEC.md`, using 256-bit (SHA-256) node IDs and keys.

- **Node identity**: each node's ID is derived as `SHA-256(IP:port)`, computed at startup from the container's own network address.
- **Routing table**: a fixed array of 256 k-buckets. Each bucket holds up to *k* contacts (default 10, configurable). Existing contacts move to the most-recently-seen end. A full bucket PINGs its least-recently-seen contact, retaining it if responsive and replacing it if unresponsive.
- **Core algorithm** (`kademlia.go`): implements the iterative node lookup (`LookupContact`), value lookup (`LookupData`, which validates `SHA-256(value) == key` before accepting any returned value), and `Store` (replicates a value to the *k* nodes closest to its hash). Sprint 1 uses a sequential (α = 1) lookup as a deliberate simplification.
- **Network layer** (`network.go`): the four RPCs (PING, STORE, FIND_NODE, FIND_VALUE) are defined behind `RPCClient`, so the transport can be replaced for simulated tests. The real implementation uses UDP, JSON serialization, random request IDs, response correlation, and configurable timeouts/retries.
- **Joining** (`maintenance.go`): contact a bootstrap node, insert it into the routing table, look up our own ID, and perform lookups for random IDs in every bucket farther away than the closest responsive neighbor. Bucket 0 is the farthest range in our implementation. Startup accepts `BOOTSTRAP=IP:PORT`; the CLI also supports `join IP:PORT`.
- **Periodic replication** (`maintenance.go`): one worker per node periodically copies its local store and republishes those values to the current closest nodes. `REPLICATION_INTERVAL` defaults to one hour. Local values have no TTL and are never removed by this worker.
- **CLI** (`main.go`): supports `ping`, `join`, `put`, `get`, `show rt`, `show ds`, and `exit`. Headless container nodes run until signaled to stop.
- **Containerization**: each node runs as a separate Docker container from a single image (`kadlab`), deployed via Docker Swarm (`docker stack deploy`). We've verified containers can reach each other over the Docker overlay network and tested scaling to 50 replicas.

## Current status (Sprint 2)

**Done:**
- Docker build + multi-container deployment; verified node-to-node connectivity and scaling to 50 nodes
- Node ID generation via `hash(IP|port)`
- All three Sprint 1 PRs merged; the shared Sprint 2 branch includes core, UDP networking, and CLI
- Core lookup, store, and value retrieval with hash verification
- PING-based full-bucket eviction, including concurrent contact-refresh protection
- Bootstrap, self-lookup, and join-time bucket refresh
- Periodic replication with configurable interval and cancellation; no expiration
- Thread-safety discussion below

**Remaining work:**
- Parallel lookups with adjustable alpha (default 3)
- Simulated network abstraction for 1000+ node testing
- Structured probe/outcome instrumentation and the required experiments
- Part 2 (decentralized package registry)

## Thread safety

The CLI, UDP request handlers, and periodic replication worker can access one node concurrently. The critical shared state is the local key-value map, each bucket's contact list and eviction state, and the network transport's lifecycle fields.

### Local values (`kademlia.go`)

`Kademlia.dataMu` is a `sync.RWMutex`. `StoreValue` takes its exclusive lock while initializing or updating the map; `localValue` and `Entries` take a read lock while inspecting it. Stored and returned byte slices are copied, preventing callers from modifying the store through an aliased slice. `Entries` returns a snapshot for display and replication. The replication worker releases this read lock before doing lookups or sending STORE RPCs, so a slow peer cannot block incoming STORE requests by holding the data lock.

### Routing tables and buckets (`routingtable.go`, `bucket.go`)

The node's identity, bucket array, configured capacity, and PING callback are initialized before concurrent use and then remain fixed. Each bucket has its own `sync.RWMutex`: inserts, recency updates, and eviction-state changes take the write lock; list traversal and length queries take a read lock. Reads copy contact structs before calculating distances or sorting, so lookup-specific distances do not modify contacts in the shared list. Contact IDs are treated as immutable after construction, including IDs exposed through contact snapshots.

A full bucket selects its least-recently-seen entry under the write lock and records that entry's observation sequence. It then releases the lock before sending PING. Only one eviction probe is allowed per bucket at a time; other existing contacts can still be refreshed, and competing new contacts are discarded while the probe is active. On completion, the bucket reacquires the lock. A failed PING removes the selected contact only if its sequence has not changed: a packet received during the probe therefore prevents a stale timeout from evicting a now-responsive contact. A successful PING moves the contact to the most-recently-seen end. No bucket lock is held across an RPC or while acquiring another bucket's lock.

`FindClosestContacts` and `Buckets` collect separate bucket snapshots. They are safe to call concurrently with updates, but do not promise a single atomic snapshot of the entire routing table. Lookup candidate maps are local to each lookup invocation and are currently updated sequentially. Parallel lookup work must preserve this ownership or introduce appropriate synchronization.

### Network and worker lifecycle (`network.go`, `maintenance.go`, `main.go`)

`Network.mu` protects the attached Kademlia pointer, listener connection, and closed flag. The receive loop copies each incoming datagram before handing it to a handler goroutine. Outgoing RPCs use their own request buffers, socket, and random request ID. Handlers send their response before updating routing state, because admitting a sender may trigger another PING; this prevents the reply from being delayed behind an eviction probe. Network identity, timeout, and retry settings are configured before concurrent use and are not changed at runtime.

One replication goroutine runs passes sequentially. Context cancellation and a completion channel stop and join it before the node closes its listener. Cancellation is checked between stored values; an in-flight store finishes under the transport's timeout/retry policy. Network or bucket locks are not held while waiting for that worker.

### Verification

`go test -race -coverpkg=./... -coverprofile=coverage.out ./...` passes. The combined coverage profile reports **86.1%** of statements in the current implementation. Tests exercise concurrent routing-table reads/writes and value-store reads/writes, full-bucket live/dead outcomes, a contact refreshed during a pending timeout, exact refresh-target bucket ranges, bootstrap/self-lookup ordering, replication after a replica leaves, and periodic-worker cancellation. Startup and CLI joining are also tested with real loopback UDP peers. These checks cover the exercised execution paths; a clean race-detector run is not a proof of all possible interleavings.

A three-node end-to-end check was also run using local processes and then Docker containers. Node A stored a file; B joined through the bootstrap address and fetched the bytes from A. Node C joined later and received the value through B's periodic replication without issuing a GET. After both A and B exited, C still returned the same bytes from its local replica. The Docker image builds with Go 1.22. The updated Swarm configuration was validated, but this Sprint 2 check did not deploy a 50-node Swarm.

## Limitations and possible improvements

- Lookups remain sequential (alpha = 1); the final specification requires adjustable parallel probes.
- The 1000-node simulator, required experiments, and structured lookup logging remain outstanding.
- Values are kept in memory. Replication helps only while at least one copy survives; stopping all nodes loses the data. Disk persistence is optional in the lab specification.
- Values currently travel inside single JSON-encoded UDP datagrams. The application-level value size limit still needs to be made explicit and enforced consistently, accounting for encoding overhead and the actual UDP payload limit.
- Join-time bucket refresh is implemented. Periodic refresh of idle routing buckets is still outstanding; it is separate from periodic value replication.
- Part 2's signed version records, mutable latest pointers, history validation, and catch-up are Sprint 3 work.

Run instructions and an explanation of the data flow are in [labs/RUNNING.md](labs/RUNNING.md).

## Sprint 1 plan / reflection

This sprint focused on the two most foundational, highest-risk pieces in parallel: containerization (to demonstrate node communication) and the core lookup algorithm (since everything else depends on it). Work was split across separate branches per person to avoid merge conflicts, coordinated around a shared `RPCClient` interface contract agreed on up front. Next sprint's focus: finish the UDP network layer, integrate all pieces end-to-end, and begin Part 2.
