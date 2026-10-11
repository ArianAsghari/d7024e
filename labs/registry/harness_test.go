package registry

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"testing"
)

const testDomain = "example.com"

func mustKey(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func mustVersion(t testing.TB, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func flipHex(s string) string {
	if s[0] == '0' {
		return "1" + s[1:]
	}
	return "0" + s[1:]
}

// A replica keeps a mutable value only if its own Validator accepts it, and fetches records from any replica like a DHT lookup.
type replica struct {
	imm      map[string][]byte
	mut      map[string][]byte
	validate *Validator
	down     bool
	lastErr  error
}

type testNet struct {
	owners   DomainOwners
	maxChain int
	replicas []*replica
	readFrom int
	immPuts  int
	mutPuts  int
}

func newTestNet(owners DomainOwners, replicas, maxChain int) *testNet {
	n := &testNet{owners: owners, maxChain: maxChain}
	for range replicas {
		n.addReplica()
	}
	return n
}

func (n *testNet) addReplica() *replica {
	r := &replica{imm: map[string][]byte{}, mut: map[string][]byte{}}
	r.validate = &Validator{Owners: n.owners, MaxChain: n.maxChain, Fetch: n.lookup(r)}
	n.replicas = append(n.replicas, r)
	return r
}

func (n *testNet) lookup(from *replica) func(string) ([]byte, error) {
	return func(hash string) ([]byte, error) {
		if b, ok := from.imm[hash]; ok {
			return b, nil
		}
		for _, r := range n.replicas {
			if b, ok := r.imm[hash]; ok {
				return b, nil
			}
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	}
}

func (n *testNet) PutImmutable(data []byte) (string, error) {
	n.immPuts++
	h := Hash(data)
	for _, r := range n.replicas {
		if !r.down {
			r.imm[h] = bytes.Clone(data)
		}
	}
	return h, nil
}

func (n *testNet) GetImmutable(hash string) ([]byte, error) {
	return n.lookup(n.replicas[n.readFrom])(hash)
}

func (n *testNet) PutMutable(key string, value []byte) error {
	n.mutPuts++
	var errs []error
	accepted := 0
	for _, r := range n.replicas {
		r.lastErr = nil
		if r.down {
			continue
		}
		if r.lastErr = r.validate.Validate(key, r.mut[key], value); r.lastErr != nil {
			errs = append(errs, r.lastErr)
			continue
		}
		r.mut[key] = bytes.Clone(value)
		accepted++
	}
	if accepted == 0 {
		return fmt.Errorf("store rejected by every replica: %w", errors.Join(errs...))
	}
	return nil
}

func (n *testNet) GetMutable(key string) ([]byte, error) {
	v, ok := n.replicas[n.readFrom].mut[key]
	if !ok {
		return nil, fmt.Errorf("%w: key %s", ErrNotFound, key)
	}
	return bytes.Clone(v), nil
}

func (n *testNet) drop(hash string) {
	for _, r := range n.replicas {
		delete(r.imm, hash)
	}
}

func (n *testNet) overwrite(hash string, data []byte) {
	for _, r := range n.replicas {
		if _, ok := r.imm[hash]; ok {
			r.imm[hash] = bytes.Clone(data)
		}
	}
}

func (n *testNet) puts() (int, int) {
	return n.immPuts, n.mutPuts
}

func (r *replica) pointer(t testing.TB, domain, pkg string) *LatestPointer {
	t.Helper()
	raw, ok := r.mut[PointerKey(domain, pkg)]
	if !ok {
		return nil
	}
	p, err := DecodeLatestPointer(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (r *replica) version(t testing.TB, domain, pkg string) string {
	t.Helper()
	p := r.pointer(t, domain, pkg)
	if p == nil {
		return "none"
	}
	return p.Version.String()
}

type fixture struct {
	t      *testing.T
	domain string
	pkg    string
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	owners *StaticOwners
	net    *testNet
	client *Client
}

func newFixture(t *testing.T, replicas, maxChain int) *fixture {
	t.Helper()
	pub, priv := mustKey(t)
	owners := NewStaticOwners()
	if err := owners.Set(testDomain, pub); err != nil {
		t.Fatal(err)
	}
	n := newTestNet(owners, replicas, maxChain)
	c := NewClient(n, owners)
	c.MaxChain = maxChain
	return &fixture{t: t, domain: testDomain, pkg: "tool", pub: pub, priv: priv, owners: owners, net: n, client: c}
}

func content(version string) []byte {
	return []byte("package content of version " + version)
}

func (f *fixture) opts(version string) PublishOptions {
	return PublishOptions{Domain: f.domain, Package: f.pkg, Version: mustVersion(f.t, version), Data: content(version)}
}

func (f *fixture) publish(version string) PublishResult {
	f.t.Helper()
	res, err := f.client.Publish(f.opts(version), f.priv)
	if err != nil {
		f.t.Fatalf("publish %s: %v", version, err)
	}
	return res
}

func (f *fixture) publishAll(versions ...string) {
	f.t.Helper()
	for _, v := range versions {
		f.publish(v)
	}
}

func (f *fixture) versions() []string {
	f.t.Helper()
	chain, err := f.client.Chain(f.domain, f.pkg)
	if err != nil {
		f.t.Fatal(err)
	}
	out := make([]string, len(chain))
	for i, e := range chain {
		out[i] = e.Version.String()
	}
	return out
}

func (f *fixture) replicaVersions() []string {
	f.t.Helper()
	var out []string
	for _, r := range f.net.replicas {
		out = append(out, r.version(f.t, f.domain, f.pkg))
	}
	return out
}

func equalStrings(a, b []string) bool {
	return slices.Equal(a, b)
}

// builder crafts records and pointers directly, including histories the Client would refuse to produce.
type builder struct {
	t      *testing.T
	domain string
	pkg    string
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
	imm    map[string][]byte
}

func newBuilder(t *testing.T) *builder {
	pub, priv := mustKey(t)
	return &builder{t: t, domain: testDomain, pkg: "tool", priv: priv, pub: pub, imm: map[string][]byte{}}
}

func (b *builder) owners() DomainOwners {
	o := NewStaticOwners()
	if err := o.Set(b.domain, b.pub); err != nil {
		b.t.Fatal(err)
	}
	return o
}

func (b *builder) validator(max int) *Validator {
	return &Validator{Owners: b.owners(), MaxChain: max, Fetch: b.fetch}
}

func (b *builder) fetch(hash string) ([]byte, error) {
	if raw, ok := b.imm[hash]; ok {
		return raw, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
}

func (b *builder) recordSignedBy(priv ed25519.PrivateKey, version, prev string) (raw []byte, hash string) {
	b.t.Helper()
	r, err := NewVersionRecord(priv, b.domain, b.pkg, mustVersion(b.t, version), Hash(content(version)), prev)
	if err != nil {
		b.t.Fatal(err)
	}
	raw, err = r.Encode()
	if err != nil {
		b.t.Fatal(err)
	}
	hash = RecordHash(raw)
	b.imm[hash] = raw
	return raw, hash
}

func (b *builder) record(version, prev string) string {
	b.t.Helper()
	_, h := b.recordSignedBy(b.priv, version, prev)
	return h
}

func (b *builder) chain(versions ...string) []string {
	b.t.Helper()
	prev := ZeroHash
	var hashes []string
	for _, v := range versions {
		prev = b.record(v, prev)
		hashes = append(hashes, prev)
	}
	return hashes
}

func (b *builder) pointerSignedBy(priv ed25519.PrivateKey, version, recHash string) []byte {
	b.t.Helper()
	p, err := NewLatestPointer(priv, b.domain, b.pkg, mustVersion(b.t, version), recHash)
	if err != nil {
		b.t.Fatal(err)
	}
	raw, err := p.Encode()
	if err != nil {
		b.t.Fatal(err)
	}
	return raw
}

func (b *builder) pointer(version, recHash string) []byte {
	return b.pointerSignedBy(b.priv, version, recHash)
}

func (b *builder) key() string {
	return PointerKey(b.domain, b.pkg)
}
