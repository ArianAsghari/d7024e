package registry

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestValidateAccepts(t *testing.T) {
	b := newBuilder(t)
	h := b.chain("1.0.0", "1.1.0", "1.2.0", "1.3.0")
	p0, p1, p3 := b.pointer("1.0.0", h[0]), b.pointer("1.1.0", h[1]), b.pointer("1.3.0", h[3])
	cases := []struct {
		name              string
		current, incoming []byte
	}{
		{"first publication", nil, p0},
		{"first publication seen late: the whole chain is verified", nil, p3},
		{"an empty stored value counts as none", []byte{}, p0},
		{"next version", p0, p1},
		{"catching up over several missed versions", p0, p3},
		{"catching up over one missed version", p1, p3},
		{"identical pointer stored again", p1, p1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.validator(0).Validate(b.key(), tc.current, tc.incoming); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateIdempotentNeedsNoFetch(t *testing.T) {
	b := newBuilder(t)
	h := b.chain("1.0.0")
	p := b.pointer("1.0.0", h[0])
	v := &Validator{Owners: b.owners(), Fetch: func(string) ([]byte, error) { return nil, errors.New("offline") }}
	if err := v.Validate(b.key(), p, p); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsHistoryViolations(t *testing.T) {
	b := newBuilder(t)
	base := b.chain("1.0.0", "1.1.0", "1.2.0", "2.0.0")
	a1, a2, a3 := base[1], base[2], base[3]
	root := base[0]

	altBranch := b.record("1.0.5", root)
	altHead := b.record("1.3.0", altBranch)
	sameVersionOtherRecord := b.record("1.1.0", ZeroHash)
	foreignRoot := b.record("0.5.0", ZeroHash)
	foreignHead := b.record("1.5.0", foreignRoot)
	startsAfter := b.record("2.0.0", ZeroHash)
	startsAfterHead := b.record("3.0.0", startsAfter)
	laterThanHead := b.record("3.0.0", root)
	orderViolation := b.record("1.5.0", laterThanHead)
	equalVersionPrev := b.record("1.5.0", ZeroHash)
	equalVersionHead := b.record("1.5.0", equalVersionPrev)
	followsCurrent := b.record("1.0.0", a3)
	followsCurrentHead := b.record("3.0.0", followsCurrent)
	repeatsCurrent := b.record("1.1.0", a1)
	repeatsCurrentHead := b.record("1.2.0", repeatsCurrent)

	ptr := b.pointer
	cases := []struct {
		name              string
		current, incoming []byte
		want              error
	}{
		{"roll back by one version", ptr("2.0.0", a3), ptr("1.2.0", a2), ErrRollback},
		{"roll back to the first version", ptr("2.0.0", a3), ptr("1.0.0", root), ErrRollback},
		{"roll back by a patch number", ptr("1.2.0", a2), ptr("1.1.9", a1), ErrRollback},
		{"fork: new head built on an older record", ptr("1.2.0", a2), ptr("1.3.0", altHead), ErrFork},
		{"fork: second successor of the same previous version", ptr("1.1.0", a1), ptr("1.2.0", b.record("1.2.0", root)), ErrFork},
		{"fork: same version, other record", ptr("1.1.0", a1), ptr("1.1.0", sameVersionOtherRecord), ErrFork},
		{"fork: unrelated history that drops below the current version", ptr("1.2.0", a2), ptr("1.5.0", foreignHead), ErrFork},
		{"fork: higher history that never meets the current record", ptr("1.0.0", root), ptr("3.0.0", startsAfterHead), ErrFork},
		{"cycle: predecessor has a higher version", ptr("1.0.0", root), ptr("1.5.0", orderViolation), ErrVersionOrder},
		{"cycle: predecessor has the same version", nil, ptr("1.5.0", equalVersionHead), ErrVersionOrder},
		{"cycle: a record claims to follow the current one with a lower version", ptr("2.0.0", a3), ptr("3.0.0", followsCurrentHead), ErrVersionOrder},
		{"cycle: a record claims to follow the current one with the same version", ptr("1.1.0", a1), ptr("1.2.0", repeatsCurrentHead), ErrVersionOrder},
		{"cycle: a record that points at a later record", nil, ptr("1.5.0", orderViolation), ErrVersionOrder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := b.validator(0).Validate(b.key(), tc.current, tc.incoming)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestValidateRollbackNeedsNoFetch(t *testing.T) {
	b := newBuilder(t)
	h := b.chain("1.0.0", "2.0.0")
	v := &Validator{Owners: b.owners()}
	if err := v.Validate(b.key(), b.pointer("2.0.0", h[1]), b.pointer("1.0.0", h[0])); !errors.Is(err, ErrRollback) {
		t.Fatalf("err = %v, want ErrRollback", err)
	}
}

func TestValidateOwnership(t *testing.T) {
	b := newBuilder(t)
	_, attacker := mustKey(t)
	good := b.chain("1.0.0", "1.1.0")

	_, evilHash := b.recordSignedBy(attacker, "1.2.0", good[1])
	_, evilRootHash := b.recordSignedBy(attacker, "0.1.0", ZeroHash)
	chainThroughForgery := b.record("1.0.0", evilRootHash)
	headOnForgery := b.record("1.1.0", chainThroughForgery)

	forgedSig := func(raw []byte) []byte {
		p, err := DecodeLatestPointer(raw)
		if err != nil {
			t.Fatal(err)
		}
		p.Sig = flipHex(p.Sig)
		out, err := p.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	ownerPtr := b.pointer("1.1.0", good[1])

	cases := []struct {
		name              string
		current, incoming []byte
		want              []error
	}{
		{"pointer validly signed by a key that is not the owner's", nil, b.pointerSignedBy(attacker, "1.1.0", good[1]), []error{ErrNotOwner, ErrBadSignature}},
		{"the same pointer as an update", b.pointer("1.0.0", good[0]), b.pointerSignedBy(attacker, "1.1.0", good[1]), []error{ErrNotOwner}},
		{"owner's pointer with a damaged signature", nil, forgedSig(ownerPtr), []error{ErrBadSignature}},
		{"owner's pointer to a record signed by a non-owner", b.pointer("1.1.0", good[1]), b.pointer("1.2.0", evilHash), []error{ErrNotOwner, ErrBadSignature}},
		{"non-owner record deep in an otherwise valid chain", nil, b.pointer("1.1.0", headOnForgery), []error{ErrNotOwner}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := b.validator(0).Validate(b.key(), tc.current, tc.incoming)
			for _, want := range tc.want {
				if !errors.Is(err, want) {
					t.Errorf("err = %v, want %v", err, want)
				}
			}
		})
	}
}

func TestValidateOwnerLookupFailures(t *testing.T) {
	b := newBuilder(t)
	h := b.chain("1.0.0")
	p := b.pointer("1.0.0", h[0])
	lookupFailure := fmt.Errorf("%w: resolver down", ErrOwnerLookup)

	cases := []struct {
		name   string
		owners DomainOwners
		want   error
	}{
		{"no owner source", nil, ErrNoOwner},
		{"unregistered domain", NewStaticOwners(), ErrNoOwner},
		{"lookup failure is passed on", ownersFunc(func(string) (ed25519.PublicKey, error) { return nil, lookupFailure }), ErrOwnerLookup},
		{"ambiguous owner record is passed on", ownersFunc(func(string) (ed25519.PublicKey, error) { return nil, ErrOwnerRecord }), ErrOwnerRecord},
		{"key of the wrong size", ownersFunc(func(string) (ed25519.PublicKey, error) { return []byte{1, 2, 3}, nil }), ErrOwnerRecord},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &Validator{Owners: tc.owners, Fetch: b.fetch}
			if err := v.Validate(b.key(), nil, p); !errors.Is(err, tc.want) {
				t.Fatalf("Validate: %v, want %v", err, tc.want)
			}
			if _, err := v.ValidateRecord(b.imm[h[0]]); !errors.Is(err, tc.want) {
				t.Fatalf("ValidateRecord: %v, want %v", err, tc.want)
			}
		})
	}
}

type ownersFunc func(domain string) (ed25519.PublicKey, error)

func (f ownersFunc) PublicKey(domain string) (ed25519.PublicKey, error) { return f(domain) }

func TestValidateKeyMismatch(t *testing.T) {
	b := newBuilder(t)
	h := b.chain("1.0.0", "1.1.0")
	p0, p1 := b.pointer("1.0.0", h[0]), b.pointer("1.1.0", h[1])
	otherPkg := PointerKey(b.domain, "other")
	otherDomain := PointerKey("evil.org", b.pkg)
	storedForOther := func() []byte {
		o := *b
		o.pkg = "other"
		oh := o.chain("1.0.0")
		return o.pointer("1.0.0", oh[0])
	}()

	cases := []struct {
		name              string
		key               string
		current, incoming []byte
	}{
		{"stored under another package's key", otherPkg, nil, p0},
		{"stored under another domain's key", otherDomain, nil, p0},
		{"stored under the hash of its own bytes", Hash(p0), nil, p0},
		{"stored under the key of its version record", h[0], nil, p0},
		{"stored under an empty key", "", nil, p0},
		{"update under another package's key", otherPkg, p0, p1},
		{"current value belongs to another package", b.key(), storedForOther, p1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.validator(0).Validate(tc.key, tc.current, tc.incoming); !errors.Is(err, ErrKeyMismatch) {
				t.Fatalf("err = %v, want ErrKeyMismatch", err)
			}
		})
	}
}

func TestValidateRecordMustMatchPointer(t *testing.T) {
	b := newBuilder(t)
	h := b.chain("1.0.0")
	other := *b
	other.pkg = "other"
	foreign := other.record("1.0.0", ZeroHash)

	cases := []struct {
		name     string
		incoming []byte
		want     error
	}{
		{"pointer claims another version than its record", b.pointer("9.9.9", h[0]), ErrInconsistent},
		{"pointer leads to a record of another package", b.pointer("1.0.0", foreign), ErrInconsistent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.validator(0).Validate(b.key(), nil, tc.incoming); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestValidateRecordInOtherDomainInChain(t *testing.T) {
	b := newBuilder(t)
	other := *b
	other.domain = "other.org"
	foreignRoot := other.record("1.0.0", ZeroHash)
	head := b.record("1.1.0", foreignRoot)
	err := b.validator(0).Validate(b.key(), nil, b.pointer("1.1.0", head))
	if !errors.Is(err, ErrInconsistent) {
		t.Fatalf("err = %v, want ErrInconsistent", err)
	}
}

func TestValidateMissingRecords(t *testing.T) {
	b := newBuilder(t)
	h := b.chain("1.0.0", "1.1.0", "1.2.0", "1.3.0")
	fresh := b.pointer("1.3.0", h[3])
	behind := b.pointer("1.0.0", h[0])

	missing := func(skip string) *Validator {
		return &Validator{Owners: b.owners(), Fetch: func(hash string) ([]byte, error) {
			if hash == skip {
				return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
			}
			return b.fetch(hash)
		}}
	}
	cases := []struct {
		name      string
		v         *Validator
		current   []byte
		incoming  []byte
		wantError error
	}{
		{"the head record is missing", missing(h[3]), behind, fresh, ErrMissingRecord},
		{"a record in the middle is missing while catching up", missing(h[1]), behind, fresh, ErrMissingRecord},
		{"a record is missing while verifying from scratch", missing(h[0]), nil, fresh, ErrMissingRecord},
		{"no fetch function at all", &Validator{Owners: b.owners()}, behind, fresh, ErrMissingRecord},
		{"the fetch returns data that does not match the hash", &Validator{Owners: b.owners(), Fetch: func(hash string) ([]byte, error) { return b.imm[h[0]], nil }}, behind, fresh, ErrMissingRecord},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.v.Validate(b.key(), tc.current, tc.incoming); !errors.Is(err, tc.wantError) {
				t.Fatalf("err = %v, want %v", err, tc.wantError)
			}
		})
	}

	t.Run("a missing record that is not needed does not matter", func(t *testing.T) {
		if err := missing(h[0]).Validate(b.key(), b.pointer("1.1.0", h[1]), b.pointer("1.3.0", h[3])); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the stored record itself is never fetched", func(t *testing.T) {
		if err := missing(h[1]).Validate(b.key(), b.pointer("1.1.0", h[1]), b.pointer("1.2.0", h[2])); err != nil {
			t.Fatal(err)
		}
	})
}

func TestValidateChainLength(t *testing.T) {
	b := newBuilder(t)
	versions := make([]string, 8)
	for i := range versions {
		versions[i] = fmt.Sprintf("1.%d.0", i)
	}
	h := b.chain(versions...)
	cases := []struct {
		name    string
		max     int
		current []byte
		head    int
		wantErr error
	}{
		{"from scratch, exactly at the limit", 5, nil, 4, nil},
		{"from scratch, one over the limit", 5, nil, 5, ErrChainTooLong},
		{"from scratch, far over the limit", 2, nil, 7, ErrChainTooLong},
		{"limit of one accepts a single record", 1, nil, 0, nil},
		{"limit of one rejects two records", 1, nil, 1, ErrChainTooLong},
		{"catching up, exactly at the limit", 3, b.pointer("1.1.0", h[1]), 4, nil},
		{"catching up, one over the limit", 3, b.pointer("1.1.0", h[1]), 5, ErrChainTooLong},
		{"catching up one version needs one record", 1, b.pointer("1.6.0", h[6]), 7, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := b.validator(tc.max).Validate(b.key(), tc.current, b.pointer(versions[tc.head], h[tc.head]))
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDefaultChainLimit(t *testing.T) {
	b := newBuilder(t)
	versions := make([]string, DefaultMaxChain+2)
	for i := range versions {
		versions[i] = fmt.Sprintf("1.%d.0", i)
	}
	h := b.chain(versions...)
	v := b.validator(0)
	if err := v.Validate(b.key(), nil, b.pointer(versions[DefaultMaxChain-1], h[DefaultMaxChain-1])); err != nil {
		t.Fatalf("a chain of exactly DefaultMaxChain records: %v", err)
	}
	if err := v.Validate(b.key(), nil, b.pointer(versions[DefaultMaxChain], h[DefaultMaxChain])); !errors.Is(err, ErrChainTooLong) {
		t.Fatalf("a chain one longer than DefaultMaxChain: %v", err)
	}
	if err := v.Validate(b.key(), b.pointer(versions[0], h[0]), b.pointer(versions[DefaultMaxChain], h[DefaultMaxChain])); err != nil {
		t.Fatalf("catching up over exactly DefaultMaxChain records: %v", err)
	}
	if err := v.Validate(b.key(), b.pointer(versions[0], h[0]), b.pointer(versions[DefaultMaxChain+1], h[DefaultMaxChain+1])); !errors.Is(err, ErrChainTooLong) {
		t.Fatalf("catching up over more than DefaultMaxChain records: %v", err)
	}
}

func TestValidateMalformedInput(t *testing.T) {
	b := newBuilder(t)
	h := b.chain("1.0.0", "1.1.0")
	good := b.pointer("1.1.0", h[1])
	older := b.pointer("1.0.0", h[0])
	cases := []struct {
		name              string
		current, incoming []byte
	}{
		{"nil incoming", nil, nil},
		{"garbage", nil, []byte("not a pointer")},
		{"a version record instead of a pointer", nil, b.imm[h[0]]},
		{"a package blob", nil, content("1.0.0")},
		{"pointer with trailing data", nil, append(append([]byte{}, good...), '\n')},
		{"pointer with an unknown member", nil, []byte(strings.Replace(string(good), `"sig"`, `"extra":1,"sig"`, 1))},
		{"oversize value", nil, []byte(`{"tag":"` + strings.Repeat("x", MaxRecordSize) + `"}`)},
		{"garbage stored value", []byte("garbage"), good},
		{"stored version record instead of a pointer", b.imm[h[0]], good},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.validator(0).Validate(b.key(), tc.current, tc.incoming); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
	if err := b.validator(0).Validate(b.key(), good, older); !errors.Is(err, ErrRollback) {
		t.Fatalf("sanity check: %v", err)
	}
}

func TestValidateRecord(t *testing.T) {
	b := newBuilder(t)
	_, attacker := mustKey(t)
	h := b.chain("1.0.0")
	forged, _ := b.recordSignedBy(attacker, "1.1.0", h[0])
	v := b.validator(0)

	rec, err := v.ValidateRecord(b.imm[h[0]])
	if err != nil {
		t.Fatal(err)
	}
	if rec.Version != (Version{1, 0, 0}) || rec.Domain != b.domain || rec.Package != b.pkg || rec.Previous != ZeroHash {
		t.Fatalf("unexpected record %+v", rec)
	}
	if _, err := v.ValidateRecord(forged); !errors.Is(err, ErrNotOwner) || !errors.Is(err, ErrBadSignature) {
		t.Errorf("record signed by a non-owner: %v", err)
	}
	if _, err := v.ValidateRecord([]byte("junk")); !errors.Is(err, ErrMalformed) {
		t.Errorf("junk: %v", err)
	}
	tampered := strings.Replace(string(b.imm[h[0]]), Hash(content("1.0.0")), Hash(content("evil")), 1)
	if _, err := v.ValidateRecord([]byte(tampered)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered record: %v", err)
	}
}

func TestValidatorConcurrentUse(t *testing.T) {
	b := newBuilder(t)
	versions := []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0", "1.4.0"}
	h := b.chain(versions...)
	ptrs := make([][]byte, len(versions))
	for i := range versions {
		ptrs[i] = b.pointer(versions[i], h[i])
	}
	v := b.validator(0)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				from, to := (g+i)%3, 3+(g+i)%2
				if err := v.Validate(b.key(), ptrs[from], ptrs[to]); err != nil {
					t.Error(err)
					return
				}
				if err := v.Validate(b.key(), ptrs[to], ptrs[from]); !errors.Is(err, ErrRollback) {
					t.Errorf("err = %v, want ErrRollback", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
