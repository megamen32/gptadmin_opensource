//go:build windows

package hub

import (
	"fmt"
	"golang.org/x/sys/windows"
	"unsafe"
)

func authFilesystemAvailable(path string) (authFilesystemSpace, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return authFilesystemSpace{}, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, &total, &free); err != nil {
		return authFilesystemSpace{}, err
	}
	root := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(name, &root[0], uint32(len(root))); err != nil {
		return authFilesystemSpace{}, err
	}
	var sectors, sectorBytes, freeClusters, totalClusters uint32
	getSpace := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")
	ok, _, callErr := getSpace.Call(uintptr(unsafe.Pointer(&root[0])), uintptr(unsafe.Pointer(&sectors)), uintptr(unsafe.Pointer(&sectorBytes)), uintptr(unsafe.Pointer(&freeClusters)), uintptr(unsafe.Pointer(&totalClusters)))
	if ok == 0 {
		return authFilesystemSpace{}, fmt.Errorf("allocation unit unavailable: %w", callErr)
	}
	return authFilesystemSpace{Available: available, Block: uint64(sectors) * uint64(sectorBytes)}, nil
}
