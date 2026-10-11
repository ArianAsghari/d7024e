// Package registry holds the pure logic of the decentralized package registry
// (lab part 2) that sits on top of the Kademlia key-value store. It has no
// dependency on the kademlia package: keys are lowercase hex SHA-256 strings
// and storage is reached through the Backend interface.
//
// # Choices
//
// Signatures are Ed25519. A domain owner proves ownership with exactly one DNS
// TXT record at the apex of the domain whose value is
//
//	kademlia-pk=<standard base64 of the 32-byte public key>
//
// Standard alphabet, with padding, no whitespace; other TXT records (SPF and so
// on) are ignored, no such record means the domain has no owner, and more than
// one means the owner is ambiguous and is rejected. DNS sits behind the
// DomainOwners interface: DNSOwners performs real lookups (with an injectable
// resolver) and StaticOwners is a fixed table, loadable from a JSON file that
// maps domain to base64 key. The key of a domain is assumed not to change; there
// is no key rotation, so the current key must verify the whole history.
//
// A version record is
//
//	{tag="version-record", domain-name, package-name, version, blobHash,
//	 previous-version-record, sig}
//
// and a latest pointer is
//
//	{tag="latest-pointer", domain-name, package-name, version,
//	 versionRecordHash, sig}
//
// Both are encoded as JSON objects with exactly these members in exactly this
// order, no whitespace, and every hash and signature as lowercase hex. Decoding
// is strict: unknown members, trailing data, and any spelling other than the
// canonical encoding are rejected (ErrMalformed), so a record has one byte
// representation and therefore one hash. sig is the hex of the Ed25519
// signature over the 32 bytes SHA-256(canonical encoding of the record without
// its sig member). The tag is inside the signed bytes, so a signature for one
// kind of record can never be replayed as the other kind. A version record is
// stored under SHA-256 of its canonical bytes; previous-version-record is that
// hash of the previous record, or 64 zeros for the first version. The latest
// pointer is stored under SHA-256 of "domain:package:latest" (PointerKey) and
// is the only value whose key is not the hash of the value.
//
// Versions are MAJOR.MINOR.PATCH, each a decimal uint32 without sign or leading
// zeros, compared numerically (1.2.10 is newer than 1.2.9). The literal "latest"
// is reserved for the pointer and is not a version. Domains are lowercase DNS
// names and package names are lowercase letters, digits, '.', '-' and '_', so
// neither can contain the ':' used in DOMAIN:PACKAGE:VERSION.
//
// # Answers to the design questions
//
// When is a version record valid? It decodes strictly, carries the right tag,
// and its signature verifies under the key that DNS gives for its domain. In
// the context of a chain it must also name the same domain and package as the
// pointer that leads to it, its version must be strictly greater than that of
// the record it points back to, and that previous record must itself be valid
// and be found, all the way back to 64 zeros.
//
// When is an update of the latest pointer valid? The pointer must be stored under
// the key of its own domain and package, signed by the domain owner, and refer
// to a valid version record with the same domain, package and version. Relative
// to the pointer currently stored it must then be one of: identical (storing
// again is harmless); or a strictly newer version whose chain of previous links
// reaches exactly the record the stored pointer names. An older version is a
// roll-back (ErrRollback). The same version with a different record, or a newer
// version whose history does not contain the stored record, is a fork
// (ErrFork). A node that has never seen the package accepts a pointer only if
// the whole chain verifies down to the first version. A node that is several
// versions behind accepts a pointer whose chain it can walk back to its own
// latest record, and in doing so catches up; if a record on the way cannot be
// fetched it answers ErrMissingRecord, which means "behind, cannot verify yet".
//
// How is an update noticed? Every store of a key that is not the hash of its
// value is passed to Validator.Validate together with the value currently held,
// so a node sees an update exactly when it is handed a different pointer for a
// key it already has.
//
// Why is the pointer structured and why does it repeat domain, package and
// version? A bare hash cannot carry a signature that binds it to a package, so
// anyone could point the mutable key at any record, such as an old one. Repeating
// the names binds the signature to the key, so a pointer cannot be replayed under
// another package, and repeating the version lets a node reject roll-backs and
// same-version forks by comparing two small values before it fetches anything.
// It is also a cross-check: the record the pointer leads to must agree.
//
// Why strictly increasing versions? A total order is what makes "newer" and
// "older" decidable by a node on its own, which roll-back and fork detection
// need. Labels like "final" or "beta" have no order, so an attacker could replay
// an old signed pointer and no node could tell a downgrade from an upgrade.
// Strictness means each version has at most one record, so history cannot fork
// at a version, and since a hash cannot refer to a later record, chains cannot
// contain cycles.
//
// # Limits
//
// A chain longer than Validator.MaxChain records (DefaultMaxChain when zero) is
// rejected with ErrChainTooLong, which bounds the work a single store can cost.
// Validation does not check that the package blob itself exists; Client.Install
// does, and checks that it hashes to the recorded blobHash. A node that has no
// state for a package cannot know about a fork that happened before it first
// heard of the package; it accepts the first complete, valid chain it is shown.
// Owner keys are looked up on every call and not cached.
package registry
