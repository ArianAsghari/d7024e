// Command experiments runs the lab's evaluation experiments on the
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
	which := flags.String("exp", "all", "experiment to run: scalability, reliability, churn or all")
	out := flags.String("out", "experiments/results", "output directory for the .jsonl logs")
	seeds := flags.Int("seeds", 10, "number of RNG seeds (1..seeds) per configuration")
	quick := flags.Bool("quick", false, "use a tiny configuration (smoke test)")
	workers := flags.Int("workers", 0, "parallel runs (0 = number of CPUs)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *which != "all" && *which != "scalability" && *which != "reliability" && *which != "churn" {
		return fmt.Errorf("unknown experiment %q", *which)
	}
	if *seeds < 1 {
		return fmt.Errorf("seeds must be positive")
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
			{Name: "alpha3-retries2", Alpha: 3, Retries: 2}, // defaults of the real node
			{Name: "alpha3-retries0", Alpha: 3, Retries: 0},
			{Name: "alpha1-retries2", Alpha: 1, Retries: 2},
			{Name: "alpha1-retries0", Alpha: 1, Retries: 0},
		},
	}
	churn := experiment.ChurnConfig{
		N: 1000, Seeds: seedList, Values: 50, NodeLookups: 100, Workers: *workers,
		Fractions: []float64{0, 0.1, 0.3, 0.5, 0.8, 0.9},
	}
	if *quick {
		scalability.Sizes, scalability.NodeLookups, scalability.ValueLookups = []int{16, 64}, 5, 5
		reliability.N, reliability.Values, reliability.Lookups = 60, 3, 10
		reliability.Losses = []float64{0, 0.3}
		reliability.Variants = reliability.Variants[:2]
		churn.N, churn.Values, churn.NodeLookups = 60, 3, 5
		churn.Fractions = []float64{0, 0.3}
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
	if *which == "all" || *which == "churn" {
		if err := writeLog(filepath.Join(*out, "churn.jsonl"), func(f *os.File) error {
			return experiment.RunChurn(f, churn)
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
