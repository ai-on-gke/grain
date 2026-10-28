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
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMCPGitHubPinsCheck(t *testing.T) {
	t.Parallel()

	pins := mcpGitHubPins{dataDir: "/var/lib/grain/data", githubHost: "github.com"}
	for _, tc := range []struct {
		name     string
		dataDir  string
		host     string
		insecure bool
		wantErr  string
	}{
		{name: "daemon's own values", dataDir: "/var/lib/grain/data", host: "github.com"},
		{name: "same data dir spelled differently", dataDir: "/var/lib/grain/data/", host: "GitHub.com"},
		{name: "no data dir loads no credential", host: "github.com"},
		{name: "foreign host", dataDir: "/var/lib/grain/data", host: "127.0.0.1:18690", wantErr: "-github-host"},
		{name: "insecure http not configured", dataDir: "/var/lib/grain/data", host: "github.com", insecure: true, wantErr: "-github-insecure-http"},
		{name: "foreign data dir", dataDir: "/tmp/elsewhere", host: "github.com", wantErr: "-data-dir"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := pins.check(tc.dataDir, tc.host, tc.insecure)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("check() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("check() = %v, want an error naming %s", err, tc.wantErr)
			}
		})
	}

	// An empty configured host is the flag's own default, github.com.
	if err := (mcpGitHubPins{}).check("", "github.com", false); err != nil {
		t.Errorf("empty pinned host refused github.com: %v", err)
	}
	// A mock deployment that really does run over plain HTTP keeps working.
	mock := mcpGitHubPins{dataDir: "/d", githubHost: "127.0.0.1:9", githubInsecureHTTP: true}
	if err := mock.check("/d", "127.0.0.1:9", true); err != nil {
		t.Errorf("mock deployment's own insecure host refused: %v", err)
	}
}

// TestServeMCPServerPinnedRefusesForeignGitHubHost is #2383's proof run
// against the executor-tunnel entry point: args naming an attacker's host
// over plain HTTP must not reach that host at all.
func TestServeMCPServerPinnedRefusesForeignGitHubHost(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer capture.Close()
	host := strings.TrimPrefix(capture.URL, "http://")

	dataDir := t.TempDir()
	args := []string{
		"-sandbox-root", t.TempDir(),
		"-data-dir", dataDir,
		"-pr-repo", "evil-owner/evil-repo",
		"-pr-branch", "main",
		"-github-host", host,
		"-github-insecure-http",
	}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pull_request_status","arguments":{}}}` + "\n")
	var out bytes.Buffer
	err := ServeMCPServerPinned(in, &out, args, &mcpGitHubPins{dataDir: dataDir, githubHost: "github.com"})
	if err == nil || !strings.Contains(err.Error(), "-github-host") {
		t.Fatalf("serveMCPServerPinned() = %v, want a refusal naming -github-host", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("attacker-chosen host received %d request(s)", n)
	}
	if strings.Contains(out.String(), "pull_request_status") {
		t.Errorf("refused server still answered a tool call: %s", out.String())
	}
}
