// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

package sensors

import "testing"

func TestPercentDelta(t *testing.T) {
	tests := []struct {
		name string
		prev cpuTimes
		cur  cpuTimes
		want float64
	}{
		{
			name: "fully idle",
			prev: cpuTimes{idle: 100},
			cur:  cpuTimes{idle: 200},
			want: 0,
		},
		{
			name: "fully busy",
			prev: cpuTimes{user: 100},
			cur:  cpuTimes{user: 200},
			want: 100,
		},
		{
			name: "half busy half idle",
			prev: cpuTimes{user: 0, idle: 0},
			cur:  cpuTimes{user: 50, idle: 50},
			want: 50,
		},
		{
			name: "no time elapsed",
			prev: cpuTimes{user: 100, idle: 50},
			cur:  cpuTimes{user: 100, idle: 50},
			want: 0,
		},
		{
			name: "iowait counted as not-active like idle",
			prev: cpuTimes{},
			cur:  cpuTimes{iowait: 100},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := percentDelta(tt.prev, tt.cur)
			if got != tt.want {
				t.Errorf("percentDelta() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseCPUTimes(t *testing.T) {
	// Real /proc/stat line format (minus the "cpu" prefix field):
	// user nice system idle iowait irq softirq steal guest guest_nice
	fields := []string{"4", "0", "119", "143", "2", "0", "1", "0", "0", "0"}
	ct, err := parseCPUTimes(fields)
	if err != nil {
		t.Fatalf("parseCPUTimes() error = %v", err)
	}
	want := cpuTimes{user: 4, nice: 0, system: 119, idle: 143, iowait: 2, irq: 0, softirq: 1, steal: 0}
	if ct != want {
		t.Errorf("parseCPUTimes() = %+v, want %+v", ct, want)
	}
}

func TestCPUUsageTracker_FirstReadIsZero(t *testing.T) {
	tr := NewCPUUsageTracker()
	usage, err := tr.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if usage.OverallPercent != 0 {
		t.Errorf("first Read() should report 0%% (no prior sample to diff against), got %v", usage.OverallPercent)
	}
}

func TestCPUUsageTracker_SecondReadHasData(t *testing.T) {
	tr := NewCPUUsageTracker()
	if _, err := tr.Read(); err != nil {
		t.Fatalf("first Read() error = %v", err)
	}
	usage, err := tr.Read()
	if err != nil {
		t.Fatalf("second Read() error = %v", err)
	}
	if usage.OverallPercent < 0 || usage.OverallPercent > 100 {
		t.Errorf("OverallPercent out of range: %v", usage.OverallPercent)
	}
	if len(usage.PerCorePercent) == 0 {
		t.Error("expected at least one per-core reading on this machine")
	}
}
