//go:build windows

package core

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup keeps a Ctrl-C in the console from reaching a child process directly (see
// the Unix version).
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
