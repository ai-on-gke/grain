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

package grit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gritapi"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
)

type sessionClientKey struct{}

func withSessionClient(ctx context.Context, client ClientRecord) context.Context {
	return context.WithValue(ctx, sessionClientKey{}, client)
}

func sessionClientFromContext(ctx context.Context) (ClientRecord, bool) {
	c, ok := ctx.Value(sessionClientKey{}).(ClientRecord)
	return c, ok
}

type hijackedConn struct {
	net.Conn
	r io.Reader
}

func (c *hijackedConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

func wrapHijackedConn(conn net.Conn, br *bufio.Reader) net.Conn {
	if br != nil && br.Buffered() > 0 {
		return &hijackedConn{
			Conn: conn,
			r:    io.MultiReader(br, conn),
		}
	}
	return conn
}

// ActiveSessionOffers returns the reverse-stream offers ("git-proxy",
// "capability-ports", "ci-api") negotiated on the most recent `/v1/session`
// connection.
func (s *Server) ActiveSessionOffers() []string {
	s.sessionMu.RLock()
	defer s.sessionMu.RUnlock()
	return append([]string(nil), s.sessionOffers...)
}

func (s *Server) setSessionOffers(offers []string) {
	s.sessionMu.Lock()
	s.sessionOffers = append([]string(nil), offers...)
	s.sessionMu.Unlock()
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request, client ClientRecord) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, "GET")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), wsupgrade.Protocol) {
		writeAPIError(w, &gritapi.APIError{
			StatusCode: http.StatusBadRequest,
			Code:       "bad_request",
			Message:    "expected WebSocket upgrade on /v1/session",
		})
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeAPIError(w, &gritapi.APIError{
			StatusCode: http.StatusInternalServerError,
			Code:       "internal_error",
			Message:    "connection hijacking not supported",
		})
		return
	}

	offers := gritapi.ParseOffers(r)
	s.setSessionOffers(offers)

	conn, brw, err := hijacker.Hijack()
	if err != nil {
		return
	}

	_, _ = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: %s\r\nConnection: Upgrade\r\n%s\r\n", wsupgrade.Protocol, wsupgrade.AcceptHeader(r))
	if err := brw.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	wrapped := wrapHijackedConn(conn, brw.Reader)
	sess := gritapi.NewSession(wrapped, true, offers)
	defer func() { _ = sess.Close() }()

	s.serveSessionMux(r.Context(), sess, &client)
}

func (s *Server) serveSessionMux(ctx context.Context, sess *gritapi.Session, preAuth *ClientRecord) {
	ln := gritapi.NewMuxListener(sess.Mux())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if preAuth != nil {
			r = r.WithContext(withSessionClient(r.Context(), *preAuth))
		}
		s.ServeHTTP(w, r)
	})
	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = sess.Close()
		case <-sess.Mux().Done():
		case <-done:
		}
	}()
	_ = httpSrv.Serve(ln)
	close(done)
}

// SessionDialConfig configures outbound reverse-tunnel `/v1/session` dialing
// (`grit serve -grain-url ...`).
type SessionDialConfig struct {
	GrainURL    string
	ClientToken string
	BearerToken string
	Offers      []string
	Server      *Server
}

// DialSession dials `GrainURL`'s `/v1/session` endpoint, upgrades the connection
// via `pkg/tunnel/wsupgrade`, wraps it in `pkg/tunnel/mux` (`IsServer: false`),
// and serves `cfg.Server` over mux streams opened by grain until `ctx` is
// cancelled or the session closes.
func DialSession(ctx context.Context, cfg SessionDialConfig) error {
	if cfg.Server == nil {
		return errors.New("grit: Server is required for DialSession")
	}
	conn, err := gritapi.DialUpgrade(ctx, cfg.GrainURL, cfg.ClientToken, cfg.BearerToken, cfg.Offers)
	if err != nil {
		return err
	}
	cfg.Server.setSessionOffers(cfg.Offers)
	sess := gritapi.NewSession(conn, false, cfg.Offers)
	defer func() { _ = sess.Close() }()

	var preAuth *ClientRecord
	if cfg.Server.noAuth {
		anon := anonymousClientRecord()
		preAuth = &anon
	}
	cfg.Server.serveSessionMux(ctx, sess, preAuth)
	return nil
}

func anonymousClientRecord() ClientRecord {
	return ClientRecord{
		ID: "anonymous",
		Scopes: []ClientScope{
			{Scope: gritapi.ScopeSubmit, Repo: "*"},
			{Scope: gritapi.ScopeRead, Repo: "*"},
			{Scope: gritapi.ScopeCancel, Repo: "*"},
			{Scope: gritapi.ScopeCacheAdmin, Repo: "*"},
			{Scope: gritapi.ScopeRelease, Repo: "*"},
		},
	}
}
