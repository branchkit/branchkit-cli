//go:build windows

package main

import "os"

// pidAlive reports whether pid names a live process. On Windows
// os.FindProcess opens a handle to the process and fails when there is
// none, so the open itself is the existence check; Signal(0) is not
// supported there and would report every process dead.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
