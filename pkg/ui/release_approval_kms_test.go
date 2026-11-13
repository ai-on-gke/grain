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
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/releaseapproval"
	"github.com/ai-on-gke/grain/pkg/secrets/kmsfake"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A deployment that signs with a Cloud KMS key reports that key's public
// half as the GRAIN_RELEASE_APPROVAL_KEY value, and keeps no key of its
// own in its data directory.
func TestTheReleaseApprovalKeyIsTheKMSKeysWhenOneIsConfigured(t *testing.T) {
	fake := kmsfake.Start(t)
	const name = "projects/example-project/locations/global/keyRings/grain/cryptoKeys/release-approval/cryptoKeyVersions/7"
	c, _, _ := testClient(t)
	c.Config.DataDir = t.TempDir()
	c.Config.ReleaseApprovalKMSKey = name
	srv := ui.NewServerWithClient(c)

	got := decode[map[string]string](t, do(t, srv, http.MethodGet, "/api/release-approval-key", ""))
	want, _ := releaseapproval.PublicKeyPEM(fake.SigningKey(name))
	if got["publicKey"] != want {
		t.Fatalf("publicKey = %q, want KMS's key %q", got["publicKey"], want)
	}
	if got["variable"] != "GRAIN_RELEASE_APPROVAL_KEY" || got["note"] == "" {
		t.Fatalf("variable = %q, note = %q; want GRAIN_RELEASE_APPROVAL_KEY and a note on changing the key", got["variable"], got["note"])
	}
	if _, err := os.Stat(filepath.Join(c.Config.DataDir, releaseapproval.KeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s = %v; want no local key made beside a KMS one", releaseapproval.KeyFile, err)
	}
}

// A KMS key version the deployment may not read is answered 502 naming
// -release-approval-kms-key, not an opaque "internal server error", and
// what KMS said stays out of the answer (walkthrough 2026-11-13 §9, F2).
func TestAnUnreadableKMSReleaseApprovalKeyIsNamedInA502(t *testing.T) {
	fake := kmsfake.Start(t)
	fake.Deny(true)
	c, _, _ := testClient(t)
	c.Config.DataDir = t.TempDir()
	c.Config.ReleaseApprovalKMSKey = "projects/example-project/locations/global/keyRings/grain/cryptoKeys/denied/cryptoKeyVersions/1"
	srv := ui.NewServerWithClient(c)

	rec := do(t, srv, http.MethodGet, "/api/release-approval-key", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body)
	}
	got := decode[map[string]string](t, rec)
	if got["code"] != apierror.CodeUpstream || !strings.Contains(got["error"], "-release-approval-kms-key") {
		t.Fatalf("answer = %v; want code %s and an error naming -release-approval-kms-key", got, apierror.CodeUpstream)
	}
	if strings.Contains(got["error"], "denied") || strings.Contains(got["error"], "403") {
		t.Fatalf("answer %q carries KMS's own answer; want it only in the log", got["error"])
	}
}

type kmsTestReleaseForge struct {
	tags       map[string]string
	dispatches int
}

func (f *kmsTestReleaseForge) GetPullRequest(string, string, int) (github.PullRequestDetail, error) {
	return github.PullRequestDetail{}, nil
}

func (f *kmsTestReleaseForge) ListMatchingTags(_, _, prefix string) ([]github.TagRef, error) {
	var out []github.TagRef
	for name, sha := range f.tags {
		if strings.HasPrefix(name, prefix) {
			out = append(out, github.TagRef{Name: name, ObjectSHA: sha, ObjectType: "commit"})
		}
	}
	return out, nil
}

func (f *kmsTestReleaseForge) GetAnnotatedTag(string, string, string) (*github.AnnotatedTag, error) {
	return nil, nil
}

func (f *kmsTestReleaseForge) DispatchWorkflow(string, string, string, string, map[string]string) error {
	f.dispatches++
	return nil
}

// Once GET /api/release-approval-key has cached a KMS key version's public
// key, releaseApprovalSigner succeeds without calling KMS; if KMS then
// refuses asymmetricSign (for example, viewPublicKey is granted without
// useToSign, or IAM is revoked after the public key was cached), Approve,
// Reject and Rollback must still answer 502 naming
// -release-approval-kms-key without leaking KMS's response.
func TestAKMSReleaseApprovalKeyThatCannotSignAfterPublicKeyCacheIsNamedInA502(t *testing.T) {
	fake := kmsfake.Start(t)
	const name = "projects/example-project/locations/global/keyRings/grain/cryptoKeys/sign-denied/cryptoKeyVersions/1"
	c, store, ctx := testClient(t)
	c.Config.DataDir = t.TempDir()
	c.Config.ReleaseApprovalKMSKey = name
	forge := &kmsTestReleaseForge{tags: map[string]string{
		"v1.0.0-pending": "0123456789abcdef0123456789abcdef01234567",
		"v0.9.0":         "89abcdef0123456789abcdef0123456789abcdef",
	}}
	c.Config.GitHubPullRequests = forge
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"acme/widgets"}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, CIMode: model.CIModeGitHub,
	}); err != nil {
		t.Fatal(err)
	}
	srv := ui.NewServerWithClient(c)

	// Populate releaseapproval's public-key cache before revoking KMS IAM.
	if rec := do(t, srv, http.MethodGet, "/api/release-approval-key", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/release-approval-key = %d %s, want 200", rec.Code, rec.Body)
	}
	fake.Deny(true)

	for _, tc := range []struct {
		path string
		body string
	}{
		{"/api/repos/acme/widgets/pending-releases/v1.0.0/approve", `{}`},
		{"/api/repos/acme/widgets/pending-releases/v1.0.0/reject", `{}`},
		{"/api/releases/v0.9.0/rollback", `{"repo":"acme/widgets"}`},
	} {
		rec := do(t, srv, http.MethodPost, tc.path, tc.body)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("POST %s = %d, want 502; body %s", tc.path, rec.Code, rec.Body)
		}
		got := decode[map[string]string](t, rec)
		if got["code"] != apierror.CodeUpstream || !strings.Contains(got["error"], "-release-approval-kms-key") {
			t.Fatalf("POST %s answer = %v; want code %s and an error naming -release-approval-kms-key", tc.path, got, apierror.CodeUpstream)
		}
		if strings.Contains(got["error"], "denied") || strings.Contains(got["error"], "403") {
			t.Fatalf("POST %s answer %q carries KMS's own answer; want it only in the log", tc.path, got["error"])
		}
	}
	if forge.dispatches != 0 {
		t.Fatalf("dispatched %d workflows after KMS refused signing, want 0", forge.dispatches)
	}
}
