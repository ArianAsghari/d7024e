package registry

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"testing"
)

func (f *fixture) recordOf(r *replica, hash string) *VersionRecord {
	f.t.Helper()
	raw, ok := r.imm[hash]
	if !ok {
		f.t.Fatalf("record %s is not stored", ShortHash(hash))
	}
	rec, err := DecodeVersionRecord(raw)
	if err != nil {
		f.t.Fatal(err)
	}
	return rec
}

func (f *fixture) snapshot() map[string][]byte {
	out := map[string][]byte{}
	for i, r := range f.net.replicas {
		out[fmt.Sprint(i)] = bytes.Clone(r.mut[PointerKey(f.domain, f.pkg)])
	}
	return out
}

func (f *fixture) assertUnchanged(before map[string][]byte) {
	f.t.Helper()
	for k, v := range f.snapshot() {
		if !bytes.Equal(v, before[k]) {
			f.t.Errorf("replica %s: the stored pointer changed", k)
		}
	}
}

func TestPublishFirstVersion(t *testing.T) {
	f := newFixture(t, 3, 0)
	res := f.publish("1.0.0")

	data := content("1.0.0")
	if res.BlobHash != Hash(data) {
		t.Errorf("BlobHash = %s", res.BlobHash)
	}
	if res.PointerKey != PointerKey(f.domain, f.pkg) {
		t.Errorf("PointerKey = %s", res.PointerKey)
	}
	for i, r := range f.net.replicas {
		if !bytes.Equal(r.imm[res.BlobHash], data) {
			t.Errorf("replica %d: the blob is not stored under its hash", i)
		}
		rec := f.recordOf(r, res.RecordHash)
		if RecordHash(r.imm[res.RecordHash]) != res.RecordHash {
			t.Errorf("replica %d: the record is not stored under its own hash", i)
		}
		if rec.Previous != ZeroHash || rec.BlobHash != res.BlobHash || rec.Version != (Version{1, 0, 0}) {
			t.Errorf("replica %d: unexpected record %+v", i, rec)
		}
		if err := rec.Verify(f.pub); err != nil {
			t.Errorf("replica %d: %v", i, err)
		}
		ptr := r.pointer(t, f.domain, f.pkg)
		if ptr == nil || ptr.VersionRecordHash != res.RecordHash || ptr.Version != (Version{1, 0, 0}) {
			t.Errorf("replica %d: unexpected pointer %+v", i, ptr)
		}
		if err := ptr.Verify(f.pub); err != nil {
			t.Errorf("replica %d: %v", i, err)
		}
	}
	if got := f.versions(); !equalStrings(got, []string{"1.0.0"}) {
		t.Errorf("chain = %v", got)
	}
}

func TestPublishUpdateChain(t *testing.T) {
	f := newFixture(t, 3, 0)
	versions := []string{"1.0.0", "1.0.1", "1.2.9", "1.2.10", "1.10.0", "2.0.0"}
	var results []PublishResult
	for _, v := range versions {
		results = append(results, f.publish(v))
	}
	want := []string{"2.0.0", "1.10.0", "1.2.10", "1.2.9", "1.0.1", "1.0.0"}
	if got := f.versions(); !equalStrings(got, want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}
	for i, res := range results {
		rec := f.recordOf(f.net.replicas[0], res.RecordHash)
		wantPrev := ZeroHash
		if i > 0 {
			wantPrev = results[i-1].RecordHash
		}
		if rec.Previous != wantPrev {
			t.Errorf("%s: previous = %s, want %s", versions[i], ShortHash(rec.Previous), ShortHash(wantPrev))
		}
	}
	for i, got := range f.replicaVersions() {
		if got != "2.0.0" {
			t.Errorf("replica %d is at %s", i, got)
		}
	}
	chain, err := f.client.Chain(f.domain, f.pkg)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range chain {
		res := results[len(results)-1-i]
		if e.RecordHash != res.RecordHash || e.BlobHash != res.BlobHash {
			t.Errorf("chain entry %d does not match the publish result", i)
		}
	}
}

func TestPublishWithExplicitPrevEqualToLatest(t *testing.T) {
	f := newFixture(t, 3, 0)
	f.publishAll("1.0.0", "1.1.0")
	o := f.opts("1.2.0")
	prev := mustVersion(t, "1.1.0")
	o.Prev = &prev
	if _, err := f.client.Publish(o, f.priv); err != nil {
		t.Fatal(err)
	}
	if got := f.versions(); !equalStrings(got, []string{"1.2.0", "1.1.0", "1.0.0"}) {
		t.Fatalf("chain = %v", got)
	}
}

func TestPublishRepublishingTheSameRecordIsIdempotent(t *testing.T) {
	f := newFixture(t, 3, 0)
	f.publishAll("1.0.0", "1.1.0")
	o := f.opts("1.1.0")
	prev := mustVersion(t, "1.0.0")
	o.Prev = &prev
	for _, force := range []bool{false, true} {
		o.Force = force
		if _, err := f.client.Publish(o, f.priv); err != nil {
			t.Fatalf("force=%v: %v", force, err)
		}
	}
	if got := f.versions(); !equalStrings(got, []string{"1.1.0", "1.0.0"}) {
		t.Fatalf("chain = %v", got)
	}
}

func TestPublishRefusedBeforeUploading(t *testing.T) {
	f := newFixture(t, 3, 0)
	f.publishAll("1.0.0", "1.1.0", "2.0.0")
	_, attacker := mustKey(t)

	ver := func(s string) *Version { v := mustVersion(t, s); return &v }
	cases := []struct {
		name    string
		version string
		prev    *Version
		priv    ed25519.PrivateKey
		want    error
	}{
		{"roll back", "1.5.0", nil, nil, ErrRollback},
		{"roll back to the first version", "1.0.0", nil, nil, ErrRollback},
		{"same version again", "2.0.0", nil, nil, ErrFork},
		{"fork from an older version", "3.0.0", ver("1.0.0"), nil, ErrFork},
		{"fork from the previous-but-one version", "3.0.0", ver("1.1.0"), nil, ErrFork},
		{"previous version does not exist", "3.0.0", ver("1.5.0"), nil, ErrUnknownVersion},
		{"previous version is newer than the new one", "1.0.5", ver("2.0.0"), nil, ErrRollback},
		{"previous is the new version itself", "2.0.0", ver("2.0.0"), nil, ErrFork},
		{"older version naming itself as previous", "1.1.0", ver("1.1.0"), nil, ErrRollback},
		{"not signed by the domain owner", "3.0.0", nil, attacker, ErrNotOwner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := f.snapshot()
			imm, mut := f.net.puts()
			o := f.opts(tc.version)
			o.Prev = tc.prev
			priv := f.priv
			if tc.priv != nil {
				priv = tc.priv
			}
			res, err := f.client.Publish(o, priv)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if res != (PublishResult{}) {
				t.Errorf("a refused publish returned %+v", res)
			}
			if i, m := f.net.puts(); i != imm || m != mut {
				t.Errorf("uploaded something: %d immutable and %d mutable puts", i-imm, m-mut)
			}
			f.assertUnchanged(before)
		})
	}
}

func TestPublishRefusedForOtherReasons(t *testing.T) {
	f := newFixture(t, 3, 0)
	prev := mustVersion(t, "1.0.0")

	t.Run("previous version named but nothing published yet", func(t *testing.T) {
		o := f.opts("1.0.0")
		o.Prev = &prev
		if _, err := f.client.Publish(o, f.priv); !errors.Is(err, ErrUnknownVersion) {
			t.Fatalf("err = %v", err)
		}
		o.Force = true
		if _, err := f.client.Publish(o, f.priv); !errors.Is(err, ErrUnknownVersion) {
			t.Fatalf("forced: err = %v", err)
		}
	})
	t.Run("domain without an owner", func(t *testing.T) {
		o := f.opts("1.0.0")
		o.Domain = "unowned.org"
		if _, err := f.client.Publish(o, f.priv); !errors.Is(err, ErrNoOwner) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("invalid names", func(t *testing.T) {
		for _, o := range []PublishOptions{
			{Domain: "Example.com", Package: "tool", Version: prev},
			{Domain: "example.com", Package: "Tool", Version: prev},
			{Domain: "", Package: "tool", Version: prev},
		} {
			if _, err := f.client.Publish(o, f.priv); !errors.Is(err, ErrInvalidName) {
				t.Errorf("%+v: err = %v", o, err)
			}
		}
	})
	t.Run("bad private key", func(t *testing.T) {
		if _, err := f.client.Publish(f.opts("1.0.0"), f.priv[:5]); !errors.Is(err, ErrBadKey) {
			t.Fatalf("err = %v", err)
		}
	})
	if imm, mut := f.net.puts(); imm != 0 || mut != 0 {
		t.Fatalf("uploaded %d immutable and %d mutable values", imm, mut)
	}
}

func TestPublishNonOwnerFirstVersion(t *testing.T) {
	f := newFixture(t, 3, 0)
	_, attacker := mustKey(t)
	res, err := f.client.Publish(f.opts("1.0.0"), attacker)
	if !errors.Is(err, ErrNotOwner) || res != (PublishResult{}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if imm, mut := f.net.puts(); imm != 0 || mut != 0 {
		t.Fatal("uploaded something")
	}
}

func TestForcePublishUploadsAndNodesReject(t *testing.T) {
	ver := func(s string) *Version { v := mustVersion(t, s); return &v }
	cases := []struct {
		name    string
		version string
		prev    *Version
		data    []byte
		want    error
	}{
		{"roll back", "1.5.0", nil, nil, ErrRollback},
		{"roll back naming a later previous version", "1.0.5", ver("2.0.0"), nil, ErrRollback},
		{"fork from an older version", "3.0.0", ver("1.0.0"), nil, ErrFork},
		{"fork: same version, other contents", "2.0.0", ver("1.1.0"), []byte("something else"), ErrFork},
		{"cycle: previous is the version itself", "2.0.0", ver("2.0.0"), nil, ErrFork},
		{"roll back: first version again, naming itself as previous", "1.0.0", ver("1.0.0"), nil, ErrRollback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, 3, 0)
			f.publishAll("1.0.0", "1.1.0", "2.0.0")
			before := f.snapshot()
			imm, mut := f.net.puts()

			o := f.opts(tc.version)
			o.Force, o.Prev = true, tc.prev
			if tc.data != nil {
				o.Data = tc.data
			}
			res, err := f.client.Publish(o, f.priv)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if res.BlobHash != Hash(o.Data) || res.RecordHash == "" || res.PointerKey != PointerKey(f.domain, f.pkg) {
				t.Errorf("the result of a forced publish should describe what was uploaded: %+v", res)
			}
			if i, m := f.net.puts(); i != imm+2 || m != mut+1 {
				t.Errorf("forced publish did %d immutable and %d mutable puts, want 2 and 1", i-imm, m-mut)
			}
			for i, r := range f.net.replicas {
				if !errors.Is(r.lastErr, tc.want) {
					t.Errorf("replica %d: err = %v, want %v", i, r.lastErr, tc.want)
				}
			}
			f.assertUnchanged(before)
			if got := f.versions(); !equalStrings(got, []string{"2.0.0", "1.1.0", "1.0.0"}) {
				t.Errorf("the chain changed: %v", got)
			}
		})
	}
}

func TestForcePublishByNonOwnerIsRejected(t *testing.T) {
	f := newFixture(t, 3, 0)
	f.publishAll("1.0.0")
	_, attacker := mustKey(t)
	before := f.snapshot()
	o := f.opts("1.1.0")
	o.Force = true
	res, err := f.client.Publish(o, attacker)
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("err = %v", err)
	}
	if res.RecordHash == "" {
		t.Error("a forced publish should still upload")
	}
	f.assertUnchanged(before)
}

func TestForcePublishWithoutAnyProblemStillWorks(t *testing.T) {
	f := newFixture(t, 3, 0)
	o := f.opts("1.0.0")
	o.Force = true
	if _, err := f.client.Publish(o, f.priv); err != nil {
		t.Fatal(err)
	}
	o = f.opts("1.1.0")
	o.Force = true
	if _, err := f.client.Publish(o, f.priv); err != nil {
		t.Fatal(err)
	}
	o = f.opts("1.2.0")
	o.Force = true
	prev := mustVersion(t, "1.1.0")
	o.Prev = &prev
	if _, err := f.client.Publish(o, f.priv); err != nil {
		t.Fatal(err)
	}
	if got := f.versions(); !equalStrings(got, []string{"1.2.0", "1.1.0", "1.0.0"}) {
		t.Fatalf("chain = %v", got)
	}
}

func TestForceFailsOnlyWhenPreviousVersionDoesNotExist(t *testing.T) {
	f := newFixture(t, 3, 0)
	f.publishAll("1.0.0", "1.1.0")
	imm, mut := f.net.puts()
	o := f.opts("2.0.0")
	o.Force = true
	missing := mustVersion(t, "1.0.5")
	o.Prev = &missing
	if _, err := f.client.Publish(o, f.priv); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v", err)
	}
	if i, m := f.net.puts(); i != imm || m != mut {
		t.Fatal("uploaded although the previous version does not exist")
	}
}

func TestForceCycleOnANodeThatSawNothing(t *testing.T) {
	f := newFixture(t, 2, 0)
	f.publishAll("1.0.0", "2.0.0")
	fresh := f.net.addReplica()

	cases := []struct {
		name    string
		version string
		prev    string
	}{
		{"previous is a later version", "1.5.0", "2.0.0"},
		{"previous is the version itself", "2.0.0", "2.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := f.opts(tc.version)
			o.Force = true
			prev := mustVersion(t, tc.prev)
			o.Prev = &prev
			_, err := f.client.Publish(o, f.priv)
			if err == nil {
				t.Fatal("the update was accepted")
			}
			if !errors.Is(fresh.lastErr, ErrVersionOrder) {
				t.Errorf("fresh replica: err = %v, want ErrVersionOrder", fresh.lastErr)
			}
			if fresh.pointer(t, f.domain, f.pkg) != nil {
				t.Error("the fresh replica stored a pointer")
			}
		})
	}
}

func TestCatchUpAfterMissingSeveralVersions(t *testing.T) {
	f := newFixture(t, 3, 0)
	f.publish("1.0.0")
	lagging := f.net.replicas[2]
	lagging.down = true
	f.publishAll("1.1.0", "1.2.0", "1.3.0")
	if got := f.replicaVersions(); !equalStrings(got, []string{"1.3.0", "1.3.0", "1.0.0"}) {
		t.Fatalf("replica versions = %v", got)
	}
	lagging.down = false
	f.publish("1.4.0")
	if got := f.replicaVersions(); !equalStrings(got, []string{"1.4.0", "1.4.0", "1.4.0"}) {
		t.Fatalf("replica versions after catching up = %v", got)
	}
	if lagging.lastErr != nil {
		t.Fatal(lagging.lastErr)
	}
}

func TestCatchUpFromScratchOnANewNode(t *testing.T) {
	f := newFixture(t, 2, 0)
	f.publishAll("1.0.0", "1.1.0", "1.2.0", "1.3.0")
	fresh := f.net.addReplica()
	f.publish("1.4.0")
	if got := fresh.version(t, f.domain, f.pkg); got != "1.4.0" {
		t.Fatalf("fresh replica is at %s (err %v)", got, fresh.lastErr)
	}
}

func TestCatchUpImpossibleWhileARecordIsMissing(t *testing.T) {
	f := newFixture(t, 3, 0)
	f.publish("1.0.0")
	lagging := f.net.replicas[2]
	lagging.down = true
	lost := f.publish("1.1.0")
	f.publish("1.2.0")
	raw, err := f.net.GetImmutable(lost.RecordHash)
	if err != nil {
		t.Fatal(err)
	}
	f.net.drop(lost.RecordHash)
	lagging.down = false

	f.publish("1.3.0")
	if got := f.replicaVersions(); !equalStrings(got, []string{"1.3.0", "1.3.0", "1.0.0"}) {
		t.Fatalf("replica versions = %v", got)
	}
	if !errors.Is(lagging.lastErr, ErrMissingRecord) {
		t.Fatalf("lagging replica: err = %v, want ErrMissingRecord", lagging.lastErr)
	}

	if _, err := f.net.PutImmutable(raw); err != nil {
		t.Fatal(err)
	}
	f.publish("1.4.0")
	if got := f.replicaVersions(); !equalStrings(got, []string{"1.4.0", "1.4.0", "1.4.0"}) {
		t.Fatalf("replica versions after the record came back = %v", got)
	}
}

func TestChainLengthLimitOnANewNode(t *testing.T) {
	f := newFixture(t, 2, 4)
	f.publishAll("1.0.0", "1.1.0", "1.2.0", "1.3.0")
	fresh := f.net.addReplica()
	f.publish("1.4.0")
	if !errors.Is(fresh.lastErr, ErrChainTooLong) {
		t.Fatalf("fresh replica: err = %v, want ErrChainTooLong", fresh.lastErr)
	}
	if fresh.pointer(t, f.domain, f.pkg) != nil {
		t.Fatal("the fresh replica stored a pointer it could not verify")
	}
	chain, err := f.client.Chain(f.domain, f.pkg)
	if !errors.Is(err, ErrChainTooLong) || len(chain) != 4 {
		t.Fatalf("Chain: %d entries, err %v; want 4 entries and ErrChainTooLong", len(chain), err)
	}
	if _, err := f.client.Install(f.domain, f.pkg, "1.0.0"); !errors.Is(err, ErrChainTooLong) {
		t.Fatalf("Install: %v", err)
	}
}

func TestInstall(t *testing.T) {
	f := newFixture(t, 3, 0)
	var results []PublishResult
	versions := []string{"1.0.0", "1.1.0", "1.2.0"}
	for _, v := range versions {
		results = append(results, f.publish(v))
	}

	for i, v := range versions {
		got, err := f.client.Install(f.domain, f.pkg, v)
		if err != nil {
			t.Fatalf("Install %s: %v", v, err)
		}
		if !bytes.Equal(got.Data, content(v)) || got.Version.String() != v || got.RecordHash != results[i].RecordHash || got.BlobHash != results[i].BlobHash {
			t.Errorf("Install %s returned %+v", v, got)
		}
	}
	latest, err := f.client.Install(f.domain, f.pkg, Latest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(latest.Data, content("1.2.0")) || latest.Version != (Version{1, 2, 0}) {
		t.Fatalf("latest = %+v", latest)
	}

	for _, spec := range []string{"9.9.9", "1.1.5", "0.0.1", "1.2.1"} {
		if _, err := f.client.Install(f.domain, f.pkg, spec); !errors.Is(err, ErrUnknownVersion) {
			t.Errorf("Install %s: err = %v, want ErrUnknownVersion", spec, err)
		}
	}
	for _, spec := range []string{"", "v1", "1.2", "LATEST", "1.2.3-beta"} {
		if _, err := f.client.Install(f.domain, f.pkg, spec); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("Install %q: err = %v, want ErrInvalidVersion", spec, err)
		}
	}
	if _, err := f.client.Install(f.domain, "nothing", Latest); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown package: %v", err)
	}
	if _, err := f.client.Install("Bad Domain", f.pkg, Latest); !errors.Is(err, ErrInvalidName) {
		t.Errorf("invalid domain: %v", err)
	}
	if _, err := f.client.Install("unowned.org", f.pkg, Latest); !errors.Is(err, ErrNoOwner) {
		t.Errorf("unowned domain: %v", err)
	}
}

func TestInstallDetectsTamperedBlob(t *testing.T) {
	f := newFixture(t, 3, 0)
	f.publish("1.0.0")
	mid := f.publish("1.1.0")
	f.publish("1.2.0")
	f.net.overwrite(mid.BlobHash, []byte("malicious replacement"))

	if _, err := f.client.Install(f.domain, f.pkg, "1.1.0"); !errors.Is(err, ErrBlobMismatch) {
		t.Fatalf("err = %v, want ErrBlobMismatch", err)
	}
	if _, err := f.client.Install(f.domain, f.pkg, "1.2.0"); err != nil {
		t.Fatalf("an untouched version should still install: %v", err)
	}
	if _, err := f.client.Install(f.domain, f.pkg, "1.0.0"); err != nil {
		t.Fatalf("an untouched version should still install: %v", err)
	}
}

func TestInstallWithMissingData(t *testing.T) {
	f := newFixture(t, 3, 0)
	f.publish("1.0.0")
	second := f.publish("1.1.0")
	f.publish("1.2.0")

	f.net.drop(second.BlobHash)
	if _, err := f.client.Install(f.domain, f.pkg, "1.1.0"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing blob: %v", err)
	}
	f.net.drop(second.RecordHash)
	if _, err := f.client.Install(f.domain, f.pkg, "1.0.0"); !errors.Is(err, ErrMissingRecord) {
		t.Errorf("missing record on the way: %v", err)
	}
	if _, err := f.client.Install(f.domain, f.pkg, Latest); err != nil {
		t.Errorf("latest does not need the missing record: %v", err)
	}
}

func TestInstallRejectsTamperedMetadata(t *testing.T) {
	_, attacker := mustKey(t)
	key := func(f *fixture) string { return PointerKey(f.domain, f.pkg) }

	t.Run("pointer replaced by one signed by a non-owner", func(t *testing.T) {
		f := newFixture(t, 2, 0)
		res := f.publish("1.0.0")
		forged, _ := NewLatestPointer(attacker, f.domain, f.pkg, Version{1, 0, 0}, res.RecordHash)
		raw, _ := forged.Encode()
		f.net.replicas[0].mut[key(f)] = raw
		if _, err := f.client.Install(f.domain, f.pkg, Latest); !errors.Is(err, ErrNotOwner) {
			t.Fatalf("Install: %v", err)
		}
		if _, err := f.client.Chain(f.domain, f.pkg); !errors.Is(err, ErrNotOwner) {
			t.Fatalf("Chain: %v", err)
		}
		if _, err := f.client.Publish(f.opts("1.1.0"), f.priv); !errors.Is(err, ErrNotOwner) {
			t.Fatalf("Publish: %v", err)
		}
	})
	t.Run("pointer of another package stored under this key", func(t *testing.T) {
		f := newFixture(t, 2, 0)
		f.publish("1.0.0")
		f.pkg = "other"
		f.publish("1.0.0")
		other := f.net.replicas[0].mut[PointerKey(f.domain, "other")]
		f.net.replicas[0].mut[PointerKey(f.domain, "tool")] = other
		if _, err := f.client.Install(f.domain, "tool", Latest); !errors.Is(err, ErrKeyMismatch) {
			t.Fatalf("Install: %v", err)
		}
	})
	t.Run("pointer and record disagree about the version", func(t *testing.T) {
		f := newFixture(t, 2, 0)
		res := f.publish("1.0.0")
		lie, _ := NewLatestPointer(f.priv, f.domain, f.pkg, Version{9, 9, 9}, res.RecordHash)
		raw, _ := lie.Encode()
		f.net.replicas[0].mut[key(f)] = raw
		if _, err := f.client.Install(f.domain, f.pkg, Latest); !errors.Is(err, ErrInconsistent) {
			t.Fatalf("Install: %v", err)
		}
		if _, err := f.client.Chain(f.domain, f.pkg); !errors.Is(err, ErrInconsistent) {
			t.Fatalf("Chain: %v", err)
		}
	})
	t.Run("record replaced by other bytes", func(t *testing.T) {
		f := newFixture(t, 2, 0)
		res := f.publish("1.0.0")
		f.net.overwrite(res.RecordHash, []byte("not the record"))
		if _, err := f.client.Install(f.domain, f.pkg, Latest); !errors.Is(err, ErrMissingRecord) {
			t.Fatalf("Install: %v", err)
		}
	})
	t.Run("record forged by a non-owner and stored under its own hash", func(t *testing.T) {
		f := newFixture(t, 2, 0)
		f.publish("1.0.0")
		evil, _ := NewVersionRecord(attacker, f.domain, f.pkg, Version{1, 1, 0}, Hash([]byte("evil")), ZeroHash)
		evilRaw, _ := evil.Encode()
		evilHash, _ := f.net.PutImmutable(evilRaw)
		ptr, _ := NewLatestPointer(f.priv, f.domain, f.pkg, Version{1, 1, 0}, evilHash)
		raw, _ := ptr.Encode()
		f.net.replicas[0].mut[key(f)] = raw
		if _, err := f.client.Install(f.domain, f.pkg, Latest); !errors.Is(err, ErrNotOwner) {
			t.Fatalf("Install: %v", err)
		}
	})
	t.Run("stored pointer is garbage", func(t *testing.T) {
		f := newFixture(t, 2, 0)
		f.publish("1.0.0")
		f.net.replicas[0].mut[key(f)] = []byte("garbage")
		if _, err := f.client.Install(f.domain, f.pkg, Latest); !errors.Is(err, ErrMalformed) {
			t.Fatalf("Install: %v", err)
		}
		if _, err := f.client.Publish(f.opts("1.1.0"), f.priv); !errors.Is(err, ErrMalformed) {
			t.Fatalf("Publish: %v", err)
		}
		forced := f.opts("1.1.0")
		forced.Force = true
		prev := mustVersion(t, "1.0.0")
		forced.Prev = &prev
		if _, err := f.client.Publish(forced, f.priv); !errors.Is(err, ErrMalformed) {
			t.Fatalf("forced Publish: %v", err)
		}
	})
}

func TestChain(t *testing.T) {
	f := newFixture(t, 3, 0)
	if _, err := f.client.Chain(f.domain, f.pkg); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished package: %v", err)
	}
	if _, err := f.client.Chain("Bad", f.pkg); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("invalid domain: %v", err)
	}
	if _, err := f.client.Chain("unowned.org", f.pkg); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("unowned domain: %v", err)
	}
	a := f.publish("1.0.0")
	b := f.publish("1.1.0")
	chain, err := f.client.Chain(f.domain, f.pkg)
	if err != nil {
		t.Fatal(err)
	}
	want := []ChainEntry{
		{Version{1, 1, 0}, b.RecordHash, b.BlobHash},
		{Version{1, 0, 0}, a.RecordHash, a.BlobHash},
	}
	if len(chain) != 2 || chain[0] != want[0] || chain[1] != want[1] {
		t.Fatalf("chain = %+v, want %+v", chain, want)
	}

	f.net.drop(a.RecordHash)
	partial, err := f.client.Chain(f.domain, f.pkg)
	if !errors.Is(err, ErrMissingRecord) || len(partial) != 1 || partial[0] != want[0] {
		t.Fatalf("with a missing record: %+v, %v", partial, err)
	}
}

func TestOwner(t *testing.T) {
	f := newFixture(t, 1, 0)
	got, err := f.client.Owner(f.domain)
	if err != nil || !f.pub.Equal(got) {
		t.Fatalf("Owner = %x, %v", got, err)
	}
	if _, err := f.client.Owner("unowned.org"); !errors.Is(err, ErrNoOwner) {
		t.Errorf("unowned domain: %v", err)
	}
	if _, err := f.client.Owner("Not A Domain"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("invalid domain: %v", err)
	}
}

type faultyBackend struct {
	Backend
	getMutableErr   error
	putImmutableErr map[int]error
	putMutableErr   error
	wrongHash       bool
	putImmutables   int
}

func (b *faultyBackend) GetMutable(key string) ([]byte, error) {
	if b.getMutableErr != nil {
		return nil, b.getMutableErr
	}
	return b.Backend.GetMutable(key)
}

func (b *faultyBackend) PutImmutable(data []byte) (string, error) {
	n := b.putImmutables
	b.putImmutables++
	if err := b.putImmutableErr[n]; err != nil {
		return "", err
	}
	h, err := b.Backend.PutImmutable(data)
	if b.wrongHash {
		return flipHex(h), err
	}
	return h, err
}

func (b *faultyBackend) PutMutable(key string, value []byte) error {
	if b.putMutableErr != nil {
		return b.putMutableErr
	}
	return b.Backend.PutMutable(key, value)
}

func TestPublishBackendFailures(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name       string
		backend    func(Backend) *faultyBackend
		wrapsBoom  bool
		wantBlob   bool
		wantRecord bool
	}{
		{"reading the pointer fails", func(b Backend) *faultyBackend { return &faultyBackend{Backend: b, getMutableErr: boom} }, true, false, false},
		{"uploading the blob fails", func(b Backend) *faultyBackend {
			return &faultyBackend{Backend: b, putImmutableErr: map[int]error{0: boom}}
		}, true, false, false},
		{"uploading the record fails", func(b Backend) *faultyBackend {
			return &faultyBackend{Backend: b, putImmutableErr: map[int]error{1: boom}}
		}, true, true, false},
		{"updating the pointer fails", func(b Backend) *faultyBackend { return &faultyBackend{Backend: b, putMutableErr: boom} }, true, true, true},
		{"the backend stores under another hash", func(b Backend) *faultyBackend { return &faultyBackend{Backend: b, wrongHash: true} }, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, 2, 0)
			fb := tc.backend(f.net)
			c := NewClient(fb, f.owners)
			res, err := c.Publish(f.opts("1.0.0"), f.priv)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.wrapsBoom && !errors.Is(err, boom) {
				t.Errorf("err = %v does not wrap the backend error", err)
			}
			if (res.BlobHash != "") != tc.wantBlob || (res.RecordHash != "") != tc.wantRecord {
				t.Errorf("result %+v does not reflect how far the upload got", res)
			}
			for i, r := range f.net.replicas {
				if r.pointer(t, f.domain, f.pkg) != nil {
					t.Errorf("replica %d stored a pointer", i)
				}
			}
		})
	}
}
