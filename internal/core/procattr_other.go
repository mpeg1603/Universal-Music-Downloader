//go:build !unix && !windows

package core

import "os/exec"

func ownProcessGroup(cmd *exec.Cmd) {}
