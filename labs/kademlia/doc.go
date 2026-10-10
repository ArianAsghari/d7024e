// Package kademlia implements a distributed key-value store with SHA-256 keys,
// iterative lookups, UDP RPCs, and periodic replication.
//
// Store publishes immutable blobs under their content hashes. For mutable
// registry pointers, install a ValueValidator with SetValueValidator on every
// participating node, then publish with StoreAt using the SHA-256 key of
// "domain:package:latest". StoreValue handles local acceptance; StoreAt sends to
// the k closest nodes and aggregates destination errors. The publisher does not
// prevalidate remote writes, allowing the registry's forced-publication command
// to attempt invalid updates that receiving nodes must reject.
//
// The registry owns value encoding, signatures, ownership, and history policy.
// Its validator checks the key and both the current and incoming pointer. If
// history is missing, the callback can use this node's LookupData to fetch each
// immutable version record by hash and verify the chain before returning nil.
// Any error rejects the pointer update; immutable records already fetched remain
// cached. There is no special "behind" error understood by the core: fetching
// and verifying missing history happens inside the callback. Callbacks must
// handle concurrent calls, repeated validation after a competing write, and
// identical updates from retries or replication. Bound history traversal and
// reject cycles in the registry; do not recursively validate the same pointer.
//
// FIND_VALUE serves the last accepted local value. LookupData validates remote
// values through StoreValue and caches accepted results; a local hit does not
// query the network for a newer pointer. Republish preserves each original key.
// Without a validator, non-content-addressed writes return ErrHashMismatch.
// A content-addressed blob cannot replace an accepted mutable value, even when
// it is the known preimage of the pointer's key; this returns ErrValueConflict.
package kademlia
