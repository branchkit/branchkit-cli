package main

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// The junction fallback keys on the error number. It used to match the
// English word "privilege" in the message, which a non-English Windows
// translates, so `dev trial` failed there instead of falling back.
func TestPrivilegeNotHeldIsTheErrorNumber(t *testing.T) {
	refused := &os.LinkError{Op: "symlink", Old: `C:\a`, New: `C:\b`, Err: syscall.Errno(1314)}
	if !isPrivilegeNotHeld(refused) {
		t.Fatal("ERROR_PRIVILEGE_NOT_HELD must select the junction fallback")
	}
	other := &os.LinkError{Op: "symlink", Old: `C:\a`, New: `C:\b`, Err: errors.New("a required privilege is not held")}
	if isPrivilegeNotHeld(other) {
		t.Fatal("a message mentioning privilege is not the error number")
	}
	if isPrivilegeNotHeld(&os.LinkError{Op: "symlink", Err: syscall.Errno(5)}) {
		t.Fatal("access denied is not the missing privilege")
	}
}
