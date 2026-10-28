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

package modelproxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every request the agent CLIs a sandbox runs were seen to make against
// this proxy, driven through it against a stub vendor
// (docs/test-runs/2026-10-01-credential-proxy-hardening.md): the pinned
// claude 2.1.263 and codex 0.153.0. The allowlist exists to refuse
// everything else, so a CLI release that starts calling something new is
// refused -- and this list is where whoever widens it says which.
func TestTheAgentCLIsOwnRequestsAreForwarded(t *testing.T) {
	type call struct {
		Method, Path string
		Header       http.Header
	}
	calls := map[string][]call{
		Claude.Name: {
			{Method: http.MethodPost, Path: "/v1/messages?beta=true"},
			{Method: http.MethodPost, Path: "/v1/messages/count_tokens?beta=true"},
		},
		OpenAI.Name: {
			{Method: http.MethodPost, Path: "/v1/responses"},
		},
	}
	eachVendor(t, func(t *testing.T, vendor Vendor) {
		for _, c := range calls[vendor.Name] {
			up := newUpstream(t)
			server, endpoint, _ := proxyFor(t, vendor, up, "cred")
			_, token, err := endpoint.Mint("run-7", testConnection)
			if err != nil {
				t.Fatal(err)
			}
			header := authenticated(vendor, token)
			for name, values := range c.Header {
				header[name] = values
			}
			resp := rawCall(t, server, c.Method, c.Path, header)
			if resp.StatusCode != http.StatusOK || up.got == nil {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("%s %s: status %d, upstream saw %v: %s", c.Method, c.Path, resp.StatusCode, up.got, body)
			}
		}
	})
}

// codex 0.153.0 opens the Responses API as a WebSocket first -- GET
// /v1/responses with Upgrade: websocket -- and spends five refused
// handshakes before falling back to HTTPS. The handshake is forwarded,
// with the deployment's credential in place of the run's token, and the
// connection that comes back is the vendor's.
func TestCodexsResponsesWebSocketHandshakeIsForwarded(t *testing.T) {
	var gotAuth string
	vendorSide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/responses" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "not a handshake", http.StatusBadRequest)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		line, _ := rw.ReadString('\n')
		_, _ = io.WriteString(conn, "echo:"+line)
	}))
	defer vendorSide.Close()
	server, endpoint, _ := proxyFor(t, OpenAI, &upstream{server: vendorSide}, "the-deployments-own-credential")
	_, token, err := endpoint.Mint("run-7", testConnection)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /v1/responses HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer "+token+
		"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("handshake status %d, want 101: %s", resp.StatusCode, body)
	}
	if gotAuth != "Bearer the-deployments-own-credential" {
		t.Errorf("vendor saw Authorization %q, want the deployment's own credential", gotAuth)
	}
	_, _ = io.WriteString(conn, "hello\n")
	if line, _ := br.ReadString('\n'); line != "echo:hello\n" {
		t.Errorf("after the upgrade read %q, want the vendor's echo", line)
	}
}
