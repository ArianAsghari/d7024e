package experiment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	Alpha        int // 0 = default
	NodeLookups  int // FIND_NODE lookups measured per run
	ValueLookups int // FIND_VALUE lookups measured per run
	Workers      int // runs executed in parallel; 0 = number of CPUs
}

// Variant is one lookup/transport configuration compared in Experiment 2.
type Variant struct {
	Name    string
	Alpha   int // lookup parallelism
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

// RunScalability writes the log of Experiment 1 to w. For every network size
// and seed it builds a fresh random network, then measures iterative node
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

// RunReliability writes the log of Experiment 2 to w. Every
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
	log := newRunLog()
	network, err := Build(BuildConfig{N: n, K: cfg.K, Alpha: cfg.Alpha, Seed: seed})
	if err != nil {
		return nil, err
	}
	rng := measurementRNG(seed, n)
	keys, err := storeRandomValues(network, rng, cfg.ValueLookups)
	if err != nil {
		return nil, err
	}

	log.meta("run", map[string]any{
		"exp": "scalability", "n": n, "seed": seed, "k": effectiveK(cfg.K), "alpha": effectiveAlpha(cfg.Alpha),
		"join_failures": network.JoinFailures,
	})
	network.SetLookupLogger(log.logger()) // from here on the nodes log their lookups

	for i := 0; i < cfg.NodeLookups; i++ {
		originIndex := rng.Intn(n)
		target := kademlia.NewContact(randomID(rng), "")
		contacts, _ := network.Nodes[originIndex].LookupContact(&target)
		// Ground truth: the nearest node other than the origin, which a lookup
		// never reports about itself.
		closest := network.Closest(target.ID, network.Contacts[originIndex].ID)
		best := ""
		if len(contacts) > 0 {
			best = shortID(contacts[0].ID)
		}
		log.meta("node_result", map[string]any{"target": shortID(target.ID), "closest": shortID(closest.ID), "best": best})
	}
	for _, key := range keys {
		_, _, _ = network.Nodes[pickReader(network, rng, key)].LookupData(key)
	}
	return log.finish()
}

func reliabilityRun(cfg ReliabilityConfig, variant Variant, loss float64, seed int64) ([]byte, error) {
	log := newRunLog()
	// The network is built and filled without loss and with default settings,
	// so every variant starts from the same state; loss, retry policy and alpha
	// are switched on afterwards.
	network, err := Build(BuildConfig{N: cfg.N, K: cfg.K, Seed: seed})
	if err != nil {
		return nil, err
	}
	rng := measurementRNG(seed, cfg.N)
	keys, err := storeRandomValues(network, rng, cfg.Values)
	if err != nil {
		return nil, err
	}

	if err := network.SetAlpha(variant.Alpha); err != nil {
		return nil, err
	}
	network.Sim.SetRetries(variant.Retries)
	network.Sim.SetPacketLoss(loss)
	log.meta("run", map[string]any{
		"exp": "reliability", "n": cfg.N, "seed": seed, "k": effectiveK(cfg.K),
		"variant": variant.Name, "alpha": variant.Alpha, "retries": variant.Retries, "loss": loss,
	})
	network.SetLookupLogger(log.logger())

	for i := 0; i < cfg.Lookups; i++ {
		key := keys[rng.Intn(len(keys))]
		_, _, _ = network.Nodes[pickReader(network, rng, key)].LookupData(key)
	}
	return log.finish()
}

func effectiveK(k int) int {
	if k <= 0 {
		return kademlia.DefaultK
	}
	return k
}

func effectiveAlpha(alpha int) int {
	if alpha <= 0 {
		return kademlia.DefaultAlpha
	}
	return alpha
}

// storeRandomValues stores count random values from random nodes and returns
// their keys. It runs before the nodes' lookup logging is switched on.
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

// runLog collects the log of one run in memory: the lookup events written by
// the nodes' own logger (slog, JSON lines) plus the labels the experiment adds.
type runLog struct {
	mu  sync.Mutex
	out bytes.Buffer
}

func newRunLog() *runLog { return &runLog{} }

// Write lets the runLog serve as the io.Writer of a slog handler; slog writes
// each record with a single Write call, so lines never interleave.
func (r *runLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.Write(p)
}

// logger returns the slog logger the nodes write their lookup events to. It
// drops what the analysis does not need (wall-clock time, level, message,
// elapsed time and addresses) and abbreviates IDs to 16 hex digits, which
// keeps the logs compact and free of wall-clock measurements.
func (r *runLog) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(r, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey, slog.LevelKey, slog.MessageKey, "elapsed_us", "peer_address":
				return slog.Attr{}
			case "node_id", "peer_id", "target":
				if v := a.Value.String(); len(v) > 16 {
					return slog.String(a.Key, v[:16])
				}
			}
			return a
		},
	}))
}

// meta writes a label record such as {"ev":"run","n":500}.
func (r *runLog) meta(kind string, fields map[string]any) {
	record := map[string]any{"ev": kind}
	for key, value := range fields {
		record[key] = value
	}
	line, _ := json.Marshal(record) // map of plain values: cannot fail
	_, _ = r.Write(append(line, '\n'))
}

func (r *runLog) finish() ([]byte, error) {
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
