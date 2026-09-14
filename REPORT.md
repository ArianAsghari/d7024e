# D7024E Lab Report — Kademlia DHT

**Group number:** 9
**Group members:** Sungtae Kim, Jerald Jia Wei Lim, Arian Asghari
**Repository:** https://github.com/ArianAsghari/d7024e (branch: `Sprint-1`)

## Frameworks and tools

- Go 1.22 — standard library only so far, no external dependencies
- Docker + Docker Swarm for containerization
- `crypto/sha256` for content hashing and node ID derivation

## System architecture overview

We are implementing the Kademlia DHT as specified in the course's `LAB-SPEC.md`, using 256-bit (SHA-256) node IDs and keys.

- **Node identity**: each node's ID is derived as `SHA-256(IP:port)`, computed at startup from the container's own network address.
- **Routing table**: a simplified flat array of 256 k-buckets (one per bit position of the XOR distance) rather than the general splitting tree. Each bucket holds up to *k* contacts (default *k* = 10, configurable), using an LRU-style replacement policy.
- **Core algorithm** (`kademlia.go`): implements the iterative node lookup (`LookupContact`), value lookup (`LookupData`, which validates `SHA-256(value) == key` before accepting any returned value), and `Store` (replicates a value to the *k* nodes closest to its hash). Sprint 1 uses a sequential (α = 1) lookup as a deliberate simplification.
- **Network layer** (`network.go`): the four RPCs (PING, STORE, FIND_NODE, FIND_VALUE) are defined behind an `RPCClient` interface that the core algorithm depends on, so the transport can later be swapped between a real UDP implementation and a simulated network for large-scale testing. The concrete UDP implementation is in progress.
- **Containerization**: each node runs as a separate Docker container from a single image (`kadlab`), deployed via Docker Swarm (`docker stack deploy`). We've verified containers can reach each other over the Docker overlay network and tested scaling to 50 replicas.

## Current status (Sprint 1)

**Done:**
- Docker build + multi-container deployment; verified node-to-node connectivity and scaling to 50 nodes
- Node ID generation via `hash(IP|port)`
- Core Kademlia algorithm (lookup, store, value retrieval with hash verification), unit-tested at 87.7% coverage, passes with `-race`

**In progress:**
- UDP-based network/RPC layer implementing `RPCClient`
- Wiring the network layer into `main.go` once ready

**Not started:**
- Simulated network abstraction for 1000+ node testing
- CLI (`put`/`get`/`exit`)
- Part 2 (decentralized package registry)

## Limitations and possible improvements

- k-buckets currently drop new contacts once full, rather than PING-ing the least-recently-seen contact and evicting it if unresponsive as described in the original paper — planned before final submission.
- The lookup algorithm is sequential (α = 1) rather than parallel; a deliberate Sprint 1 simplification, to revisit once correctness is established.
- No persistence, periodic replication, or CLI yet.

## Sprint 1 plan / reflection

This sprint focused on the two most foundational, highest-risk pieces in parallel: containerization (to demonstrate node communication) and the core lookup algorithm (since everything else depends on it). Work was split across separate branches per person to avoid merge conflicts, coordinated around a shared `RPCClient` interface contract agreed on up front. Next sprint's focus: finish the UDP network layer, integrate all pieces end-to-end, and begin Part 2.
