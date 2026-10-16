package experiment

import (
	"bytes"
	"math"
	"math/rand"
	"testing"

	"d7024e/kademlia"
)

func TestChurnMeasuresDeparturesAndReplicaRepair(t *testing.T) {
	cfg := ChurnConfig{N: 40, K: 3, Seeds: []int64{1, 2}, Fractions: []float64{0, 0.5}, Values: 8, NodeLookups: 10, Workers: 2}
	var out bytes.Buffer
	if err := RunChurn(&out, cfg); err != nil {
		t.Fatal(err)
	}
	var run record
	counts := make(map[string]int)
	for _, r := range parseLog(t, out.Bytes()) {
		kind := r["ev"].(string)
		counts[kind]++
		switch kind {
		case "run":
			run = r
			if r["alive"].(float64)+r["crashed"].(float64) != 40 || r["loss"] != float64(0) || r["periodic_maintenance"] != false {
				t.Fatalf("invalid measurement controls: %v", r)
			}
		case "churn_lookup":
			if r["probes"].(float64) < 1 || r["rounds"].(float64) < 1 || r["failed_probes"].(float64) > r["probes"].(float64) {
				t.Fatalf("invalid core/transport measurements: %v", r)
			}
			if run["crashed"] == float64(0) && (r["failed_probes"] != float64(0) || r["success"] != true) {
				t.Fatalf("healthy-network lookup failed: %v", r)
			}
			if r["kind"] == "value" && r["surviving_replicas"] == float64(0) && r["success"] == true {
				t.Fatalf("a value with no live copy was returned: %v", r)
			}
		case "churn_repair":
			if r["errors"] != float64(0) {
				t.Fatalf("repair failed without packet loss: %v", r)
			}
		case "churn_replicas":
			if r["before_crash"] != float64(3) || r["after_crash"].(float64) > 3 || r["before_repair"].(float64) < r["after_crash"].(float64) {
				t.Fatalf("inconsistent replica counts: %v", r)
			}
			if r["after_crash"] == float64(0) {
				if r["after_repair"] != float64(0) {
					t.Fatalf("repair invented a lost value: %v", r)
				}
			} else if r["after_repair"].(float64) < r["before_repair"].(float64) ||
				r["closest_live_after_repair"].(float64) > min(3, r["after_repair"].(float64)) {
				t.Fatalf("repair removed copies or reported impossible closest-node coverage: %v", r)
			}
		}
	}
	if counts["run"] != 4 || counts["churn_lookup"] != 4*18 || counts["churn_repair"] != 4 || counts["churn_replicas"] != 4*8 {
		t.Fatalf("incomplete measurements: %v", counts)
	}
}

func TestChurnGroundTruthIgnoresCrashedAndOriginNodes(t *testing.T) {
	network, err := Build(BuildConfig{N: 12, K: 2, Seed: 4})
	if err != nil {
		t.Fatal(err)
	}
	target := network.Contacts[3].ID
	dead := map[int]bool{3: true}
	closest := closestAlive(network, target, 0, dead, 12)
	if len(closest) != 10 {
		t.Fatalf("ground truth has %d nodes, want 10", len(closest))
	}
	for _, contact := range closest {
		if contact.ID.Equals(target) || contact.ID.Equals(network.Contacts[0].ID) {
			t.Fatal("ground truth included a dead node or lookup origin")
		}
	}
	value := []byte("only on crashed node")
	key, err := network.Nodes[3].Store(value)
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly put a value on every surviving node, so no valid remote
	// reader remains. Selection must return instead of looping indefinitely.
	for _, node := range network.Nodes {
		if err := node.StoreValue(kademlia.NewKademliaID(key), value); err != nil {
			t.Fatal(err)
		}
	}
	if got := replicaCounts(network, []string{key}, dead)[key]; got != 11 {
		t.Fatalf("counted %d live copies, want 11", got)
	}
	if got := aliveIndex(network, rand.New(rand.NewSource(1)), dead, key); got != -1 {
		t.Fatalf("reader %d already holds the value", got)
	}
}

func TestChurnRejectsInvalidConfiguration(t *testing.T) {
	valid := ChurnConfig{N: 20, Seeds: []int64{1}, Fractions: []float64{0}, Values: 1, NodeLookups: 1}
	for _, fraction := range []float64{-0.1, 1, math.NaN(), 0.99} {
		cfg := valid
		cfg.Fractions = []float64{fraction}
		if err := RunChurn(&bytes.Buffer{}, cfg); err == nil {
			t.Fatalf("accepted crash fraction %v", fraction)
		}
	}
	for _, cfg := range []ChurnConfig{{}, {N: 20}, {N: 1, Values: 1, NodeLookups: 1, Seeds: []int64{1}, Fractions: []float64{0}}} {
		if err := RunChurn(&bytes.Buffer{}, cfg); err == nil {
			t.Fatalf("accepted incomplete configuration %+v", cfg)
		}
	}
}
