//go:build linux || darwin || freebsd

package main

import (
	"os"
	"syscall"
	"unsafe"
)

const liveDisplaySupported = true

// termSize returns the terminal's columns and rows, or zeros if f isn't a terminal.
func termSize(f *os.File) (int, int) {
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0, 0
	}
	return int(ws.Col), int(ws.Row)
}

// diskFree returns the bytes available to us on the filesystem holding path, or -1.
func diskFree(path string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
