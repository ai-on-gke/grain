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

// The controller-local config repository API and the one property that
// matters most: a UI with no repository behind it says so rather than
// erroring on every call.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeStateRepo records what the handler asked for, so a test can assert
// on the call rather than on a real clone.
type fakeStateRepo struct {
	status      ui.StateRepoStatus
	importedKey string
	synced      bool
	keyError    error
	// remotes is every SetRemote, in order.
	remotes   []ui.StateRepoRemoteRequest
	remoteErr error
	// secretsRepos is every SetSecretsRepo, in order, so a test can tell
	// "named nothing" from "was never asked".
	secretsRepos   []ui.SecretsRepoRequest
	secretsRepoErr error
	// dismissals counts DismissResetNotices, and dismissErr is what it
	// fails with when a test is asking what the pane does with a
	// dismissal that did not happen.
	dismissals int
	dismissErr error
	// tree and files are what the browser reads, and fileErr what one
	// path answers with instead -- the refusals are the manager's to
	// make (pkg/staterepo), and what is asserted here is what this API
	// does with one.
	tree      ui.StateRepoTree
	files     map[string]ui.StateRepoFile
	fileErr   error
	readPaths []string
	// readLimits records the bound each read was asked for, so a test
	// can tell a limit that reached the manager from one the handler
	// dropped.
	readLimits []int64
}

func (f *fakeStateRepo) Status(context.Context) (ui.StateRepoStatus, error) { return f.status, nil }

func (f *fakeStateRepo) ImportSecretsKey(_ context.Context, key string) (ui.StateRepoStatus, error) {
	if f.keyError != nil {
		return ui.StateRepoStatus{}, f.keyError
	}
	f.importedKey = key
	f.status.SecretsError = ""
	return f.status, nil
}

func (f *fakeStateRepo) SetRemote(_ context.Context, req ui.StateRepoRemoteRequest) (ui.StateRepoStatus, error) {
	if f.remoteErr != nil {
		return ui.StateRepoStatus{}, f.remoteErr
	}
	f.remotes = append(f.remotes, req)
	f.status.Remote = req.Remote
	if req.Branch != "" {
		f.status.Branch = req.Branch
	}
	return f.status, nil
}

func (f *fakeStateRepo) SetSecretsRepo(_ context.Context, req ui.SecretsRepoRequest) (ui.StateRepoStatus, error) {
	if f.secretsRepoErr != nil {
		return ui.StateRepoStatus{}, f.secretsRepoErr
	}
	f.secretsRepos = append(f.secretsRepos, req)
	f.status.SecretsRepo, f.status.SecretsRepoBranch = req.Remote, req.Branch
	return f.status, nil
}

func (f *fakeStateRepo) DismissResetNotices(context.Context) (ui.StateRepoStatus, error) {
	if f.dismissErr != nil {
		return ui.StateRepoStatus{}, f.dismissErr
	}
	f.dismissals++
	f.status.ResetNotices, f.status.ResetNoticesDropped = nil, 0
	return f.status, nil
}

func (f *fakeStateRepo) Sync(context.Context) (ui.StateRepoStatus, error) {
	f.synced = true
	return f.status, nil
}

func (f *fakeStateRepo) Tree(context.Context) (ui.StateRepoTree, error) { return f.tree, nil }

func (f *fakeStateRepo) File(_ context.Context, path string, limit int64) (ui.StateRepoFile, error) {
	f.readPaths = append(f.readPaths, path)
	f.readLimits = append(f.readLimits, limit)
	if f.fileErr != nil {
		return ui.StateRepoFile{}, f.fileErr
	}
	file, ok := f.files[path]
	if !ok {
		return ui.StateRepoFile{}, &ui.StateRepoPathMissing{Reason: path + ": no such file"}
	}
	return file, nil
}

func stateRepoServer(t *testing.T, fake *fakeStateRepo) *ui.Server {
	t.Helper()
	_, client := testServer(t)
	storeSettingsRow(t, client)
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = credentials
	client.Config.StateRepo = fake
	return ui.NewServerWithClient(client)
}

func TestStateRepoStatusReportsWhereStateLives(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{
		Branch: "main", Head: "abc123", SchemaVersion: 16, BuildSchemaVersion: 16,
		SecretsPublicKey: "grain-secret-pub-v1:AAAA", SecretsKeyFile: "/var/lib/grain/data/secrets/secrets.key",
	}}
	rec := do(t, stateRepoServer(t, fake), http.MethodGet, "/api/state-repo", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[ui.StateRepoStatus](t, rec)
	if !got.Available || got.Branch != "main" || got.Head != "abc123" {
		t.Fatalf("got %+v", got)
	}
	// The public key is safe to show and is how an operator checks the key
	// they hold is the one this deployment encrypts to. Nothing else about
	// a secret ever comes back through here.
	if got.SecretsPublicKey == "" || got.SecretsKeyFile == "" {
		t.Fatalf("the pane cannot tell an operator which key to back up: %+v", got)
	}
}

func TestStateRepoIsUnavailableWithoutOne(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	rec := do(t, srv, http.MethodGet, "/api/state-repo", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.StateRepoStatus](t, rec); got.Available {
		t.Fatal("a UI with no state repository claimed to have one")
	}
	if rec := do(t, srv, http.MethodPost, "/api/state-repo", `{"mode":"remote"}`); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("removed mirror endpoint returned status %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodPost, "/api/state-repo/secrets-repo",
		`{"remote":"https://github.com/owner/private-secrets"}`); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// The other half of moving a deployment onto this host: a restore
// brings the sealed file, and this brings the key that opens it.
func TestBootstrapImportsASecretsKey(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{
		SecretsError: "secrets: this file is encrypted to a different key",
	}}
	rec := do(t, stateRepoServer(t, fake), http.MethodPost, "/api/state-repo/secrets-key",
		`{"key":"grain-secret-key-v1:AAAA"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if fake.importedKey != "grain-secret-key-v1:AAAA" {
		t.Fatalf("the key did not reach the manager: %q", fake.importedKey)
	}
	if body := rec.Body.String(); strings.Contains(body, "grain-secret-key-v1:AAAA") {
		t.Fatalf("the private key came back in the response: %s", body)
	}
	if got := decode[ui.StateRepoStatus](t, rec); got.SecretsError != "" {
		t.Fatalf("the store still reports itself unreadable: %+v", got)
	}
}

func TestAnEmptySecretsKeyIsRefused(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{}
	rec := do(t, stateRepoServer(t, fake), http.MethodPost, "/api/state-repo/secrets-key", `{"key":"  "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if fake.importedKey != "" {
		t.Fatal("an empty key reached the manager")
	}
}

// A key that cannot open the file is refused by the manager, and the
// operator has to be told which one it wanted -- not left with a store
// that fails the next time a run asks it for a credential.
func TestARefusedSecretsKeySaysWhy(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{keyError: errors.New(
		"secrets: this file is encrypted to a different key: secrets.enc is encrypted to grain-secret-pub-v1:BBBB")}
	rec := do(t, stateRepoServer(t, fake), http.MethodPost, "/api/state-repo/secrets-key",
		`{"key":"grain-secret-key-v1:AAAA"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "grain-secret-pub-v1:BBBB") {
		t.Fatalf("the failure does not say which key the file wants: %s", rec.Body)
	}
}

func TestTheStateRepositoryMirrorCanBeNamedAndCleared(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	srv := stateRepoServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/state-repo/remote",
		`{"remote":"  https://github.com/owner/grain-config  ","branch":"trunk","token":"ghp_m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[ui.StateRepoStatus](t, rec)
	if got.Remote != "https://github.com/owner/grain-config" || got.Branch != "trunk" {
		t.Fatalf("the pane is not told where the mirror now goes: %+v", got)
	}
	if len(fake.remotes) != 1 {
		t.Fatalf("the manager saw %+v", fake.remotes)
	}
	if req := fake.remotes[0]; req.Remote != "https://github.com/owner/grain-config" ||
		req.Branch != "trunk" || req.Token != "ghp_m" {
		t.Fatalf("the manager was asked for %+v", req)
	}

	rec = do(t, srv, http.MethodPost, "/api/state-repo/remote", `{"none":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(fake.remotes) != 2 || fake.remotes[1].Remote != "" {
		t.Fatalf("the manager was asked for %+v", fake.remotes)
	}
}

func TestExternalConfigRepoRefusesSettingsMutationsAndExposesMode(t *testing.T) {
	t.Parallel()
	const extRemote = "https://github.com/acme/grain-config.git"
	fake := &fakeStateRepo{status: ui.StateRepoStatus{
		Remote: extRemote,
		Branch: "trunk",
	}}
	srv := stateRepoServer(t, fake)

	// GET /api/state-repo normalizes Mode and Repository.
	rec := do(t, srv, http.MethodGet, "/api/state-repo", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/state-repo status %d: %s", rec.Code, rec.Body)
	}
	gotStatus := decode[ui.StateRepoStatus](t, rec)
	if gotStatus.Mode != "external" || gotStatus.Repository != "acme/grain-config" {
		t.Fatalf("GET /api/state-repo = %+v, want Mode=external Repository=acme/grain-config", gotStatus)
	}

	// GET /api/settings and GET /api/config expose externalConfigRepo and externalConfigBranch.
	rec = do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings status %d: %s", rec.Code, rec.Body)
	}
	gotSettings := decode[ui.Settings](t, rec)
	if gotSettings.ExternalConfigRepo != extRemote || gotSettings.ExternalConfigBranch != "trunk" {
		t.Fatalf("GET /api/settings external config = (%q, %q), want (%q, trunk)",
			gotSettings.ExternalConfigRepo, gotSettings.ExternalConfigBranch, extRemote)
	}

	// PUT /api/settings and POST /api/settings are refused with 409 Conflict.
	for _, method := range []string{http.MethodPut, http.MethodPost} {
		rec = do(t, srv, method, "/api/settings", `{"environmentName":"prod"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s /api/settings status %d (want 409): %s", method, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "read-only") || !strings.Contains(rec.Body.String(), extRemote) {
			t.Fatalf("%s /api/settings body = %s, want read-only message naming %s", method, rec.Body, extRemote)
		}
	}

	// Mutating repo-exported tables and settings endpoints is refused with
	// 409 Conflict. Edits of rows that exist, and the file and patch
	// each refusal carries, are config_refusal_test.go's.
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/templates", `{"name":"t1","title":"T1"}`},
		{http.MethodPost, "/api/boards", `{"name":"b1","title":"B1"}`},
		{http.MethodPost, "/api/console-sessions", `{"name":"cs1","title":"CS1"}`},
		{http.MethodPut, "/api/console-sessions/director", `{"title":"Director 2"}`},
		{http.MethodDelete, "/api/console-sessions/director", ``},
		{http.MethodPut, "/api/deployment/repos/acme/widgets/config", `{"maxConcurrent":2}`},
		{http.MethodPut, "/api/repos/acme/widgets/settings", `{"maxConcurrent":2}`},
		{http.MethodPut, "/api/repos/acme/widgets/workflows", `{"workflows":[]}`},
		{http.MethodPut, "/api/repos/acme/widgets/qualification-plan", `{"items":[]}`},
		{http.MethodPost, "/api/deployment/git-connections", `{"host":"github.com","type":"pat","token":"ghp_test"}`},
		{http.MethodDelete, "/api/deployment/git-connections/gh-main", ``},
		{http.MethodPost, "/api/gcp-keys", `{"name":"prod","projectId":"p1","serviceAccountEmail":"sa@p1.iam.gserviceaccount.com"}`},
		{http.MethodPut, "/api/gcp-keys/prod", `{"name":"prod","projectId":"p1","serviceAccountEmail":"sa@p1.iam.gserviceaccount.com"}`},
		{http.MethodDelete, "/api/gcp-keys/prod", ``},
	} {
		rec = do(t, srv, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s %s status %d (want 409): %s", tc.method, tc.path, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "read-only") || !strings.Contains(rec.Body.String(), extRemote) {
			t.Fatalf("%s %s body = %s, want read-only message naming %s", tc.method, tc.path, rec.Body, extRemote)
		}
	}

	// PUT /api/github-tokens/{name} writes the host-local credential file without
	// auto-bootstrapping a repo-managed GitConnections row when in external mode.
	rec = do(t, srv, http.MethodPut, "/api/github-tokens/ext-tok", `{"value":"ghp_ext123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/github-tokens/ext-tok status %d: %s", rec.Code, rec.Body)
	}
	if row := tokenRow(t, tokenBody(t, rec), "ext-tok"); row == nil || row["default"] == true {
		t.Fatalf("PUT /api/github-tokens/ext-tok in external mode mutated default GitConnections: %+v", row)
	}

	// POST /api/state-repo/import-key and POST /api/state-repo/remote remain allowed.
	rec = do(t, srv, http.MethodPost, "/api/state-repo/import-key", `{"key":"grain-secret-key-v1:AAAA"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/state-repo/import-key status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodPost, "/api/state-repo/remote", `{"none":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/state-repo/remote status %d: %s", rec.Code, rec.Body)
	}

	// Once back in internal mode, POST /api/settings writes a bootstrap
	// field directly, and refuses any other with the edit to make in
	// grain/config instead.
	rec = do(t, srv, http.MethodPost, "/api/settings", `{"maxWorkers":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/settings (bootstrap) in internal mode status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodPost, "/api/settings", `{"environmentName":"prod"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"repo":"grain/config"`) ||
		!strings.Contains(rec.Body.String(), `+    \"environment_name\": \"prod\"`) {
		t.Fatalf("POST /api/settings (non-bootstrap) in internal mode status %d: %s", rec.Code, rec.Body)
	}
}

func TestClearingTheStateRepositoryMirrorMustBeAskedForOutright(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{}
	rec := do(t, stateRepoServer(t, fake), http.MethodPost, "/api/state-repo/remote", `{"remote":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(fake.remotes) != 0 {
		t.Fatalf("an empty remote reached the manager: %+v", fake.remotes)
	}
}

func TestTheSecretsRepositoryCanBeNamedAndCleared(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{}}
	srv := stateRepoServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/state-repo/secrets-repo",
		`{"remote":"  https://github.com/owner/private-secrets  ","branch":"main","token":"ghp_x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.StateRepoStatus](t, rec); got.SecretsRepo != "https://github.com/owner/private-secrets" {
		t.Fatalf("the pane is not told where the file now goes: %+v", got)
	}
	if len(fake.secretsRepos) != 1 {
		t.Fatalf("the manager saw %+v", fake.secretsRepos)
	}
	// Trimmed on the way through, like every other pasted URL here: an
	// operator who copied a trailing space has not named a different
	// repository.
	if got := fake.secretsRepos[0]; got.Remote != "https://github.com/owner/private-secrets" ||
		got.Branch != "main" || got.Token != "ghp_x" {
		t.Fatalf("the manager was asked for %+v", got)
	}

	// And stopping, which has to be possible from the same pane: a
	// repository named by mistake is one an operator has to be able to
	// take back without a terminal.
	rec = do(t, srv, http.MethodPost, "/api/state-repo/secrets-repo", `{"none":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(fake.secretsRepos) != 2 || fake.secretsRepos[1].Remote != "" {
		t.Fatalf("the manager was asked for %+v", fake.secretsRepos)
	}
}

// The mistake worth being careful about: a body that lost its remote
// field is not an operator asking for their only off-host copy of every
// credential to stop being written.
func TestClearingTheSecretsRepositoryMustBeAskedForOutright(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{}
	rec := do(t, stateRepoServer(t, fake), http.MethodPost, "/api/state-repo/secrets-repo", `{"remote":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(fake.secretsRepos) != 0 {
		t.Fatalf("an empty remote reached the manager: %+v", fake.secretsRepos)
	}
}

func TestSecretsRepositoryErrorsReachTheCaller(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{secretsRepoErr: errors.New("cannot save secrets repository")}
	rec := do(t, stateRepoServer(t, fake), http.MethodPost, "/api/state-repo/secrets-repo",
		`{"remote":"https://github.com/owner/grain-secrets"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "cannot save") {
		t.Fatalf("the error does not say why: %s", rec.Body)
	}
}

// Saying "this is deliberate" is a preference about how loudly the rest
// of the UI mentions a local-only deployment, so it has a route of its
// own: it takes back nothing and adopts nothing, and it must be possible
// to withdraw.
func TestTheStatusCarriesAResetNobodyCouldBeToldAbout(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 7, 10, 15, 0, 0, time.UTC)
	fake := &fakeStateRepo{status: ui.StateRepoStatus{ResetNotices: []ui.ResetNotice{{
		At:     at,
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Reason: "this deployment's state repository is local only, so there is nowhere to say it",
		Values: []ui.ResetValue{{
			Path: "tables/grain_config.json", Column: "time_zone", Row: "id = 1",
			From: "'Europe/London'", To: "'America/Los_Angeles'",
		}},
		MoreValues: 4,
	}}}}
	rec := do(t, stateRepoServer(t, fake), http.MethodGet, "/api/state-repo", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[ui.StateRepoStatus](t, rec)
	if len(got.ResetNotices) != 1 {
		t.Fatalf("the pane is not told what the merge took away: %+v", got)
	}
	notice := got.ResetNotices[0]
	// The values, because they exist nowhere else a reader will find
	// them; the reason, because it is the pane's whole answer to "why am
	// I being shown this here"; and the count past the cap, so a reader
	// can tell the whole of it from the start of it.
	if len(notice.Values) != 1 || notice.Values[0].From != "'Europe/London'" {
		t.Errorf("the values did not survive the round trip: %+v", notice)
	}
	if notice.Reason == "" || notice.MoreValues != 4 || !notice.At.Equal(at) {
		t.Errorf("the notice cannot say why, when, or how much is missing: %+v", notice)
	}
	// Not a failure: this one is a deployment working exactly as it was
	// set up to, and the pane says a different thing about those.
	if notice.Failed {
		t.Errorf("having nowhere to post a notice arrived as something going wrong: %+v", notice)
	}
}

// Dismissing is a person saying they have read it, and it is a delete:
// there is no field to send and nothing to withdraw, which is the whole
// difference between this and the local-only box above -- one is an
// event that happened, the other a condition that is still true.
func TestAResetNoticeCanBeDismissed(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{ResetNotices: []ui.ResetNotice{{
		Commit: "abc123", Reason: "there was nowhere to say it",
	}}}}
	srv := stateRepoServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/state-repo/reset-notice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if fake.dismissals != 1 {
		t.Fatalf("the dismissal did not reach the manager: %d", fake.dismissals)
	}
	// The pane redraws from what comes back, so it has to come back
	// empty rather than the browser having to guess.
	if got := decode[ui.StateRepoStatus](t, rec); len(got.ResetNotices) != 0 {
		t.Fatalf("the dismissed notice came back: %+v", got)
	}
}

// A dismissal that did not happen must not read as one. The marker is on
// the daemon's disk, so a failure here is the notice coming back on the
// next poll with nothing to say why -- unless this says so.
func TestADismissalThatFailedSaysSo(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{dismissErr: errors.New("read-only file system")}
	rec := do(t, stateRepoServer(t, fake), http.MethodPost, "/api/state-repo/reset-notice", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	// It says the dismissal failed and where to look; the filesystem's
	// own error stays in the daemon's log (bwsalmon/grain#2424).
	if strings.Contains(rec.Body.String(), "read-only") || !strings.Contains(rec.Body.String(), `"ref":`) {
		t.Fatalf("the failure does not carry a log reference, or leaks its cause: %s", rec.Body)
	}
}

func TestSyncNowGoesThroughTheManager(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{}
	rec := do(t, stateRepoServer(t, fake), http.MethodPost, "/api/state-repo/sync", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !fake.synced {
		t.Fatal("the sync did not reach the manager")
	}
}

// Reading the repository from the UI. This is the only way a person who
// runs a deployment through a browser can see its exported
// configuration, or the memory prose every run in a scope is told before
// it starts: the repository is controller-local, so there is no forge
// page for it and no remote to point a browser at.

func TestStateRepoFilesListsTheExportedContent(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{tree: ui.StateRepoTree{Head: "ff335f5", Entries: []ui.StateRepoEntry{
		{Path: "README.md", Size: 412},
		{Path: "memory", Dir: true},
		{Path: "memory/deployment.md", Size: 3872},
		{Path: "tables", Dir: true},
		{Path: "tables/grain_config.json", Size: 2048},
	}}}
	rec := do(t, stateRepoServer(t, fake), http.MethodGet, "/api/state-repo/files", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	got := decode[ui.StateRepoTree](t, rec)
	if len(got.Entries) != 5 || got.Entries[0].Path != "README.md" {
		t.Fatalf("got %+v", got)
	}
	// Which commit the listing was read at, so the pane can say what is
	// on screen rather than leaving a reader to assume it is current.
	if got.Head != "ff335f5" {
		t.Errorf("the listing does not say which commit it is of: %+v", got)
	}
	if !strings.Contains(body, `"dir":true`) {
		t.Errorf("a directory is not marked as one: %s", body)
	}
}

func TestStateRepoFileIsReadThroughTheManager(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{files: map[string]ui.StateRepoFile{
		"memory/repos/bwsalmon/grain.md": {
			Path: "memory/repos/bwsalmon/grain.md", Size: 24,
			Content: "what grain has learned\n", Head: "ff335f5",
		},
	}}
	srv := stateRepoServer(t, fake)
	rec := do(t, srv, http.MethodGet,
		"/api/state-repo/file?path=memory%2Frepos%2Fbwsalmon%2Fgrain.md", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[ui.StateRepoFile](t, rec)
	if got.Content != "what grain has learned\n" || got.Head != "ff335f5" {
		t.Fatalf("got %+v", got)
	}
	// The path reaches the manager as it was asked for: this API does
	// no cleaning of its own, because a request rewritten here and
	// checked there is exactly how a browser ends up serving something
	// nobody allowed.
	if len(fake.readPaths) != 1 || fake.readPaths[0] != "memory/repos/bwsalmon/grain.md" {
		t.Fatalf("the manager was asked for %v", fake.readPaths)
	}
	// And a bound, whether or not the caller asked for one.
	if len(fake.readLimits) != 1 || fake.readLimits[0] <= 0 {
		t.Fatalf("the read was unbounded: %v", fake.readLimits)
	}
}

// A file bigger than the bound comes back cut and says it was cut,
// rather than looking like the whole of it.
func TestStateRepoFileReportsTruncation(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{files: map[string]ui.StateRepoFile{
		"tables/task.json": {Path: "tables/task.json", Size: 4 << 20, Content: "[{", Truncated: true},
	}}
	rec := do(t, stateRepoServer(t, fake), http.MethodGet,
		"/api/state-repo/file?path=tables/task.json&limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[ui.StateRepoFile](t, rec)
	if !got.Truncated || got.Size != 4<<20 {
		t.Fatalf("a cut file came back as %+v", got)
	}
	if fake.readLimits[0] != 2 {
		t.Errorf("the caller's limit did not reach the manager: %v", fake.readLimits)
	}
}

// A limit past what this API will serve is capped rather than honoured:
// the bound is the server's, not the caller's.
func TestStateRepoFileCapsTheLimitItIsAsked(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{files: map[string]ui.StateRepoFile{"README.md": {Path: "README.md"}}}
	rec := do(t, stateRepoServer(t, fake), http.MethodGet,
		"/api/state-repo/file?path=README.md&limit=999999999", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if fake.readLimits[0] > 1<<20 {
		t.Fatalf("an unbounded read reached the manager: %v", fake.readLimits)
	}
}

// The refusal. .git, a secrets path, "..", a symlink out of the tree:
// the manager decides, and what this API owes the reader is a 403 --
// not a 404 -- carrying the sentence that says what is browsable
// instead.
func TestStateRepoFileRefusalSaysWhatIsBrowsable(t *testing.T) {
	t.Parallel()
	const reason = `".git/config" is not part of grain's exported configuration; ` +
		"this repository is browsable at README.md, schema-version and under memory/, scripts/, tables/, workflows/"
	fake := &fakeStateRepo{fileErr: &ui.StateRepoPathRefused{Reason: reason}}
	for _, path := range []string{".git/config", "secrets/secrets.key", "../../etc/passwd", "tables/../.git/config"} {
		rec := do(t, stateRepoServer(t, fake), http.MethodGet,
			"/api/state-repo/file?path="+url.QueryEscape(path), "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("asking for %s: status %d: %s", path, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "browsable at README.md") {
			t.Fatalf("asking for %s does not say what is browsable: %s", path, rec.Body)
		}
	}
}

// And a path inside the browsable set that is not there is the other
// answer, because it is a different fact: a table this deployment has
// never written is nobody's refusal.
func TestStateRepoFileThatIsNotThereIsNotARefusal(t *testing.T) {
	t.Parallel()
	rec := do(t, stateRepoServer(t, &fakeStateRepo{}), http.MethodGet,
		"/api/state-repo/file?path=tables/no_such_table.json", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestStateRepoFileNeedsAPath(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{}
	rec := do(t, stateRepoServer(t, fake), http.MethodGet, "/api/state-repo/file?path=%20", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(fake.readPaths) != 0 {
		t.Fatalf("an empty path reached the manager: %v", fake.readPaths)
	}
}

// Browsing is read-only: there is no route that writes one of these
// files, because changing any of it is a pull request against
// grain/config and that is the deployment's own model.
func TestBrowsingTheStateRepoIsReadOnly(t *testing.T) {
	t.Parallel()
	srv := stateRepoServer(t, &fakeStateRepo{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := do(t, srv, method, "/api/state-repo/file?path=README.md", `{"content":"x"}`)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s on a file: status %d: %s", method, rec.Code, rec.Body)
		}
		rec = do(t, srv, method, "/api/state-repo/files", `{}`)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s on the listing: status %d: %s", method, rec.Code, rec.Body)
		}
	}
}

// A UI with no repository behind it says so here too, rather than
// erroring differently on this pair of routes than on every other.
func TestBrowsingWithoutAStateRepositorySaysSo(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	storeSettingsRow(t, client)
	srv := ui.NewServerWithClient(client)
	for _, path := range []string{"/api/state-repo/files", "/api/state-repo/file?path=README.md"} {
		rec := do(t, srv, http.MethodGet, path, "")
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body)
		}
	}
}
