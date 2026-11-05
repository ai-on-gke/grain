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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type cliTestIdentityVerifier struct{}

func (cliTestIdentityVerifier) Name() string      { return "cli-test-forwarded" }
func (cliTestIdentityVerifier) Headers() []string { return []string{"X-Forwarded-User"} }
func (cliTestIdentityVerifier) Verify(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	raw := strings.TrimSpace(r.Header.Get("X-Forwarded-User"))
	if raw == "" {
		return ui.VerifiedIdentity{}, false, nil
	}
	return ui.VerifiedIdentity{Email: strings.ToLower(raw), Kind: model.PrincipalHuman}, true, nil
}

func captureStdoutCLI(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	os.Stdout = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out, runErr
}

func TestTaskOwnerCLICreateUpdateShowAndList(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Cleanup(ui.RegisterIdentityVerifier(cliTestIdentityVerifier{}))

	store, _ := sqlitetest.Open(t)
	uiCfg := withAccessCode(ui.Config{
		AllowedPrincipals: []string{
			"user:alice@example.com",
			"user:bob@example.com",
			"user:carol@example.com",
		},
	})
	ts := httptest.NewServer(ui.NewServer(uiCfg, store))
	t.Cleanup(ts.Close)

	callAs := func(user string, args ...string) string {
		t.Helper()
		base := []string{"-server", ts.URL}
		if user != "" {
			base = append(base, "-as", user)
		} else {
			base = append(base, "-token", testAccessCode)
		}
		out, err := captureStdoutCLI(t, func() error {
			return runCLI(append(base, args...))
		})
		if err != nil {
			t.Fatalf("runCLI(%q, %v): %v\nout:\n%s", user, args, err, out)
		}
		return out
	}

	// 1. Default owner from signed-in caller.
	out1 := callAs("alice@example.com", "create", "-title", "Alice default task", "-no-repo")
	if !strings.Contains(out1, "owner:      alice@example.com") {
		t.Fatalf("create default owner output missing owner line:\n%s", out1)
	}

	// 2. Explicit owner at creation (-owner user:Bob@Example.com).
	out2 := callAs("alice@example.com", "create", "-title", "Bob explicit task", "-no-repo", "-owner", "user:Bob@Example.com")
	if !strings.Contains(out2, "owner:      bob@example.com") {
		t.Fatalf("create explicit owner output missing normalized owner line:\n%s", out2)
	}

	// 3. Transfer ownership with `grain update -owner` and inspect via `grain show`.
	outUpdate := callAs("alice@example.com", "update", "-owner", "carol@example.com", "2")
	if !strings.Contains(outUpdate, "owner:      carol@example.com") {
		t.Fatalf("update -owner output missing new owner:\n%s", outUpdate)
	}

	outShow := callAs("alice@example.com", "show", "2")
	if !strings.Contains(outShow, "owner:      carol@example.com") {
		t.Fatalf("grain show output missing owner:\n%s", outShow)
	}
	if !strings.Contains(outShow, "proposed") || !strings.Contains(outShow, "by alice@example.com") {
		t.Fatalf("grain show output missing proposed attribution:\n%s", outShow)
	}
	if !strings.Contains(outShow, string(model.TransitionOwnerChanged)) || !strings.Contains(outShow, "bob@example.com -> carol@example.com") {
		t.Fatalf("grain show output missing owner_changed audit:\n%s", outShow)
	}

	// 4. Filter with `grain list -owner` and `grain list -owner me`.
	listCarol := callAs("alice@example.com", "list", "-owner", "user:Carol@Example.com")
	if !strings.Contains(listCarol, "Bob explicit task") || strings.Contains(listCarol, "Alice default task") {
		t.Fatalf("grain list -owner user:Carol@Example.com unexpected output:\n%s", listCarol)
	}

	listMeAlice := callAs("alice@example.com", "list", "-owner", "me")
	if !strings.Contains(listMeAlice, "Alice default task") || strings.Contains(listMeAlice, "Bob explicit task") {
		t.Fatalf("grain list -owner me (as alice) unexpected output:\n%s", listMeAlice)
	}

	listFilterMe := callAs("carol@example.com", "list", "-filter", "owner:any:me")
	if !strings.Contains(listFilterMe, "Bob explicit task") || strings.Contains(listFilterMe, "Alice default task") {
		t.Fatalf("grain list -filter owner:any:me (as carol) unexpected output:\n%s", listFilterMe)
	}
}
