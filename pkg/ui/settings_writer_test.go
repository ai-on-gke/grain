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
	"bytes"
	"context"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// notedSyncStateRepo exports each write the way pkg/daemon's
// StateManager does, with the commit message pkg/ui hands it.
type notedSyncStateRepo struct {
	*fakeStateRepo
	repo   *staterepo.Repo
	client *ui.Client
	notes  []string
}

func (r *notedSyncStateRepo) SyncAfterWrite(ctx context.Context, note string) error {
	r.notes = append(r.notes, note)
	_, err := staterepo.SyncNoted(ctx, r.repo, r.client.Store.DB(), model.SchemaVersion, note)
	return err
}

// TestSettingsSaveByAVerifiedCallerRecordsWhoSavedIt pins the 2026-11-02
// verified-identity walkthrough's O1: alice@example.com, verified by
// IAP, saved gcpProject and got 200, and nothing anywhere said she had
// -- the daemon log named only the change, and the state repository's
// commit said "Update grain state". Both now name her, and a save with
// the access code names that instead of a person.
//
// Not parallel: it reads the standard logger's output.
func TestSettingsSaveByAVerifiedCallerRecordsWhoSavedIt(t *testing.T) {
	ctx := context.Background()
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, true)
	ui.SetIAPAudience(tunnelAudience)
	_, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}

	stateDir := filepath.Join(t.TempDir(), "state-repo")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: stateDir})
	if err != nil {
		t.Fatalf("staterepo.Open: %v", err)
	}
	if err := staterepo.Load(ctx, repo, client.Store.DB(), model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Load: %v", err)
	}
	syncer := &notedSyncStateRepo{
		fakeStateRepo: &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}},
		repo:          repo,
		client:        client,
	}
	client.Config.StateRepo = syncer
	srv := ui.NewServerWithClient(client)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	h := map[string]string{
		ui.IAPAssertionHeader: signIAPToken(t, priv, tunnelKid, tunnelAudience, "alice@example.com", "s", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute)),
		"Content-Type":        "application/json",
	}
	if rec := serveFrom(srv, "203.0.113.20:443", http.MethodPut, "/api/settings", `{"gcpProject":"my-proj-456"}`, h); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings as alice: %d %s", rec.Code, rec.Body)
	}

	if want := "settings saved by alice@example.com: changed GCPProject"; !strings.Contains(logs.String(), want) {
		t.Errorf("daemon log does not say who saved the setting; want %q in:\n%s", want, logs.String())
	}
	if len(syncer.notes) != 1 {
		t.Fatalf("SyncAfterWrite called %d times, want 1", len(syncer.notes))
	}
	subject := gitSubject(t, stateDir)
	if want := "Update grain state after a write by alice@example.com"; subject != want {
		t.Errorf("state repository commit subject = %q, want %q", subject, want)
	}
	files, err := exec.Command("git", "-C", stateDir, "show", "--name-only", "--format=", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files), "grain_config") {
		t.Errorf("alice's commit does not touch the settings: %s", files)
	}

	// The access code's holder is nobody grain can name, and is not
	// recorded as the deployment's configured actor either.
	logs.Reset()
	rec := do(t, srv, http.MethodPut, "/api/settings", `{"gcpProject":"my-proj-789"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings with the access code: %d %s", rec.Code, rec.Body)
	}
	if want := "settings saved by the access code: changed GCPProject"; !strings.Contains(logs.String(), want) {
		t.Errorf("want %q in:\n%s", want, logs.String())
	}
	if subject, want := gitSubject(t, stateDir), "Update grain state after a write by the access code"; subject != want {
		t.Errorf("state repository commit subject = %q, want %q", subject, want)
	}
}

func gitSubject(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%s").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	return strings.TrimSpace(string(out))
}
