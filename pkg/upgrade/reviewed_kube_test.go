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

package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/reviewedmerge"
)

// fakeRegistry serves documents by their sha256 digest, behind the
// anonymous bearer-token exchange Docker Hub asks for.
type fakeRegistry struct {
	mu   sync.Mutex
	docs map[string][]byte
	// served overrides what is served for a digest, for a registry that
	// answers with something other than what it was asked for.
	served map[string][]byte
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *fakeRegistry) put(doc string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.docs == nil {
		r.docs = map[string][]byte{}
	}
	d := digestOf([]byte(doc))
	r.docs[d] = []byte(doc)
	return d
}

// image puts a config blob labelled with revision and a manifest naming
// it, and returns the manifest's digest.
func (r *fakeRegistry) image(revision string) string {
	return r.imageFor(revision, "amd64")
}

func (r *fakeRegistry) imageFor(revision, arch string) string {
	config := r.put(`{"architecture":"` + arch + `","os":"linux","config":{"Labels":{"org.opencontainers.image.revision":"` + revision + `"}}}`)
	return r.put(`{"schemaVersion":2,"mediaType":"` + mediaOCIManifest + `","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + config + `","size":1},"layers":[]}`)
}

// index puts an index of manifests, each as a linux platform, plus a
// build attestation that carries no labels.
func (r *fakeRegistry) index(manifests ...string) string {
	attestation := r.put(`{"schemaVersion":2,"mediaType":"` + mediaOCIManifest + `","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + r.put(`{}`) + `","size":2},"layers":[]}`)
	var entries []string
	for _, m := range manifests {
		entries = append(entries, `{"mediaType":"`+mediaOCIManifest+`","digest":"`+m+`","size":1,"platform":{"architecture":"amd64","os":"linux"}}`)
	}
	entries = append(entries, `{"mediaType":"`+mediaOCIManifest+`","digest":"`+attestation+`","size":1,"annotations":{"vnd.docker.reference.type":"attestation-manifest"},"platform":{"architecture":"unknown","os":"unknown"}}`)
	return r.put(`{"schemaVersion":2,"mediaType":"` + mediaOCIIndex + `","manifests":[` + strings.Join(entries, ",") + `]}`)
}

func (r *fakeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/token" {
		w.Write([]byte(`{"token":"anonymous"}`))
		return
	}
	if req.Header.Get("Authorization") != "Bearer anonymous" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="https://registry-1.docker.io/token",service="registry.docker.io",scope="repository:example/grain:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	rest, ok := strings.CutPrefix(req.URL.Path, "/v2/example/grain/")
	if !ok {
		http.NotFound(w, req)
		return
	}
	_, digest, _ := strings.Cut(rest, "/")
	r.mu.Lock()
	doc, found := r.served[digest]
	if !found {
		doc, found = r.docs[digest]
	}
	r.mu.Unlock()
	if !found {
		http.NotFound(w, req)
		return
	}
	w.Write(doc)
}

// kubeReviewedUpgrader is a Kubernetes upgrader whose verifier signs off
// on digest, whose cluster runs it, and whose registry is reg.
func kubeReviewedUpgrader(t *testing.T, reg *fakeRegistry, digest string, f chainForge) (*Upgrader, func() string, func() []string) {
	t.Helper()
	srv := httptest.NewTLSServer(reg)
	t.Cleanup(srv.Close)
	registryHosts(t, map[string]*httptest.Server{"registry-1.docker.io": srv})
	kubectl, calls, manifests := fakeKubectl(t, "Succeeded|False|docker-pullable://docker.io/example/grain@"+digest+" |", "")
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Kube.VerifyCmd = []string{"sh", "-c", `printf '[%s]\n' '` + cosignPayload("example/grain", digest) + `'`, "sh"}
	u.cfg.Kube.ReviewedMerge = &ReviewedMergeConfig{
		Policy:          reviewedmerge.Policy{Repo: "example/grain", Branches: []string{"main"}, Mergers: []string{"grain-bot[bot]"}},
		Forge:           f,
		Compare:         f.compare,
		RunningRevision: func() string { return runningSHA },
	}
	return u, calls, manifests
}

// refusedBeforeAnyPod runs stageKube expecting a refusal, and fails the
// test if a pod was created or the Deployment repointed on the way.
func refusedBeforeAnyPod(t *testing.T, u *Upgrader, calls func() string, manifests func() []string) string {
	t.Helper()
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()
	_, err := u.stageKube(context.Background(), p, "main")
	if err == nil {
		t.Fatal("stageKube succeeded")
	}
	if got := manifests(); len(got) != 0 {
		t.Errorf("%d pods were created for a refused image", len(got))
	}
	if strings.Contains(calls(), "set image") {
		t.Errorf("the Deployment was repointed: %q", calls())
	}
	return err.Error()
}

// TestStageKubeChecksTheChainFromTheSignedDigestsLabel: with nothing
// pulled, the commit is the revision label the registry serves for the
// verified digest, and a chain grain merged goes ahead.
func TestStageKubeChecksTheChainFromTheSignedDigestsLabel(t *testing.T) {
	reg := &fakeRegistry{}
	digest := reg.image(targetSHA)
	u, _, _ := kubeReviewedUpgrader(t, reg, digest, chainForge{})
	detail, status := stageKubeStatus(t, u, "main")
	if got := stepDetail(t, status, stepVerify); !strings.Contains(got, "through 2 reviewed merge(s)") {
		t.Errorf("signature step detail = %q, want it to say the two merges were reviewed", got)
	}
	if !strings.Contains(detail, "rolled out example/grain:main@"+digest) {
		t.Errorf("detail = %q, want the verified digest rolled out", detail)
	}
}

// TestStageKubeRefusesASelfMergedCommitUnderAReviewedOne is the attack
// the check exists for, on the Kubernetes path: no pod ever runs the
// image.
func TestStageKubeRefusesASelfMergedCommitUnderAReviewedOne(t *testing.T) {
	reg := &fakeRegistry{}
	digest := reg.image(targetSHA)
	u, calls, manifests := kubeReviewedUpgrader(t, reg, digest, chainForge{selfMerged: true})
	if msg := refusedBeforeAnyPod(t, u, calls, manifests); !strings.Contains(msg, "merged by mallory") {
		t.Errorf("error = %q, want it to name the unreviewed merge", msg)
	}
}

// TestStageKubeRefusesTheReviewedMergeCheckWithoutASignature: as on the
// image path, the label is only what the registry says unless a
// signature covers it.
func TestStageKubeRefusesTheReviewedMergeCheckWithoutASignature(t *testing.T) {
	reg := &fakeRegistry{}
	digest := reg.image(targetSHA)
	u, calls, manifests := kubeReviewedUpgrader(t, reg, digest, chainForge{})
	u.cfg.Kube.VerifyCmd = nil
	if msg := refusedBeforeAnyPod(t, u, calls, manifests); !strings.Contains(msg, "no signature verification is (-upgrade-verify-cmd)") {
		t.Errorf("error = %q, want it to ask for -upgrade-verify-cmd", msg)
	}
}

// TestStageKubeRefusesALabelTheSignatureDoesNotCover: a registry that
// serves a config blob other than the one the signed manifest names --
// one relabelled with a reviewed commit -- is refused, not believed.
func TestStageKubeRefusesALabelTheSignatureDoesNotCover(t *testing.T) {
	reg := &fakeRegistry{}
	digest := reg.image(forkSHA)
	var manifest struct {
		Config struct{ Digest string } `json:"config"`
	}
	if err := json.Unmarshal(reg.docs[digest], &manifest); err != nil {
		t.Fatal(err)
	}
	reg.served = map[string][]byte{
		manifest.Config.Digest: []byte(`{"config":{"Labels":{"org.opencontainers.image.revision":"` + targetSHA + `"}}}`),
	}
	u, calls, manifests := kubeReviewedUpgrader(t, reg, digest, chainForge{})
	if msg := refusedBeforeAnyPod(t, u, calls, manifests); !strings.Contains(msg, "not what was signed") {
		t.Errorf("error = %q, want it to say the registry served something unsigned", msg)
	}
}

// TestRegistryImageRevisionReadsEveryPlatformOfAnIndex: a signed index
// names one commit only if every platform a node could pull names it.
func TestRegistryImageRevisionReadsEveryPlatformOfAnIndex(t *testing.T) {
	reg := &fakeRegistry{}
	same := reg.index(reg.image(targetSHA), reg.imageFor(targetSHA, "arm64"))
	mixed := reg.index(reg.image(targetSHA), reg.image(forkSHA))
	srv := httptest.NewTLSServer(reg)
	defer srv.Close()
	registryHosts(t, map[string]*httptest.Server{"registry-1.docker.io": srv})

	got, err := registryImageRevision(context.Background(), "example/grain:main@"+same)
	if err != nil || got != targetSHA {
		t.Errorf("registryImageRevision(index of one commit) = %q, %v; want %s", got, err, targetSHA)
	}
	if _, err := registryImageRevision(context.Background(), "example/grain:main@"+mixed); err == nil || !strings.Contains(err.Error(), "name different commits") {
		t.Errorf("registryImageRevision(index of two commits) error = %v, want it refused", err)
	}
	// A platform with no label is a platform naming no commit, whichever
	// side of the labelled one it sits.
	for _, unlabelled := range []string{
		reg.index(reg.image(""), reg.imageFor(targetSHA, "arm64")),
		reg.index(reg.image(targetSHA), reg.imageFor("", "arm64")),
	} {
		if got, err := registryImageRevision(context.Background(), "example/grain:main@"+unlabelled); err == nil || !strings.Contains(err.Error(), "name different commits") {
			t.Errorf("registryImageRevision(index with an unlabelled platform) = %q, %v; want it refused", got, err)
		}
	}
	if _, err := registryImageRevision(context.Background(), "example/grain:main"); err == nil {
		t.Error("registryImageRevision read an unpinned tag")
	}
}

// TestKubeReleaseNotesRefusesAnUnreviewedImage: the preview runs the
// image in a pod to read its notes, so it asks first, as the upgrade
// does.
func TestKubeReleaseNotesRefusesAnUnreviewedImage(t *testing.T) {
	reg := &fakeRegistry{}
	digest := reg.image(targetSHA)
	u, _, manifests := kubeReviewedUpgrader(t, reg, digest, chainForge{selfMerged: true})
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()
	_, _, refused, err := u.kubeReleaseNotes(context.Background(), p, "main", []string{"release-notes"})
	if err == nil || refused != nil || !strings.Contains(err.Error(), "merged by mallory") {
		t.Errorf("kubeReleaseNotes = refused %v, err %v; want an error naming the unreviewed merge", refused, err)
	}
	if got := manifests(); len(got) != 0 {
		t.Errorf("%d pods were created to read an unreviewed image's notes", len(got))
	}
}
