package sensors

import (
	"time"

	"tempmonitor/internal/core"
)

// Sampler ties together temperature, CPU and memory readers into a single
// Sample() call that produces one core.SamplePoint. This is the one type
// that both the standalone mode and a future network-facing agent mode
// would use identically — only what happens to the resulting SamplePoint
// (write to disk locally vs. push over a websocket) would differ.
type Sampler struct {
	temps *TempReader
	cpu   *CPUUsageTracker
}

// NewSampler discovers available sensors and prepares the CPU usage
// tracker. Safe to call once at program startup.
func NewSampler() (*Sampler, error) {
	temps, err := NewTempReader()
	if err != nil {
		return nil, err
	}
	return &Sampler{
		temps: temps,
		cpu:   NewCPUUsageTracker(),
	}, nil
}

// TempSensorCount reports how many temperature sensors were discovered, so
// callers can warn the user up front if it's zero.
func (s *Sampler) TempSensorCount() int {
	return s.temps.Count()
}

// Sample takes one snapshot of all available telemetry. Errors reading
// memory or CPU stats are non-fatal — the corresponding fields are left at
// their zero value and the error is returned so the caller can log/display
// it, but the sample as a whole is still usable (e.g. temps may still be
// valid even if /proc/meminfo hiccuped).
func (s *Sampler) Sample() (core.SamplePoint, error) {
	point := core.SamplePoint{
		Timestamp: time.Now(),
		TempsC:    s.temps.Read(),
	}

	var firstErr error

	cpuUsage, err := s.cpu.Read()
	if err != nil {
		firstErr = err
	} else {
		point.CPUUsagePercent = cpuUsage.OverallPercent
		point.PerCoreUsagePercent = cpuUsage.PerCorePercent
	}

	mem, err := ReadMemUsage()
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
	} else {
		point.MemUsedMB = mem.UsedMB
		point.MemTotalMB = mem.TotalMB
		point.MemUsagePct = mem.UsagePct
	}

	return point, firstErr
}
