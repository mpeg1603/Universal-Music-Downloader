//go:build !linux && !darwin && !freebsd && !windows

package core

import (
	"fmt"
	"os"
)

const liveDisplaySupported = false

func termSize(f *os.File) (int, int) { return 0, 0 }

func diskFree(path string) int64 { return -1 }

func diskSpace(path string) (total, free int64) { return -1, -1 }

func deviceOf(path string) string { return "" }

func fileIdentity(info os.FileInfo) string {
	return fmt.Sprintf("%s:%d:%d", info.Name(), info.Size(), info.ModTime().UnixNano())
}

func isCrossDevice(err error) bool { return true }
