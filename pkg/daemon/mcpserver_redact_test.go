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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// A per-run mcpserver whose daemon serves redaction never opens the
// secrets key: the secrets dir here has no key at all, and redaction
// still works, because the daemon did it.
func TestChildRedactorAsksTheDaemonInsteadOfOpeningTheKey(t *testing.T) {
	var calls atomic.Int32
	var up atomic.Bool
	up.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/secrets/redact" || !up.Load() {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		calls.Add(1)
		var req ui.SecretsRedactRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := ui.SecretsRedactResponse{Lines: []string{}}
		for _, l := range req.Lines {
			if strings.Contains(l, "hunter2") {
				out.Redacted++
			}
			out.Lines = append(out.Lines, strings.ReplaceAll(l, "hunter2", "[redacted: secret db/password]"))
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	client := ui.NewHTTPClient(srv.URL)
	client.RetryPolicy = &ui.RetryPolicy{}

	redact := childRedactor(client, t.TempDir())
	if redact == nil {
		t.Fatal("childRedactor = nil with a daemon serving redaction")
	}
	if calls.Load() != 0 {
		t.Fatal("building the redactor reached the daemon; only a redaction should")
	}
	lines, n := redact([]string{"pw=hunter2"})
	if n != 1 || lines[0] != "pw=[redacted: secret db/password]" {
		t.Fatalf("redact = %q, %d", lines, n)
	}
	if calls.Load() != 1 {
		t.Fatalf("daemon calls = %d, want 1", calls.Load())
	}

	// A daemon that stops answering withholds lines, never serves them
	// with only the shapes cut.
	up.Store(false)
	lines, _ = redact([]string{"pw=hunter2"})
	if strings.Contains(lines[0], "hunter2") {
		t.Fatalf("a failed daemon call served %q", lines[0])
	}
}

// With no daemon to ask and no secrets key to read, there is no
// redactor, and the journal tools refuse -- the old behaviour.
func TestChildRedactorFallsBackWithoutADaemon(t *testing.T) {
	if r := childRedactor(nil, t.TempDir()); r != nil {
		t.Fatal("childRedactor with no daemon and no key = non-nil")
	}
}

// A daemon too old to serve redaction (404) is answered from the store
// on disk; with no key there either, lines are withheld, never served.
func TestChildRedactorFallsBackWhenTheDaemonDoesNotServeIt(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	client := ui.NewHTTPClient(srv.URL)
	client.RetryPolicy = &ui.RetryPolicy{}
	redact := childRedactor(client, t.TempDir())
	lines, _ := redact([]string{"pw=hunter2"})
	if strings.Contains(lines[0], "hunter2") {
		t.Fatalf("served %q with nothing to redact it by", lines[0])
	}
}
