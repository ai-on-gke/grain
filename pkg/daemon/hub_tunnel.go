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

package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
)

const (
	// DefaultTunnelBaseBackoff is the initial redial backoff (250 ms).
	DefaultTunnelBaseBackoff = 250 * time.Millisecond
	// DefaultTunnelMaxBackoff caps exponential redial backoff (30 s).
	DefaultTunnelMaxBackoff = 30 * time.Second
	// DefaultTunnelAuthBackoff is the minimum backoff on 401/403 handshake responses (60 s).
	DefaultTunnelAuthBackoff = 60 * time.Second
	// DefaultTunnelMaxRetryAfter caps Retry-After headers on 429/503 responses (5 m).
	DefaultTunnelMaxRetryAfter = 5 * time.Minute
	// DefaultTunnelStableSession is how long a tunnel must stay up to reset the
	// failure backoff counter back to DefaultTunnelBaseBackoff (30 s).
	DefaultTunnelStableSession = 30 * time.Second
	// DefaultTunnelJitterFraction is the ±25% uniform jitter applied to redial delays.
	DefaultTunnelJitterFraction = 0.25
	// DefaultTunnelMaxSessionAge is the target lifetime before proactively
	// dialing a replacement tunnel session to avoid the 24 h load-balancer cliff (23 h).
	DefaultTunnelMaxSessionAge = 23 * time.Hour
	// DefaultTunnelSessionAgeJitter is the ±30 m uniform jitter on session rotation age.
	DefaultTunnelSessionAgeJitter = 30 * time.Minute
	// DefaultTunnelDrainTimeout bounds how long a rotated or shutting-down tunnel
	// session drains in-flight HTTP streams before closing (2 m).
	DefaultTunnelDrainTimeout = 2 * time.Minute
)

// RegistryTunnelSupervisorOptions configures a RegistryTunnelSupervisor.
type RegistryTunnelSupervisorOptions struct {
	// Handler serves incoming HTTP requests over accepted mux streams. In
	// production this is ui.Server.RegistryTunnelHandler(), which enforces the
	// unconditional X-Grain-Registry-Assertion verifier gate.
	Handler http.Handler

	// Connect overrides api.ConnectMux for unit testing when non-nil.
	Connect func(ctx context.Context, cfg api.ConnectConfig, muxCfg *mux.Config) (*api.TunnelSession, error)
	// MuxConfig optionally overrides the client-side mux configuration.
	MuxConfig *mux.Config

	// TLSConfig optionally overrides TLS verification for tunnel dials.
	TLSConfig *tls.Config
	// Proxy optionally overrides HTTP/HTTPS forward proxy resolution (defaults
	// to http.ProxyFromEnvironment).
	Proxy func(*http.Request) (*url.URL, error)

	BaseBackoff      time.Duration
	MaxBackoff       time.Duration
	AuthBackoff      time.Duration
	MaxRetryAfter    time.Duration
	StableSession    time.Duration
	JitterFraction   float64
	DisableJitter    bool
	MaxSessionAge    time.Duration
	SessionAgeJitter time.Duration
	DrainTimeout     time.Duration

	// OnUnauthorized is invoked when a dial fails with HTTP 401 so cached OIDC
	// tokens can be invalidated before the next attempt.
	OnUnauthorized func()
	// OnSessionStart is an optional hook called whenever a tunnel session starts
	// serving streams.
	OnSessionStart func(sessionID string)
	// OnSessionRotate is an optional hook called when graceful session rotation
	// switches from oldSessionID to newSessionID.
	OnSessionRotate func(oldSessionID, newSessionID string)

	Now  func() time.Time
	Logf func(format string, args ...any)
}

// RegistryTunnelSupervisor supervises the outbound `-hub-connect` tunnel
// (`wss://<registry-host>/api/entries/<name>/connect`), serving accepted mux
// streams in-process over a MuxListener with jittered exponential redial
// backoff (250 ms -> 30 s) and proactive 23 h ± 30 m graceful session rotation.
type RegistryTunnelSupervisor struct {
	opts RegistryTunnelSupervisorOptions
}

// NewRegistryTunnelSupervisor constructs a RegistryTunnelSupervisor with
// production defaults filled in for any zero options.
func NewRegistryTunnelSupervisor(opts RegistryTunnelSupervisorOptions) *RegistryTunnelSupervisor {
	if opts.Connect == nil {
		opts.Connect = api.ConnectMux
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = DefaultTunnelBaseBackoff
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = DefaultTunnelMaxBackoff
	}
	if opts.AuthBackoff <= 0 {
		opts.AuthBackoff = DefaultTunnelAuthBackoff
	}
	if opts.MaxRetryAfter <= 0 {
		opts.MaxRetryAfter = DefaultTunnelMaxRetryAfter
	}
	if opts.StableSession <= 0 {
		opts.StableSession = DefaultTunnelStableSession
	}
	if opts.JitterFraction <= 0 && !opts.DisableJitter {
		opts.JitterFraction = DefaultTunnelJitterFraction
	}
	if opts.MaxSessionAge <= 0 {
		opts.MaxSessionAge = DefaultTunnelMaxSessionAge
	}
	if opts.SessionAgeJitter < 0 || (opts.SessionAgeJitter == 0 && !opts.DisableJitter && opts.MaxSessionAge == DefaultTunnelMaxSessionAge) {
		opts.SessionAgeJitter = DefaultTunnelSessionAgeJitter
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = DefaultTunnelDrainTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	return &RegistryTunnelSupervisor{opts: opts}
}

type activeTunnelServer struct {
	sess        *api.TunnelSession
	ln          *api.MuxListener
	srv         *http.Server
	connectedAt time.Time
}

func (s *RegistryTunnelSupervisor) startSessionServer(sess *api.TunnelSession) *activeTunnelServer {
	ln := sess.Listener()
	handler := s.opts.Handler
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		_ = srv.Serve(ln)
	}()
	if s.opts.OnSessionStart != nil {
		s.opts.OnSessionStart(sess.SessionID)
	}
	return &activeTunnelServer{
		sess:        sess,
		ln:          ln,
		srv:         srv,
		connectedAt: s.opts.Now(),
	}
}

func (s *RegistryTunnelSupervisor) drainAndCloseSession(a *activeTunnelServer) {
	if a == nil {
		return
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), s.opts.DrainTimeout)
	defer cancel()
	_ = a.srv.Shutdown(drainCtx)
	_ = a.sess.Close()
}

func (s *RegistryTunnelSupervisor) closeSessionImmediately(a *activeTunnelServer) {
	if a == nil {
		return
	}
	_ = a.ln.Close()
	_ = a.srv.Close()
	_ = a.sess.Close()
}

// RunTunnel dials and supervises the outbound tunnel until ctx is cancelled.
func (s *RegistryTunnelSupervisor) RunTunnel(ctx context.Context, cfg api.ConnectConfig) error {
	if s == nil {
		return nil
	}
	dialCfg := cfg
	if dialCfg.TLSConfig == nil && s.opts.TLSConfig != nil {
		dialCfg.TLSConfig = s.opts.TLSConfig
	}
	if dialCfg.Proxy == nil && s.opts.Proxy != nil {
		dialCfg.Proxy = s.opts.Proxy
	}

	var drainWG sync.WaitGroup
	defer drainWG.Wait()

	var consecutiveFailures int
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		sess, err := s.opts.Connect(ctx, dialCfg, s.opts.MuxConfig)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if api.IsUnauthorized(err) && s.opts.OnUnauthorized != nil {
				s.opts.OnUnauthorized()
			}
			consecutiveFailures++
			delay := s.ComputeBackoff(consecutiveFailures, err)
			if consecutiveFailures == 1 || consecutiveFailures%10 == 0 {
				redactedErr, _ := (*redact.Redactor)(nil).Text(err.Error())
				s.opts.Logf("grain daemon: hub tunnel (%s) dial failed (attempt %d, retrying in %s): %s",
					dialCfg.Entry, consecutiveFailures, delay.Round(10*time.Millisecond), redactedErr)
			}
			if !s.sleep(ctx, delay) {
				return ctx.Err()
			}
			continue
		}

		active := s.startSessionServer(sess)
		s.opts.Logf("grain daemon: hub tunnel (%s) connected (session=%s)", dialCfg.Entry, sess.SessionID)

		// Supervise active session (and any proactive 23h ± 30m replacement rotations)
		// until the active session drops or ctx is cancelled.
		droppedSessionAge, dropErr := s.runActiveSessionLoop(ctx, dialCfg, &active, &drainWG)
		if ctx.Err() != nil {
			s.drainAndCloseSession(active)
			return ctx.Err()
		}

		if droppedSessionAge >= s.opts.StableSession {
			consecutiveFailures = 0
		}
		consecutiveFailures++
		delay := s.ComputeBackoff(consecutiveFailures, dropErr)
		if !s.sleep(ctx, delay) {
			return ctx.Err()
		}
	}
}

func (s *RegistryTunnelSupervisor) runActiveSessionLoop(
	ctx context.Context,
	dialCfg api.ConnectConfig,
	activePtr **activeTunnelServer,
	drainWG *sync.WaitGroup,
) (time.Duration, error) {
	rotateDelay := s.ComputeSessionAge()
	rotateTimer := time.NewTimer(rotateDelay)
	defer rotateTimer.Stop()

	var rotateFailures int
	for {
		active := *activePtr
		select {
		case <-ctx.Done():
			return s.opts.Now().Sub(active.connectedAt), ctx.Err()

		case <-active.sess.Done():
			age := s.opts.Now().Sub(active.connectedAt)
			s.closeSessionImmediately(active)
			*activePtr = nil
			return age, nil

		case <-rotateTimer.C:
			// Proactive 23 h ± 30 m session rotation: dial the replacement tunnel
			// while the current session continues serving requests.
			nextSess, err := s.opts.Connect(ctx, dialCfg, s.opts.MuxConfig)
			if err != nil {
				if ctx.Err() != nil {
					return s.opts.Now().Sub(active.connectedAt), ctx.Err()
				}
				if api.IsUnauthorized(err) && s.opts.OnUnauthorized != nil {
					s.opts.OnUnauthorized()
				}
				rotateFailures++
				retryDelay := s.ComputeBackoff(rotateFailures, err)
				redactedErr, _ := (*redact.Redactor)(nil).Text(err.Error())
				s.opts.Logf("grain daemon: hub tunnel (%s) rotation dial failed (retrying in %s): %s",
					dialCfg.Entry, retryDelay.Round(10*time.Millisecond), redactedErr)
				rotateTimer.Reset(retryDelay)
				continue
			}

			rotateFailures = 0
			oldActive := active
			newActive := s.startSessionServer(nextSess)
			*activePtr = newActive
			if s.opts.OnSessionRotate != nil {
				s.opts.OnSessionRotate(oldActive.sess.SessionID, newActive.sess.SessionID)
			}
			s.opts.Logf("grain daemon: hub tunnel (%s) rotated session %s -> %s (draining old session up to %s)",
				dialCfg.Entry, oldActive.sess.SessionID, newActive.sess.SessionID, s.opts.DrainTimeout)

			drainWG.Add(1)
			go func(prev *activeTunnelServer) {
				defer drainWG.Done()
				s.drainAndCloseSession(prev)
			}(oldActive)

			rotateTimer.Reset(s.ComputeSessionAge())
		}
	}
}

// ComputeBackoff returns the redial delay for consecutive failure count
// failures and dial error err:
//   - 250 ms -> 500 ms -> 1 s -> ... -> 30 s with ±25% jitter;
//   - HTTP 401/403 enforces a 60 s minimum backoff;
//   - HTTP 429/503 (or any APIError with RetryAfter > 0) honours Retry-After
//     capped at 5 m.
func (s *RegistryTunnelSupervisor) ComputeBackoff(failures int, err error) time.Duration {
	if failures < 1 {
		failures = 1
	}
	delay := s.opts.BaseBackoff
	for i := 1; i < failures && delay < s.opts.MaxBackoff; i++ {
		delay *= 2
		if delay > s.opts.MaxBackoff {
			delay = s.opts.MaxBackoff
			break
		}
	}
	delay = s.jitterDuration(delay)

	if api.IsUnauthorized(err) || api.IsForbidden(err) {
		if delay < s.opts.AuthBackoff {
			delay = s.opts.AuthBackoff
		}
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		ra := apiErr.RetryAfter
		if ra > s.opts.MaxRetryAfter {
			ra = s.opts.MaxRetryAfter
		}
		if ra > delay {
			delay = ra
		}
	}
	return delay
}

// ComputeSessionAge returns the target session lifetime before proactive
// rotation (MaxSessionAge ± SessionAgeJitter, 23 h ± 30 m by default).
func (s *RegistryTunnelSupervisor) ComputeSessionAge() time.Duration {
	base := s.opts.MaxSessionAge
	jitter := s.opts.SessionAgeJitter
	if s.opts.DisableJitter || jitter <= 0 {
		return base
	}
	delta := time.Duration(float64(jitter) * ((2 * rand.Float64()) - 1))
	out := base + delta
	if out <= 0 {
		return base
	}
	return out
}

func (s *RegistryTunnelSupervisor) jitterDuration(d time.Duration) time.Duration {
	if d <= 0 || s.opts.DisableJitter || s.opts.JitterFraction <= 0 {
		return d
	}
	f := s.opts.JitterFraction
	mult := (1 - f) + (2 * f * rand.Float64())
	out := time.Duration(float64(d) * mult)
	if out <= 0 {
		return time.Millisecond
	}
	return out
}

func (s *RegistryTunnelSupervisor) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
