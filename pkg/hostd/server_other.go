// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !linux

package hostd

import (
	"context"
	"errors"
	"net"
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

	Remote                             func(ctx context.Context) (net.Conn, error)
	RemoteMinBackoff, RemoteMaxBackoff time.Duration

	Builtins       []BuiltinEntry
	BuiltinCommand string
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
