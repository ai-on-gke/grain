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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// -ui-require-iap with no IAP audience would leave the UI reachable from
// the daemon's own host only, so the daemon refuses to start (task 2318).
func TestCheckConfigRefusesRequireIAPWithoutAnAudience(t *testing.T) {
	t.Setenv("GRAIN_IAP_AUDIENCE", "")
	t.Setenv("GRAIN_REQUIRE_IAP", "")
	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data", "-ui-require-iap"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UiRequireIAP {
		t.Fatal("-ui-require-iap did not set UiRequireIAP")
	}
	start := startable()
	start.UiRequireIAP = true
	err = checkConfig(start)
	if err == nil || !strings.Contains(err.Error(), "-ui-require-iap needs an IAP audience") {
		t.Fatalf("checkConfig(-ui-require-iap, no audience) = %v, want a refusal naming the missing audience", err)
	}

	start.UiIAPAudience = "/projects/1/global/backendServices/2"
	if err := checkConfig(start); err != nil {
		t.Errorf("-ui-require-iap with -ui-iap-audience: %v", err)
	}
	start.UiIAPAudience = ""
	t.Setenv("GRAIN_IAP_AUDIENCE", "/projects/1/global/backendServices/2")
	if err := checkConfig(start); err != nil {
		t.Errorf("-ui-require-iap with $GRAIN_IAP_AUDIENCE: %v", err)
	}
	start.UiRequireIAP = false
	t.Setenv("GRAIN_IAP_AUDIENCE", "")
	if err := checkConfig(start); err != nil {
		t.Errorf("no -ui-require-iap, no audience: %v", err)
	}
}

func TestRequireIAPFromTheEnvironment(t *testing.T) {
	t.Setenv("GRAIN_REQUIRE_IAP", "true")
	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UiRequireIAP {
		t.Error("GRAIN_REQUIRE_IAP=true did not set UiRequireIAP")
	}
	if cfg, _, _, _ := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data", "-ui-require-iap=false"}); cfg.UiRequireIAP {
		t.Error("-ui-require-iap=false did not override GRAIN_REQUIRE_IAP")
	}
	t.Setenv("GRAIN_REQUIRE_IAP", "maybe")
	if _, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data"}); err == nil {
		t.Error("GRAIN_REQUIRE_IAP=maybe was accepted")
	}
}

// `grain access-code` says when the server it would talk to takes the
// code over loopback only.
func TestAccessCodeNotesAServerThatRequiresIAP(t *testing.T) {
	requireIAP := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/access-code" {
			http.NotFound(w, r)
			return
		}
		if requireIAP {
			w.Write([]byte(`{"authenticated":false,"requireIAP":true,"accessCodeAccepted":true}`))
			return
		}
		w.Write([]byte(`{"authenticated":false}`))
	}))
	defer srv.Close()
	t.Setenv(serverEnvVar, srv.URL)
	dir := t.TempDir()
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv(dataDirEnvVar, dir)
	code, err := ui.EnsureLocalAPIToken(ui.LocalAPITokenPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runAccessCode(nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != code {
		t.Errorf("stdout = %q, want only the code", out.String())
	}
	if !strings.Contains(errOut.String(), "not accepted over the network") {
		t.Errorf("stderr = %q, want the require-IAP note", errOut.String())
	}

	requireIAP = false
	out.Reset()
	errOut.Reset()
	if err := runAccessCode(nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q against a server that takes the code, want nothing", errOut.String())
	}
}
