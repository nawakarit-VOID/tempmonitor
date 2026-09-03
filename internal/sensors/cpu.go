package sensors

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// cpuTimes holds the raw jiffie counters for one CPU line from /proc/stat.
// Field order matches the kernel's documented /proc/stat format:
// user nice system idle iowait irq softirq steal guest guest_nice
type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (c cpuTimes) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

func (c cpuTimes) active() uint64 {
	return c.total() - c.idle - c.iowait
}

// CPUUsageTracker computes CPU utilization percentage by comparing
// successive /proc/stat snapshots. It is stateful by design: usage is only
// meaningful as a delta over some interval, so the tracker remembers the
// previous read internally. Create one and call Read() on a fixed interval
// (e.g. every 100ms) from a single goroutine.
type CPUUsageTracker struct {
	prevTotal cpuTimes
	prevCores map[string]cpuTimes
	hasPrev   bool
}

// NewCPUUsageTracker returns a tracker with no prior sample. The first call
// to Read() will report 0% for everything since there's no delta yet to
// compare against — callers should expect the first data point to be a
// throwaway warm-up sample, not a real measurement.
func NewCPUUsageTracker() *CPUUsageTracker {
	return &CPUUsageTracker{prevCores: make(map[string]cpuTimes)}
}

// CPUUsage is the result of one Read(): overall percentage plus optional
// per-core breakdown, ordered cpu0, cpu1, cpu2...
type CPUUsage struct {
	OverallPercent float64
	PerCorePercent []float64
}

// Read parses /proc/stat and returns the CPU usage percentage(s) since the
// previous call. Cheap: one small file read + line parsing, no allocation
// beyond the returned slice.
func (t *CPUUsageTracker) Read() (CPUUsage, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return CPUUsage{}, fmt.Errorf("opening /proc/stat: %w", err)
	}
	defer f.Close()

	var overall cpuTimes
	cores := make(map[string]cpuTimes)
	var coreOrder []string

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		name := fields[0]
		ct, err := parseCPUTimes(fields[1:])
		if err != nil {
			continue
		}
		if name == "cpu" {
			overall = ct
		} else {
			cores[name] = ct
			coreOrder = append(coreOrder, name)
		}
	}
	if err := sc.Err(); err != nil {
		return CPUUsage{}, fmt.Errorf("scanning /proc/stat: %w", err)
	}

	result := CPUUsage{}
	if t.hasPrev {
		result.OverallPercent = percentDelta(t.prevTotal, overall)
		result.PerCorePercent = make([]float64, 0, len(coreOrder))
		for _, name := range coreOrder {
			result.PerCorePercent = append(result.PerCorePercent, percentDelta(t.prevCores[name], cores[name]))
		}
	}

	t.prevTotal = overall
	t.prevCores = cores
	t.hasPrev = true

	return result, nil
}

// percentDelta computes utilization percent between two cumulative jiffie
// snapshots. Returns 0 if no time has passed (avoids a divide-by-zero on
// back-to-back reads that land in the same clock tick).
func percentDelta(prev, cur cpuTimes) float64 {
	totalDelta := cur.total() - prev.total()
	if totalDelta == 0 {
		return 0
	}
	activeDelta := cur.active() - prev.active()
	return (float64(activeDelta) / float64(totalDelta)) * 100.0
}

func parseCPUTimes(fields []string) (cpuTimes, error) {
	nums := make([]uint64, len(fields))
	for i, f := range fields {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return cpuTimes{}, err
		}
		nums[i] = v
	}
	get := func(i int) uint64 {
		if i < len(nums) {
			return nums[i]
		}
		return 0
	}
	return cpuTimes{
		user:    get(0),
		nice:    get(1),
		system:  get(2),
		idle:    get(3),
		iowait:  get(4),
		irq:     get(5),
		softirq: get(6),
		steal:   get(7),
	}, nil
}
