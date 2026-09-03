// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.
// Package sensors reads temperature, CPU and memory telemetry straight from
// the Linux kernel's sysfs/procfs interfaces. Deliberately avoids shelling
// out to external tools (sensors, nvidia-smi, etc.) for the hot path so that
// polling at high frequency (e.g. every 100ms) stays cheap — each read is a
// handful of small file opens, no process spawn.
package sensors

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// tempSensor is one discovered temperature input file, resolved once at
// startup so the hot read loop never has to glob the filesystem again.
type tempSensor struct {
	label string // e.g. "coretemp:Package id 0"
	path  string // e.g. /sys/class/hwmon/hwmon2/temp1_input
}

// TempReader reads CPU/board temperatures from hwmon. Not safe for
// concurrent use from multiple goroutines without external locking, but a
// single reader is expected to live in one polling goroutine.
type TempReader struct {
	sensors []tempSensor
}

// NewTempReader discovers all available hwmon temperature inputs under
// /sys/class/hwmon and caches their paths. Call this once at startup, then
// call Read() repeatedly on the returned reader.
//
// It is not an error for zero sensors to be found (e.g. running in a
// container without hwmon exposed) — Read() will just return an empty map,
// and callers should surface that to the user rather than crash.
func NewTempReader() (*TempReader, error) {
	const hwmonRoot = "/sys/class/hwmon"

	entries, err := os.ReadDir(hwmonRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return &TempReader{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", hwmonRoot, err)
	}

	var found []tempSensor
	for _, e := range entries {
		chipDir := filepath.Join(hwmonRoot, e.Name())
		chipName := readTrimmed(filepath.Join(chipDir, "name"))
		if chipName == "" {
			chipName = e.Name()
		}

		inputs, err := filepath.Glob(filepath.Join(chipDir, "temp*_input"))
		if err != nil {
			continue
		}
		for _, inputPath := range inputs {
			label := labelFor(chipDir, inputPath, chipName)
			found = append(found, tempSensor{label: label, path: inputPath})
		}
	}

	// Stable order makes CSV columns and GUI ordering deterministic across runs.
	sort.Slice(found, func(i, j int) bool { return found[i].label < found[j].label })

	return &TempReader{sensors: found}, nil
}

// labelFor builds a human-readable label such as "coretemp:Package id 0" by
// pairing tempN_input with its sibling tempN_label, falling back to the raw
// index (e.g. "coretemp:temp1") when no label file exists.
func labelFor(chipDir, inputPath, chipName string) string {
	base := filepath.Base(inputPath)          // "temp1_input"
	idx := strings.TrimSuffix(base, "_input") // "temp1"
	labelPath := filepath.Join(chipDir, idx+"_label")

	if l := readTrimmed(labelPath); l != "" {
		return chipName + ":" + l
	}
	return chipName + ":" + idx
}

// readTrimmed reads a small sysfs file and returns its trimmed contents, or
// "" if the file doesn't exist or can't be read. Missing optional files
// (like temp1_label) are common and not an error condition.
func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Read takes one snapshot of all discovered temperature sensors, in degrees
// Celsius. hwmon reports temps in millidegrees C as plain integers, so each
// read is just: open, read a few bytes, parse int, divide by 1000.
func (r *TempReader) Read() map[string]float64 {
	out := make(map[string]float64, len(r.sensors))
	for _, s := range r.sensors {
		raw, err := os.ReadFile(s.path)
		if err != nil {
			continue // sensor may have been unplugged/gone; skip, don't crash the loop
		}
		milliC, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			continue
		}
		out[s.label] = float64(milliC) / 1000.0
	}
	return out
}

// Count returns how many temperature sensors were discovered, useful for a
// GUI to warn the user ("no temperature sensors found — try running as
// root, or install lm-sensors' kernel modules") when it's zero.
func (r *TempReader) Count() int {
	return len(r.sensors)
}
