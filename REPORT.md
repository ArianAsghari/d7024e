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
- **Core algorithm** (`kademlia.go`, `lookup.go`): implements iterative node and value lookups in rounds of up to alpha concurrent probes (default 3; adjustable through `ALPHA` or `SetAlpha`). Each peer is queried at most once; candidates are owned by the coordinator, which incorporates RPC results between rounds. Value lookup checks size and `SHA-256(value) == key` before caching. `Store` replicates to the *k* closest nodes.
- **Network layer** (`network.go`): the four RPCs (PING, STORE, FIND_NODE, FIND_VALUE) are defined behind `RPCClient`, so the transport can be replaced for simulated tests. The real implementation uses UDP, JSON serialization, random request IDs, response correlation, and configurable timeouts/retries.
- **Joining** (`maintenance.go`): contact a bootstrap node, insert it into the routing table, look up our own ID, and perform lookups for random IDs in every bucket farther away than the closest responsive neighbor. Bucket 0 is the farthest range in our implementation. Startup accepts `BOOTSTRAP=IP:PORT`; the CLI also supports `join IP:PORT`.
- **Periodic replication** (`maintenance.go`): one worker per node periodically copies its local store and republishes those values to the current closest nodes. `REPLICATION_INTERVAL` defaults to one hour. Local values have no TTL and are never removed by this worker.
- **Periodic routing refresh** (`maintenance.go`): a separate worker scans for buckets with no outgoing lookup in their range for `REFRESH_INTERVAL` (default one hour). It looks up a random ID in each idle range, including empty buckets, and skips isolated nodes. A failed refresh advances the attempt timestamp and is retried after the interval.
- **Instrumentation** (`lookup.go`): JSON probe and outcome events correlate by node ID and lookup ID. Outcomes include success, errors, logical probe counts, rounds, elapsed time, local cache hits, and exact-target discovery. Retransmissions are excluded from logical probe counts.
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
- Adjustable alpha-parallel node and value lookup (default 3)
- Periodic idle-bucket refresh with cancellation
- A 4096-byte value limit enforced at store and retrieval boundaries
- JSON lookup probe/outcome instrumentation
- Thread-safety discussion below

**Remaining work:**
- Simulated network abstraction for 1000+ node testing
- The required scalability and packet-loss experiments using the updated core
- Part 2 (decentralized package registry)

## Thread safety

The CLI, UDP request handlers, concurrent lookup probes, and periodic replication and refresh workers can access one node concurrently. The critical shared state is the local key-value map, each bucket's contact list, eviction state and lookup timestamp, lookup configuration, and the network transport's lifecycle fields.

### Local values (`kademlia.go`)

`Kademlia.dataMu` is a `sync.RWMutex`. `StoreValue` takes its exclusive lock while initializing or updating the map; `localValue` and `Entries` take a read lock while inspecting it. Stored and returned byte slices are copied, preventing callers from modifying the store through an aliased slice. `Entries` returns a snapshot for display and replication. The replication worker releases this read lock before doing lookups or sending STORE RPCs, so a slow peer cannot block incoming STORE requests by holding the data lock.

### Routing tables and buckets (`routingtable.go`, `bucket.go`)

The node's identity, bucket array, configured capacity, and PING callback are initialized before concurrent use and then remain fixed. Each bucket has its own `sync.RWMutex`: inserts, recency updates, and eviction-state changes take the write lock; list traversal and length queries take a read lock. Reads copy contact structs before calculating distances or sorting, so lookup-specific distances do not modify contacts in the shared list. Contact IDs are treated as immutable after construction, including IDs exposed through contact snapshots.

A full bucket selects its least-recently-seen entry under the write lock and records that entry's observation sequence. It then releases the lock before sending PING. Only one eviction probe is allowed per bucket at a time; other existing contacts can still be refreshed, and competing new contacts are discarded while the probe is active. On completion, the bucket reacquires the lock. A failed PING removes the selected contact only if its sequence has not changed: a packet received during the probe therefore prevents a stale timeout from evicting a now-responsive contact. A successful PING moves the contact to the most-recently-seen end. No bucket lock is held across an RPC or while acquiring another bucket's lock.

`FindClosestContacts` and `Buckets` collect separate bucket snapshots. They are safe to call concurrently with updates, but do not promise a single atomic snapshot of the entire routing table. Lookup candidate maps remain local to each invocation and are mutated only by that lookup's coordinator. Up to alpha workers perform RPCs and send results through a buffered channel; the entire batch is collected before processing. No candidate-map mutex is needed. `configMu` protects alpha and logger changes, and each lookup snapshots both settings. Lookup IDs use an atomic counter; standard `slog` handlers serialize concurrent writes. Simulated transports must independently protect their mutable state.

Bucket lookup timestamps share the bucket mutex. The refresh worker atomically checks and advances an idle timestamp before releasing the lock and making a lookup, avoiding duplicate scheduling by concurrent passes. Contact recency and lookup activity are separate: receiving a referral or reading a cached local value does not postpone a range's refresh.

### Network and worker lifecycle (`network.go`, `maintenance.go`, `main.go`)

`Network.mu` protects the attached Kademlia pointer, listener connection, and closed flag. The receive loop copies each incoming datagram before handing it to a handler goroutine. Outgoing RPCs use their own request buffers, socket, and random request ID. Handlers send their response before updating routing state, because admitting a sender may trigger another PING; this prevents the reply from being delayed behind an eviction probe. Network identity, timeout, and retry settings are configured before concurrent use and are not changed at runtime.

One replication goroutine and one refresh goroutine each run passes sequentially. Context cancellation and a wait group stop and join both before the node closes its listener. Cancellation is checked between stored values or buckets; in-flight lookups/stores finish under the transport's timeout/retry policy. Network or bucket locks are not held while waiting for workers. A value lookup also drains its current batch before returning, leaving no outstanding lookup RPCs behind.

### Verification

On October 9, `go test -race -coverpkg=./... -coverprofile=coverage.out ./...`, `go vet ./...`, and `go build` passed on the development Mac with Go 1.26.3. The combined statement coverage is **88.4%**, up from the September 28 baseline of 86.1%. Tests exercise concurrent routing-table/value access, full-bucket eviction, refresh-target ranges, bootstrap ordering, replication after churn, and worker cancellation. New tests use barriers to verify overlapping probes and alpha bounds for both lookup types, check JSON probe/outcome counts, duplicate referrals and timeouts, idle versus active ranges, refresh of empty ranges, and value-size boundaries. Real loopback UDP tests cover multi-peer lookups and a maximum-sized value round trip. These checks cover the exercised execution paths; a clean race-detector run is not a proof of all possible interleavings.

A three-node end-to-end check was also run using local processes and then Docker containers. Node A stored a file; B joined through the bootstrap address and fetched the bytes from A. Node C joined later and received the value through B's periodic replication without issuing a GET. After both A and B exited, C still returned the same bytes from its local replica. The Docker image builds with Go 1.22. The updated Swarm configuration was validated, but this Sprint 2 check did not deploy a 50-node Swarm.

## Limitations and possible improvements

- Lookup rounds wait for the slowest RPC in the batch. Alpha is a per-lookup limit, not a node-wide limit. Parallelism does not itself guarantee fewer total probes; the experiments must compare probe counts separately from rounds and latency.
- The 1000-node simulator and required experiments remain outstanding in this checkout.
- Values are kept in memory. Replication helps only while at least one copy survives; stopping all nodes loses the data. Disk persistence is optional in the lab specification.
- Values travel inside single JSON-encoded UDP datagrams and are limited to **4096 bytes (4 KiB)**. Base64 expands that to 5464 characters plus metadata. This conservative limit exceeds the required 255-byte minimum and accommodates the development Mac's smaller UDP send limit: a proposed 32 KiB value failed a real loopback test with `message too long`. The wire encoder also checks the 65,507-byte IPv4 payload ceiling. Fragmentation/loss remains possible. Larger values need chunking or a TCP data plane.
- Idle refresh can perform up to 256 lookups in a scan; scans are sequential and an unresponsive network can delay the next pass or shutdown until the current lookup finishes.
- Part 2's signed version records, mutable latest pointers, history validation, and catch-up are Sprint 3 work.

Run instructions and an explanation of the data flow are in [labs/RUNNING.md](labs/RUNNING.md).

## Sprint 1 plan / reflection

This sprint focused on the two most foundational, highest-risk pieces in parallel: containerization (to demonstrate node communication) and the core lookup algorithm (since everything else depends on it). Work was split across separate branches per person to avoid merge conflicts, coordinated around a shared `RPCClient` interface contract agreed on up front. Next sprint's focus: finish the UDP network layer, integrate all pieces end-to-end, and begin Part 2.
