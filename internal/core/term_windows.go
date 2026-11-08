//go:build windows

package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// diskSpace is the size of the disk holding path and what's available to us, or -1s.
func diskSpace(path string) (total, free int64) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return -1, -1
	}
	var avail, size uint64
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW").Call(
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&size)), 0)
	if r == 0 {
		return -1, -1
	}
	return int64(size), int64(avail)
}

// deviceOf names the disk a path is on.
func deviceOf(path string) string { return strings.ToUpper(filepath.VolumeName(path)) }

// fileIdentity names a file's content for as long as it isn't changed.
func fileIdentity(info os.FileInfo) string {
	return fmt.Sprintf("%s:%d:%d", info.Name(), info.Size(), info.ModTime().UnixNano())
}

// isCrossDevice recognizes a rename between disks (ERROR_NOT_SAME_DEVICE).
func isCrossDevice(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == 17
}
