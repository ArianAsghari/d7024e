package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type DomainOwners interface {
	PublicKey(domain string) (ed25519.PublicKey, error)
}

type StaticOwners struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey
}

func NewStaticOwners() *StaticOwners {
	return &StaticOwners{keys: make(map[string]ed25519.PublicKey)}
}

func LoadStaticOwners(path string) (*StaticOwners, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	owners, err := ParseStaticOwners(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return owners, nil
}

func ParseStaticOwners(data []byte) (*StaticOwners, error) {
	var raw map[string]string
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: owners file: %v", ErrOwnerRecord, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: owners file has trailing data", ErrOwnerRecord)
	}
	owners := NewStaticOwners()
	for domain, b64 := range raw {
		pub, err := DecodePublicKey(b64)
		if err != nil {
			return nil, fmt.Errorf("owner of %q: %w", domain, err)
		}
		if err := owners.Set(domain, pub); err != nil {
			return nil, err
		}
	}
	return owners, nil
}

func (s *StaticOwners) Set(domain string, pub ed25519.PublicKey) error {
	if err := ValidateDomainName(domain); err != nil {
		return err
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: public key has %d bytes", ErrBadKey, len(pub))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[domain] = slices.Clone(pub)
	return nil
}

func (s *StaticOwners) PublicKey(domain string) (ed25519.PublicKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pub, ok := s.keys[domain]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoOwner, domain)
	}
	return slices.Clone(pub), nil
}

func (s *StaticOwners) Domains() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	domains := make([]string, 0, len(s.keys))
	for d := range s.keys {
		domains = append(domains, d)
	}
	slices.Sort(domains)
	return domains
}

type LookupTXTFunc func(ctx context.Context, name string) ([]string, error)

const defaultDNSTimeout = 5 * time.Second

type DNSOwners struct {
	Lookup  LookupTXTFunc
	Timeout time.Duration
}

func NewDNSOwners() *DNSOwners {
	return &DNSOwners{Lookup: net.DefaultResolver.LookupTXT, Timeout: defaultDNSTimeout}
}

func (d *DNSOwners) PublicKey(domain string) (ed25519.PublicKey, error) {
	if err := ValidateDomainName(domain); err != nil {
		return nil, err
	}
	lookup := d.Lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = defaultDNSTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	records, err := lookup(ctx, domain)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, fmt.Errorf("%w: %s has no TXT records", ErrNoOwner, domain)
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrOwnerLookup, domain, err)
	}
	pub, err := ParseOwnerTXT(records)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", domain, err)
	}
	return pub, nil
}

func ParseOwnerTXT(records []string) (ed25519.PublicKey, error) {
	var found []string
	for _, r := range records {
		if strings.HasPrefix(r, TXTPrefix) {
			found = append(found, r[len(TXTPrefix):])
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%w: no %s TXT record", ErrNoOwner, strings.TrimSuffix(TXTPrefix, "="))
	case 1:
		pub, err := DecodePublicKey(found[0])
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrOwnerRecord, err)
		}
		return pub, nil
	default:
		return nil, fmt.Errorf("%w: %d %s TXT records, want exactly one", ErrOwnerRecord, len(found), strings.TrimSuffix(TXTPrefix, "="))
	}
}
