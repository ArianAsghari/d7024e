package dht

import (
	"bytes"
	"crypto/ed25519"
	"sort"
	"testing"

	"d7024e/experiment"
	"d7024e/kademlia"
	"d7024e/registry"
)

const (
	testDomain = "example.com"
	otherHost  = "other.org"
	testPkg    = "tool"
)

type regNet struct {
	t         *testing.T
	net       *experiment.Network
	owners    *registry.StaticOwners
	pub       ed25519.PublicKey
	priv      ed25519.PrivateKey
	otherPub  ed25519.PublicKey
	otherPriv ed25519.PrivateKey
}

func newRegNet(t *testing.T, n int, seed int64) *regNet {
	t.Helper()
	network, err := experiment.Build(experiment.BuildConfig{N: n, Seed: seed})
	if err != nil {
		t.Fatal(err)
	}
	r := &regNet{t: t, net: network, owners: registry.NewStaticOwners()}
	r.pub, r.priv = mustKey(t)
	r.otherPub, r.otherPriv = mustKey(t)
	if err := r.owners.Set(testDomain, r.pub); err != nil {
		t.Fatal(err)
	}
	if err := r.owners.Set(otherHost, r.otherPub); err != nil {
		t.Fatal(err)
	}
	for _, node := range network.Nodes {
		Install(node, r.owners)
	}
	return r
}

func mustKey(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := registry.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func mustVersion(t testing.TB, s string) registry.Version {
	t.Helper()
	v, err := registry.ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func content(version string) []byte {
	return []byte("package content of version " + version)
}

func (r *regNet) client(i int) *registry.Client {
	return registry.NewClient(NewBackend(r.net.Nodes[i]), r.owners)
}

func (r *regNet) opts(version string) registry.PublishOptions {
	return registry.PublishOptions{Domain: testDomain, Package: testPkg, Version: mustVersion(r.t, version), Data: content(version)}
}

func (r *regNet) publish(i int, version string) registry.PublishResult {
	r.t.Helper()
	res, err := r.client(i).Publish(r.opts(version), r.priv)
	if err != nil {
		r.t.Fatalf("publish %s from node %d: %v", version, i, err)
	}
	return res
}

func (r *regNet) key() string {
	return registry.PointerKey(testDomain, testPkg)
}

func (r *regNet) keyID() *kademlia.KademliaID {
	return kademlia.NewKademliaID(r.key())
}

func storedValue(node *kademlia.Kademlia, key string) []byte {
	for _, entry := range node.Entries() {
		if entry.Key == key {
			return entry.Value
		}
	}
	return nil
}

// pointers returns the raw pointer every node holds under the package key.
func (r *regNet) pointers() map[int][]byte {
	out := map[int][]byte{}
	for i, node := range r.net.Nodes {
		if v := storedValue(node, r.key()); v != nil {
			out[i] = v
		}
	}
	return out
}

func (r *regNet) closestIndex() int {
	want := r.net.Closest(r.keyID(), nil)
	for i, c := range r.net.Contacts {
		if c.ID.Equals(want.ID) {
			return i
		}
	}
	r.t.Fatal("no closest node")
	return -1
}

// ranked returns node indexes ordered by XOR distance to the pointer key,
// nearest first. The first k are the nodes a StoreAt of the pointer reaches.
func (r *regNet) ranked() []int {
	order := make([]int, len(r.net.Nodes))
	for i := range order {
		order[i] = i
	}
	key := r.keyID()
	sort.Slice(order, func(a, b int) bool {
		return r.net.Contacts[order[a]].ID.CalcDistance(key).Less(r.net.Contacts[order[b]].ID.CalcDistance(key))
	})
	return order
}

func (r *regNet) holders(key string) int {
	total := 0
	for _, node := range r.net.Nodes {
		if storedValue(node, key) != nil {
			total++
		}
	}
	return total
}

// legitPointer returns the pointer the closest node holds, which every honest
// publication reaches.
func (r *regNet) legitPointer() []byte {
	r.t.Helper()
	raw := storedValue(r.net.Nodes[r.closestIndex()], r.key())
	if raw == nil {
		r.t.Fatal("the node closest to the pointer key holds no pointer")
	}
	return raw
}

// assertOnly fails if any node holds a pointer outside accepted.
func (r *regNet) assertOnly(accepted ...[]byte) {
	r.t.Helper()
	for i, raw := range r.pointers() {
		found := false
		for _, a := range accepted {
			found = found || bytes.Equal(raw, a)
		}
		if !found {
			p, _ := registry.DecodeLatestPointer(raw)
			r.t.Errorf("node %d holds a pointer that was never accepted: %+v", i, p)
		}
	}
}

func pointerVersion(t testing.TB, raw []byte) string {
	t.Helper()
	p, err := registry.DecodeLatestPointer(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p.Version.String()
}
