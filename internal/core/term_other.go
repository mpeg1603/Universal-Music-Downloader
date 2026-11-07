//go:build !linux && !darwin && !freebsd && !windows

package core

import "os"

const liveDisplaySupported = false

func termSize(f *os.File) (int, int) { return 0, 0 }

func diskFree(path string) int64 { return -1 }
