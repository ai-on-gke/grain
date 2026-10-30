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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// The 2 vCPU / 7.8 GiB host from the 10-28 walkthrough, as the daemon
// answers GET /api/settings for it: setup.sh's readiness report greps
// this command's first word and prints its fix line, so both are pinned.
func TestSandboxBudgetReportsAHostThatAdmitsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/settings" {
			t.Errorf("unexpected %s %s -- the check only reads the settings", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sandboxCommitment":{"cpusPerSandbox":2,"memoryMbPerSandbox":8192,"hostCpus":2,"hostMemoryMb":7987,
			"budget":{"cpuOvercommit":2,"memoryOvercommit":1,"reserveMemoryMb":3993,"cpus":0,"cpusBounded":true,"memoryMb":3994,"memoryBounded":true}}}`))
	}))
	defer srv.Close()

	v, err := sandboxBudget(context.Background(), ui.NewHTTPClient(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printSandboxBudget(&buf, v)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output = %q, want a verdict line and a fix line", buf.String())
	}
	if !strings.HasPrefix(lines[0], "admits-nothing: no run can ever be admitted") {
		t.Errorf("verdict line = %q", lines[0])
	}
	if want := "fix: grain settings -sandbox-cpu-overcommit 0 -sandbox-memory-mb 6144 -sandbox-memory-reserve-mb 1792"; lines[1] != want {
		t.Errorf("fix line = %q\nwant       %q", lines[1], want)
	}
}

func TestSandboxBudgetWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"sandboxCommitment":{"cpusPerSandbox":2,"memoryMbPerSandbox":8192,"hostCpus":16,"hostMemoryMb":65536,
			"budget":{"cpuOvercommit":2,"memoryOvercommit":1,"reserveMemoryMb":8192,"cpus":28,"cpusBounded":true,"memoryMb":57344,"memoryBounded":true}}}`))
	}))
	defer srv.Close()
	v, err := sandboxBudget(context.Background(), ui.NewHTTPClient(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printSandboxBudget(&buf, v)
	if got := buf.String(); !strings.HasPrefix(got, "fits: one 8192 MiB sandbox fits") || strings.Contains(got, "fix:") {
		t.Errorf("output = %q, want a fits line and no fix", got)
	}
}
