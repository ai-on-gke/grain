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

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/sandboximage"
)

// The sandbox image's grain answers the sandbox CLI, not only host-script:
// every run after the Stage 5 upgrade failed its first `grain` call on an
// image whose grain knew host-script alone, and had to build grain from
// source to reach its own deployment.
func TestSandboxGrainAnswersTheSandboxCLI(t *testing.T) {
	var gotPath, gotAuth string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.RequestURI(), r.Header.Get("Authorization")
		_, _ = w.Write([]byte("8fa91afd/\n"))
	}))
	defer proxy.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GRAIN_SERVER", "")
	t.Setenv("GRAIN_SANDBOX_CLI", "")
	t.Setenv("GRAIN_SANDBOX_PROXY", proxy.URL)
	t.Setenv("GRAIN_SANDBOX_TOKEN", "tok")

	var out, errOut bytes.Buffer
	if code := run([]string{"ls", "ci://acme/widgets"}, &out, &errOut); code != 0 {
		t.Fatalf("grain ls = %d, stderr %q", code, errOut.String())
	}
	if !strings.HasPrefix(gotPath, "/fs/ci/acme/widgets") || gotAuth == "" || out.String() != "8fa91afd/\n" {
		t.Errorf("grain ls asked %q (auth %q) and printed %q", gotPath, gotAuth, out.String())
	}

	out.Reset()
	if code := run([]string{"log", "--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "--min-priority") {
		t.Errorf("grain log --help = %d, %q", code, out.String())
	}

	// A command it does not have gets the sandbox CLI's help, not this
	// binary's out-of-sandbox usage.
	errOut.Reset()
	if code := run([]string{"frobnicate"}, &out, &errOut); code != 2 ||
		!strings.Contains(errOut.String(), `unknown command "frobnicate"`) || !strings.Contains(errOut.String(), "sandbox CLI for this grain deployment") {
		t.Errorf("grain frobnicate = %d, %q", code, errOut.String())
	}
}

// Outside a sandbox there is no proxy to answer over, and the error says
// so instead of claiming the command does not exist.
func TestSandboxGrainOutsideASandbox(t *testing.T) {
	t.Setenv("GRAIN_SANDBOX_CLI", "0")
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "host-script") {
		t.Errorf("grain help = %d, %q", code, out.String())
	}
	errOut.Reset()
	if code := run([]string{"ci", "wait"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "scoped proxy") {
		t.Errorf("grain ci wait outside a sandbox = %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"daemon"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "not a command") {
		t.Errorf("grain daemon = %d, %q", code, errOut.String())
	}
}

// pkg/sandboximage.VerifyScript decides whether an image's grain carries
// the sandbox CLI by reading this usage, outside any sandbox.
func TestUsageNamesTheSandboxCLI(t *testing.T) {
	t.Setenv("GRAIN_SANDBOX_CLI", "0")
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatalf("grain help = %d, %q", code, errOut.String())
	}
	for _, sub := range append(append([]string(nil), sandboximage.SandboxCLICommands...), "host-script") {
		if !regexp.MustCompile(`(?m)grain ` + sub + `( |$)`).MatchString(out.String()) {
			t.Errorf("grain help does not name %q:\n%s", "grain "+sub, out.String())
		}
	}
}
