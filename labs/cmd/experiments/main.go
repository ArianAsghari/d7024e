// Command experiments runs the lab's two mandatory experiments on the
// simulated network and writes their event logs (JSON lines) to a directory.
// The logs are analysed by scripts/analyze.py.
//
//	go run ./cmd/experiments -out experiments/results
//	go run ./cmd/experiments -quick            # tiny configuration for a smoke test
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"d7024e/experiment"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "experiments:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("experiments", flag.ContinueOnError)
	which := flags.String("exp", "all", "experiment to run: scalability, reliability or all")
	out := flags.String("out", "experiments/results", "output directory for the .jsonl logs")
	seeds := flags.Int("seeds", 10, "number of RNG seeds (1..seeds) per configuration")
	quick := flags.Bool("quick", false, "use a tiny configuration (smoke test)")
	workers := flags.Int("workers", 0, "parallel runs (0 = number of CPUs)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *which != "all" && *which != "scalability" && *which != "reliability" {
		return fmt.Errorf("unknown experiment %q", *which)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	seedList := make([]int64, *seeds)
	for i := range seedList {
		seedList[i] = int64(i + 1)
	}

	scalability := experiment.ScalabilityConfig{
		Sizes: []int{16, 32, 64, 128, 256, 512, 1024, 2048}, Seeds: seedList,
		NodeLookups: 50, ValueLookups: 50, Workers: *workers,
	}
	reliability := experiment.ReliabilityConfig{
		N: 500, Seeds: seedList, Values: 20, Lookups: 100, Workers: *workers,
		Losses: []float64{0, 0.05, 0.1, 0.2, 0.3, 0.4, 0.5},
		Variants: []experiment.Variant{
			{Name: "retries0", Retries: 0},
			{Name: "retries1", Retries: 1},
			{Name: "retries2", Retries: 2}, // the real transport's default
		},
	}
	if *quick {
		scalability.Sizes, scalability.NodeLookups, scalability.ValueLookups = []int{16, 64}, 5, 5
		reliability.N, reliability.Values, reliability.Lookups = 60, 3, 10
		reliability.Losses = []float64{0, 0.3}
		reliability.Variants = []experiment.Variant{{Name: "retries0", Retries: 0}, {Name: "retries2", Retries: 2}}
	}

	if *which == "all" || *which == "scalability" {
		if err := writeLog(filepath.Join(*out, "scalability.jsonl"), func(f *os.File) error {
			return experiment.RunScalability(f, scalability)
		}); err != nil {
			return err
		}
	}
	if *which == "all" || *which == "reliability" {
		if err := writeLog(filepath.Join(*out, "reliability.jsonl"), func(f *os.File) error {
			return experiment.RunReliability(f, reliability)
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeLog(path string, write func(*os.File) error) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(file); err != nil {
		file.Close()
		return err
	}
	fmt.Println("wrote", path)
	return file.Close()
}
