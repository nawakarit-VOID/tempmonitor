// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

package sensors

import "testing"

// TestNewTempReader_NoHwmonIsNotAnError verifies the "container/sandbox with
// no /sys/class/hwmon at all" case (which is exactly this test environment)
// degrades gracefully to zero sensors instead of erroring out — important
// because the standalone GUI should still start and show "0 temp sensors
// found" rather than crash on machines/VMs without exposed hwmon.
func TestNewTempReader_NoHwmonIsNotAnError(t *testing.T) {
	r, err := NewTempReader()
	if err != nil {
		t.Fatalf("NewTempReader() error = %v", err)
	}
	// Whatever the count is on this machine (likely 0 in a container),
	// Read() must not panic and must return a usable, non-nil map.
	got := r.Read()
	if got == nil {
		t.Error("Read() returned nil map, want non-nil (possibly empty) map")
	}
	if r.Count() != len(got) {
		// Note: Count() reflects discovered sensors; a live Read() could in
		// theory return fewer entries if a sensor file vanished between
		// discovery and read, so this is a sanity check for the common case
		// (no removable sensors), not an invariant Read() must guarantee.
		t.Logf("Count()=%d differs from len(Read())=%d (only a problem if sensors weren't hot-removed)", r.Count(), len(got))
	}
}

func TestLabelFor_FallsBackToIndexWithoutLabelFile(t *testing.T) {
	// labelFor reads sibling *_label files from disk; when none exist (as in
	// this test, using a bogus chipDir) it should fall back to "chip:tempN"
	// rather than erroring.
	got := labelFor("/nonexistent/chip/dir", "/nonexistent/chip/dir/temp3_input", "testchip")
	want := "testchip:temp3"
	if got != want {
		t.Errorf("labelFor() = %q, want %q", got, want)
	}
}

func TestSampler_TempLabelsDoesNotAffectCPUTracker(t *testing.T) {
	s, err := NewSampler()
	if err != nil {
		t.Fatalf("NewSampler() error = %v", err)
	}

	// Calling TempLabels() any number of times must not consume the CPU
	// tracker's "first sample" warm-up, unlike Sample() which does.
	_ = s.TempLabels()
	_ = s.TempLabels()
	_ = s.TempLabels()

	first, err := s.Sample()
	if err != nil {
		t.Fatalf("Sample() error = %v", err)
	}
	if first.CPUUsagePercent != 0 {
		t.Errorf("first Sample() after TempLabels() calls should still report 0%% CPU (no prior delta), got %v", first.CPUUsagePercent)
	}

	labels := s.TempLabels()
	if labels == nil {
		t.Error("TempLabels() returned nil, want non-nil (possibly empty) slice")
	}
	// Labels should be sorted.
	for i := 1; i < len(labels); i++ {
		if labels[i-1] > labels[i] {
			t.Errorf("TempLabels() not sorted: %v", labels)
			break
		}
	}
}
