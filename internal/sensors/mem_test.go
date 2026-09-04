// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

package sensors

import "testing"

func TestReadMemUsage(t *testing.T) {
	mem, err := ReadMemUsage()
	if err != nil {
		t.Fatalf("ReadMemUsage() error = %v", err)
	}
	if mem.TotalMB == 0 {
		t.Error("expected non-zero TotalMB on a real /proc/meminfo")
	}
	if mem.UsagePct < 0 || mem.UsagePct > 100 {
		t.Errorf("UsagePct out of range: %v", mem.UsagePct)
	}
}

func TestParseMeminfoKB(t *testing.T) {
	tests := []struct {
		line string
		want uint64
	}{
		{"MemTotal:       16384000 kB", 16384000},
		{"MemAvailable:    8192000 kB", 8192000},
		{"malformed line", 0},
		{"MemTotal:", 0},
	}
	for _, tt := range tests {
		if got := parseMeminfoKB(tt.line); got != tt.want {
			t.Errorf("parseMeminfoKB(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}
