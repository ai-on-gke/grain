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

//go:build linux

package hostd

import (
	"context"
	"time"

	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
)

// A session that lasted this long was a working one: the next reconnect
// starts again from the shortest backoff.
const remoteStableSession = 30 * time.Second

// serveRemote keeps a session to the controller open through s.Remote
// until ctx is done, reconnecting with backoff whenever it drops or
// cannot be made. Every stream the controller opens over it is served as
// a socket connection is (serveConn), in the same frame protocol: the
// controller authenticated hostd's token when it took the connection,
// and hostd chose the controller it dialed, so no peer uid is asked.
func (s *Server) serveRemote(ctx context.Context) {
	minWait, maxWait := s.RemoteMinBackoff, s.RemoteMaxBackoff
	if minWait <= 0 {
		minWait = time.Second
	}
	if maxWait <= 0 {
		maxWait = 30 * time.Second
	}
	wait := minWait
	for ctx.Err() == nil {
		began := time.Now()
		err := s.remoteSession(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(began) >= remoteStableSession {
			wait = minWait
		}
		s.logf("hostd: the session to the controller ended (%v); reconnecting in %s", err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
		if wait > maxWait {
			wait = maxWait
		}
	}
}

// remoteSession is one session to the controller, served until it drops
// or ctx is done.
func (s *Server) remoteSession(ctx context.Context) error {
	conn, err := s.Remote(ctx)
	if err != nil {
		return err
	}
	m := mux.NewMux(conn, mux.Config{
		IsServer:     false,
		PingInterval: 15 * time.Second,
		PingTimeout:  15 * time.Second,
	})
	defer m.Close()
	stop := context.AfterFunc(ctx, func() { _ = m.Close() })
	defer stop()
	s.logf("hostd: connected to the controller (hostd protocol %d, build %s)", ProtocolVersion, s.Build)
	for {
		st, err := m.AcceptStream()
		if err != nil {
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer st.Close()
			s.serveConn(ctx, st)
		}()
	}
}
