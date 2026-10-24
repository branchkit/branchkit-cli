//go:build windows

package main

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// dialPipe connects to the app's operator pipe (`\\.\pipe\…`).
func dialPipe(ctx context.Context, name string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, name)
}
