package system

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestResourceSummaryNowReturnsTimestampedPartialOrCompleteSample(t *testing.T) {
	before := time.Now().UTC()
	summary := ResourceSummaryNow(context.Background())
	after := time.Now().UTC()
	if summary.SampledAt.Before(before) || summary.SampledAt.After(after) {
		t.Fatalf("sampled_at=%s outside call interval %s..%s", summary.SampledAt, before, after)
	}
	if summary.Status != "available" && summary.Status != "partial" && summary.Status != "unavailable" {
		t.Fatalf("unexpected summary status %q", summary.Status)
	}
	for name, status := range map[string]ComponentStatus{"cpu": summary.CPU.ComponentStatus, "memory": summary.Memory.ComponentStatus, "filesystem": summary.RootFS.ComponentStatus} {
		if status.Status != "available" && status.Status != "unavailable" {
			t.Errorf("%s status=%q", name, status.Status)
		}
		if status.Status == "available" && status.Error != "" {
			t.Errorf("%s available with error %q", name, status.Error)
		}
		if status.Status == "unavailable" && status.Error == "" {
			t.Errorf("%s unavailable without reason", name)
		}
	}
	if summary.CPU.Status == "available" {
		if summary.CPU.Cores < 1 || summary.CPU.UtilizationPercent < 0 || summary.CPU.UtilizationPercent > 100 || summary.CPU.SampleIntervalMS < 1 {
			t.Errorf("invalid CPU sample: %+v", summary.CPU)
		}
	}
	if summary.Memory.Status == "available" && (summary.Memory.TotalBytes == 0 || summary.Memory.AvailableBytes > summary.Memory.TotalBytes || summary.Memory.UsedBytes+summary.Memory.AvailableBytes != summary.Memory.TotalBytes) {
		t.Errorf("invalid memory sample: %+v", summary.Memory)
	}
	if summary.RootFS.Status == "available" && (summary.RootFS.Mount != "/" || summary.RootFS.TotalBytes == 0 || summary.RootFS.FreeBytes > summary.RootFS.TotalBytes || summary.RootFS.UsedBytes > summary.RootFS.TotalBytes) {
		t.Errorf("invalid root filesystem sample: %+v", summary.RootFS)
	}
}

func TestResourceSummaryNowHonorsCancelledCPUContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summary := ResourceSummaryNow(ctx)
	if runtime.GOOS == "linux" && (summary.CPU.Status == "available" || summary.CPU.Error != "cpu_sample_cancelled") {
		t.Fatalf("cancelled CPU sample was not marked unavailable: %+v", summary.CPU)
	}
}
