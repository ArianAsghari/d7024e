package dht

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"math/rand"
	"slices"
	"testing"

	"d7024e/experiment"
	"d7024e/kademlia"
	"d7024e/registry"
)

// TestThousandNodeRegistry runs the package registry on 1000 simulated nodes:
// honest publications, a series of invalid updates that every replica must
// refuse, a replica that missed updates and catches up, and installs from nodes
// that know nothing about the package.
//
// Publishers and attackers are chosen among the nodes nearest to the pointer
// key. A node farther away that publishes or installs keeps a cached copy of
// the pointer which a later update does not reach, and FIND_VALUE serves that
// stale copy to other nodes; avoiding such nodes keeps the expected outcome of
// every step exact.
func TestThousandNodeRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("1000-node test skipped in -short mode")
	}
	const n = 1000
	r := newRegNet(t, n, 42)
	if r.net.Sim.NodeCount() != n || r.net.JoinFailures > n/100 {
		t.Fatalf("%d nodes on the network, %d failed to join", r.net.Sim.NodeCount(), r.net.JoinFailures)
	}
	rng := rand.New(rand.NewSource(7))
	ranked := r.ranked()
	replicas := ranked[:kademlia.DefaultK]

	var accepted [][]byte
	publish := func(from int, version string) registry.PublishResult {
		t.Helper()
		res := r.publish(from, version)
		want := storedValue(r.net.Nodes[from], r.key())
		accepted = append(accepted, want)
		if pointerVersion(t, want) != version {
			t.Fatalf("publisher %d holds %s after publishing %s", from, pointerVersion(t, want), version)
		}
		for _, i := range replicas {
			if got := storedValue(r.net.Nodes[i], r.key()); !bytes.Equal(got, want) {
				t.Fatalf("after %s, replica %d holds %v", version, i, got)
			}
		}
		return res
	}
	first := publish(ranked[3], "1.0.0")
	publish(ranked[6], "1.1.0")
	publish(ranked[8], "2.0.0")
	v3 := r.legitPointer()

	attacks := []struct {
		name string
		opts registry.PublishOptions
		priv ed25519.PrivateKey
		want error
	}{
		{"wrong owner", forced(r.opts("2.1.0"), nil), r.otherPriv, registry.ErrNotOwner},
		{"roll back", forced(r.opts("1.0.1"), nil), r.priv, registry.ErrRollback},
		{"cycle to the first version", forced(r.opts("1.0.0"), version(t, "2.0.0")), r.priv, registry.ErrRollback},
		{"same version, other contents", forced(withData(r.opts("2.0.0"), "replaced contents"), nil), r.priv, registry.ErrFork},
		{"branch from an old version", forced(r.opts("2.1.0"), version(t, "1.0.0")), r.priv, registry.ErrFork},
	}
	for i, a := range attacks {
		_, err := r.client(ranked[i]).Publish(a.opts, a.priv)
		if !errors.Is(err, a.want) {
			t.Fatalf("%s: publish = %v, want %v", a.name, err, a.want)
		}
	}
	forgery, err := registry.DecodeLatestPointer(v3)
	if err != nil {
		t.Fatal(err)
	}
	forgery.Version = mustVersion(t, "9.0.0")
	forgedRaw, err := forgery.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.net.Nodes[ranked[0]].StoreAt(r.keyID(), forgedRaw); !errors.Is(err, registry.ErrNotOwner) {
		t.Fatalf("forged pointer = %v", err)
	}
	r.assertOnly(accepted...)
	for _, i := range replicas {
		if got := storedValue(r.net.Nodes[i], r.key()); !bytes.Equal(got, v3) {
			t.Fatalf("replica %d moved away from 2.0.0 to %s", i, pointerVersion(t, got))
		}
	}

	// The registry still works for the owner after the attacks.
	publish(ranked[1], "2.1.0")

	// A replica that is cut off misses two updates. When the newest pointer
	// reaches it, it fetches the records it lacks and accepts.
	behind := ranked[9]
	before := storedValue(r.net.Nodes[behind], r.key())
	r.net.Sim.Remove(r.net.Contacts[behind].Address)
	replicas = ranked[:9]
	publish(ranked[0], "2.2.0")
	publish(ranked[2], "2.3.0")
	latest := storedValue(r.net.Nodes[ranked[0]], r.key())
	if bytes.Equal(storedValue(r.net.Nodes[behind], r.key()), latest) || pointerVersion(t, before) != "2.1.0" {
		t.Fatal("the cut-off replica should still hold 2.1.0")
	}
	if err := r.net.Nodes[behind].StoreValue(r.keyID(), latest); err != nil {
		t.Fatalf("catching up: %v", err)
	}
	if !bytes.Equal(storedValue(r.net.Nodes[behind], r.key()), latest) {
		t.Fatal("the replica did not move to the newest pointer")
	}
	chain, err := r.client(behind).Chain(testDomain, testPkg)
	if err != nil || len(chain) != 6 {
		t.Fatalf("chain on the replica that caught up: %d entries, %v", len(chain), err)
	}
	for _, e := range chain[:2] {
		if !experiment.Holds(r.net.Nodes[behind], e.RecordHash) {
			t.Fatalf("record of %s was not fetched", e.Version)
		}
	}

	// Installs from nodes that hold nothing: they validate the pointer and the
	// whole history they are shown, and download the package.
	skip := map[int]bool{}
	for _, i := range ranked[:30] {
		skip[i] = true
	}
	for len(skip) < 30+25 {
		reader := rng.Intn(n)
		if skip[reader] {
			continue
		}
		skip[reader] = true
		c := r.client(reader)
		got, err := c.Install(testDomain, testPkg, registry.Latest)
		if err != nil || got.Version.String() != "2.3.0" || !bytes.Equal(got.Data, content("2.3.0")) {
			t.Fatalf("node %d: install latest = %v, %v", reader, got, err)
		}
		old, err := c.Install(testDomain, testPkg, "1.0.0")
		if err != nil || old.RecordHash != first.RecordHash || !bytes.Equal(old.Data, content("1.0.0")) {
			t.Fatalf("node %d: install 1.0.0 = %v, %v", reader, old, err)
		}
		if got := chainVersions(t, c, testDomain, testPkg); !slices.Equal(got, []string{"2.3.0", "2.2.0", "2.1.0", "2.0.0", "1.1.0", "1.0.0"}) {
			t.Fatalf("node %d: chain = %v", reader, got)
		}
	}
	r.assertOnly(accepted...)
}

func forced(opts registry.PublishOptions, prev *registry.Version) registry.PublishOptions {
	opts.Force = true
	opts.Prev = prev
	return opts
}

func withData(opts registry.PublishOptions, data string) registry.PublishOptions {
	opts.Data = []byte(data)
	return opts
}

func version(t testing.TB, s string) *registry.Version {
	v := mustVersion(t, s)
	return &v
}
