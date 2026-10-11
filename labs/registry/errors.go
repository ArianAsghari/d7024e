package registry

import "errors"

var (
	ErrMalformed      = errors.New("malformed record")
	ErrInvalidName    = errors.New("invalid name")
	ErrInvalidVersion = errors.New("invalid version")
	ErrBadKey         = errors.New("bad key")
	ErrBadSignature   = errors.New("bad signature")
	ErrNotOwner       = errors.New("signer is not the domain owner")
	ErrNoOwner        = errors.New("domain has no owner key")
	ErrOwnerRecord    = errors.New("invalid owner record")
	ErrOwnerLookup    = errors.New("owner lookup failed")
	ErrKeyMismatch    = errors.New("storage key does not match domain and package")
	ErrInconsistent   = errors.New("pointer and version record disagree")
	ErrRollback       = errors.New("update would roll back to an older version")
	ErrFork           = errors.New("update would fork the version history")
	ErrVersionOrder   = errors.New("versions do not strictly increase along the chain")
	ErrChainTooLong   = errors.New("version chain exceeds the maximum length")
	ErrMissingRecord  = errors.New("version record unavailable")
	ErrNotFound       = errors.New("not found")
	ErrUnknownVersion = errors.New("unknown version")
	ErrBlobMismatch   = errors.New("blob does not match its recorded hash")
	ErrBadArgs        = errors.New("bad arguments")
)
