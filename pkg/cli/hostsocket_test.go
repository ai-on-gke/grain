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
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// The host CLI dials the daemon's API socket in place of a loopback
// -server when one is there, and then offers no access code: on the
// socket it is who its account maps to.
func TestHostCLIPrefersTheAPISocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "gcli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	tokenFile := filepath.Join(dir, "local-api-token")
	if err := os.WriteFile(tokenFile, []byte("the-access-code\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRAIN_DATA_DIR", dir)
	t.Setenv(ui.LocalAPITokenFileEnv, tokenFile)
	t.Setenv(ui.APISocketEnv, "")
	for _, env := range ui.TokenEnvVars {
		t.Setenv(env, "")
	}

	if got := hostSocket("http://127.0.0.1:8420", ""); got != "" {
		t.Fatalf("hostSocket with no socket listening = %q; want none", got)
	}

	ln, err := ui.ListenAPISocket(ui.APISocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var gotAuth []string
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		json.NewEncoder(w).Encode(ui.AuthWhoamiResponse{Authenticated: true, Principal: "human:alice@example.com"})
	})}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })

	sock := ui.APISocketPath(dir)
	for _, tc := range []struct {
		server, credential, want string
	}{
		{"http://127.0.0.1:8420", "", sock},
		{"http://localhost:8420", "", sock},
		{"http://127.0.0.1:8420", "-token", ""},
		{"http://127.0.0.1:8420", "context work", ""},
		{"https://grain.example.com", "", ""},
	} {
		if got := hostSocket(tc.server, tc.credential); got != tc.want {
			t.Errorf("hostSocket(%q, %q) = %q; want %q", tc.server, tc.credential, got, tc.want)
		}
	}
	t.Setenv(ui.APISocketEnv, "none")
	if got := hostSocket("http://127.0.0.1:8420", ""); got != "" {
		t.Errorf("hostSocket with %s=none = %q; want none", ui.APISocketEnv, got)
	}
	t.Setenv(ui.APISocketEnv, "")

	c := ui.NewHTTPClient("http://127.0.0.1:1")
	c.HTTP, c.Socket = cliSocketHTTPClient(sock, 5*time.Second, 5*time.Second), sock
	who, err := c.AuthWhoami(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if who.Principal != "human:alice@example.com" {
		t.Fatalf("whoami over the socket = %+v", who)
	}
	if len(gotAuth) == 0 || gotAuth[0] != "" {
		t.Fatalf("a request on the socket carried Authorization %q; want no access code offered", gotAuth)
	}
}
