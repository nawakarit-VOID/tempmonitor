// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

package recorder

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tempmonitor/internal/core"
)

func TestRecorder_WriteAndFinish(t *testing.T) {
	dir := t.TempDir()
	meta := core.TestRunMeta{RunID: "run1", StartedAt: time.Now(), StressCmd: "stress-ng"}
	labels := []string{"coretemp:Package id 0", "coretemp:Core 0"}

	r, err := New(dir, meta, labels)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	p1 := core.SamplePoint{
		Timestamp:       time.Now(),
		TempsC:          map[string]float64{"coretemp:Package id 0": 55.5, "coretemp:Core 0": 50.1},
		CPUUsagePercent: 12.3,
		MemUsedMB:       1000,
		MemTotalMB:      8000,
		MemUsagePct:     12.5,
	}
	// Second point deliberately missing one sensor reading, to verify the
	// CSV writer emits an empty field instead of misaligning columns.
	p2 := core.SamplePoint{
		Timestamp:       time.Now(),
		TempsC:          map[string]float64{"coretemp:Package id 0": 60.0},
		CPUUsagePercent: 99.9,
		MemUsedMB:       2000,
		MemTotalMB:      8000,
		MemUsagePct:     25.0,
	}

	if err := r.Write(p1); err != nil {
		t.Fatalf("Write(p1) error = %v", err)
	}
	if err := r.Write(p2); err != nil {
		t.Fatalf("Write(p2) error = %v", err)
	}

	if err := r.Finish("completed"); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	// Verify CSV contents.
	runDir := filepath.Join(dir, "run1")
	f, err := os.Open(filepath.Join(runDir, samplesFileName))
	if err != nil {
		t.Fatalf("opening samples.csv: %v", err)
	}
	defer f.Close()

	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("reading csv: %v", err)
	}
	if len(rows) != 3 { // header + 2 samples
		t.Fatalf("got %d rows, want 3 (header + 2 samples)", len(rows))
	}

	wantHeader := []string{"timestamp", "cpu_usage_percent", "mem_used_mb", "mem_total_mb", "mem_usage_percent", "coretemp:Core 0", "coretemp:Package id 0"}
	for i, col := range wantHeader {
		if rows[0][i] != col {
			t.Errorf("header[%d] = %q, want %q", i, rows[0][i], col)
		}
	}

	// p2's row should have an empty "coretemp:Core 0" field (index 5) since
	// that sensor was missing from p2, while "coretemp:Package id 0" (index 6)
	// should be populated.
	if rows[2][5] != "" {
		t.Errorf("expected empty field for missing sensor, got %q", rows[2][5])
	}
	if rows[2][6] != "60.00" {
		t.Errorf("rows[2][6] = %q, want %q", rows[2][6], "60.00")
	}

	// Verify meta.json reflects a clean finish.
	metaPath := filepath.Join(runDir, metaFileName)
	b, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("reading meta.json: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("meta.json is empty")
	}
}

func TestDetectUnfinishedRuns(t *testing.T) {
	dir := t.TempDir()

	// Simulate a crashed run: create a Recorder, write a sample, but never
	// call Finish() — meta.json will be left with a zero StoppedAt, exactly
	// as if the process had died mid-test.
	crashedMeta := core.TestRunMeta{RunID: "crashed-run", StartedAt: time.Now()}
	crashed, err := New(dir, crashedMeta, nil)
	if err != nil {
		t.Fatalf("New(crashed) error = %v", err)
	}
	if err := crashed.Write(core.SamplePoint{Timestamp: time.Now(), CPUUsagePercent: 50}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := crashed.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	// Deliberately no Finish() call here — this IS the crash scenario.

	// A second, normal run that finishes cleanly should NOT show up as
	// unfinished.
	cleanMeta := core.TestRunMeta{RunID: "clean-run", StartedAt: time.Now()}
	clean, err := New(dir, cleanMeta, nil)
	if err != nil {
		t.Fatalf("New(clean) error = %v", err)
	}
	if err := clean.Finish("completed"); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	unfinished, err := DetectUnfinishedRuns(dir)
	if err != nil {
		t.Fatalf("DetectUnfinishedRuns() error = %v", err)
	}
	if len(unfinished) != 1 {
		t.Fatalf("got %d unfinished runs, want 1", len(unfinished))
	}
	if unfinished[0].Meta.RunID != "crashed-run" {
		t.Errorf("unfinished run ID = %q, want %q", unfinished[0].Meta.RunID, "crashed-run")
	}
}

func TestDetectUnfinishedRuns_EmptyBaseDir(t *testing.T) {
	// A base dir that doesn't exist yet (first ever run) should return an
	// empty result, not an error.
	unfinished, err := DetectUnfinishedRuns(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("DetectUnfinishedRuns() error = %v", err)
	}
	if len(unfinished) != 0 {
		t.Errorf("got %d unfinished runs, want 0", len(unfinished))
	}
}
