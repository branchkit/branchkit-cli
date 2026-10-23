package main

import (
	"errors"
	"syscall"
)

// errPrivilegeNotHeld is Windows' ERROR_PRIVILEGE_NOT_HELD (1314): what
// os.Symlink returns without SeCreateSymbolicLink. A number, not the
// message, which Windows translates into the user's language.
const errPrivilegeNotHeld = syscall.Errno(1314)

// isPrivilegeNotHeld reports whether err is the missing-privilege refusal
// of a symlink. Plain errno comparison, so it is testable on any OS.
func isPrivilegeNotHeld(err error) bool {
	return errors.Is(err, errPrivilegeNotHeld)
}
