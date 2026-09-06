// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

// Package report turns one run's samples.csv + meta.json into a single,
// self-contained HTML file with interactive-ish line charts (CPU%, memory%,
// and every discovered temperature sensor overlaid), so the data can be
// opened directly in a browser without needing any other tool.
//
// Deliberately does NOT pull in a charting library from a CDN (Chart.js,
// D3, etc.): the generated report embeds its own small vanilla-JS canvas
// renderer, so it opens correctly even on a machine with no internet
// access at viewing time — which matters here since the whole point is
// often "look at this after a stress test that might have been offline or
// on another machine."
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"tempmonitor/internal/core"
)

const (
	samplesFileName = "samples.csv"
	metaFileName    = "meta.json"
	reportFileName  = "report.html"
)

// reportData is the JSON payload embedded into the generated HTML. Missing
// values are nil (marshals to JSON `null`) rather than 0, so the chart
// renderer can leave a visible gap instead of drawing a misleading dip to
// zero for a sensor that failed to read on a given tick.
type reportData struct {
	RunID            string       `json:"run_id"`
	StartedAt        string       `json:"started_at"`
	StoppedAt        string       `json:"stopped_at"`
	EndReason        string       `json:"end_reason"`
	SampleIntervalMs int64        `json:"sample_interval_ms"`
	SampleCount      int          `json:"sample_count"`
	ElapsedSec       []float64    `json:"elapsed_sec"`
	CPUUsagePercent  []*float64   `json:"cpu_usage_percent"`
	MemUsagePercent  []*float64   `json:"mem_usage_percent"`
	TempLabels       []string     `json:"temp_labels"`
	TempSeries       [][]*float64 `json:"temp_series"` // outer index matches TempLabels order
}

// Generate reads <runDir>/samples.csv and <runDir>/meta.json, builds
// <runDir>/report.html, and returns its full path. Safe to call on a run
// that's still being actively recorded (monitoring not yet stopped) — it
// just reports on whatever has been fsynced to samples.csv so far, which
// may lag "now" by up to the recorder's flush interval (~1s).
func Generate(runDir string) (string, error) {
	data, err := loadReportData(runDir)
	if err != nil {
		return "", err
	}

	html, err := renderHTML(data)
	if err != nil {
		return "", fmt.Errorf("rendering report HTML: %w", err)
	}

	outPath := filepath.Join(runDir, reportFileName)
	if err := os.WriteFile(outPath, []byte(html), 0o644); err != nil {
		return "", fmt.Errorf("writing report.html: %w", err)
	}
	return outPath, nil
}

func loadReportData(runDir string) (*reportData, error) {
	rows, header, err := readSamplesCSV(filepath.Join(runDir, samplesFileName))
	if err != nil {
		return nil, err
	}

	meta, err := readMeta(filepath.Join(runDir, metaFileName))
	if err != nil {
		return nil, err
	}

	// Fixed leading columns written by recorder.New — see internal/recorder.
	// Everything after them is a temperature sensor column, in CSV order.
	const (
		colTimestamp = 0
		colCPUUsage  = 1
		// colMemUsedMB   = 2 (not currently surfaced in the report)
		// colMemTotalMB  = 3 (not currently surfaced in the report)
		colMemUsagePct = 4
		firstTempCol   = 5
	)

	if len(header) < firstTempCol {
		return nil, fmt.Errorf("samples.csv has %d columns, expected at least %d (malformed or truncated file)", len(header), firstTempCol)
	}
	tempLabels := make([]string, len(header)-firstTempCol)
	copy(tempLabels, header[firstTempCol:])

	data := &reportData{
		RunID:            meta.RunID,
		StartedAt:        meta.StartedAt.Format(time.RFC3339),
		EndReason:        meta.EndReason,
		SampleIntervalMs: meta.SampleIntervalMs,
		TempLabels:       tempLabels,
		TempSeries:       make([][]*float64, len(tempLabels)),
	}
	if !meta.StoppedAt.IsZero() {
		data.StoppedAt = meta.StoppedAt.Format(time.RFC3339)
	} else {
		data.StoppedAt = "" // still running, or crashed — left blank rather than a zero-time string
	}

	var firstTS time.Time
	for i, row := range rows {
		ts, err := time.Parse(time.RFC3339Nano, row[colTimestamp])
		if err != nil {
			continue // skip a corrupted row rather than aborting the whole report
		}
		if i == 0 || firstTS.IsZero() {
			firstTS = ts
		}

		data.ElapsedSec = append(data.ElapsedSec, ts.Sub(firstTS).Seconds())
		data.CPUUsagePercent = append(data.CPUUsagePercent, parseFloatPtr(row[colCPUUsage]))
		data.MemUsagePercent = append(data.MemUsagePercent, parseFloatPtr(row[colMemUsagePct]))

		for ti := range tempLabels {
			col := firstTempCol + ti
			var v *float64
			if col < len(row) {
				v = parseFloatPtr(row[col])
			}
			data.TempSeries[ti] = append(data.TempSeries[ti], v)
		}
	}
	data.SampleCount = len(data.ElapsedSec)

	return data, nil
}

// parseFloatPtr parses a CSV cell as a float64, returning nil for an empty
// cell (recorder writes "" for a sensor that was missing on a given tick —
// see internal/recorder) or one that fails to parse, rather than silently
// defaulting to 0.
func parseFloatPtr(s string) *float64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func readSamplesCSV(path string) (rows [][]string, header []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	// stress-ng/agent runs can be interrupted while a row is half-written;
	// FieldsPerRecord=-1 tells encoding/csv not to reject the whole file
	// over one ragged trailing line — readSamplesCSV below still drops any
	// row it can't make sense of.
	r.FieldsPerRecord = -1

	all, err := r.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(all) == 0 {
		return nil, nil, fmt.Errorf("%s is empty (no header row)", path)
	}

	header = all[0]
	for _, row := range all[1:] {
		if len(row) < len(header) {
			continue // ragged/truncated row (e.g. crash mid-write) — skip it
		}
		rows = append(rows, row)
	}
	return rows, header, nil
}

func readMeta(path string) (core.TestRunMeta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return core.TestRunMeta{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var m core.TestRunMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return core.TestRunMeta{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return m, nil
}

// LatestRunDir returns the most recently started run directory under
// baseDir, identified by sorting run ID subdirectory names — run IDs are
// formatted as "20060102-150405" (see gui.onStartMonitor), which sorts
// lexically in the same order as chronologically. Returns "" with no error
// if baseDir has no run subdirectories yet.
func LatestRunDir(baseDir string) (string, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", baseDir, err)
	}

	var runIDs []string
	for _, e := range entries {
		if e.IsDir() {
			runIDs = append(runIDs, e.Name())
		}
	}
	if len(runIDs) == 0 {
		return "", nil
	}
	sort.Strings(runIDs)
	return filepath.Join(baseDir, runIDs[len(runIDs)-1]), nil
}

// escapeForInlineScript prevents embedded JSON from prematurely closing the
// surrounding <script> tag if a string value ever happened to contain
// "</script" (extremely unlikely for our data — sensor labels and run IDs —
// but cheap to guard against regardless).
func escapeForInlineScript(jsonBytes []byte) string {
	return strings.ReplaceAll(string(jsonBytes), "</", "<\\/")
}
