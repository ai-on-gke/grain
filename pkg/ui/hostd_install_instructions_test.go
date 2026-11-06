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

package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

// issueHostd creates hostd name through the API and returns the answer.
func issueHostd(t *testing.T, srv *ui.Server, name, owner string) ui.IssuedHostdToken {
	t.Helper()
	rec := do(t, srv, http.MethodPost, "/api/hostds", `{"name":"`+name+`","owner":"`+owner+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/hostds = %d %s", rec.Code, rec.Body.String())
	}
	var issued ui.IssuedHostdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	return issued
}

// assertNoToken fails when token appears anywhere in inst's lines or note.
func assertNoToken(t *testing.T, what string, inst *ui.HostdInstall, token string) {
	t.Helper()
	if inst == nil {
		t.Fatalf("%s: no install instructions", what)
	}
	if token == "" {
		t.Fatalf("%s: no token to look for", what)
	}
	for _, l := range inst.Lines {
		if strings.Contains(l, token) {
			t.Errorf("%s: install line %q carries the token", what, l)
		}
	}
	if strings.Contains(inst.Note, token) {
		t.Errorf("%s: install note %q carries the token", what, inst.Note)
	}
}

// With nothing registered, create, rotate and GET .../install all answer
// with the root installer's one line Settings -> Hostds used to assemble
// itself, and no note; none of them carries the token.
func TestHostdInstallInstructionsDefaultToTheRootInstaller(t *testing.T) {
	srv, _ := testServer(t)
	issued := issueHostd(t, srv, "ws-1", "alice@example.com")
	want := `sudo sh -c "$(curl -fsSL 'http://example.com/api/hostd/install.sh')" grain-hostd-install --name ws-1`
	if rel := version.Get().Release; rel != "" {
		want += " --release " + rel
	} else {
		want += " --any-version"
	}
	if issued.Install == nil || len(issued.Install.Lines) != 1 || issued.Install.Lines[0] != want || issued.Install.Note != "" {
		t.Fatalf("created install = %+v, want the one line %s", issued.Install, want)
	}
	assertNoToken(t, "create", issued.Install, issued.Token)

	rec := do(t, srv, http.MethodPost, "/api/hostds/ws-1/rotate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body.String())
	}
	var rotated ui.IssuedHostdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Install == nil || len(rotated.Install.Lines) != 1 || rotated.Install.Lines[0] != want {
		t.Errorf("rotated install = %+v, want the one line %s", rotated.Install, want)
	}
	assertNoToken(t, "rotate", rotated.Install, rotated.Token)
	assertNoToken(t, "rotate (the token before it)", rotated.Install, issued.Token)

	rec = do(t, srv, http.MethodGet, "/api/hostds/ws-1/install", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/hostds/ws-1/install = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), rotated.Token) {
		t.Fatalf("GET /api/hostds/ws-1/install carries the token: %s", rec.Body.String())
	}
	var again ui.HostdInstall
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if len(again.Lines) != 1 || again.Lines[0] != want {
		t.Errorf("GET install = %+v, want the one line %s", again, want)
	}
}

// A registered provider's lines and note replace the default's, written
// from the hostd's name and owner and the origin the caller reached the
// daemon at; unregistering puts the default back.
func TestHostdInstallInstructionsFromARegisteredProvider(t *testing.T) {
	var got []ui.HostdInstallContext
	unregister := ui.RegisterHostdInstall(func(c ui.HostdInstallContext) ui.HostdInstallInstructions {
		got = append(got, c)
		return ui.HostdInstallInstructions{
			Lines: []string{
				"git clone https://git.example.com/grain-hostd && cd grain-hostd",
				"./install-hostd.sh --server " + c.Origin + " --name " + c.Name + " --owner " + c.Owner,
			},
			Note: "Run these as yourself, no sudo; prefix the last with GRAIN_HOSTD_TOKEN=<token>, or paste it when asked.",
		}
	})
	t.Cleanup(unregister)
	srv, _ := testServer(t)

	issued := issueHostd(t, srv, "ws-2", "bob@example.com")
	wantLines := []string{
		"git clone https://git.example.com/grain-hostd && cd grain-hostd",
		"./install-hostd.sh --server http://example.com --name ws-2 --owner bob@example.com",
	}
	if issued.Install == nil || strings.Join(issued.Install.Lines, "\n") != strings.Join(wantLines, "\n") {
		t.Fatalf("install = %+v, want lines %q", issued.Install, wantLines)
	}
	if !strings.Contains(issued.Install.Note, "no sudo") {
		t.Errorf("install note = %q, want the provider's", issued.Install.Note)
	}
	for _, l := range issued.Install.Lines {
		if strings.Contains(l, "sudo") {
			t.Errorf("line %q is the default's, not the provider's", l)
		}
	}
	assertNoToken(t, "create", issued.Install, issued.Token)
	if len(got) != 1 || got[0].Origin != "http://example.com" || got[0].Name != "ws-2" || got[0].Owner != "bob@example.com" {
		t.Errorf("provider was told %+v", got)
	}

	rec := do(t, srv, http.MethodGet, "/api/hostds/ws-2/install", "")
	var again ui.HostdInstall
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &again) != nil || len(again.Lines) != 2 || again.Note == "" {
		t.Errorf("GET /api/hostds/ws-2/install = %d %s, want the provider's lines and note", rec.Code, rec.Body.String())
	}

	unregister()
	rec = do(t, srv, http.MethodGet, "/api/hostds/ws-2/install", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "grain-hostd-install --name ws-2") || strings.Contains(rec.Body.String(), "no sudo") {
		t.Errorf("after unregistering, GET install = %d %s, want the default line", rec.Code, rec.Body.String())
	}
}

// A provider that hands back no lines leaves the default's in place
// (TestHostdInstallForDropsInstructionsCarryingTheToken covers one whose
// lines would carry the token).
func TestHostdInstallInstructionsWithoutLinesKeepTheDefault(t *testing.T) {
	srv, _ := testServer(t)
	unregister := ui.RegisterHostdInstall(func(c ui.HostdInstallContext) ui.HostdInstallInstructions {
		return ui.HostdInstallInstructions{Note: "nothing to run"}
	})
	issued := issueHostd(t, srv, "ws-3", "")
	unregister()
	if issued.Install == nil || len(issued.Install.Lines) != 1 || !strings.HasPrefix(issued.Install.Lines[0], "sudo sh -c") || issued.Install.Note != "" {
		t.Errorf("a provider with no lines gave %+v, want the default line", issued.Install)
	}

	if issued.Install != nil {
		assertNoToken(t, "a provider with no lines", issued.Install, issued.Token)
	}
}

// GET .../install answers 404 for an unknown hostd and 400 for the
// co-located one and a revoked one.
func TestHostdInstallInstructionsRefuseWhatIsNotInstallable(t *testing.T) {
	srv, client := testServer(t)
	if err := client.Store.EnsureLocalHostd(context.Background()); err != nil {
		t.Fatal(err)
	}
	issueHostd(t, srv, "ws-4", "")
	if rec := do(t, srv, http.MethodPost, "/api/hostds/ws-4/revoke", ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	for path, want := range map[string]int{
		"/api/hostds/nope/install":  http.StatusNotFound,
		"/api/hostds/local/install": http.StatusBadRequest,
		"/api/hostds/ws-4/install":  http.StatusBadRequest,
	} {
		if rec := do(t, srv, http.MethodGet, path, ""); rec.Code != want {
			t.Errorf("GET %s = %d %s, want %d", path, rec.Code, rec.Body.String(), want)
		}
	}
}
