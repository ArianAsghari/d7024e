package registry

import (
	"crypto/ed25519"
	"errors"
	"fmt"
)

// GetImmutable and GetMutable must return an error wrapping ErrNotFound when the key is absent.
type Backend interface {
	PutImmutable(data []byte) (hash string, err error)
	GetImmutable(hash string) ([]byte, error)
	PutMutable(key string, value []byte) error
	GetMutable(key string) ([]byte, error)
}

type Client struct {
	Backend  Backend
	Owners   DomainOwners
	MaxChain int
}

type PublishOptions struct {
	Domain  string
	Package string
	Version Version
	Data    []byte
	Prev    *Version
	// Force skips publisher-side update validation, so an invalid update is
	// uploaded and attempted. Receivers still validate it. An explicit Prev
	// must resolve to an existing record before that record can be referenced.
	Force bool
}

type PublishResult struct {
	BlobHash   string
	RecordHash string
	PointerKey string
}

type ChainEntry struct {
	Version    Version
	RecordHash string
	BlobHash   string
}

type Installed struct {
	Version    Version
	RecordHash string
	BlobHash   string
	Data       []byte
}

func NewClient(b Backend, owners DomainOwners) *Client {
	return &Client{Backend: b, Owners: owners}
}

func (c *Client) validator(fetch func(string) ([]byte, error)) *Validator {
	if fetch == nil {
		fetch = c.Backend.GetImmutable
	}
	return &Validator{Owners: c.Owners, Fetch: fetch, MaxChain: c.MaxChain}
}

func (c *Client) Owner(domain string) (ed25519.PublicKey, error) {
	if err := ValidateDomainName(domain); err != nil {
		return nil, err
	}
	return c.validator(nil).ownerKey(domain)
}

func (c *Client) Publish(opts PublishOptions, priv ed25519.PrivateKey) (PublishResult, error) {
	if err := checkNames(opts.Domain, opts.Package); err != nil {
		return PublishResult{}, err
	}
	key := PointerKey(opts.Domain, opts.Package)
	curRaw, err := c.Backend.GetMutable(key)
	if errors.Is(err, ErrNotFound) {
		curRaw, err = nil, nil
	}
	if err != nil {
		return PublishResult{}, fmt.Errorf("reading the latest pointer: %w", err)
	}
	prev, err := c.previousRecord(opts, curRaw)
	if err != nil {
		return PublishResult{}, err
	}

	blobHash := Hash(opts.Data)
	rec, err := NewVersionRecord(priv, opts.Domain, opts.Package, opts.Version, blobHash, prev)
	if err != nil {
		return PublishResult{}, err
	}
	recRaw, err := rec.Encode()
	if err != nil {
		return PublishResult{}, err
	}
	recHash := RecordHash(recRaw)
	ptr, err := NewLatestPointer(priv, opts.Domain, opts.Package, opts.Version, recHash)
	if err != nil {
		return PublishResult{}, err
	}
	ptrRaw, err := ptr.Encode()
	if err != nil {
		return PublishResult{}, err
	}

	if !opts.Force {
		v := c.validator(func(h string) ([]byte, error) {
			if h == recHash {
				return recRaw, nil
			}
			return c.Backend.GetImmutable(h)
		})
		if err := v.Validate(key, curRaw, ptrRaw); err != nil {
			return PublishResult{}, fmt.Errorf("refusing to publish %s:%s:%s: %w", opts.Domain, opts.Package, opts.Version, err)
		}
	}

	res := PublishResult{PointerKey: key}
	if err := c.putImmutable(opts.Data, blobHash); err != nil {
		return res, fmt.Errorf("uploading the package: %w", err)
	}
	res.BlobHash = blobHash
	if err := c.putImmutable(recRaw, recHash); err != nil {
		return res, fmt.Errorf("uploading the version record: %w", err)
	}
	res.RecordHash = recHash
	if err := c.Backend.PutMutable(key, ptrRaw); err != nil {
		return res, fmt.Errorf("updating the latest pointer: %w", err)
	}
	return res, nil
}

func (c *Client) putImmutable(data []byte, want string) error {
	got, err := c.Backend.PutImmutable(data)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("stored under %s, expected %s", ShortHash(got), ShortHash(want))
	}
	return nil
}

func (c *Client) previousRecord(opts PublishOptions, curRaw []byte) (string, error) {
	name := fmt.Sprintf("%s:%s", opts.Domain, opts.Package)
	if len(curRaw) == 0 {
		if opts.Prev != nil {
			return "", fmt.Errorf("%w: %s has no versions, so there is no previous version %s", ErrUnknownVersion, name, opts.Prev)
		}
		return ZeroHash, nil
	}

	var (
		cur *LatestPointer
		w   *walker
		err error
	)
	if opts.Force {
		cur, err = DecodeLatestPointer(curRaw)
		w = c.validator(nil).walker(nil, opts.Domain, opts.Package, true)
	} else {
		var pub ed25519.PublicKey
		if pub, err = c.Owner(opts.Domain); err != nil {
			return "", err
		}
		cur, err = verifiedPointer(curRaw, opts.Domain, opts.Package, pub)
		w = c.validator(nil).walker(pub, opts.Domain, opts.Package, false)
	}
	if err != nil {
		return "", fmt.Errorf("latest pointer of %s: %w", name, err)
	}
	if opts.Prev == nil {
		return cur.VersionRecordHash, nil
	}
	hash, _, err := w.find(cur, opts.Prev)
	return hash, err
}

func (c *Client) Chain(domain, pkg string) ([]ChainEntry, error) {
	ptr, w, err := c.head(domain, pkg)
	if err != nil {
		return nil, err
	}
	var chain []ChainEntry
	_, err = w.runFrom(ptr, "", func(hash string, rec *VersionRecord) (bool, error) {
		chain = append(chain, ChainEntry{Version: rec.Version, RecordHash: hash, BlobHash: rec.BlobHash})
		return false, nil
	})
	return chain, err
}

func (c *Client) Install(domain, pkg, spec string) (*Installed, error) {
	var target *Version
	if spec != Latest {
		v, err := ParseVersion(spec)
		if err != nil {
			return nil, err
		}
		target = &v
	}
	ptr, w, err := c.head(domain, pkg)
	if err != nil {
		return nil, err
	}
	hash, rec, err := w.find(ptr, target)
	if err != nil {
		return nil, err
	}
	blob, err := c.Backend.GetImmutable(rec.BlobHash)
	if err != nil {
		return nil, fmt.Errorf("fetching package %s of %s:%s %s: %w", ShortHash(rec.BlobHash), domain, pkg, rec.Version, err)
	}
	if Hash(blob) != rec.BlobHash {
		return nil, fmt.Errorf("%w: %s:%s %s should hash to %s but hashes to %s", ErrBlobMismatch, domain, pkg, rec.Version, ShortHash(rec.BlobHash), ShortHash(Hash(blob)))
	}
	return &Installed{Version: rec.Version, RecordHash: hash, BlobHash: rec.BlobHash, Data: blob}, nil
}

func (c *Client) head(domain, pkg string) (*LatestPointer, *walker, error) {
	if err := checkNames(domain, pkg); err != nil {
		return nil, nil, err
	}
	pub, err := c.Owner(domain)
	if err != nil {
		return nil, nil, err
	}
	raw, err := c.Backend.GetMutable(PointerKey(domain, pkg))
	if err != nil {
		return nil, nil, fmt.Errorf("latest pointer of %s:%s: %w", domain, pkg, err)
	}
	ptr, err := verifiedPointer(raw, domain, pkg, pub)
	if err != nil {
		return nil, nil, fmt.Errorf("latest pointer of %s:%s: %w", domain, pkg, err)
	}
	return ptr, c.validator(nil).walker(pub, domain, pkg, false), nil
}

func verifiedPointer(raw []byte, domain, pkg string, pub ed25519.PublicKey) (*LatestPointer, error) {
	ptr, err := DecodeLatestPointer(raw)
	if err != nil {
		return nil, err
	}
	if ptr.Domain != domain || ptr.Package != pkg {
		return nil, fmt.Errorf("%w: stored pointer is for %s:%s", ErrKeyMismatch, ptr.Domain, ptr.Package)
	}
	if err := ptr.Verify(pub); err != nil {
		return nil, signerError(err, "latest pointer", domain)
	}
	return ptr, nil
}
