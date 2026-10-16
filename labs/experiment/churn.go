package experiment

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"sync/atomic"

	"d7024e/kademlia"
)

// ChurnConfig measures abrupt departures after a healthy join/store phase.
// Fractions are independent crash experiments, not departures per unit time.
// No nodes join, no packets are lost, and periodic maintenance is disabled.
type ChurnConfig struct {
	N           int
	K           int
	Alpha       int
	Seeds       []int64
	Fractions   []float64
	Values      int // distinct values, each read once from a surviving non-holder
	NodeLookups int
	Workers     int
}

// RunChurn writes compact per-lookup and per-key JSON-lines measurements.
// Each (fraction, seed) starts from a fresh healthy network. After measurement,
// a manual repair pass republishes from one original surviving holder per key
// (calls on the same node are deduplicated), then counts restored replicas.
func RunChurn(w io.Writer, cfg ChurnConfig) error {
	if cfg.N < 2 || cfg.Values < 1 || cfg.NodeLookups < 1 || len(cfg.Seeds) == 0 || len(cfg.Fractions) == 0 {
		return fmt.Errorf("churn needs at least two nodes, positive measurement counts, seeds and crash fractions")
	}
	var jobs []func() ([]byte, error)
	for _, fraction := range cfg.Fractions {
		if math.IsNaN(fraction) || fraction < 0 || fraction >= 1 || cfg.N-int(float64(cfg.N)*fraction) < 2 {
			return fmt.Errorf("invalid crash fraction %v for %d nodes", fraction, cfg.N)
		}
		for _, seed := range cfg.Seeds {
			fraction, seed := fraction, seed
			jobs = append(jobs, func() ([]byte, error) { return churnRun(cfg, fraction, seed) })
		}
	}
	return runJobs(w, cfg.Workers, jobs)
}

func churnRun(cfg ChurnConfig, fraction float64, seed int64) ([]byte, error) {
	network, err := Build(BuildConfig{N: cfg.N, K: cfg.K, Alpha: cfg.Alpha, Seed: seed})
	if err != nil {
		return nil, err
	}
	rng := measurementRNG(seed, cfg.N)
	keys, err := storeRandomValues(network, rng, cfg.Values)
	if err != nil {
		return nil, err
	}
	// Separate streams keep targets and values paired across crash fractions.
	crashRNG := rand.New(rand.NewSource(seed*2_000_003 + int64(cfg.N)))
	order := crashRNG.Perm(cfg.N)
	dead := make(map[int]bool)
	before := replicaCounts(network, keys, dead)
	for _, index := range order[:int(float64(cfg.N)*fraction)] {
		dead[index] = true
		network.Sim.Remove(network.Contacts[index].Address)
	}
	after := replicaCounts(network, keys, dead)
	// Pick repair sources before reads add immutable caches. Thus repair never
	// depends on a measurement-created cache to preserve an original replica.
	repairNodes := make(map[int]bool)
	for _, key := range keys {
		for index, node := range network.Nodes {
			if !dead[index] && Holds(node, key) {
				repairNodes[index] = true
				break
			}
		}
	}

	log := newRunLog()
	log.meta("run", map[string]any{
		"exp": "churn", "n": cfg.N, "seed": seed, "k": effectiveK(cfg.K), "alpha": effectiveAlpha(cfg.Alpha),
		"crash_fraction": fraction, "crashed": len(dead), "alive": network.Sim.NodeCount(),
		"join_failures": network.JoinFailures, "values": cfg.Values, "node_lookups": cfg.NodeLookups,
		"loss": 0, "retries": 0, "periodic_maintenance": false,
	})
	metrics := &completionMetrics{}
	network.SetLookupLogger(slog.New(metrics))
	var failed, probes atomic.Int64
	network.Sim.SetObserver(func(event kademlia.RPCEvent) {
		if event.Type == "FIND_NODE" || event.Type == "FIND_VALUE" {
			probes.Add(1)
			if !event.OK {
				failed.Add(1)
			}
		}
	})
	for i := 0; i < cfg.NodeLookups; i++ {
		origin := aliveIndex(network, rng, dead, "")
		target := kademlia.NewContact(randomID(rng), "")
		closest := closestAlive(network, target.ID, origin, dead, 1)[0]
		contacts, err := network.Nodes[origin].LookupContact(&target)
		exact := len(contacts) > 0 && contacts[0].ID.Equals(closest.ID)
		log.meta("churn_lookup", map[string]any{
			"kind": "node", "sample": i, "origin": origin, "target": target.ID.String(),
			"success": err == nil && len(contacts) > 0, "exact_closest": exact,
			"probes": probes.Swap(0), "failed_probes": failed.Swap(0), "rounds": metrics.rounds,
		})
	}
	for i, key := range keys {
		origin := aliveIndex(network, rng, dead, key)
		if origin < 0 {
			return nil, fmt.Errorf("no surviving non-holder to measure key %s", key)
		}
		_, source, err := network.Nodes[origin].LookupData(key)
		log.meta("churn_lookup", map[string]any{
			"kind": "value", "sample": i, "origin": origin, "key": key,
			"success": err == nil && source != nil, "surviving_replicas": after[key],
			"probes": probes.Swap(0), "failed_probes": failed.Swap(0), "rounds": metrics.rounds,
		})
	}
	network.SetLookupLogger(nil)
	network.Sim.SetObserver(nil)
	beforeRepair := replicaCounts(network, keys, dead)
	var sources []int
	for index := range repairNodes {
		sources = append(sources, index)
	}
	sort.Ints(sources)
	repairErrors := 0
	for _, index := range sources {
		if err := network.Nodes[index].Republish(); err != nil {
			repairErrors++
		}
	}
	afterRepair := replicaCounts(network, keys, dead)
	log.meta("churn_repair", map[string]any{"sources": len(sources), "errors": repairErrors})
	for _, key := range keys {
		id := kademlia.NewKademliaID(key)
		closestCopies := 0
		for _, contact := range closestAlive(network, id, -1, dead, effectiveK(cfg.K)) {
			if Holds(network.Sim.Node(contact.Address), key) {
				closestCopies++
			}
		}
		log.meta("churn_replicas", map[string]any{
			"key": key, "before_crash": before[key], "after_crash": after[key],
			"before_repair": beforeRepair[key], "after_repair": afterRepair[key],
			"closest_live_after_repair": closestCopies,
		})
	}
	return log.finish()
}

func replicaCounts(network *Network, keys []string, dead map[int]bool) map[string]int {
	counts := make(map[string]int, len(keys))
	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		wanted[key] = true
	}
	for index, node := range network.Nodes {
		if dead[index] {
			continue
		}
		for _, entry := range node.Entries() {
			if wanted[entry.Key] {
				counts[entry.Key]++
			}
		}
	}
	return counts
}

func aliveIndex(network *Network, rng *rand.Rand, dead map[int]bool, key string) int {
	// Bounded selection also handles small test networks where every survivor
	// already holds a key; an unbounded retry loop would hang in that case.
	for _, index := range rng.Perm(len(network.Nodes)) {
		if !dead[index] && (key == "" || !Holds(network.Nodes[index], key)) {
			return index
		}
	}
	return -1
}

func closestAlive(network *Network, target *kademlia.KademliaID, exclude int, dead map[int]bool, limit int) []kademlia.Contact {
	var contacts []kademlia.Contact
	for index, contact := range network.Contacts {
		if index != exclude && !dead[index] {
			contacts = append(contacts, contact)
		}
	}
	sort.Slice(contacts, func(i, j int) bool {
		return contacts[i].ID.CalcDistance(target).Less(contacts[j].ID.CalcDistance(target))
	})
	return contacts[:min(limit, len(contacts))]
}

// completionMetrics extracts rounds from the core's own lookup completion
// event without retaining thousands of verbose probe events. Lookups run
// sequentially; only their worker RPCs overlap, and Handle runs on the caller.
type completionMetrics struct{ rounds int64 }

func (m *completionMetrics) Enabled(context.Context, slog.Level) bool { return true }
func (m *completionMetrics) WithAttrs([]slog.Attr) slog.Handler       { return m }
func (m *completionMetrics) WithGroup(string) slog.Handler            { return m }
func (m *completionMetrics) Handle(_ context.Context, record slog.Record) error {
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "rounds" {
			m.rounds = attr.Value.Int64()
		}
		return true
	})
	return nil
}
