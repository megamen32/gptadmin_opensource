//go:build !linux

package system

import "context"

func collectResourceSummary(context.Context) ResourceSummary {
	unsupported := ComponentStatus{Status: "unavailable", Error: "resource_sampling_unsupported_on_this_os"}
	return ResourceSummary{
		CPU:    CPUResource{ComponentStatus: unsupported},
		Memory: MemoryResource{ComponentStatus: unsupported},
		RootFS: FilesystemResource{ComponentStatus: unsupported},
	}
}
