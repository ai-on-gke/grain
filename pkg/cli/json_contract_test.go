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

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ciRunTestServer answers GET /api/ci/runs/ci-42 with one finished run,
// conclusion as given, so `grain ci show` and `grain ci watch` have
// something to print without a CI service behind them.
func ciRunTestServer(t *testing.T, conclusion string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ci/runs/ci-42" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ci-42","repo":"acme/widgets","ref":"main","status":"completed","conclusion":"` + conclusion + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// #XC-12: every verb answers -json with exactly one JSON value on stdout,
// whichever side of the verb -json was written on.
func TestEveryExternalVerbSupportsJSONOutput(t *testing.T) {
	server := listTestServer(t)
	ciServer := ciRunTestServer(t, "success")
	for _, tc := range []struct {
		name   string
		server string
		args   []string
	}{
		{"list", server, []string{"list"}},
		{"get", server, []string{"get", "1"}},
		{"create", server, []string{"create", "-title", "Contract work", "-no-repo"}},
		{"update", server, []string{"update", "1", "-title", "Renamed"}},
		{"approve", server, []string{"approve", "2"}},
		{"withdraw", server, []string{"withdraw", "2"}},
		{"defer", server, []string{"defer", "2"}},
		{"undefer", server, []string{"undefer", "2"}},
		{"comment", server, []string{"comment", "1", "a note"}},
		{"close", server, []string{"close", "2"}},
		{"reopen", server, []string{"reopen", "2"}},
		{"metrics", server, []string{"metrics"}},
		{"failures", server, []string{"failures"}},
		{"usage", server, []string{"usage"}},
		{"pause", server, []string{"pause"}},
		{"resume", server, []string{"resume", "2"}},
		{"config", server, []string{"config"}},
		{"settings", server, []string{"settings"}},
		{"qa status", server, []string{"qa", "status"}},
		{"repo", server, []string{"repo", "list"}},
		{"access", server, []string{"access", "list"}},
		{"git-connections", server, []string{"git-connections"}},
		{"hostds", server, []string{"hostds", "list"}},
		{"capability-servers", server, []string{"capability-servers", "list"}},
		{"gcp-key", server, []string{"gcp-key", "list"}},
		{"sandbox-budget", server, []string{"sandbox-budget"}},
		{"capability", server, []string{"capability", "1", "self-debug", "attach"}},
		{"retry", server, []string{"retry", "1"}},
		{"run-now", server, []string{"run-now", "2"}},
		{"broadcast", server, []string{"broadcast", "hello"}},
		{"default-model", server, []string{"default-model", "some-model"}},
		{"ci list", server, []string{"ci", "list"}},
		{"ci faults", server, []string{"ci", "faults"}},
		{"ci show", ciServer, []string{"ci", "show", "ci-42"}},
		{"ci watch", ciServer, []string{"ci", "watch", "ci-42"}},
	} {
		for _, placement := range []string{"before", "after"} {
			t.Run(tc.name+"/"+placement, func(t *testing.T) {
				args := append([]string{"-server", tc.server, "--json"}, tc.args...)
				if placement == "after" {
					args = append(append([]string{"-server", tc.server}, tc.args...), "--json")
					if freeTextCommands[tc.args[0]] {
						// A comment's body is free text, so -json goes
						// before it (liftGlobalFlags, freeTextCommands).
						args = append([]string{"-server", tc.server, tc.args[0], "--json"}, tc.args[1:]...)
					}
				}
				var runErr error
				out := captureStdout(t, func() { runErr = runCLIWithAccessCode(args) })
				if runErr != nil {
					t.Fatalf("grain %s: %v", strings.Join(args, " "), runErr)
				}
				dec := json.NewDecoder(strings.NewReader(out))
				var v any
				if err := dec.Decode(&v); err != nil {
					t.Fatalf("grain %s printed %q, want one JSON value: %v", strings.Join(args, " "), out, err)
				}
				if dec.More() {
					t.Fatalf("grain %s printed %q, want exactly one JSON value", strings.Join(args, " "), out)
				}
				switch v.(type) {
				case map[string]any, []any:
				default:
					t.Fatalf("grain %s printed %q, want a JSON object or array", strings.Join(args, " "), out)
				}
			})
		}
	}
}
