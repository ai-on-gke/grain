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

package orchestrator

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fixtureForge is one GitHub repository's tags, release notes and
// workflow dispatches, served at the REST level so the real
// github.RESTClient reads it.
type fixtureForge struct {
	mu         sync.Mutex
	repo       string                // owner/name
	tags       map[string]fixtureTag // name -> tag
	files      map[string]string     // path on main -> contents
	dispatches []map[string]any      // each dispatch's body
	reads      int                   // matching-refs reads
}

type fixtureTag struct {
	commit  string
	message string // "" for a lightweight tag
}

var (
	matchingRefsPath = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/git/matching-refs/tags/([^?]*)`)
	tagObjectPath    = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/git/tags/(.+)$`)
	contentsPath     = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/contents/([^?]+)\?ref=main$`)
	dispatchPath     = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/actions/workflows/([^/]+)/dispatches$`)
)

func (f *fixtureForge) Request(method, path string, _ map[string]string, body []byte) (github.ApiResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	jsonResp := func(v any) (github.ApiResponse, error) {
		b, _ := json.Marshal(v)
		return github.ApiResponse{Status: http.StatusOK, Body: b}, nil
	}
	notFound := github.ApiResponse{Status: http.StatusNotFound, Body: []byte(`{"message":"Not Found"}`)}
	switch {
	case method == "GET" && matchingRefsPath.MatchString(path):
		m := matchingRefsPath.FindStringSubmatch(path)
		if m[1] != f.repo {
			return notFound, nil
		}
		f.reads++
		var out []map[string]any
		for name, t := range f.tags {
			if !strings.HasPrefix(name, m[2]) {
				continue
			}
			obj := map[string]string{"sha": t.commit, "type": "commit"}
			if t.message != "" {
				obj = map[string]string{"sha": "tagobj-" + name, "type": "tag"}
			}
			out = append(out, map[string]any{"ref": "refs/tags/" + name, "object": obj})
		}
		return jsonResp(out)
	case method == "GET" && tagObjectPath.MatchString(path):
		m := tagObjectPath.FindStringSubmatch(path)
		name, ok := strings.CutPrefix(m[2], "tagobj-")
		t, found := f.tags[name]
		if m[1] != f.repo || !ok || !found {
			return notFound, nil
		}
		return jsonResp(map[string]any{"message": t.message, "object": map[string]string{"sha": t.commit, "type": "commit"}})
	case method == "GET" && contentsPath.MatchString(path):
		m := contentsPath.FindStringSubmatch(path)
		text, ok := f.files[m[2]]
		if m[1] != f.repo || !ok {
			return notFound, nil
		}
		return github.ApiResponse{Status: http.StatusOK, Body: []byte(text)}, nil
	case method == "POST" && dispatchPath.MatchString(path):
		m := dispatchPath.FindStringSubmatch(path)
		if m[1] != f.repo || m[2] != "promote-release.yml" {
			return notFound, nil
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			return github.ApiResponse{Status: http.StatusUnprocessableEntity}, nil
		}
		f.dispatches = append(f.dispatches, req)
		return github.ApiResponse{Status: http.StatusNoContent}, nil
	}
	return notFound, nil
}

func (f *fixtureForge) setTag(name, commit, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags[name] = fixtureTag{commit: commit, message: message}
}

func (f *fixtureForge) deleteTag(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tags, name)
}

func (f *fixtureForge) dispatched() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.dispatches...)
}

func resetReleaseWatch(t *testing.T) {
	t.Helper()
	clear := func() {
		releasePendingPolled.Range(func(k, _ any) bool { releasePendingPolled.Delete(k); return true })
	}
	clear()
	t.Cleanup(clear)
}

// The whole of gap 4 on a fixture repository that releases from GitHub
// Actions: a -pending tag raises the notification with the release
// notes, a person's Approve dispatches promote-release.yml once with a
// record the deployment signed, an agent and a schedule are refused, and
// the notification clears when the release is tagged.
func TestAPendingTagOnAGitHubActionsRepoIsApprovedFromTheInbox(t *testing.T) {
	resetReleaseWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "example", Name: "widgets"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	forge := &fixtureForge{repo: repo.String(), tags: map[string]fixtureTag{
		"v1.1.0":         {commit: strings.Repeat("b", 40)},
		"v1.2.0-rc.3":    {commit: commit, message: "rc"},
		"v1.2.0-pending": {commit: commit, message: "Release v1.2.0 pending approval\n\nQualification: PASS"},
	}, files: map[string]string{"docs/releases/v1.2.0.md": "# v1.2.0\n\n- Widgets now spin."}}
	client := github.NewClient(forge, nil)
	deps := Deps{Store: store, Client: client}
	now := time.Now().UTC()
	subject := model.NotificationSubject(model.FamilyReleasePending, "v1.2.0")

	// 1. The -pending tag raises release.pending, with the notes.
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("no open %s after the watcher saw v1.2.0-pending (err %v)", subject, err)
	}
	if n.Repo != repo.String() || n.Title != "v1.2.0 is ready to promote" || strings.Join(n.Actions, ",") != "approve-release,reject-release" {
		t.Errorf("notification = %+v, want example/widgets' v1.2.0 with Approve and Reject", n)
	}
	for _, want := range []string{"Widgets now spin.", "Qualification: PASS", "docs/releases/v1.2.0.md"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("body does not say %q:\n%s", want, n.Body)
		}
	}
	// Within the poll interval nothing is read; after it, nothing is
	// raised again over the open one.
	reads := forge.reads
	_ = reconcileReleasePending(ctx, deps, now.Add(time.Minute))
	if forge.reads != reads {
		t.Errorf("the tags were read again within the poll interval")
	}
	_ = reconcileReleasePending(ctx, deps, now.Add(releasePendingPollInterval))
	if again, _ := store.OpenNotification(ctx, subject); again == nil || again.RaiseCount != 1 {
		t.Errorf("a second look raised it again: %+v", again)
	}

	person := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}
	uic := ui.NewClient(ui.Config{Actor: person, DataDir: t.TempDir(), GitHubPullRequests: client}, store)

	// 2. Neither an agent nor a schedule's automation may answer it.
	for _, p := range []model.Principal{
		{Kind: model.PrincipalAgent, ID: "run-7"},
		{Kind: model.PrincipalAutomation, ID: "schedule:release-train"},
		{Kind: model.PrincipalService, ID: "steward"},
	} {
		if _, err := uic.DecideRelease(ui.WithPrincipal(ctx, p), repo.String(), "v1.2.0", true, ui.ReleaseApproval{}); err == nil ||
			!strings.Contains(err.Error(), "only a person may approve") {
			t.Errorf("%s approving: %v, want refused", model.PrincipalString(p), err)
		}
	}
	if d := forge.dispatched(); len(d) != 0 {
		t.Fatalf("a non-person's approval dispatched: %v", d)
	}

	// 3. A person's Approve dispatches promote-release.yml on main, once.
	decision, err := uic.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v1.2.0", true, ui.ReleaseApproval{})
	if err != nil {
		t.Fatalf("a person's approval: %v", err)
	}
	if decision.Dispatched == nil || decision.Dispatched.Commit != commit || decision.Dispatched.By != person.ID {
		t.Errorf("decision = %+v, want a dispatch for %s by %s", decision, commit, person.ID)
	}
	if _, err := uic.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v1.2.0", true, ui.ReleaseApproval{}); err == nil {
		t.Error("a second press was accepted; want it refused while the first dispatch's record is good")
	}
	d := forge.dispatched()
	if len(d) != 1 {
		t.Fatalf("dispatched %d times, want once", len(d))
	}
	inputs, _ := d[0]["inputs"].(map[string]any)
	if d[0]["ref"] != "main" || inputs["version"] != "v1.2.0" || inputs["decision"] != "approve" || inputs["short_soak_ok"] != "false" ||
		inputs["allow_breaking"] != "false" {
		t.Errorf("dispatch = %v, want v1.2.0 approve on main", d[0])
	}
	record := verifyDispatchedRecord(t, uic, inputs)
	for _, want := range []string{"repo: example/widgets\n", "version: v1.2.0\n", "decision: approve\n",
		"commit: " + commit + "\n", "approver: alice@example.com\n", "short_soak_ok: false\n", "allow_breaking: false\n"} {
		if !strings.Contains(record, want) {
			t.Errorf("the signed record does not say %q:\n%s", strings.TrimSpace(want), record)
		}
	}

	// The answer cleared the notification, and the watcher does not raise
	// it again while the promotion run has the -pending tag still there.
	if open, _ := store.OpenNotification(ctx, subject); open != nil {
		t.Errorf("%s still open after the approval", subject)
	}
	_ = reconcileReleasePending(ctx, deps, now.Add(2*releasePendingPollInterval))
	if open, _ := store.OpenNotification(ctx, subject); open != nil {
		t.Errorf("the watcher raised %s again right after a person answered it", subject)
	}

	// 4. The next release: raised, then cleared when vX.Y.0 appears.
	forge.setTag("v1.3.0-pending", strings.Repeat("d", 40), "Release v1.3.0 pending approval")
	_ = reconcileReleasePending(ctx, deps, now.Add(3*releasePendingPollInterval))
	next := model.NotificationSubject(model.FamilyReleasePending, "v1.3.0")
	if open, _ := store.OpenNotification(ctx, next); open == nil || !strings.Contains(open.Body, "has not merged yet") {
		t.Fatalf("v1.3.0-pending raised %+v, want an open notification saying its notes have not merged", open)
	}
	forge.setTag("v1.3.0", strings.Repeat("d", 40), "Release v1.3.0")
	forge.deleteTag("v1.3.0-pending")
	_ = reconcileReleasePending(ctx, deps, now.Add(4*releasePendingPollInterval))
	if open, _ := store.OpenNotification(ctx, next); open != nil {
		t.Errorf("%s still open after v1.3.0 was tagged", next)
	}
}

// verifyDispatchedRecord checks the dispatch's record against the
// deployment's public key, as scripts/verify-release-approval.sh does in
// the workflow, and returns its text.
func verifyDispatchedRecord(t *testing.T, uic *ui.Client, inputs map[string]any) string {
	t.Helper()
	keyPEM, err := uic.ReleaseApprovalKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		t.Fatal("the approval key is not PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	record, err := base64.StdEncoding.DecodeString(fmt.Sprint(inputs["approval"]))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(fmt.Sprint(inputs["approval_signature"]))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(record)
	if !ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), digest[:], sig) {
		t.Fatal("the dispatched record's signature does not verify against the deployment's key")
	}
	return string(record)
}

// A repository on grain CI is the train's own run to notify about; the
// watcher reads no tags there.
func TestTheReleaseWatchReadsOnlyGitHubActionsRepos(t *testing.T) {
	resetReleaseWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "example", Name: "gadgets"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	forge := &fixtureForge{repo: repo.String(), tags: map[string]fixtureTag{"v2.0.0-pending": {commit: "e"}}}
	if err := reconcileReleasePending(ctx, Deps{Store: store, Client: github.NewClient(forge, nil)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if forge.reads != 0 {
		t.Errorf("read a grain CI repository's tags %d times", forge.reads)
	}
}
