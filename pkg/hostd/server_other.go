//go:build !linux

package hostd

import (
	"context"
	"errors"
	"time"
)

// Server is only implemented on Linux, the one platform grain installs on.
type Server struct {
	Socket          string
	Allowlist       string
	AllowlistOwners []uint32
	StateDir        string
	DisconnectGrace time.Duration
	Env             []string
	Logf            func(format string, args ...any)
	Build           string
	CurrentLink     string
	ExitedRetention time.Duration
	Poll            time.Duration
}

// Drain does nothing off Linux.
func (s *Server) Drain() {}

// Cutover does nothing off Linux.
func (s *Server) Cutover() {}

// Draining is always false off Linux.
func (s *Server) Draining() bool { return false }

// Processes is always empty off Linux.
func (s *Server) Processes() []Process { return nil }

// Serve reports that hostd needs Linux.
func (s *Server) Serve(context.Context) error {
	return errors.New("grain hostd runs on Linux only")
}
