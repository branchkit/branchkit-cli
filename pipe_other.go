//go:build !windows

package main

import (
	"context"
	"errors"
	"net"
)

// dialPipe never runs off Windows: only a Windows app publishes an operator
// pipe in its address file.
func dialPipe(context.Context, string) (net.Conn, error) {
	return nil, errors.New("an operator pipe exists only on Windows")
}
