// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tempmonitor/internal/core"
)

// writeTestRun writes a minimal samples.csv + meta.json into dir, mimicking
// what internal/recorder.New/Write/Finish produce, so report.Generate can
// be exercised without depending on the recorder package directly.
func writeTestRun(t *testing.T, dir string, csvBody string, meta core.TestRunMeta) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, samplesFileName), []byte(csvBody), 0o644); err != nil {
		t.Fatalf("writing samples.csv: %v", err)
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshalling meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, metaFileName), b, 0o644); err != nil {
		t.Fatalf("writing meta.json: %v", err)
	}
}

func TestGenerate_BasicRun(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	csvBody := "timestamp,cpu_usage_percent,mem_used_mb,mem_total_mb,mem_usage_percent,coretemp:Core 0,coretemp:Core 1\n" +
		t0.Format(time.RFC3339Nano) + ",10.00,1000,8000,12.50,45.00,46.00\n" +
		t0.Add(1*time.Second).Format(time.RFC3339Nano) + ",95.00,2000,8000,25.00,70.00,72.00\n" +
		t0.Add(2*time.Second).Format(time.RFC3339Nano) + ",90.00,2100,8000,26.25,,71.00\n" // one missing temp value

	meta := core.TestRunMeta{
		RunID:            "20260101-120000",
		StartedAt:        t0,
		StoppedAt:        t0.Add(2 * time.Second),
		EndReason:        "completed",
		SampleIntervalMs: 1000,
	}
	writeTestRun(t, dir, csvBody, meta)

	path, err := Generate(dir)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if filepath.Base(path) != reportFileName {
		t.Errorf("Generate() path = %q, want basename %q", path, reportFileName)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated report: %v", err)
	}
	html := string(out)

	// Sanity: key metadata and data values should appear somewhere in the
	// output (either in the header text or the embedded JSON).
	for _, want := range []string{
		"20260101-120000",
		"completed",
		"coretemp:Core 0",
		"coretemp:Core 1",
		"45",   // first Core 0 reading
		"null", // the missing Core 0 reading on the third row
	} {
		if !strings.Contains(html, want) {
			t.Errorf("report HTML missing expected substring %q", want)
		}
	}

	// The doc should be well-formed enough to at least contain matching
	// <html>/<script> tags and no unescaped "</script" from data leaking out.
	if !strings.Contains(html, "<script>") || !strings.Contains(html, "</script>") {
		t.Error("report HTML missing <script> block")
	}
}

func TestGenerate_NoSensors(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now().UTC()
	csvBody := "timestamp,cpu_usage_percent,mem_used_mb,mem_total_mb,mem_usage_percent\n" +
		t0.Format(time.RFC3339Nano) + ",5.00,500,8000,6.25\n"

	meta := core.TestRunMeta{RunID: "no-sensors-run", StartedAt: t0, StoppedAt: t0, EndReason: "completed"}
	writeTestRun(t, dir, csvBody, meta)

	path, err := Generate(dir)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated report: %v", err)
	}
	if !strings.Contains(string(out), `"temp_labels":[]`) && !strings.Contains(string(out), `"temp_labels": []`) {
		t.Error("expected empty temp_labels in embedded JSON for a run with no sensor columns")
	}
}

func TestGenerate_EmptyDataStillProducesReport(t *testing.T) {
	// A run that was started but has zero completed samples (e.g. stopped
	// within the same instant it began) should still produce a valid
	// report, not an error — the report itself shows a "no data" message.
	dir := t.TempDir()
	csvBody := "timestamp,cpu_usage_percent,mem_used_mb,mem_total_mb,mem_usage_percent,coretemp:Core 0\n"
	meta := core.TestRunMeta{RunID: "empty-run", StartedAt: time.Now(), StoppedAt: time.Now()}
	writeTestRun(t, dir, csvBody, meta)

	path, err := Generate(dir)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated report: %v", err)
	}
	if !strings.Contains(string(out), `"sample_count":0`) {
		t.Error("expected sample_count of 0 in embedded JSON for an empty run")
	}
}

func TestGenerate_CrashedRunHasBlankStoppedAt(t *testing.T) {
	// Mirrors the recorder's crash-detection convention: a zero StoppedAt
	// means the run never finished cleanly. The report should render that
	// as a dash/blank, not a misleading zero-time string like "0001-01-01".
	dir := t.TempDir()
	t0 := time.Now().UTC()
	csvBody := "timestamp,cpu_usage_percent,mem_used_mb,mem_total_mb,mem_usage_percent,coretemp:Core 0\n" +
		t0.Format(time.RFC3339Nano) + ",50.00,1000,8000,12.50,60.00\n"
	meta := core.TestRunMeta{RunID: "crashed-run", StartedAt: t0} // StoppedAt left zero
	writeTestRun(t, dir, csvBody, meta)

	path, err := Generate(dir)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated report: %v", err)
	}
	if strings.Contains(string(out), "0001-01-01") {
		t.Error("report should not render Go's zero-time string for an unfinished/crashed run")
	}
}

func TestGenerate_MissingFilesError(t *testing.T) {
	dir := t.TempDir() // no samples.csv or meta.json written
	if _, err := Generate(dir); err == nil {
		t.Error("Generate() on a directory with no run files should return an error, not silently succeed")
	}
}

func TestParseFloatPtr(t *testing.T) {
	tests := []struct {
		in      string
		wantNil bool
		wantVal float64
	}{
		{"", true, 0},
		{"garbage", true, 0},
		{"12.5", false, 12.5},
		{"0", false, 0},
		{"-3.2", false, -3.2},
	}
	for _, tt := range tests {
		got := parseFloatPtr(tt.in)
		if tt.wantNil {
			if got != nil {
				t.Errorf("parseFloatPtr(%q) = %v, want nil", tt.in, *got)
			}
			continue
		}
		if got == nil {
			t.Errorf("parseFloatPtr(%q) = nil, want %v", tt.in, tt.wantVal)
			continue
		}
		if *got != tt.wantVal {
			t.Errorf("parseFloatPtr(%q) = %v, want %v", tt.in, *got, tt.wantVal)
		}
	}
}

func TestLatestRunDir(t *testing.T) {
	base := t.TempDir()
	for _, id := range []string{"20260101-100000", "20260103-090000", "20260102-150000"} {
		if err := os.MkdirAll(filepath.Join(base, id), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", id, err)
		}
	}

	got, err := LatestRunDir(base)
	if err != nil {
		t.Fatalf("LatestRunDir() error = %v", err)
	}
	want := filepath.Join(base, "20260103-090000")
	if got != want {
		t.Errorf("LatestRunDir() = %q, want %q", got, want)
	}
}

func TestLatestRunDir_EmptyBaseDir(t *testing.T) {
	got, err := LatestRunDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("LatestRunDir() error = %v", err)
	}
	if got != "" {
		t.Errorf("LatestRunDir() = %q, want empty string", got)
	}
}

func TestEscapeForInlineScript(t *testing.T) {
	in := []byte(`{"a":"</script>alert(1)</script>"}`)
	got := escapeForInlineScript(in)
	if strings.Contains(got, "</script>") {
		t.Errorf("escapeForInlineScript() left a literal </script> in output: %q", got)
	}
}
