package experiment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"runtime"
	"sync"

	"d7024e/kademlia"
)

// ScalabilityConfig describes Experiment 1: how many probes does a lookup need
// as a function of the network size N?
type ScalabilityConfig struct {
	Sizes        []int
	Seeds        []int64
	K            int // 0 = default
	NodeLookups  int // FIND_NODE lookups measured per run
	ValueLookups int // FIND_VALUE lookups measured per run
	Workers      int // runs executed in parallel; 0 = number of CPUs
}

// Variant is one transport configuration compared in Experiment 2.
type Variant struct {
	Name    string
	Retries int // extra RPC attempts after a lost packet
}

// ReliabilityConfig describes Experiment 2: how often does a lookup still find
// a stored value when packets are lost?
type ReliabilityConfig struct {
	N        int
	K        int
	Seeds    []int64
	Losses   []float64 // per-packet loss probabilities to sweep
	Variants []Variant
	Values   int // values stored per run (before loss is switched on)
	Lookups  int // value lookups measured per run
	Workers  int
}

// RunScalability writes the event log of Experiment 1 to w. For every network
// size and seed it builds a fresh random network, then measures iterative node
// lookups for random targets and value lookups for stored random values, each
// started from a random node.
func RunScalability(w io.Writer, cfg ScalabilityConfig) error {
	var jobs []func() ([]byte, error)
	for _, n := range cfg.Sizes {
		for _, seed := range cfg.Seeds {
			n, seed := n, seed
			jobs = append(jobs, func() ([]byte, error) { return scalabilityRun(cfg, n, seed) })
		}
	}
	return runJobs(w, cfg.Workers, jobs)
}

// RunReliability writes the event log of Experiment 2 to w. Every
// (variant, loss, seed) combination is measured on a network built with the
// same seed, so the variants are compared on comparable data.
func RunReliability(w io.Writer, cfg ReliabilityConfig) error {
	var jobs []func() ([]byte, error)
	for _, variant := range cfg.Variants {
		for _, loss := range cfg.Losses {
			for _, seed := range cfg.Seeds {
				variant, loss, seed := variant, loss, seed
				jobs = append(jobs, func() ([]byte, error) { return reliabilityRun(cfg, variant, loss, seed) })
			}
		}
	}
	return runJobs(w, cfg.Workers, jobs)
}

func scalabilityRun(cfg ScalabilityConfig, n int, seed int64) ([]byte, error) {
	rec := newRecorder()
	network, err := Build(BuildConfig{N: n, K: cfg.K, Seed: seed})
	if err != nil {
		return nil, err
	}
	network.Sim.SetObserver(rec.observe)
	rng := measurementRNG(seed, n)
	rec.meta("run", map[string]any{
		"exp": "scalability", "n": n, "seed": seed, "k": effectiveK(cfg.K), "join_failures": network.JoinFailures,
	})

	for i := 0; i < cfg.NodeLookups; i++ {
		originIndex := rng.Intn(n)
		origin := network.Nodes[originIndex]
		target := kademlia.NewContact(randomID(rng), "")
		// Ground truth: the nearest node other than the origin, which a lookup
		// never reports about itself.
		closest := network.Closest(target.ID, network.Contacts[originIndex].ID)
		rec.meta("truth", map[string]any{"kind": "node", "target": shortID(target.ID), "closest": shortID(closest.ID)})
		rec.measure("node", network.Contacts[originIndex], target.ID, func() (bool, string, string) {
			contacts, err := origin.LookupContact(&target)
			best := ""
			if len(contacts) > 0 {
				best = shortID(contacts[0].ID)
			}
			return err == nil, best, ""
		})
	}

	keys, err := storeRandomValues(network, rng, cfg.ValueLookups)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		readerIndex := pickReader(network, rng, key)
		rec.measureValue(network, readerIndex, key)
	}
	return rec.finish()
}

func reliabilityRun(cfg ReliabilityConfig, variant Variant, loss float64, seed int64) ([]byte, error) {
	rec := newRecorder()
	// The network is built and filled without loss, so every variant starts
	// from the same state; loss and retry policy are switched on afterwards.
	network, err := Build(BuildConfig{N: cfg.N, K: cfg.K, Seed: seed})
	if err != nil {
		return nil, err
	}
	rng := measurementRNG(seed, cfg.N)
	keys, err := storeRandomValues(network, rng, cfg.Values)
	if err != nil {
		return nil, err
	}

	network.Sim.SetRetries(variant.Retries)
	network.Sim.SetPacketLoss(loss)
	network.Sim.SetObserver(rec.observe)
	rec.meta("run", map[string]any{
		"exp": "reliability", "n": cfg.N, "seed": seed, "k": effectiveK(cfg.K),
		"variant": variant.Name, "retries": variant.Retries, "loss": loss,
	})

	for i := 0; i < cfg.Lookups; i++ {
		key := keys[rng.Intn(len(keys))]
		rec.measureValue(network, pickReader(network, rng, key), key)
	}
	return rec.finish()
}

func effectiveK(k int) int {
	if k <= 0 {
		return kademlia.DefaultK
	}
	return k
}

// storeRandomValues stores count random values from random nodes and returns
// their keys. Stores happen before the recorder is attached.
func storeRandomValues(network *Network, rng *rand.Rand, count int) ([]string, error) {
	keys := make([]string, 0, count)
	for i := 0; i < count; i++ {
		value := make([]byte, 16+rng.Intn(241))
		_, _ = rng.Read(value)
		key, err := network.Nodes[rng.Intn(len(network.Nodes))].Store(value)
		if err != nil {
			return nil, fmt.Errorf("storing value %d: %w", i, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// pickReader chooses a random node that does not already hold key, so that the
// lookup really has to cross the network, and returns its index.
func pickReader(network *Network, rng *rand.Rand, key string) int {
	for {
		index := rng.Intn(len(network.Nodes))
		if !Holds(network.Nodes[index], key) {
			return index
		}
	}
}

func measurementRNG(seed int64, n int) *rand.Rand {
	return rand.New(rand.NewSource(seed*1_000_003 + int64(n) + 7))
}

func randomID(rng *rand.Rand) *kademlia.KademliaID {
	var id kademlia.KademliaID
	_, _ = rng.Read(id[:])
	return &id
}

func shortID(id *kademlia.KademliaID) string {
	return id.String()[:16]
}

// recorder turns the RPCs a measured lookup sends into JSON-lines events. A
// "probe" is one FIND_NODE or FIND_VALUE RPC; retransmissions of the same RPC
// by the transport are not extra probes. Measurements run one at a time, so
// every FIND_* RPC seen while one is active belongs to it.
type recorder struct {
	mu     sync.Mutex
	out    bytes.Buffer
	active bool
	id     uint64
	kind   string
	origin kademlia.Contact
	probes int
	failed int
}

func newRecorder() *recorder { return &recorder{} }

func (r *recorder) write(record map[string]any) {
	line, _ := json.Marshal(record) // map of plain values: cannot fail
	r.out.Write(append(line, '\n'))
}

func (r *recorder) meta(kind string, fields map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := map[string]any{"ev": kind}
	for key, value := range fields {
		record[key] = value
	}
	r.write(record)
}

// observe is the SimNetwork observer.
func (r *recorder) observe(event kademlia.RPCEvent) {
	if event.Type != "FIND_NODE" && event.Type != "FIND_VALUE" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return
	}
	r.probes++
	if !event.OK {
		r.failed++
	}
	r.write(map[string]any{
		"ev": "probe", "lookup": r.id, "kind": r.kind, "node": shortID(r.origin.ID),
		"to": shortID(IDFromAddress(event.To)), "rpc": event.Type, "ok": event.OK,
	})
}

// measure records one lookup. run performs it and reports success, the closest
// node it found (node lookups) and the node a value came from (value lookups).
func (r *recorder) measure(kind string, origin kademlia.Contact, target *kademlia.KademliaID, run func() (ok bool, best, from string)) {
	r.mu.Lock()
	r.active, r.id, r.kind, r.origin, r.probes, r.failed = true, r.id+1, kind, origin, 0, 0
	r.mu.Unlock()

	ok, best, from := run()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = false
	record := map[string]any{
		"ev": "lookup", "lookup": r.id, "kind": kind, "node": shortID(origin.ID), "target": shortID(target),
		"ok": ok, "probes": r.probes, "failed": r.failed,
	}
	if best != "" {
		record["best"] = best
	}
	if from != "" {
		record["from"] = from
	}
	r.write(record)
}

func (r *recorder) measureValue(network *Network, readerIndex int, key string) {
	hash := kademlia.NewKademliaID(key)
	r.measure("value", network.Contacts[readerIndex], hash, func() (bool, string, string) {
		_, source, err := network.Nodes[readerIndex].LookupData(key)
		from := ""
		if source != nil {
			from = shortID(source.ID)
		}
		return err == nil, "", from
	})
}

func (r *recorder) finish() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.Bytes(), nil
}

// runJobs executes the jobs on a pool of workers and writes their outputs to w
// in job order, so the log content does not depend on scheduling.
func runJobs(w io.Writer, workers int, jobs []func() ([]byte, error)) error {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	results := make([][]byte, len(jobs))
	errs := make([]error, len(jobs))
	next := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				results[i], errs[i] = jobs[i]()
			}
		}()
	}
	for i := range jobs {
		next <- i
	}
	close(next)
	wg.Wait()

	for i := range jobs {
		if errs[i] != nil {
			return errs[i]
		}
		if _, err := w.Write(results[i]); err != nil {
			return err
		}
	}
	return nil
}
