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

package container

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A throwaway OCI registry on localhost, for the upgrade tests.
//
// A real `docker pull` against a real registry is the point: the upgrade
// path's first step is exactly that, and stubbing it would test everything
// except the thing most likely to be wrong. `localhost` is also the one
// host docker will speak plain HTTP to without an insecure-registry entry,
// which is why the tag names below are localhost:<port> rather than
// anything prettier.
//
// Built once for the package and torn down by TestMain, rather than per
// test: the two tags are the same bytes, so the second costs only a
// manifest write, and standing a registry up twice would double the
// slowest part of this file for no coverage.
var registryOnce struct {
	sync.Once
	repo   string
	server *http.Server
	err    error
}

// registry is the repository the upgrade tests pull from, stood up on
// first use.
func registry(t *testing.T) string {
	t.Helper()
	registryOnce.Do(func() { registryOnce.err = startRegistry() })
	if registryOnce.err != nil {
		t.Fatalf("standing up a local registry: %v", registryOnce.err)
	}
	return registryOnce.repo
}

type inMemoryRegistry struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	manifests map[string]registryManifest
	uploads   map[string]*bytes.Buffer
}

type registryManifest struct {
	body        []byte
	contentType string
	digest      string
}

func newInMemoryRegistry() *inMemoryRegistry {
	return &inMemoryRegistry{
		blobs:     make(map[string][]byte),
		manifests: make(map[string]registryManifest),
		uploads:   make(map[string]*bytes.Buffer),
	}
}

func (r *inMemoryRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")

	path := req.URL.Path
	if path == "/v2" || path == "/v2/" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
		return
	}

	if !strings.HasPrefix(path, "/v2/") {
		http.NotFound(w, req)
		return
	}

	rest := strings.TrimPrefix(path, "/v2/")

	// /v2/<name>/blobs/uploads/
	if strings.Contains(rest, "/blobs/uploads") {
		idx := strings.Index(rest, "/blobs/uploads")
		name := rest[:idx]
		uploadPath := rest[idx+len("/blobs/uploads"):]
		uploadPath = strings.TrimPrefix(uploadPath, "/")

		if uploadPath == "" {
			// POST /v2/<name>/blobs/uploads/
			if req.Method == http.MethodPost {
				r.mu.Lock()
				defer r.mu.Unlock()

				if mount := req.URL.Query().Get("mount"); mount != "" {
					if _, ok := r.blobs[mount]; ok {
						w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, mount))
						w.Header().Set("Docker-Content-Digest", mount)
						w.WriteHeader(http.StatusCreated)
						return
					}
				}

				if digest := req.URL.Query().Get("digest"); digest != "" {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					r.blobs[digest] = body
					w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
					w.Header().Set("Docker-Content-Digest", digest)
					w.WriteHeader(http.StatusCreated)
					return
				}

				id, err := randomHex(16)
				if err != nil {
					id = fmt.Sprintf("%d", time.Now().UnixNano())
				}
				r.uploads[id] = new(bytes.Buffer)
				w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id))
				w.Header().Set("Range", "0-0")
				w.Header().Set("Docker-Upload-UUID", id)
				w.WriteHeader(http.StatusAccepted)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		id := uploadPath
		switch req.Method {
		case http.MethodPatch:
			r.mu.Lock()
			buf, ok := r.uploads[id]
			if !ok {
				r.mu.Unlock()
				http.NotFound(w, req)
				return
			}
			_, err := io.Copy(buf, req.Body)
			size := buf.Len()
			r.mu.Unlock()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id))
			if size > 0 {
				w.Header().Set("Range", fmt.Sprintf("0-%d", size-1))
			} else {
				w.Header().Set("Range", "0-0")
			}
			w.Header().Set("Docker-Upload-UUID", id)
			w.WriteHeader(http.StatusAccepted)
			return

		case http.MethodPut:
			r.mu.Lock()
			buf, ok := r.uploads[id]
			if !ok {
				buf = new(bytes.Buffer)
			}
			_, err := io.Copy(buf, req.Body)
			if err != nil {
				r.mu.Unlock()
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			data := buf.Bytes()
			digest := req.URL.Query().Get("digest")
			if digest == "" {
				digest = fmt.Sprintf("sha256:%x", sha256.Sum256(data))
			}
			r.blobs[digest] = data
			delete(r.uploads, id)
			r.mu.Unlock()

			w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusCreated)
			return

		case http.MethodDelete:
			r.mu.Lock()
			delete(r.uploads, id)
			r.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}

	// /v2/<name>/blobs/<digest>
	if strings.Contains(rest, "/blobs/") {
		idx := strings.Index(rest, "/blobs/")
		digest := rest[idx+len("/blobs/"):]

		r.mu.Lock()
		blob, ok := r.blobs[digest]
		r.mu.Unlock()

		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"code":"BLOB_UNKNOWN","message":"blob unknown"}]}`))
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
		w.WriteHeader(http.StatusOK)
		if req.Method == http.MethodGet {
			_, _ = w.Write(blob)
		}
		return
	}

	// /v2/<name>/manifests/<ref>
	if strings.Contains(rest, "/manifests/") {
		idx := strings.Index(rest, "/manifests/")
		name := rest[:idx]
		ref := rest[idx+len("/manifests/"):]

		switch req.Method {
		case http.MethodPut:
			body, err := io.ReadAll(req.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
			ct := req.Header.Get("Content-Type")
			if ct == "" {
				ct = "application/vnd.docker.distribution.manifest.v2+json"
			}
			entry := registryManifest{
				body:        body,
				contentType: ct,
				digest:      digest,
			}

			r.mu.Lock()
			r.manifests[name+":"+ref] = entry
			r.manifests[name+":"+digest] = entry
			r.mu.Unlock()

			w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, ref))
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusCreated)
			return

		case http.MethodGet, http.MethodHead:
			r.mu.Lock()
			entry, ok := r.manifests[name+":"+ref]
			r.mu.Unlock()

			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
				return
			}

			w.Header().Set("Content-Type", entry.contentType)
			w.Header().Set("Docker-Content-Digest", entry.digest)
			w.Header().Set("Content-Length", strconv.Itoa(len(entry.body)))
			w.WriteHeader(http.StatusOK)
			if req.Method == http.MethodGet {
				_, _ = w.Write(entry.body)
			}
			return

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}

	http.NotFound(w, req)
}

func startRegistry() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port

	server := &http.Server{
		Handler: newInMemoryRegistry(),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	registryOnce.server = server

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(60 * time.Second)
	for {
		status, _, err := call(base, http.MethodGet, "/v2/", "", nil, 5*time.Second)
		if err == nil && status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the local registry never answered %s/v2/: %v", base, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	registryOnce.repo = fmt.Sprintf("localhost:%d/grain", port)
	// Two tags of the same image, standing in for two branches CI
	// published.
	for _, tag := range []string{"v-one", "v-two"} {
		ref := registryOnce.repo + ":" + tag
		if _, err := tryDocker("tag", image, ref); err != nil {
			return err
		}
		if _, err := tryDocker("push", ref); err != nil {
			return err
		}
	}
	return nil
}

// TestMain removes the registry container the upgrade tests stand up. It
// is the only package-wide teardown Go offers, and the registry is the
// only thing here that outlives the test that created it -- every daemon
// container is removed by its own t.Cleanup.
func TestMain(m *testing.M) {
	code := m.Run()
	if registryOnce.server != nil {
		_ = registryOnce.server.Close()
	}
	os.Exit(code)
}

// upgradeTo starts an upgrade and waits for it to leave `running`.
func upgradeTo(t *testing.T, d *Daemon, branch string) upgradeStatus {
	t.Helper()

	status, raw := d.api(t, http.MethodPost, "/api/upgrade", map[string]string{"branch": branch})
	if status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("starting an upgrade: %d %s", status, raw)
	}

	var final upgradeStatus
	waitFor(t, "the upgrade to "+branch+" to finish", 300*time.Second, func() error {
		_, raw := d.api(t, http.MethodGet, "/api/upgrade", nil)
		var current upgradeStatus
		decode(t, raw, &current)
		if current.Phase != "ok" && current.Phase != "failed" {
			return errNothingYet
		}
		final = current
		return nil
	})
	return final
}

// upgradeDaemon is the daemon shape both upgrade tests need: the docker
// socket mounted (the pull goes through it), and the three flags that make
// the Upgrade button real -- what to pull, the ref file to rewrite, and
// how to ask the host to restart.
func upgradeDaemon(t *testing.T, root, repo, refFile, restart string) *Daemon {
	t.Helper()
	return start(t, root, options{
		runArgs: append([]string{
			"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		}, dockerGroupArgs()...),
		flags: []string{
			"-upgrade-image", repo,
			"-upgrade-image-ref-file", refFile,
			"-upgrade-restart-cmd", "touch",
			"-upgrade-restart-cmd", restart,
			// Every tag these tests publish stands in for a branch the
			// deployment trusts; which branches it trusts is
			// pkg/upgrade's own tests' business.
			"-upgrade-allowed-branches", "*",
		},
	})
}

// The container deployment's whole upgrade path, end to end.
//
// Pull the tag CI publishes for a branch, prove the pulled image runs, and
// write the one `GRAIN_IMAGE=` line the systemd unit reads as an
// EnvironmentFile -- after which the restart command brings the deployment
// up on it. Everything here is real: a real registry, a real `docker pull`
// through the mounted socket, a real health check run of the pulled image,
// and the real ref file.
func TestAnUpgradePullsABranchTagAndRepointsTheDeployment(t *testing.T) {
	requireImage(t)
	repo := registry(t)
	root := t.TempDir()

	control := filepath.Join(root, "data", "control")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatalf("laying out the control directory: %v", err)
	}
	refFile := filepath.Join(root, "data", "image.env")
	restart := filepath.Join(control, "restart")
	writeRef(t, refFile, repo+":v-one")

	d := upgradeDaemon(t, root, repo, refFile, restart)

	_, raw := d.api(t, http.MethodGet, "/api/upgrade", nil)
	var status upgradeStatus
	decode(t, raw, &status)
	if !status.Enabled {
		t.Fatalf("the daemon does not offer an upgrade at all: %s", raw)
	}
	// And it says what it is upgrading *from*, before anything has been
	// upgraded: this deployment's ref file was written by the equivalent
	// of scripts/setup.sh, so there is no attempt on record to infer it
	// from (grain/task-50).
	if want := repo + ":v-one"; status.Running.Image != want {
		t.Fatalf("the daemon reports running %q, want %q: %s", status.Running.Image, want, raw)
	}

	final := upgradeTo(t, d, "v-two")
	if final.Phase != "ok" {
		t.Fatalf("the upgrade ended %q: %s\n%s", final.Phase, final.Detail, d.logs())
	}
	if !strings.Contains(final.Detail, repo+":v-two") {
		t.Fatalf("the upgrade does not name the tag it pulled: %s", final.Detail)
	}

	// The ref file is the deployment: the unit interpolates it into its own
	// ExecStart, so this line *is* what comes up next. It names the image
	// the pull fetched by its digest, not just the tag, so CI pushing the
	// branch again before the restart cannot change what comes up.
	pinned := repo + ":v-two@" + digestOf(t, repo, repo+":v-two")
	if got := readRef(t, refFile); got != "GRAIN_IMAGE="+pinned {
		t.Fatalf("the ref file reads %q, want it pinned to %s", got, pinned)
	}
	if want := pinned; final.Running.Image != want {
		t.Fatalf("after the upgrade the daemon reports running %q, want %q", final.Running.Image, want)
	}
	waitFor(t, "the restart request the upgrade ends with", 60*time.Second, func() error {
		_, err := os.Stat(restart)
		return err
	})
}

// digestOf is the digest this host's docker records for ref as pulled
// from repo -- what pkg/upgrade's pinImage reads.
func digestOf(t *testing.T, repo, ref string) string {
	t.Helper()
	out := docker(t, "image", "inspect", "--format", "{{range .RepoDigests}}{{println .}}{{end}}", ref)
	for _, line := range strings.Split(out, "\n") {
		if digest, ok := strings.CutPrefix(strings.TrimSpace(line), repo+"@"); ok {
			return digest
		}
	}
	t.Fatalf("docker records no digest of %s from %s: %q", ref, repo, out)
	return ""
}

// A failed upgrade must be a no-op, not a broken deployment.
//
// The image path has no rollback and does not need one: the pull comes
// first, and the ref file -- the only thing that decides what the service
// runs -- is not touched until an image has been pulled *and* proved to
// run. A branch nobody published an image for is the ordinary way to reach
// that (a push whose build has not finished, or a typo), so it is worth
// knowing it leaves the file exactly as it was.
func TestABranchWithNoPublishedImageLeavesTheDeploymentAlone(t *testing.T) {
	requireImage(t)
	repo := registry(t)
	root := t.TempDir()

	control := filepath.Join(root, "data", "control")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatalf("laying out the control directory: %v", err)
	}
	refFile := filepath.Join(root, "data", "image.env")
	restart := filepath.Join(control, "restart")
	writeRef(t, refFile, repo+":v-one")

	d := upgradeDaemon(t, root, repo, refFile, restart)

	final := upgradeTo(t, d, "no-such-branch")
	if final.Phase != "failed" {
		t.Fatalf("an unpublished branch upgraded anyway: %q %s", final.Phase, final.Detail)
	}
	if !strings.Contains(final.Detail, "pull") {
		t.Fatalf("the failure does not name the pull: %s", final.Detail)
	}
	d.stop()

	if got := readRef(t, refFile); got != "GRAIN_IMAGE="+repo+":v-one" {
		t.Fatalf("a failed upgrade rewrote the ref file to %q", got)
	}
	if _, err := os.Stat(restart); err == nil {
		t.Error("a failed upgrade still asked for a restart")
	}
}

func writeRef(t *testing.T, path, ref string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("GRAIN_IMAGE="+ref+"\n"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func readRef(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.TrimSpace(string(raw))
}
