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
	"io"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A run's REST reads of a GitHub-on-Prem repository go through the host
// record's proxy (uplink), and a github.com repository's never do; the
// daemon's own pull-request client, which names repositories by
// owner/name on its one host, refuses a GHOP repository rather than
// asking github.com about it.
func TestGHOPRestCallsGoThroughTheHostsProxy(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.String())
		mu.Unlock()
		io.WriteString(w, `{"login":"octo"}`)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	uplink := "http://" + ln.Addr().String()

	creds, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"depot", "dotcom"} {
		if err := creds.SetPAT(name, "tok-"+name); err != nil {
			t.Fatal(err)
		}
	}
	if err := creds.SetHost("depot", gitproxy.CredentialHost{Host: "git.forge.example", Kind: model.GitHostGitHub,
		BaseURL: "http://git.forge.example", Proxy: uplink}); err != nil {
		t.Fatal(err)
	}

	transport, credHost, err := repoTransport(creds, false, "github.com", "git.forge.example/o/r", "depot")
	if err != nil {
		t.Fatal(err)
	}
	if credHost.Proxy != uplink || transport.PathPrefix != "/api/v3" {
		t.Fatalf("depot transport = %+v, host %+v", transport, credHost)
	}
	resp, err := transport.Request("GET", "/repos/o/r", map[string]string{"Authorization": "token tok-depot"}, nil)
	if err != nil || resp.Status != http.StatusOK {
		t.Fatalf("depot REST read = %+v, %v", resp, err)
	}
	if len(seen) != 1 || seen[0] != "GET http://git.forge.example/api/v3/repos/o/r" {
		t.Fatalf("uplink saw %q", seen)
	}

	dotcom, _, err := repoTransport(creds, false, "github.com", "o/r", "dotcom")
	if err != nil {
		t.Fatal(err)
	}
	if dotcom.Client != nil {
		t.Fatalf("github.com transport has a client of its own (%+v): it must be reached as before", dotcom.Client)
	}

	// The daemon's client reaches depot itself now (forgeRouter), so a
	// repository there is not refused as one on another host.
	src := credentialTokenSource{credentials: creds}
	if reason := src.GitOnly("git.forge.example/o", "r"); reason != "" {
		t.Fatalf("GitOnly(depot) = %q; depot has a forge API the daemon's client reaches", reason)
	}
	if reason := src.GitOnly("o", "r"); reason != "" {
		t.Fatalf("GitOnly(github.com) = %q", reason)
	}
}
