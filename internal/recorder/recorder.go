// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

// Package recorder persists SamplePoints to disk as they arrive, rather than
// buffering everything in memory until the run ends. This is a direct
// consequence of the crash-safety discussion: if the machine locks up or
// reboots mid-test, only the last fsync interval's worth of data (at most)
// is lost, instead of the entire run.
package recorder

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"tempmonitor/internal/core"
)

const (
	samplesFileName = "samples.csv"
	metaFileName    = "meta.json"
	flushInterval   = 1 * time.Second
)

// Recorder writes one run's samples to <dir>/samples.csv and metadata to
// <dir>/meta.json. Create one per test run with New(), call Write() for
// each sample, and call Finish() when the run ends (normally or by user
// request) so meta.json gets a StoppedAt timestamp — its absence is exactly
// the signal DetectUnfinishedRuns uses to flag a crashed run.
type Recorder struct {
	mu        sync.Mutex
	dir       string
	file      *os.File
	buf       *bufio.Writer
	csvw      *csv.Writer
	tempCols  []string // fixed, sorted temp sensor labels — CSV columns must be stable
	meta      core.TestRunMeta
	closed    bool
	stopTimer chan struct{}
}

// New creates a new run directory (timestamped, under baseDir) and opens
// samples.csv + meta.json for writing. tempLabels should be the full set of
// temperature sensor labels this machine exposes (from Sampler.TempSensorCount
// / TempReader) — fixed up front so every row has the same column count even
// if a particular sample is missing a reading for one sensor.
func New(baseDir string, meta core.TestRunMeta, tempLabels []string) (*Recorder, error) {
	runDir := filepath.Join(baseDir, meta.RunID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating run dir: %w", err)
	}

	cols := append([]string(nil), tempLabels...)
	sort.Strings(cols)

	f, err := os.Create(filepath.Join(runDir, samplesFileName))
	if err != nil {
		return nil, fmt.Errorf("creating samples file: %w", err)
	}

	buf := bufio.NewWriter(f)
	w := csv.NewWriter(buf)

	header := append([]string{"timestamp", "cpu_usage_percent", "mem_used_mb", "mem_total_mb", "mem_usage_percent"}, cols...)
	if err := w.Write(header); err != nil {
		f.Close()
		return nil, fmt.Errorf("writing csv header: %w", err)
	}
	w.Flush()

	r := &Recorder{
		dir:       runDir,
		file:      f,
		buf:       buf,
		csvw:      w,
		tempCols:  cols,
		meta:      meta,
		stopTimer: make(chan struct{}),
	}

	if err := r.writeMeta(); err != nil {
		f.Close()
		return nil, err
	}

	go r.flushLoop()

	return r, nil
}

// flushLoop periodically flushes the buffered writer AND fsyncs the
// underlying file. This is the actual crash-safety mechanism: bufio.Flush
// alone only pushes bytes to the OS page cache, which is just as gone as
// unflushed memory if the machine loses power. Sync() forces it to disk.
func (r *Recorder) flushLoop() {
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			r.Flush()
		case <-r.stopTimer:
			return
		}
	}
}

// Write appends one sample as a CSV row. Safe to call from the same
// goroutine that's sampling; internally locked so it's also safe to call
// concurrently with Flush()/Finish() from other goroutines if needed.
func (r *Recorder) Write(p core.SamplePoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return fmt.Errorf("recorder is closed")
	}

	row := make([]string, 0, 5+len(r.tempCols))
	row = append(row,
		p.Timestamp.Format(time.RFC3339Nano),
		strconv.FormatFloat(p.CPUUsagePercent, 'f', 2, 64),
		strconv.FormatUint(p.MemUsedMB, 10),
		strconv.FormatUint(p.MemTotalMB, 10),
		strconv.FormatFloat(p.MemUsagePct, 'f', 2, 64),
	)
	for _, label := range r.tempCols {
		if v, ok := p.TempsC[label]; ok {
			row = append(row, strconv.FormatFloat(v, 'f', 2, 64))
		} else {
			row = append(row, "") // sensor missing this tick — empty, not zero
		}
	}

	return r.csvw.Write(row)
}

// Flush pushes buffered CSV data to the OS and fsyncs the file to disk.
// Called automatically every flushInterval, but exposed for callers that
// want an extra flush at a specific moment (e.g. right before Finish).
func (r *Recorder) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushLocked()
}

func (r *Recorder) flushLocked() error {
	if r.closed {
		return nil
	}
	r.csvw.Flush()
	if err := r.csvw.Error(); err != nil {
		return fmt.Errorf("flushing csv writer: %w", err)
	}
	if err := r.buf.Flush(); err != nil {
		return fmt.Errorf("flushing buffer: %w", err)
	}
	if err := r.file.Sync(); err != nil {
		return fmt.Errorf("fsyncing samples file: %w", err)
	}
	return nil
}

// Finish marks the run as ended (reason is a short string like "completed",
// "stopped_by_user", or "error"), does a final flush+fsync, writes the
// updated meta.json, and closes the underlying file. After Finish, Write
// will return an error.
func (r *Recorder) Finish(reason string) error {
	r.mu.Lock()
	r.meta.StoppedAt = time.Now()
	r.meta.EndReason = reason
	r.mu.Unlock()

	close(r.stopTimer)

	if err := r.writeMeta(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.flushLocked(); err != nil {
		return err
	}
	r.closed = true
	return r.file.Close()
}

func (r *Recorder) writeMeta() error {
	r.mu.Lock()
	meta := r.meta
	dir := r.dir
	r.mu.Unlock()

	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, metaFileName), b, 0o644); err != nil {
		return fmt.Errorf("writing meta.json: %w", err)
	}
	return nil
}

// UnfinishedRun describes a previous run directory whose meta.json has no
// StoppedAt, i.e. the program (or the machine) never got to call Finish().
// A GUI can surface this list on startup so the user isn't silently unaware
// that a prior test's tail data might be incomplete.
type UnfinishedRun struct {
	Dir  string
	Meta core.TestRunMeta
}

// DetectUnfinishedRuns scans baseDir for run subdirectories with a
// meta.json whose StoppedAt is still the zero time, meaning that run never
// finished cleanly — most likely because the program or the machine crashed
// mid-test.
func DetectUnfinishedRuns(baseDir string) ([]UnfinishedRun, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", baseDir, err)
	}

	var out []UnfinishedRun
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(baseDir, e.Name(), metaFileName)
		b, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var m core.TestRunMeta
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		if m.StoppedAt.IsZero() {
			out = append(out, UnfinishedRun{Dir: filepath.Join(baseDir, e.Name()), Meta: m})
		}
	}
	return out, nil
}
