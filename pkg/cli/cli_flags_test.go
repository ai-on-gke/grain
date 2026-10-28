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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunCLI_GlobalFlags(t *testing.T) {
	var gotAuth, gotTunnel, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTunnel = r.Header.Get("X-Grain-Tunnel-Token")
		gotUser = r.Header.Get("X-Forwarded-User")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	err := runCLI([]string{
		"-server=" + srv.URL,
		"-token=my-secret-token",
		"-as=alice@example.com",
		"list",
	})
	if err != nil {
		t.Fatalf("runCLI failed: %v", err)
	}

	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("got Authorization %q, want Bearer my-secret-token", gotAuth)
	}
	if gotTunnel != "my-secret-token" {
		t.Errorf("got X-Grain-Tunnel-Token %q, want my-secret-token", gotTunnel)
	}
	if gotUser != "alice@example.com" {
		t.Errorf("got X-Forwarded-User %q, want alice@example.com", gotUser)
	}
}

func TestRunCLI_UsageIncludesNewFlags(t *testing.T) {
	for _, flag := range []string{"-token", "-audience", "-as", "-server"} {
		if !strings.Contains(usage, flag) {
			t.Errorf("usage missing expected flag %q", flag)
		}
	}
}
