package dht

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"d7024e/experiment"
	"d7024e/kademlia"
	"d7024e/registry"
)

func flipHex(s string) string {
	if s[0] == '0' {
		return "1" + s[1:]
	}
	return "0" + s[1:]
}

func chainVersions(t *testing.T, c *registry.Client, domain, pkg string) []string {
	t.Helper()
	chain, err := c.Chain(domain, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range chain {
		out = append(out, e.Version.String())
	}
	return out
}

func TestPublishInstallAndChain(t *testing.T) {
	r := newRegNet(t, 30, 1)
	first := r.publish(0, "1.0.0")
	r.publish(7, "1.1.0")
	r.publish(21, "2.0.0")

	if got := pointerVersion(t, r.legitPointer()); got != "2.0.0" {
		t.Fatalf("the node closest to the pointer holds %s, want 2.0.0", got)
	}
	for _, reader := range []int{3, 12, 29} {
		c := r.client(reader)
		latest, err := c.Install(testDomain, testPkg, registry.Latest)
		if err != nil {
			t.Fatalf("node %d: install latest: %v", reader, err)
		}
		if latest.Version.String() != "2.0.0" || !bytes.Equal(latest.Data, content("2.0.0")) {
			t.Fatalf("node %d: latest = %s %q", reader, latest.Version, latest.Data)
		}
		old, err := c.Install(testDomain, testPkg, "1.0.0")
		if err != nil {
			t.Fatalf("node %d: install 1.0.0: %v", reader, err)
		}
		if !bytes.Equal(old.Data, content("1.0.0")) || old.RecordHash != first.RecordHash || old.BlobHash != first.BlobHash {
			t.Fatalf("node %d: installed %s with record %s", reader, old.Version, old.RecordHash)
		}
		if got := chainVersions(t, c, testDomain, testPkg); !slices.Equal(got, []string{"2.0.0", "1.1.0", "1.0.0"}) {
			t.Fatalf("node %d: chain = %v", reader, got)
		}
		if _, err := c.Install(testDomain, testPkg, "1.0.5"); !errors.Is(err, registry.ErrUnknownVersion) {
			t.Fatalf("node %d: install of a version never published = %v", reader, err)
		}
	}
	if _, err := r.client(4).Install(testDomain, "absent", registry.Latest); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("install of a package never published = %v", err)
	}
}

func TestPublishingIsPerDomain(t *testing.T) {
	r := newRegNet(t, 20, 4)
	r.publish(0, "1.0.0")
	good := r.legitPointer()

	opts := registry.PublishOptions{Domain: otherHost, Package: testPkg, Version: mustVersion(t, "0.1.0"), Data: []byte("other")}
	if _, err := r.client(9).Publish(opts, r.otherPriv); err != nil {
		t.Fatal(err)
	}
	got, err := r.client(11).Install(otherHost, testPkg, registry.Latest)
	if err != nil || string(got.Data) != "other" {
		t.Fatalf("install other.org: %v, %v", got, err)
	}
	r.assertOnly(good, storedValue(r.net.Nodes[r.closestIndex()], registry.PointerKey(otherHost, testPkg)))
	if v := pointerVersion(t, r.legitPointer()); v != "1.0.0" {
		t.Fatalf("example.com pointer moved to %s", v)
	}

	opts.Domain = "unknown.net"
	if _, err := r.client(9).Publish(opts, r.otherPriv); !errors.Is(err, registry.ErrNoOwner) {
		t.Fatalf("publish for a domain nobody owns = %v", err)
	}
}

func TestWrongOwnerIsRejected(t *testing.T) {
	r := newRegNet(t, 30, 2)
	r.publish(0, "1.0.0")
	good := r.legitPointer()

	opts := r.opts("1.1.0")
	evil := r.client(5)
	if _, err := evil.Publish(opts, r.otherPriv); !errors.Is(err, registry.ErrNotOwner) {
		t.Fatalf("publish by a non-owner without --force = %v", err)
	}
	if n := r.holders(registry.Hash(opts.Data)); n != 0 {
		t.Fatalf("a refused publish uploaded the package to %d nodes", n)
	}

	opts.Force = true
	_, err := evil.Publish(opts, r.otherPriv)
	if !errors.Is(err, registry.ErrNotOwner) || !strings.Contains(err.Error(), "not signed by the owner of example.com") {
		t.Fatalf("forced publish by a non-owner = %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("error is not one line: %q", err)
	}
	r.assertOnly(good)
	if got := chainVersions(t, r.client(8), testDomain, testPkg); !slices.Equal(got, []string{"1.0.0"}) {
		t.Fatalf("chain after the attack = %v", got)
	}
}

func TestTamperedPointerIsRejected(t *testing.T) {
	r := newRegNet(t, 30, 5)
	r.publish(0, "1.0.0")
	r.publish(1, "1.1.0")
	good := r.legitPointer()
	p, err := registry.DecodeLatestPointer(good)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(p registry.LatestPointer) []byte {
		raw, err := p.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	newer := *p
	newer.Version = mustVersion(t, "9.9.9")
	badSig := *p
	badSig.Sig = flipHex(p.Sig)
	elsewhere, err := registry.NewLatestPointer(r.priv, testDomain, "other-package", p.Version, p.VersionRecordHash)
	if err != nil {
		t.Fatal(err)
	}
	noRecord, err := registry.NewLatestPointer(r.priv, testDomain, testPkg, mustVersion(t, "3.0.0"), registry.Hash([]byte("no such record")))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		value []byte
		want  error
	}{
		{"version changed after signing", encode(newer), registry.ErrNotOwner},
		{"signature altered", encode(badSig), registry.ErrNotOwner},
		{"pointer of another package", encode(*elsewhere), registry.ErrKeyMismatch},
		{"signed pointer to a record nobody has", encode(*noRecord), registry.ErrMissingRecord},
		{"not a pointer", []byte("not a pointer"), registry.ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := r.net.Nodes[3].StoreAt(r.keyID(), tc.value)
			if !errors.Is(err, tc.want) {
				t.Fatalf("StoreAt = %v, want %v", err, tc.want)
			}
		})
	}
	r.assertOnly(good, storedValue(r.net.Nodes[0], r.key()))
}

func TestRollbackAndForkAreRefusedByReplicas(t *testing.T) {
	cases := []struct {
		name    string
		version string
		prev    string
		data    string
		want    error
	}{
		{"older than the latest", "1.0.1", "", "", registry.ErrRollback},
		{"back to the first version", "1.0.0", "1.1.0", "", registry.ErrRollback},
		{"latest version again with other content", "1.1.0", "", "different", registry.ErrFork},
		{"branch from an older record", "1.2.0", "1.0.0", "", registry.ErrFork},
	}
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			r := newRegNet(t, 30, 6)
			r.publish(0, "1.0.0")
			first := r.legitPointer()
			r.publish(7, "1.1.0")
			second := r.legitPointer()
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					opts := r.opts(tc.version)
					if tc.data != "" {
						opts.Data = []byte(tc.data)
					}
					if tc.prev != "" {
						v := mustVersion(t, tc.prev)
						opts.Prev = &v
					}
					opts.Force = force
					blobHolders := r.holders(registry.Hash(opts.Data))
					_, err := r.client(12).Publish(opts, r.priv)
					if !errors.Is(err, tc.want) {
						t.Fatalf("publish = %v, want %v", err, tc.want)
					}
					if strings.Contains(err.Error(), "\n") {
						t.Fatalf("error is not one line: %q", err)
					}
					if n := r.holders(registry.Hash(opts.Data)); !force && n != blobHolders {
						t.Fatalf("publish without --force uploaded the package to %d more nodes before refusing", n-blobHolders)
					}
					if force && !strings.Contains(err.Error(), "updating the latest pointer") {
						t.Fatalf("a forced publish should fail at the replicas: %v", err)
					}
					r.assertOnly(first, second)
					if got := pointerVersion(t, r.legitPointer()); got != "1.1.0" {
						t.Fatalf("latest moved to %s", got)
					}
				})
			}
			r.publish(3, "1.2.0")
			if got := chainVersions(t, r.client(20), testDomain, testPkg); !slices.Equal(got, []string{"1.2.0", "1.1.0", "1.0.0"}) {
				t.Fatalf("chain after the attempts = %v", got)
			}
		})
	}
}

func TestMissingHistoryCatchUp(t *testing.T) {
	r := newRegNet(t, 8, 3)
	r.publish(0, "1.0.0")
	behind := r.net.Nodes[5]
	v1 := storedValue(behind, r.key())
	if v1 == nil {
		t.Fatal("with k larger than the network, every node should hold the first pointer")
	}
	if !r.net.Sim.Remove(r.net.Contacts[5].Address) {
		t.Fatal("node 5 was not on the network")
	}

	second := r.publish(1, "2.0.0")
	third := r.publish(1, "3.0.0")
	v3 := storedValue(r.net.Nodes[1], r.key())
	if pointerVersion(t, v3) != "3.0.0" {
		t.Fatal("the publisher does not hold the new pointer")
	}
	if got := pointerVersion(t, storedValue(behind, r.key())); got != "1.0.0" {
		t.Fatalf("the removed node holds %s", got)
	}
	for _, hash := range []string{second.RecordHash, third.RecordHash} {
		if experiment.Holds(behind, hash) {
			t.Fatal("the removed node already holds a record it could not have received")
		}
	}

	// LookupData answers from the local store, so the node still reports the
	// version it has.
	stale, err := r.client(5).Install(testDomain, testPkg, registry.Latest)
	if err != nil || stale.Version.String() != "1.0.0" {
		t.Fatalf("install latest on the stale node = %v, %v", stale, err)
	}

	// This is what the node's STORE handler does when the new pointer arrives.
	if err := behind.StoreValue(r.keyID(), v3); err != nil {
		t.Fatalf("catching up: %v", err)
	}
	if got := storedValue(behind, r.key()); !bytes.Equal(got, v3) {
		t.Fatalf("the node holds %s after catching up", pointerVersion(t, got))
	}
	for _, hash := range []string{second.RecordHash, third.RecordHash} {
		if !experiment.Holds(behind, hash) {
			t.Fatalf("catching up did not fetch record %s", registry.ShortHash(hash))
		}
	}
	caught, err := r.client(5).Install(testDomain, testPkg, registry.Latest)
	if err != nil || caught.Version.String() != "3.0.0" || !bytes.Equal(caught.Data, content("3.0.0")) {
		t.Fatalf("install latest after catching up = %v, %v", caught, err)
	}
	if got := chainVersions(t, r.client(5), testDomain, testPkg); !slices.Equal(got, []string{"3.0.0", "2.0.0", "1.0.0"}) {
		t.Fatalf("chain on the caught-up node = %v", got)
	}
}

func TestBehindNodeThatCannotFetchHistoryKeepsItsPointer(t *testing.T) {
	r := newRegNet(t, 8, 3)
	r.publish(0, "1.0.0")
	behind := r.net.Nodes[5]
	v1 := storedValue(behind, r.key())
	r.net.Sim.Remove(r.net.Contacts[5].Address)
	r.publish(1, "2.0.0")
	r.publish(1, "3.0.0")
	v3 := storedValue(r.net.Nodes[1], r.key())
	for i, c := range r.net.Contacts {
		if i != 5 {
			r.net.Sim.Remove(c.Address)
		}
	}

	err := behind.StoreValue(r.keyID(), v3)
	if !errors.Is(err, registry.ErrMissingRecord) {
		t.Fatalf("pointer whose history cannot be fetched = %v", err)
	}
	if got := storedValue(behind, r.key()); !bytes.Equal(got, v1) {
		t.Fatal("a failed catch-up changed the stored pointer")
	}
}

func TestStalePointerCannotRollBackTheNetwork(t *testing.T) {
	r := newRegNet(t, 8, 3)
	r.publish(0, "1.0.0")
	behind := r.net.Nodes[5]
	r.net.Sim.Remove(r.net.Contacts[5].Address)
	r.publish(1, "2.0.0")

	err := behind.Republish()
	if !errors.Is(err, registry.ErrRollback) {
		t.Fatalf("replicating a stale pointer = %v", err)
	}
	if got := pointerVersion(t, r.legitPointer()); got != "2.0.0" {
		t.Fatalf("the network moved to %s", got)
	}
}

func TestPublisherRefreshesItsOwnCachedPointer(t *testing.T) {
	r := newRegNet(t, 40, 8)
	const publisher = 17
	r.publish(0, "1.0.0")
	if _, err := r.client(publisher).Install(testDomain, testPkg, registry.Latest); err != nil {
		t.Fatal(err)
	}
	r.publish(publisher, "1.1.0")
	got, err := r.client(publisher).Install(testDomain, testPkg, registry.Latest)
	if err != nil || got.Version.String() != "1.1.0" {
		t.Fatalf("publisher installs %v, %v", got, err)
	}
	r.publish(publisher, "1.2.0")
}

func TestStaleLocalPointerLimitation(t *testing.T) {
	r := newRegNet(t, 40, 8)
	r.publish(0, "1.0.0")
	reader := -1
	for i := range r.net.Nodes {
		if i != 0 && storedValue(r.net.Nodes[i], r.key()) == nil {
			reader = i
			break
		}
	}
	if _, err := r.client(reader).Install(testDomain, testPkg, registry.Latest); err != nil {
		t.Fatal(err)
	}
	r.publish(0, "1.1.0")

	// The reader cached 1.0.0 and a local hit never asks the network, so it
	// neither installs the new latest version nor can it extend the history.
	got, err := r.client(reader).Install(testDomain, testPkg, registry.Latest)
	if err != nil || got.Version.String() != "1.0.0" {
		t.Fatalf("install latest on a node with a stale pointer = %v, %v", got, err)
	}
	if _, err := r.client(reader).Publish(r.opts("1.2.0"), r.priv); !errors.Is(err, registry.ErrFork) {
		t.Fatalf("publish from a node with a stale pointer = %v", err)
	}
	if v := pointerVersion(t, r.legitPointer()); v != "1.1.0" {
		t.Fatalf("network latest = %s", v)
	}
}

func TestBackendAbsentValuesAreNotFound(t *testing.T) {
	r := newRegNet(t, 12, 9)
	b := NewBackend(r.net.Nodes[2])
	if _, err := b.GetMutable(r.key()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("GetMutable of an absent pointer = %v", err)
	}
	if _, err := b.GetImmutable(registry.Hash([]byte("nothing"))); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("GetImmutable of an absent value = %v", err)
	}
	hash, err := b.PutImmutable([]byte("stored"))
	if err != nil || hash != registry.Hash([]byte("stored")) {
		t.Fatalf("PutImmutable = %q, %v", hash, err)
	}
	got, err := NewBackend(r.net.Nodes[9]).GetImmutable(hash)
	if err != nil || string(got) != "stored" {
		t.Fatalf("GetImmutable from another node = %q, %v", got, err)
	}
}

func TestBackendOtherErrorsAreNeverNotFound(t *testing.T) {
	r := newRegNet(t, 6, 10)
	lonely := NewBackend(kademlia.NewKademlia(nil, nil))
	for name, err := range map[string]error{
		"invalid hash":       func() error { _, err := NewBackend(r.net.Nodes[0]).GetImmutable("xyz"); return err }(),
		"invalid mutable":    func() error { _, err := NewBackend(r.net.Nodes[0]).GetMutable(""); return err }(),
		"no routing table":   func() error { _, err := lonely.GetImmutable(registry.Hash(nil)); return err }(),
		"no routing mutable": func() error { _, err := lonely.GetMutable(r.key()); return err }(),
	} {
		if err == nil || errors.Is(err, registry.ErrNotFound) {
			t.Errorf("%s: error = %v, must not be nil or ErrNotFound", name, err)
		}
	}

	// A pointer exists but one of its records cannot be fetched: the pointer
	// is rejected for the missing record, which wraps registry.ErrNotFound for
	// the validator, and must not look like an unpublished package.
	blob := registry.Hash(content("1.0.0"))
	rec, err := registry.NewVersionRecord(r.priv, testDomain, testPkg, mustVersion(t, "1.0.0"), blob, registry.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	recRaw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	ptr, err := registry.NewLatestPointer(r.priv, testDomain, testPkg, mustVersion(t, "1.0.0"), registry.RecordHash(recRaw))
	if err != nil {
		t.Fatal(err)
	}
	ptrRaw, err := ptr.Encode()
	if err != nil {
		t.Fatal(err)
	}
	holder := r.net.Nodes[0]
	holder.SetValueValidator(func(*kademlia.KademliaID, []byte, []byte) error { return nil })
	if err := holder.StoreValue(r.keyID(), ptrRaw); err != nil {
		t.Fatal(err)
	}
	Install(holder, r.owners)

	_, err = NewBackend(r.net.Nodes[3]).GetMutable(r.key())
	if err == nil || errors.Is(err, registry.ErrNotFound) || !errors.Is(err, registry.ErrMissingRecord) {
		t.Fatalf("GetMutable of a pointer with an unfetchable record = %v", err)
	}
	if _, err := r.client(3).Publish(r.opts("1.1.0"), r.priv); err == nil || !strings.Contains(err.Error(), "reading the latest pointer") {
		t.Fatalf("publish must not treat an unreadable pointer as a first publication: %v", err)
	}
}

func TestBackendRejectsMalformedRequests(t *testing.T) {
	r := newRegNet(t, 6, 11)
	b := NewBackend(r.net.Nodes[0])
	for _, key := range []string{"", "abc", strings.Repeat("g", 64), strings.ToUpper(r.key()), r.key() + "00"} {
		if err := b.PutMutable(key, []byte("x")); err == nil || !strings.Contains(err.Error(), "storage key") {
			t.Errorf("PutMutable(%q) = %v", key, err)
		}
	}
	hash, err := b.PutImmutable(make([]byte, kademlia.MaxValueSize+1))
	if hash != "" || !errors.Is(err, kademlia.ErrValueTooLarge) {
		t.Fatalf("PutImmutable of an oversized value = %q, %v", hash, err)
	}
}

func TestInstallEnforcesTheRegistryRules(t *testing.T) {
	r := newRegNet(t, 4, 12)
	node := r.net.Nodes[0]
	node.SetValueValidator(nil)
	rec, err := registry.NewVersionRecord(r.priv, testDomain, testPkg, mustVersion(t, "1.0.0"), registry.Hash(content("1.0.0")), registry.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	recRaw, _ := rec.Encode()
	if _, err := node.Store(recRaw); err != nil {
		t.Fatal(err)
	}
	ptr, err := registry.NewLatestPointer(r.priv, testDomain, testPkg, mustVersion(t, "1.0.0"), registry.RecordHash(recRaw))
	if err != nil {
		t.Fatal(err)
	}
	ptrRaw, _ := ptr.Encode()

	if err := node.StoreValue(r.keyID(), ptrRaw); !errors.Is(err, kademlia.ErrHashMismatch) {
		t.Fatalf("before Install: %v", err)
	}
	v := Install(node, r.owners)
	if v == nil || v.Owners == nil || v.Fetch == nil {
		t.Fatalf("Install returned %+v", v)
	}
	if err := node.StoreValue(r.keyID(), ptrRaw); err != nil {
		t.Fatalf("after Install: %v", err)
	}
	wrongKey := kademlia.NewKademliaID(registry.PointerKey(testDomain, "other"))
	if err := node.StoreValue(wrongKey, ptrRaw); !errors.Is(err, registry.ErrKeyMismatch) {
		t.Fatalf("pointer under another package's key: %v", err)
	}
	if err := ValueValidator(v)(nil, nil, ptrRaw); err == nil {
		t.Fatal("a value without a key was accepted")
	}
}

func TestSummarizeGivesOneLine(t *testing.T) {
	refused := errors.New("update would roll back")
	err := summarize(errors.Join(
		fmt.Errorf("store on 10.0.0.1:8000: %w", refused),
		fmt.Errorf("store on 10.0.0.2:8000: %w", refused),
		fmt.Errorf("store on 10.0.0.3:8000: %w", errors.New("no response\nfrom node")),
		errors.New("local\nproblem"),
	))
	want := "update would roll back (refused by 2 nodes); no response from node; local problem"
	if err.Error() != want {
		t.Fatalf("summary = %q, want %q", err, want)
	}
	if !errors.Is(err, refused) {
		t.Fatal("summarize hid the underlying error")
	}
	if single := summarize(refused); single.Error() != refused.Error() {
		t.Fatalf("single error = %q", single)
	}
}

func TestFailureHidesOnlyNotFound(t *testing.T) {
	f := &failure{err: fmt.Errorf("%w: %w", registry.ErrMissingRecord, registry.ErrNotFound)}
	if errors.Is(f, registry.ErrNotFound) {
		t.Fatal("failure reports ErrNotFound")
	}
	if !errors.Is(f, registry.ErrMissingRecord) {
		t.Fatal("failure hides the real reason")
	}
}
