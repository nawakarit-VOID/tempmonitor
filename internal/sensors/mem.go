package sensors

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// MemUsage is a snapshot of memory utilization at read time.
type MemUsage struct {
	TotalMB  uint64
	UsedMB   uint64
	UsagePct float64
}

// ReadMemUsage reads /proc/meminfo and computes used memory as
// MemTotal - MemAvailable, which is the same definition tools like `free`
// use and is more accurate than MemTotal - MemFree (the latter ignores
// reclaimable cache/buffers and makes usage look artificially high).
func ReadMemUsage() (MemUsage, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return MemUsage{}, fmt.Errorf("opening /proc/meminfo: %w", err)
	}
	defer f.Close()

	var totalKB, availKB uint64
	found := 0

	sc := bufio.NewScanner(f)
	for sc.Scan() && found < 2 {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			totalKB = parseMeminfoKB(line)
			found++
		case strings.HasPrefix(line, "MemAvailable:"):
			availKB = parseMeminfoKB(line)
			found++
		}
	}
	if err := sc.Err(); err != nil {
		return MemUsage{}, fmt.Errorf("scanning /proc/meminfo: %w", err)
	}
	if totalKB == 0 {
		return MemUsage{}, fmt.Errorf("MemTotal not found in /proc/meminfo")
	}

	usedKB := totalKB - availKB
	return MemUsage{
		TotalMB:  totalKB / 1024,
		UsedMB:   usedKB / 1024,
		UsagePct: (float64(usedKB) / float64(totalKB)) * 100.0,
	}, nil
}

// parseMeminfoKB extracts the numeric value from a line like
// "MemTotal:       16384000 kB". Returns 0 on any parse failure rather than
// erroring — a single malformed line shouldn't abort the whole read.
func parseMeminfoKB(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return v
}
