// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

// Package core defines the shared data types used across sensors, stress,
// recorder and gui packages. Keeping these in one place means the standalone
// mode and the future agent/monitor split can reuse the exact same schema.
package core

import "time"

// SamplePoint is one snapshot of system telemetry taken at a point in time.
// Every field except Timestamp is optional in the sense that a given machine
// may not expose all sensors (e.g. no discrete GPU) — missing values are
// simply absent from the maps rather than zero, so downstream code (CSV
// writer, GUI) can tell "not measured" apart from "measured as zero".
type SamplePoint struct {
	Timestamp time.Time `json:"ts"`

	// TempsC maps a human-readable sensor label (e.g. "coretemp:Package id 0",
	// "k10temp:Tctl") to a temperature reading in Celsius.
	TempsC map[string]float64 `json:"temps_c"`

	// CPUUsagePercent is overall CPU utilization 0-100, computed as a delta
	// between two /proc/stat reads.
	CPUUsagePercent float64 `json:"cpu_usage_percent"`

	// PerCoreUsagePercent optionally breaks CPU usage down by logical core.
	PerCoreUsagePercent []float64 `json:"per_core_usage_percent,omitempty"`

	MemUsedMB   uint64  `json:"mem_used_mb"`
	MemTotalMB  uint64  `json:"mem_total_mb"`
	MemUsagePct float64 `json:"mem_usage_percent"`
}

// TestRunMeta describes one stress-test run, written once at the start and
// updated/finalized at the end. Persisting this alongside the samples lets a
// later viewer distinguish a clean finish from a run that never got a
// StoppedAt (i.e. the program — or the machine — died mid-test).
type TestRunMeta struct {
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
	// StoppedAt is the zero time.Time{} until the run finishes normally.
	// Callers should check StoppedAt.IsZero() rather than relying on JSON
	// omission to detect an unfinished/crashed run.
	StoppedAt  time.Time `json:"stopped_at"`
	StressCmd  string    `json:"stress_cmd"`
	StressArgs []string  `json:"stress_args"`
	EndReason  string    `json:"end_reason,omitempty"` // "completed", "stopped_by_user", "crashed", ""

	// SampleIntervalMs records how frequently telemetry was logged during
	// this run (user-configurable in the GUI), so samples.csv can be
	// correctly interpreted later without guessing the sample rate from
	// timestamp deltas.
	SampleIntervalMs int64 `json:"sample_interval_ms"`
}
