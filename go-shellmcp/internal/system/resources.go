package system

import (
	"context"
	"time"
)

// ComponentStatus describes whether one host resource was measured.
type ComponentStatus struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// CPUResource contains a sampled utilization percentage, not a load average.
type CPUResource struct {
	ComponentStatus
	Cores              int     `json:"cores,omitempty"`
	UtilizationPercent float64 `json:"utilization_percent,omitempty"`
	SampleIntervalMS   int64   `json:"sample_interval_ms,omitempty"`
}

// MemoryResource contains current Linux memory totals in bytes.
type MemoryResource struct {
	ComponentStatus
	TotalBytes     uint64 `json:"total_bytes,omitempty"`
	AvailableBytes uint64 `json:"available_bytes,omitempty"`
	UsedBytes      uint64 `json:"used_bytes,omitempty"`
}

// FilesystemResource describes capacity and free-to-user bytes for the root mount.
type FilesystemResource struct {
	ComponentStatus
	Mount      string `json:"mount,omitempty"`
	TotalBytes uint64 `json:"total_bytes,omitempty"`
	UsedBytes  uint64 `json:"used_bytes,omitempty"`
	FreeBytes  uint64 `json:"free_bytes,omitempty"`
}

// ResourceSummary is a one-time, target-local sample. A partial sample keeps
// successful components while marking failed components unavailable.
type ResourceSummary struct {
	SampledAt time.Time          `json:"sampled_at"`
	Status    string             `json:"status"`
	CPU       CPUResource        `json:"cpu"`
	Memory    MemoryResource     `json:"memory"`
	RootFS    FilesystemResource `json:"root_filesystem"`
}

// ResourceSummaryNow samples supported resources on this host. The CPU probe
// is bounded to one short interval; callers can cancel it through ctx.
func ResourceSummaryNow(ctx context.Context) ResourceSummary {
	summary := collectResourceSummary(ctx)
	summary.SampledAt = time.Now().UTC()
	available := 0
	for _, status := range []string{summary.CPU.Status, summary.Memory.Status, summary.RootFS.Status} {
		if status == "available" {
			available++
		}
	}
	switch available {
	case 3:
		summary.Status = "available"
	case 0:
		summary.Status = "unavailable"
	default:
		summary.Status = "partial"
	}
	return summary
}
