//go:build linux || darwin || freebsd

package core

import (
	"errors"
	"fmt"
	"os"
	"strconv"
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

// diskSpace is the size of the filesystem holding path and what's available to us, or -1s.
func diskSpace(path string) (total, free int64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return -1, -1
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize)
}

// deviceOf names the filesystem a path is on.
func deviceOf(path string) string {
	var st syscall.Stat_t
	if syscall.Stat(path, &st) != nil {
		return ""
	}
	return strconv.FormatUint(uint64(st.Dev), 10)
}

// fileIdentity names a file's content for as long as it isn't changed, even if it's moved.
func fileIdentity(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d:%d:%d", st.Dev, st.Ino, info.Size(), info.ModTime().UnixNano())
	}
	return fmt.Sprintf("%s:%d:%d", info.Name(), info.Size(), info.ModTime().UnixNano())
}

func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

// diskFree returns the bytes available to us on the filesystem holding path, or -1.
func diskFree(path string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
