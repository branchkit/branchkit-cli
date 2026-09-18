//go:build !windows

package main

import (
	"os"
	"syscall"
)

// pidAlive reports whether pid names a live process. Signal 0 checks
// existence without sending anything.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
