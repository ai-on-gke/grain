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
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
)

// DefaultRenewInterval is how often a connected key-bound host silently
// renews its 30-day credential over its active session.
const DefaultRenewInterval = 12 * time.Hour

// A session that lasted this long was a working one: the next reconnect
// starts again from the shortest backoff.
const remoteStableSession = 30 * time.Second

// defaultRemoteMaxBackoff is the longest wait between attempts to reach
// the controller. It is kept well inside dialRetry, the controller's own
// wait for a hostd that is not connected: a daemon restarted after an
// outage of any length (an upgrade, a rescheduled pod) waits that long
// for hostd's hello to adopt the host runs it left, and a backoff that
// had grown to match it could reconnect only after it gave up.
const defaultRemoteMaxBackoff = 5 * time.Second

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
		maxWait = defaultRemoteMaxBackoff
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
	ctx = context.WithValue(ctx, sessionKey{}, m)
	renewCtx, cancelRenew := context.WithCancel(ctx)
	defer cancelRenew()
	if len(s.RenewKey) == ed25519.PrivateKeySize {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.renewLoop(renewCtx, m)
		}()
	}
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

func (s *Server) renewLoop(ctx context.Context, m *mux.Mux) {
	every := s.RenewInterval
	if every <= 0 {
		every = DefaultRenewInterval
	}
	// The first renewal is due RenewInterval after the credential was last
	// issued, not after this connection opened: a ticker started afresh on
	// every connection never fires for a host that reconnects more often
	// than RenewInterval (a daemon upgraded or restarted twice a day), and
	// its 30-day credential would run out while it was connected.
	wait := s.renewDue(every, time.Now())
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-m.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := s.renewOnce(ctx, m); err != nil && ctx.Err() == nil {
			s.logf("hostd: silent credential renewal deferred: %v", err)
		}
		wait = every
	}
}

// renewDue is how long from now until this host's credential is due a
// silent renewal: every after it was last issued or renewed (RenewedAt,
// else the first connection), or at once when that has already passed.
func (s *Server) renewDue(every time.Duration, now time.Time) time.Duration {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	if s.renewedAt.IsZero() {
		s.renewedAt = s.RenewedAt
		if s.renewedAt.IsZero() || s.renewedAt.After(now) {
			s.renewedAt = now
		}
	}
	return max(s.renewedAt.Add(every).Sub(now), 0)
}

func (s *Server) renewOnce(ctx context.Context, m *mux.Mux) error {
	var curTok string
	if s.CurrentToken != nil {
		curTok = s.CurrentToken()
	}
	proof, err := SignRenewProof(s.RenewName, curTok, s.RenewKey, time.Now().UTC())
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st, err := m.OpenStream(rctx)
	if err != nil {
		return err
	}
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(10 * time.Second))
	if err := writeJSON(st, frameRequest, Request{Op: OpRenew, Renew: &proof}); err != nil {
		return err
	}
	typ, payload, err := readFrame(st)
	if err != nil {
		return err
	}
	switch typ {
	case frameRenewed:
		var rep RenewReply
		if err := json.Unmarshal(payload, &rep); err != nil || rep.Token == "" {
			return fmt.Errorf("malformed renew reply")
		}
		s.renewMu.Lock()
		s.renewedAt = time.Now()
		s.renewMu.Unlock()
		if s.OnTokenRenewed != nil {
			s.OnTokenRenewed(rep.Token, rep.ExpiresAt)
		}
		return nil
	case frameRefused:
		return refusal(payload)
	default:
		return fmt.Errorf("unexpected renew frame %q", typ)
	}
}
