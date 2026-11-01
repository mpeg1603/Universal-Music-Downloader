//go:build !unix && !windows

package main

import "os/exec"

func ownProcessGroup(cmd *exec.Cmd) {}
