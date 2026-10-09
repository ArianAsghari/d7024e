package experiment

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"testing"

	"d7024e/kademlia"
)

type record map[string]any

func parseLog(t *testing.T, raw []byte) []record {
	t.Helper()
	var records []record
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("invalid log line %q: %v", line, err)
		}
		records = append(records, r)
	}
	return records
}

func TestRandomAddressesAreDistinctAndHashToIDs(t *testing.T) {
	addresses := RandomAddresses(rand.New(rand.NewSource(1)), 500)
	seen := map[string]bool{}
	for _, address := range addresses {
		if seen[address] || !strings.HasPrefix(address, "10.") || !strings.Contains(address, ":") {
			t.Fatalf("bad or duplicate address %q", address)
		}
		seen[address] = true
	}
	sum := sha256.Sum256([]byte(addresses[0]))
	if !IDFromAddress(addresses[0]).Equals((*kademlia.KademliaID)(&sum)) {
		t.Fatal("node IDs must be hash(IP|port)")
	}
	again := RandomAddresses(rand.New(rand.NewSource(1)), 500)
	for i := range addresses {
		if addresses[i] != again[i] {
			t.Fatal("the same seed must give the same topology")
		}
	}
}

func TestBuildCreatesAWorkingNetwork(t *testing.T) {
	if _, err := Build(BuildConfig{N: 0}); err == nil {
		t.Fatal("an empty network was accepted")
	}
	network, err := Build(BuildConfig{N: 60, K: 5, Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.Nodes) != 60 || network.Sim.NodeCount() != 60 || network.JoinFailures != 0 {
		t.Fatalf("nodes %d, registered %d, join failures %d", len(network.Nodes), network.Sim.NodeCount(), network.JoinFailures)
	}
	rng := rand.New(rand.NewSource(9))
	for i := 0; i < 20; i++ {
		target := kademlia.NewContact(randomID(rng), "")
		origin := rng.Intn(60)
		contacts, err := network.Nodes[origin].LookupContact(&target)
		if err != nil || !contacts[0].ID.Equals(network.Closest(target.ID, network.Contacts[origin].ID).ID) {
			t.Fatalf("lookup %d did not find the closest node: %v", i, err)
		}
	}
}

func TestClosestAndHolds(t *testing.T) {
	network, err := Build(BuildConfig{N: 12, Seed: 5})
	if err != nil {
		t.Fatal(err)
	}
	target := network.Contacts[3].ID
	if got := network.Closest(target, nil); !got.ID.Equals(target) {
		t.Fatal("a node is its own closest node when it is not excluded")
	}
	if got := network.Closest(target, target); got.ID.Equals(target) {
		t.Fatal("the excluded node must not be returned")
	}
	value := []byte("stored somewhere")
	key, err := network.Nodes[0].Store(value)
	if err != nil {
		t.Fatal(err)
	}
	holders := 0
	for _, node := range network.Nodes {
		if Holds(node, key) {
			holders++
		}
	}
	if holders == 0 || holders > kademlia.DefaultK+1 {
		t.Fatalf("%d nodes hold the value, want between 1 and k", holders)
	}
}

func TestScalabilityLogIsCompleteAndConsistent(t *testing.T) {
	cfg := ScalabilityConfig{Sizes: []int{20, 40}, Seeds: []int64{1, 2}, NodeLookups: 6, ValueLookups: 4, Workers: 4}
	var out bytes.Buffer
	if err := RunScalability(&out, cfg); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	truths := map[string]string{}
	probesSeen := map[float64]int{} // lookup id -> probe events, per run
	hits, nodeLookups := 0, 0
	for _, r := range parseLog(t, out.Bytes()) {
		ev := r["ev"].(string)
		counts[ev]++
		switch ev {
		case "run":
			probesSeen = map[float64]int{}
		case "truth":
			truths[r["target"].(string)] = r["closest"].(string)
		case "probe":
			probesSeen[r["lookup"].(float64)]++
		case "lookup":
			if r["ok"] != true || r["probes"].(float64) < 1 {
				t.Fatalf("lookup without loss must succeed and send probes: %v", r)
			}
			if float64(probesSeen[r["lookup"].(float64)]) != r["probes"].(float64) {
				t.Fatalf("lookup summary says %v probes but %d probe events were logged: %v",
					r["probes"], probesSeen[r["lookup"].(float64)], r)
			}
			if r["kind"] == "node" {
				nodeLookups++
				if r["best"] == truths[r["target"].(string)] {
					hits++
				}
			}
		}
	}
	if counts["run"] != 4 || counts["lookup"] != 4*(6+4) || counts["truth"] != 4*6 || counts["probe"] == 0 {
		t.Fatalf("event counts = %v", counts)
	}
	if hits < nodeLookups*9/10 {
		t.Fatalf("only %d of %d node lookups found the true closest node", hits, nodeLookups)
	}
}

func TestReliabilityLogCoversTheLossSweep(t *testing.T) {
	cfg := ReliabilityConfig{
		N: 40, Seeds: []int64{1, 2}, Losses: []float64{0, 1}, Values: 3, Lookups: 5, Workers: 2,
		Variants: []Variant{{Name: "r0", Retries: 0}, {Name: "r2", Retries: 2}},
	}
	var out bytes.Buffer
	if err := RunReliability(&out, cfg); err != nil {
		t.Fatal(err)
	}
	var run record
	runs, lookups := 0, 0
	for _, r := range parseLog(t, out.Bytes()) {
		switch r["ev"] {
		case "run":
			run = r
			runs++
		case "lookup":
			lookups++
			total := run["loss"] == float64(1)
			if r["ok"] == total { // everything lost => must fail; no loss => must succeed
				t.Fatalf("loss=%v lookup ok=%v", run["loss"], r["ok"])
			}
			if total && r["failed"] != r["probes"] {
				t.Fatalf("with total loss every probe must fail: %v", r)
			}
		}
	}
	if runs != 2*2*2 || lookups != runs*5 {
		t.Fatalf("runs=%d lookups=%d", runs, lookups)
	}
}

func TestRunJobsReportsErrorsInOrder(t *testing.T) {
	failure := errors.New("boom")
	jobs := []func() ([]byte, error){
		func() ([]byte, error) { return []byte("a\n"), nil },
		func() ([]byte, error) { return nil, failure },
	}
	if err := runJobs(&bytes.Buffer{}, 2, jobs); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	var out bytes.Buffer
	if err := runJobs(&out, 0, jobs[:1]); err != nil || out.String() != "a\n" {
		t.Fatalf("runJobs = %q, %v", out.String(), err)
	}
	if err := runJobs(errWriter{}, 1, jobs[:1]); err == nil {
		t.Fatal("write errors must be returned")
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

// TestThousandNodeNetwork is the lab's "at least 1000 nodes" requirement: one
// thousand nodes grow through the real join procedure and then serve lookups,
// stores, churn and packet loss over the simulated network.
func TestThousandNodeNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("1000-node test skipped in -short mode")
	}
	const n = 1000
	network, err := Build(BuildConfig{N: n, Seed: 42})
	if err != nil {
		t.Fatal(err)
	}
	if network.Sim.NodeCount() != n {
		t.Fatalf("%d nodes on the network, want %d", network.Sim.NodeCount(), n)
	}
	if network.JoinFailures > n/100 {
		t.Fatalf("%d of %d nodes failed to join", network.JoinFailures, n)
	}
	rng := rand.New(rand.NewSource(1))

	// Every lookup must converge on the node nearest to the target.
	correct := 0
	for i := 0; i < 100; i++ {
		target := kademlia.NewContact(randomID(rng), "")
		origin := rng.Intn(n)
		contacts, err := network.Nodes[origin].LookupContact(&target)
		if err != nil {
			t.Fatalf("lookup %d failed: %v", i, err)
		}
		if contacts[0].ID.Equals(network.Closest(target.ID, network.Contacts[origin].ID).ID) {
			correct++
		}
	}
	if correct < 98 {
		t.Fatalf("only %d of 100 lookups found the closest node", correct)
	}

	// Values stored from random nodes are found from other random nodes.
	keys, err := storeRandomValues(network, rng, 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		value, source, err := network.Nodes[pickReader(network, rng, key)].LookupData(key)
		if err != nil || source == nil || len(value) == 0 {
			t.Fatalf("value %s not found: %v", key[:8], err)
		}
	}

	// Churn: a tenth of the network crashes; the values must survive on the
	// remaining replicas, and lookups must route around the dead nodes.
	dead := map[int]bool{}
	for len(dead) < n/10 {
		i := rng.Intn(n)
		if !dead[i] {
			dead[i] = true
			network.Sim.Remove(network.Contacts[i].Address)
		}
	}
	aliveReader := func(key string) *kademlia.Kademlia {
		for {
			if i := rng.Intn(n); !dead[i] && !Holds(network.Nodes[i], key) {
				return network.Nodes[i]
			}
		}
	}
	for _, key := range keys {
		if _, _, err := aliveReader(key).LookupData(key); err != nil {
			t.Fatalf("value %s lost after 10%% of the nodes crashed: %v", key[:8], err)
		}
	}

	// Packet loss: with 10% loss per packet and two retries, lookups still work.
	network.Sim.SetPacketLoss(0.10)
	network.Sim.SetRetries(2)
	found := 0
	for _, key := range keys {
		if _, _, err := aliveReader(key).LookupData(key); err == nil {
			found++
		}
	}
	if found < 28 {
		t.Fatalf("only %d of 30 lookups succeeded under 10%% packet loss", found)
	}
}
