//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup keeps a Ctrl-C in the terminal from reaching a child process directly:
// the first Ctrl-C lets the tracks in progress finish, which needs their downloads and
// conversions to keep running. (A second Ctrl-C cancels them through their context.)
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
