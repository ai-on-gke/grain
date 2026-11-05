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
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// The local-script approval callback listener serves the whole UI
// handler, so it must refuse a DNS-rebound Host the way the -ui-addr
// listener does, while still routing the IP-literal Host that
// localexec.HTTPApprover sends.
func TestLocalScriptCallbackServerGuardsHost(t *testing.T) {
	var routed atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routed.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	srv, ln, err := localScriptCallbackServer(handler)
	if err != nil {
		t.Fatalf("localScriptCallbackServer: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	base := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}

	do := func(host string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+"/api/local-script-approvals", nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET with Host %q: %v", host, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := do("evil.example"); got != http.StatusMisdirectedRequest {
		t.Errorf("Host evil.example: status %d, want %d", got, http.StatusMisdirectedRequest)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if got := do("evil.example:" + port); got != http.StatusMisdirectedRequest {
		t.Errorf("Host evil.example:<port>: status %d, want %d", got, http.StatusMisdirectedRequest)
	}
	if n := routed.Load(); n != 0 {
		t.Fatalf("a foreign Host reached the handler %d time(s)", n)
	}

	// What HTTPApprover sends: the IP-literal host:port of the published URL.
	if got := do(""); got != http.StatusNoContent {
		t.Errorf("IP-literal Host %s: status %d, want %d (routed)", ln.Addr(), got, http.StatusNoContent)
	}
	if n := routed.Load(); n != 1 {
		t.Errorf("IP-literal Host routed %d time(s), want 1", n)
	}
}
