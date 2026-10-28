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

package hostd

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
)

// RemotePath is the controller's route a hostd dialing out upgrades.
const RemotePath = "/api/hostd/connect"

// The upgrade request's own headers: the shared hostd token (beside, or
// in place of, Authorization, which IAP may need for its own token), and
// what the hostd says about itself for the controller's status.
const (
	TokenHeader    = "X-Grain-Hostd-Token"
	HostHeader     = "X-Grain-Hostd-Host"
	BuildHeader    = "X-Grain-Hostd-Build"
	ProtocolHeader = "X-Grain-Hostd-Protocol"
)

// Dialer is a hostd's way out to a controller it cannot be reached from
// (Server.Remote): one HTTP/1.1 upgrade of <Server>/api/hostd/connect,
// dressed as a WebSocket handshake so Google's front end and IAP pass it
// (wsupgrade), over which the controller then runs a mux session.
type Dialer struct {
	// Server is the controller's URL: https://grain.example.com, or
	// http://127.0.0.1:4242. A path other than "/" replaces RemotePath.
	Server string
	// Token is the shared hostd token the controller checks.
	Token string
	// IDToken, when set, is a token for a proxy in front of the
	// controller -- IAP's OIDC token -- sent as Authorization, with
	// Token moved to TokenHeader alone.
	IDToken func(ctx context.Context) (string, error)
	// TLS is the client configuration for an https Server, or for an
	// http one to be dialed over TLS anyway; the system roots and the
	// URL's host when nil.
	TLS *tls.Config
	// Host and Build describe this hostd to the controller.
	Host, Build string
	// NetDial replaces the TCP dial, for tests.
	NetDial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Dial makes one upgraded connection to the controller.
func (d Dialer) Dial(ctx context.Context) (net.Conn, error) {
	u, err := url.Parse(strings.TrimSpace(d.Server))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("hostd: -server %q is not a URL like https://grain.example.com", d.Server)
	}
	secure := u.Scheme == "https" || u.Scheme == "wss"
	if !secure && u.Scheme != "http" && u.Scheme != "ws" {
		return nil, fmt.Errorf("hostd: -server %q: scheme %q is not http or https", d.Server, u.Scheme)
	}
	addr := u.Host
	if u.Port() == "" {
		if secure {
			addr = net.JoinHostPort(u.Hostname(), "443")
		} else {
			addr = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	dial := d.NetDial
	if dial == nil {
		nd := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		dial = nd.DialContext
	}
	raw, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("hostd: connecting to %s: %w", addr, err)
	}
	if secure || d.TLS != nil {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if d.TLS != nil {
			cfg = d.TLS.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		tc := tls.Client(raw, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("hostd: TLS to %s: %w", addr, err)
		}
		raw = tc
	}
	conn, err := d.upgrade(ctx, raw, u)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}

func (d Dialer) upgrade(ctx context.Context, raw net.Conn, u *url.URL) (net.Conn, error) {
	path := u.EscapedPath()
	if path == "" || path == "/" {
		path = RemotePath
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Host = u.Host
	wsupgrade.SetRequestHeaders(req.Header)
	req.Header.Set("User-Agent", "grain-hostd")
	req.Header.Set(HostHeader, d.Host)
	req.Header.Set(BuildHeader, d.Build)
	req.Header.Set(ProtocolHeader, strconv.Itoa(ProtocolVersion))
	var idToken string
	if d.IDToken != nil {
		if idToken, err = d.IDToken(ctx); err != nil {
			return nil, fmt.Errorf("hostd: getting the token for the proxy in front of %s: %w", u.Host, err)
		}
		idToken = strings.TrimSpace(strings.TrimPrefix(idToken, "Bearer "))
	}
	if d.Token != "" {
		req.Header.Set(TokenHeader, d.Token)
	}
	switch {
	case idToken != "":
		req.Header.Set("Authorization", "Bearer "+idToken)
	case d.Token != "":
		req.Header.Set("Authorization", "Bearer "+d.Token)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	} else {
		_ = raw.SetDeadline(time.Now().Add(30 * time.Second))
	}
	if err := req.Write(raw); err != nil {
		return nil, fmt.Errorf("hostd: writing the upgrade to %s: %w", u.Host, err)
	}
	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("hostd: reading the upgrade's answer from %s: %w", u.Host, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("hostd: %s refused the connection (%s): %s", u.Host, resp.Status, strings.TrimSpace(string(body)))
	}
	_ = raw.SetDeadline(time.Time{})
	if br.Buffered() == 0 {
		return raw, nil
	}
	return &bufferedConn{Conn: raw, r: io.MultiReader(br, raw)}, nil
}

// bufferedConn reads what the upgrade's reader already holds before the
// connection itself.
type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
