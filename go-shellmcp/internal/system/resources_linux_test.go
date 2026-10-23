//go:build linux

package system

import "testing"

func TestCPUUtilizationUsesBusyAndIdleCounterDeltas(t *testing.T) {
	got, ok := cpuUtilizationPercent(cpuCounters{total: 1000, idle: 700}, cpuCounters{total: 1200, idle: 800})
	if !ok || got != 50 {
		t.Fatalf("utilization=(%v,%v), want (50,true)", got, ok)
	}
	if _, ok := cpuUtilizationPercent(cpuCounters{total: 1200, idle: 800}, cpuCounters{total: 1200, idle: 800}); ok {
		t.Fatal("zero-delta sample was accepted")
	}
	if _, ok := cpuUtilizationPercent(cpuCounters{total: 1200, idle: 800}, cpuCounters{total: 1300, idle: 810}); !ok {
		t.Fatal("valid sample with a small idle delta was rejected")
	}
}
