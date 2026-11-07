//go:build windows

package core

import (
	"os"
	"syscall"
	"unsafe"
)

// The live panel needs ANSI cursor movement, which older Windows consoles don't do; Windows
// gets plain line-by-line output.
const liveDisplaySupported = false

func termSize(f *os.File) (int, int) { return 0, 0 }

func diskFree(path string) int64 {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return -1
	}
	var free uint64
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW").Call(
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0)
	if r == 0 {
		return -1
	}
	return int64(free)
}
