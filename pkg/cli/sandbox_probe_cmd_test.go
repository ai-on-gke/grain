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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func probeServer(t *testing.T, exit int, got *ui.SandboxProbeRequest) *ui.HTTPClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sandbox-probe" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Errorf("decoding probe request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(ui.SandboxProbeResult{
			Sandbox: "probe-" + got.ID, Pod: "grain-granule-probe-" + got.ID, Access: "dialout",
			Stdout: strings.Join(got.Argv[1:], " ") + "\n", ExitCode: exit,
		})
	}))
	t.Cleanup(srv.Close)
	return ui.NewHTTPClient(srv.URL)
}

func TestSandboxProbeSendsTheCommandAndPrintsItsOutput(t *testing.T) {
	var got ui.SandboxProbeRequest
	c := probeServer(t, 0, &got)
	var stdout, stderr bytes.Buffer
	err := cmdSandboxProbe(context.Background(), c, &printer{}, &stdout, &stderr,
		[]string{"--id=gke-check-1", "--access=dialout", "--", "echo", "dialout-ok:gke-check-1"})
	if err != nil {
		t.Fatalf("cmdSandboxProbe: %v", err)
	}
	want := ui.SandboxProbeRequest{ID: "gke-check-1", Access: "dialout", Argv: []string{"echo", "dialout-ok:gke-check-1"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request = %+v, want %+v", got, want)
	}
	if stdout.String() != "dialout-ok:gke-check-1\n" {
		t.Errorf("stdout = %q, want the command's own output", stdout.String())
	}
	if !strings.Contains(stderr.String(), "grain-granule-probe-gke-check-1") {
		t.Errorf("stderr = %q, want it to name the probe pod", stderr.String())
	}
}

func TestSandboxProbeFailsWhenTheCommandDoes(t *testing.T) {
	var got ui.SandboxProbeRequest
	c := probeServer(t, 3, &got)
	var stdout, stderr bytes.Buffer
	err := cmdSandboxProbe(context.Background(), c, &printer{}, &stdout, &stderr, []string{"-id=x", "--", "false"})
	if err == nil || !strings.Contains(err.Error(), "status 3") {
		t.Fatalf("err = %v, want the command's exit status", err)
	}
}

func TestSandboxProbeRefusesAMissingIDOrCommand(t *testing.T) {
	for _, args := range [][]string{{"--", "true"}, {"-id=x"}} {
		var stdout, stderr bytes.Buffer
		if err := cmdSandboxProbe(context.Background(), ui.NewHTTPClient("http://127.0.0.1:1"), &printer{}, &stdout, &stderr, args); err == nil {
			t.Errorf("args %q: want a refusal before any call", args)
		}
	}
}
