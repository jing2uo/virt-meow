//go:build !linux

package conn

import (
	"context"
	"errors"
	"net"
)

// spawnSession starts a session daemon on Linux only: elsewhere a local
// libvirt (Homebrew's on macOS) is the user's to start.
func spawnSession(context.Context, Target) (net.Conn, error) {
	return nil, errors.New("starting a session daemon is done on Linux only")
}
