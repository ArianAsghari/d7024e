package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunQuickWritesAllLogs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "results") // created on demand
	if err := run([]string{"-quick", "-seeds", "1", "-workers", "1", "-out", dir}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scalability.jsonl", "reliability.jsonl", "churn.jsonl"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() == 0 {
			t.Fatalf("%s missing or empty: %v", name, err)
		}
	}
}

func TestRunSingleExperiment(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"-quick", "-seeds", "1", "-workers", "1", "-exp", "scalability", "-out", dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reliability.jsonl")); err == nil {
		t.Fatal("reliability ran although only scalability was requested")
	}
	if _, err := os.Stat(filepath.Join(dir, "scalability.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsBadArguments(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"-exp", "bogus", "-out", dir}); err == nil {
		t.Fatal("unknown experiment accepted")
	}
	if err := run([]string{"-no-such-flag"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	for _, seeds := range []string{"0", "-1"} {
		if err := run([]string{"-seeds", seeds, "-out", dir}); err == nil {
			t.Fatalf("accepted non-positive seed count %s", seeds)
		}
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-quick", "-out", filepath.Join(file, "sub")}); err == nil {
		t.Fatal("an output directory below a regular file was accepted")
	}
}

func TestWriteLogReportsErrors(t *testing.T) {
	if err := writeLog(filepath.Join(t.TempDir(), "missing", "x.jsonl"), func(*os.File) error { return nil }); err == nil {
		t.Fatal("creating a file in a missing directory should fail")
	}
	path := filepath.Join(t.TempDir(), "x.jsonl")
	if err := writeLog(path, func(*os.File) error { return os.ErrInvalid }); err == nil {
		t.Fatal("writer errors must be returned")
	}
}
