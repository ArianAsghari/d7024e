package registry

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	TagVersionRecord = "version-record"
	TagLatestPointer = "latest-pointer"

	ZeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

	MaxRecordSize = 4096
)

type VersionRecord struct {
	Tag      string  `json:"tag"`
	Domain   string  `json:"domain-name"`
	Package  string  `json:"package-name"`
	Version  Version `json:"version"`
	BlobHash string  `json:"blobHash"`
	Previous string  `json:"previous-version-record"`
	Sig      string  `json:"sig,omitempty"`
}

type LatestPointer struct {
	Tag               string  `json:"tag"`
	Domain            string  `json:"domain-name"`
	Package           string  `json:"package-name"`
	Version           Version `json:"version"`
	VersionRecordHash string  `json:"versionRecordHash"`
	Sig               string  `json:"sig,omitempty"`
}

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func RecordHash(raw []byte) string {
	return Hash(raw)
}

func PointerKey(domain, pkg string) string {
	return Hash([]byte(domain + ":" + pkg + ":" + Latest))
}

func NewVersionRecord(priv ed25519.PrivateKey, domain, pkg string, version Version, blobHash, previous string) (*VersionRecord, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: private key has %d bytes", ErrBadKey, len(priv))
	}
	r := &VersionRecord{
		Tag:      TagVersionRecord,
		Domain:   domain,
		Package:  pkg,
		Version:  version,
		BlobHash: blobHash,
		Previous: previous,
	}
	if err := r.checkBody(); err != nil {
		return nil, err
	}
	h, err := r.SigningHash()
	if err != nil {
		return nil, err
	}
	r.Sig = hex.EncodeToString(ed25519.Sign(priv, h))
	return r, nil
}

func NewLatestPointer(priv ed25519.PrivateKey, domain, pkg string, version Version, recordHash string) (*LatestPointer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: private key has %d bytes", ErrBadKey, len(priv))
	}
	p := &LatestPointer{
		Tag:               TagLatestPointer,
		Domain:            domain,
		Package:           pkg,
		Version:           version,
		VersionRecordHash: recordHash,
	}
	if err := p.checkBody(); err != nil {
		return nil, err
	}
	h, err := p.SigningHash()
	if err != nil {
		return nil, err
	}
	p.Sig = hex.EncodeToString(ed25519.Sign(priv, h))
	return p, nil
}

func (r *VersionRecord) Encode() ([]byte, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	return json.Marshal(r)
}

func (p *LatestPointer) Encode() ([]byte, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

func DecodeVersionRecord(raw []byte) (*VersionRecord, error) {
	var r VersionRecord
	if err := decodeCanonical(raw, &r, (*VersionRecord).check); err != nil {
		return nil, err
	}
	return &r, nil
}

func DecodeLatestPointer(raw []byte) (*LatestPointer, error) {
	var p LatestPointer
	if err := decodeCanonical(raw, &p, (*LatestPointer).check); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *VersionRecord) SigningHash() ([]byte, error) {
	return signingHash(r.withoutSig())
}

func (p *LatestPointer) SigningHash() ([]byte, error) {
	return signingHash(p.withoutSig())
}

func (r *VersionRecord) Verify(pub ed25519.PublicKey) error {
	h, err := r.SigningHash()
	if err != nil {
		return err
	}
	return verifySig(pub, h, r.Sig)
}

func (p *LatestPointer) Verify(pub ed25519.PublicKey) error {
	h, err := p.SigningHash()
	if err != nil {
		return err
	}
	return verifySig(pub, h, p.Sig)
}

func (r *VersionRecord) withoutSig() *VersionRecord {
	c := *r
	c.Sig = ""
	return &c
}

func (p *LatestPointer) withoutSig() *LatestPointer {
	c := *p
	c.Sig = ""
	return &c
}

func (r *VersionRecord) checkBody() error {
	if r.Tag != TagVersionRecord {
		return fmt.Errorf("%w: tag %q, want %q", ErrMalformed, r.Tag, TagVersionRecord)
	}
	if err := checkNames(r.Domain, r.Package); err != nil {
		return err
	}
	if !isHash(r.BlobHash) {
		return fmt.Errorf("%w: blobHash is not a lowercase hex SHA-256", ErrMalformed)
	}
	if !isHash(r.Previous) {
		return fmt.Errorf("%w: previous-version-record is not a lowercase hex SHA-256", ErrMalformed)
	}
	return nil
}

func (r *VersionRecord) check() error {
	if err := r.checkBody(); err != nil {
		return err
	}
	return checkSigFormat(r.Sig)
}

func (p *LatestPointer) checkBody() error {
	if p.Tag != TagLatestPointer {
		return fmt.Errorf("%w: tag %q, want %q", ErrMalformed, p.Tag, TagLatestPointer)
	}
	if err := checkNames(p.Domain, p.Package); err != nil {
		return err
	}
	if !isHash(p.VersionRecordHash) {
		return fmt.Errorf("%w: versionRecordHash is not a lowercase hex SHA-256", ErrMalformed)
	}
	return nil
}

func (p *LatestPointer) check() error {
	if err := p.checkBody(); err != nil {
		return err
	}
	return checkSigFormat(p.Sig)
}

func checkNames(domain, pkg string) error {
	if err := ValidateDomainName(domain); err != nil {
		return err
	}
	return ValidatePackageName(pkg)
}

func checkSigFormat(sig string) error {
	if len(sig) != 2*ed25519.SignatureSize || !isLowerHex(sig) {
		return fmt.Errorf("%w: sig is not a lowercase hex Ed25519 signature", ErrMalformed)
	}
	return nil
}

func signingHash(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}

func verifySig(pub ed25519.PublicKey, h []byte, sigHex string) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: public key has %d bytes", ErrBadKey, len(pub))
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature is not %d hex-encoded bytes", ErrBadSignature, ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub, h, sig) {
		return ErrBadSignature
	}
	return nil
}

// Re-encoding and comparing closes the malleability gaps encoding/json leaves open: case-insensitive names, duplicate keys, null, whitespace.
func decodeCanonical[T any](raw []byte, dst *T, check func(*T) error) error {
	if len(raw) > MaxRecordSize {
		return fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrMalformed, len(raw), MaxRecordSize)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing data after the record", ErrMalformed)
	}
	if err := check(dst); err != nil {
		if errors.Is(err, ErrMalformed) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	canon, err := json.Marshal(dst)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if !bytes.Equal(canon, raw) {
		return fmt.Errorf("%w: not in canonical form", ErrMalformed)
	}
	return nil
}

func isHash(s string) bool {
	return len(s) == 64 && isLowerHex(s)
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func ValidateDomainName(s string) error {
	if s == "" || len(s) > 253 {
		return fmt.Errorf("%w: domain %q must be 1 to 253 bytes", ErrInvalidName, s)
	}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '.' {
			continue
		}
		if err := checkLabel(s, s[start:i]); err != nil {
			return err
		}
		start = i + 1
	}
	return nil
}

func checkLabel(domain, label string) error {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return fmt.Errorf("%w: domain %q has a label that is empty, too long, or starts or ends with '-'", ErrInvalidName, domain)
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return fmt.Errorf("%w: domain %q must be lowercase letters, digits, '-' and '.'", ErrInvalidName, domain)
		}
	}
	return nil
}

func ValidatePackageName(s string) error {
	if s == "" || len(s) > 100 {
		return fmt.Errorf("%w: package %q must be 1 to 100 bytes", ErrInvalidName, s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !alnum && (i == 0 || (c != '.' && c != '-' && c != '_')) {
			return fmt.Errorf("%w: package %q must start with a lowercase letter or digit and contain only lowercase letters, digits, '.', '-' and '_'", ErrInvalidName, s)
		}
	}
	return nil
}
