//go:build linux

package system

import (
	"bufio"
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const cpuSampleInterval = 150 * time.Millisecond

type cpuCounters struct {
	total uint64
	idle  uint64
}

func collectResourceSummary(ctx context.Context) ResourceSummary {
	return ResourceSummary{
		CPU:    sampleCPU(ctx),
		Memory: sampleMemory(),
		RootFS: sampleRootFilesystem(),
	}
}

func sampleCPU(ctx context.Context) CPUResource {
	first, err := readCPUCounters()
	if err != nil {
		return CPUResource{ComponentStatus: ComponentStatus{Status: "unavailable", Error: "cpu_sample_unavailable"}}
	}
	started := time.Now()
	timer := time.NewTimer(cpuSampleInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return CPUResource{ComponentStatus: ComponentStatus{Status: "unavailable", Error: "cpu_sample_cancelled"}}
	case <-timer.C:
	}
	second, err := readCPUCounters()
	utilization, ok := cpuUtilizationPercent(first, second)
	if err != nil || !ok {
		return CPUResource{ComponentStatus: ComponentStatus{Status: "unavailable", Error: "cpu_sample_unavailable"}}
	}
	return CPUResource{
		ComponentStatus:    ComponentStatus{Status: "available"},
		Cores:              runtime.NumCPU(),
		UtilizationPercent: utilization,
		SampleIntervalMS:   time.Since(started).Milliseconds(),
	}
}

func readCPUCounters() (cpuCounters, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuCounters{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return cpuCounters{}, err
		}
		return cpuCounters{}, errors.New("missing aggregate cpu counters")
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuCounters{}, errors.New("invalid aggregate cpu counters")
	}
	values := fields[1:]
	// The first eight fields are disjoint counters; guest and guest_nice are
	// already included in user and nice and must not be counted twice.
	if len(values) > 8 {
		values = values[:8]
	}
	var counters cpuCounters
	for i, raw := range values {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return cpuCounters{}, err
		}
		counters.total += value
		if i == 3 || i == 4 { // idle and iowait are both non-busy time.
			counters.idle += value
		}
	}
	return counters, nil
}

func cpuUtilizationPercent(first, second cpuCounters) (float64, bool) {
	if second.total <= first.total || second.idle < first.idle {
		return 0, false
	}
	totalDelta := second.total - first.total
	idleDelta := second.idle - first.idle
	if totalDelta == 0 || idleDelta > totalDelta {
		return 0, false
	}
	return 100 * float64(totalDelta-idleDelta) / float64(totalDelta), true
}

func sampleMemory() MemoryResource {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return MemoryResource{ComponentStatus: ComponentStatus{Status: "unavailable", Error: "memory_sample_unavailable"}}
	}
	defer f.Close()
	var totalKB, availableKB uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			totalKB = value
		case "MemAvailable":
			availableKB = value
		}
	}
	if sc.Err() != nil || totalKB == 0 || availableKB > totalKB {
		return MemoryResource{ComponentStatus: ComponentStatus{Status: "unavailable", Error: "memory_sample_unavailable"}}
	}
	total, available := totalKB*1024, availableKB*1024
	return MemoryResource{ComponentStatus: ComponentStatus{Status: "available"}, TotalBytes: total, AvailableBytes: available, UsedBytes: total - available}
}

func sampleRootFilesystem() FilesystemResource {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil || stat.Blocks == 0 {
		return FilesystemResource{ComponentStatus: ComponentStatus{Status: "unavailable", Error: "root_filesystem_sample_unavailable"}}
	}
	blockSize := uint64(stat.Bsize)
	total := uint64(stat.Blocks) * blockSize
	free := uint64(stat.Bavail) * blockSize
	usedBlocks := uint64(stat.Blocks) - min(uint64(stat.Bfree), uint64(stat.Blocks))
	return FilesystemResource{ComponentStatus: ComponentStatus{Status: "available"}, Mount: "/", TotalBytes: total, UsedBytes: usedBlocks * blockSize, FreeBytes: free}
}
