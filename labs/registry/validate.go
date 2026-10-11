package registry

import (
	"crypto/ed25519"
	"errors"
	"fmt"
)

const DefaultMaxChain = 1000

type Validator struct {
	Owners   DomainOwners
	Fetch    func(hash string) ([]byte, error)
	MaxChain int
}

func (v *Validator) Validate(key string, current, incoming []byte) error {
	ptr, err := DecodeLatestPointer(incoming)
	if err != nil {
		return err
	}
	if want := PointerKey(ptr.Domain, ptr.Package); key != want {
		return fmt.Errorf("%w: pointer for %s:%s belongs under %s, not %s", ErrKeyMismatch, ptr.Domain, ptr.Package, ShortHash(want), ShortHash(key))
	}
	pub, err := v.ownerKey(ptr.Domain)
	if err != nil {
		return err
	}
	if err := ptr.Verify(pub); err != nil {
		return signerError(err, "latest pointer", ptr.Domain)
	}

	var cur *LatestPointer
	if len(current) > 0 {
		if cur, err = DecodeLatestPointer(current); err != nil {
			return fmt.Errorf("stored pointer: %w", err)
		}
		if PointerKey(cur.Domain, cur.Package) != key {
			return fmt.Errorf("%w: stored pointer is for %s:%s", ErrKeyMismatch, cur.Domain, cur.Package)
		}
		switch ptr.Version.Compare(cur.Version) {
		case -1:
			return fmt.Errorf("%w: %s is older than the current %s", ErrRollback, ptr.Version, cur.Version)
		case 0:
			if ptr.VersionRecordHash == cur.VersionRecordHash {
				return nil
			}
			return fmt.Errorf("%w: version %s already points at record %s, not %s", ErrFork, cur.Version, ShortHash(cur.VersionRecordHash), ShortHash(ptr.VersionRecordHash))
		}
	}

	w := v.walker(pub, ptr.Domain, ptr.Package, false)
	stopAt := ""
	if cur != nil {
		stopAt = cur.VersionRecordHash
	}
	reached, err := w.runFrom(ptr, stopAt, func(hash string, rec *VersionRecord) (bool, error) {
		if cur != nil && rec.Version.Compare(cur.Version) <= 0 {
			if rec.Previous == cur.VersionRecordHash {
				return false, fmt.Errorf("%w: %s (record %s) follows the current %s", ErrVersionOrder, rec.Version, ShortHash(hash), cur.Version)
			}
			return false, fmt.Errorf("%w: history of %s runs through %s (record %s) without meeting the current record %s", ErrFork, ptr.Version, rec.Version, ShortHash(hash), ShortHash(cur.VersionRecordHash))
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	if cur != nil && !reached {
		return fmt.Errorf("%w: history of %s starts without containing the current record %s", ErrFork, ptr.Version, ShortHash(cur.VersionRecordHash))
	}
	return nil
}

func (v *Validator) ValidateRecord(raw []byte) (*VersionRecord, error) {
	rec, err := DecodeVersionRecord(raw)
	if err != nil {
		return nil, err
	}
	pub, err := v.ownerKey(rec.Domain)
	if err != nil {
		return nil, err
	}
	if err := rec.Verify(pub); err != nil {
		return nil, signerError(err, "version record", rec.Domain)
	}
	return rec, nil
}

func (v *Validator) ownerKey(domain string) (ed25519.PublicKey, error) {
	if v.Owners == nil {
		return nil, fmt.Errorf("%w: %s (no owner source configured)", ErrNoOwner, domain)
	}
	pub, err := v.Owners.PublicKey(domain)
	if err != nil {
		return nil, fmt.Errorf("owner of %s: %w", domain, err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: owner key of %s has %d bytes", ErrOwnerRecord, domain, len(pub))
	}
	return pub, nil
}

func (v *Validator) walker(pub ed25519.PublicKey, domain, pkg string, skipSig bool) *walker {
	limit := v.MaxChain
	if limit <= 0 {
		limit = DefaultMaxChain
	}
	return &walker{fetch: v.Fetch, pub: pub, domain: domain, pkg: pkg, limit: limit, skipSig: skipSig}
}

// A bad signature cannot be told apart from one made by a non-owner, so the error answers to both.
func signerError(err error, what, domain string) error {
	if errors.Is(err, ErrBadSignature) {
		return fmt.Errorf("%w: %w: %s is not signed by the owner of %s", ErrNotOwner, ErrBadSignature, what, domain)
	}
	return err
}

type walker struct {
	fetch   func(hash string) ([]byte, error)
	pub     ed25519.PublicKey
	domain  string
	pkg     string
	limit   int
	skipSig bool
}

func (w *walker) load(hash string) (*VersionRecord, error) {
	if w.fetch == nil {
		return nil, fmt.Errorf("%w: %s: no way to fetch records", ErrMissingRecord, ShortHash(hash))
	}
	raw, err := w.fetch(hash)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrMissingRecord, ShortHash(hash), err)
	}
	if Hash(raw) != hash {
		return nil, fmt.Errorf("%w: %s: the data returned does not hash to the requested key", ErrMissingRecord, ShortHash(hash))
	}
	rec, err := DecodeVersionRecord(raw)
	if err != nil {
		return nil, fmt.Errorf("record %s: %w", ShortHash(hash), err)
	}
	if rec.Domain != w.domain || rec.Package != w.pkg {
		return nil, fmt.Errorf("%w: record %s belongs to %s:%s, not %s:%s", ErrInconsistent, ShortHash(hash), rec.Domain, rec.Package, w.domain, w.pkg)
	}
	if !w.skipSig {
		if err := rec.Verify(w.pub); err != nil {
			return nil, signerError(err, fmt.Sprintf("record %s", ShortHash(hash)), w.domain)
		}
	}
	return rec, nil
}

// Strictly decreasing versions rule out repeated records, so no cycle check is needed; the record named by stopAt is never fetched.
func (w *walker) run(headHash, stopAt string, visit func(hash string, rec *VersionRecord) (bool, error)) (stopped bool, err error) {
	hash := headHash
	rec, err := w.load(hash)
	if err != nil {
		return false, err
	}
	for n := 1; ; n++ {
		done, err := visit(hash, rec)
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}
		prev := rec.Previous
		if prev == stopAt {
			return true, nil
		}
		if prev == ZeroHash {
			return false, nil
		}
		if n >= w.limit {
			return false, fmt.Errorf("%w: more than %d records", ErrChainTooLong, w.limit)
		}
		next, err := w.load(prev)
		if err != nil {
			return false, err
		}
		if !next.Version.Less(rec.Version) {
			return false, fmt.Errorf("%w: %s (record %s) is followed by %s (record %s)", ErrVersionOrder, next.Version, ShortHash(prev), rec.Version, ShortHash(hash))
		}
		hash, rec = prev, next
	}
}

func (w *walker) runFrom(ptr *LatestPointer, stopAt string, visit func(hash string, rec *VersionRecord) (bool, error)) (bool, error) {
	return w.run(ptr.VersionRecordHash, stopAt, func(hash string, rec *VersionRecord) (bool, error) {
		if hash == ptr.VersionRecordHash && rec.Version != ptr.Version {
			return false, fmt.Errorf("%w: pointer says %s but the record says %s", ErrInconsistent, ptr.Version, rec.Version)
		}
		return visit(hash, rec)
	})
}

// A nil target selects the record the pointer names.
func (w *walker) find(ptr *LatestPointer, target *Version) (string, *VersionRecord, error) {
	var (
		foundHash string
		found     *VersionRecord
	)
	_, err := w.runFrom(ptr, "", func(hash string, rec *VersionRecord) (bool, error) {
		switch {
		case target == nil || rec.Version == *target:
			foundHash, found = hash, rec
			return true, nil
		case rec.Version.Less(*target):
			return false, fmt.Errorf("%w: %s is not in the history of %s:%s", ErrUnknownVersion, target, w.domain, w.pkg)
		}
		return false, nil
	})
	if err != nil {
		return "", nil, err
	}
	if found == nil {
		return "", nil, fmt.Errorf("%w: %s is not in the history of %s:%s", ErrUnknownVersion, target, w.domain, w.pkg)
	}
	return foundHash, found, nil
}
