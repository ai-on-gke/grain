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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"
	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
)

const (
	testCommit1   = "1111111111111111111111111111111111111111"
	testCommit2   = "2222222222222222222222222222222222222222"
	testDaemonID1 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testDaemonID2 = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testGuestID1  = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testK8sID1    = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

type fakeReleaseSpec struct {
	tag             string
	targetCommitish string
	assets          map[string][]byte
	redirectBlobURL string
	// id, createdAt, body and prerelease are answered as the release's
	// id, created_at, body and prerelease.
	id         int64
	createdAt  string
	body       string
	prerelease bool
}

type fakeForgeServer struct {
	srv            *httptest.Server
	blobSrv        *httptest.Server
	owner          string
	repo           string
	wantAuthHeader string
	requireAuth404 bool

	tagLookups    atomic.Int64
	tarDownloads  atomic.Int64
	blobAuthSeen  atomic.Bool
	lastAskedTags []string
	releases      map[string]*fakeReleaseSpec
	// latestTag is the release GET .../releases/latest answers with.
	latestTag string
}

func newFakeForgeServer(t *testing.T, owner, repo string) *fakeForgeServer {
	t.Helper()
	f := &fakeForgeServer{
		owner:    owner,
		repo:     repo,
		releases: map[string]*fakeReleaseSpec{},
	}
	f.blobSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			f.blobAuthSeen.Store(true)
		}
		tag := strings.TrimPrefix(r.URL.Query().Get("tag"), "")
		name := r.URL.Query().Get("name")
		rel, ok := f.releases[tag]
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, ok := rel.assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(name, ".tar.gz") {
			f.tarDownloads.Add(1)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	}))
	t.Cleanup(f.blobSrv.Close)

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.wantAuthHeader != "" && r.Header.Get("Authorization") != f.wantAuthHeader {
			if f.requireAuth404 {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}

		prefix := fmt.Sprintf("/repos/%s/%s/releases", f.owner, f.repo)
		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		if path == prefix {
			var list []githubRelease
			for _, spec := range f.releases {
				list = append(list, f.toGitHubRelease(spec, r.URL.Path != path))
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(list)
			return
		}
		if path == prefix+"/latest" {
			spec, exists := f.releases[f.latestTag]
			if !exists {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.toGitHubRelease(spec, r.URL.Path != path))
			return
		}
		if rest, ok := strings.CutPrefix(path, prefix+"/tags/"); ok {
			f.tagLookups.Add(1)
			f.lastAskedTags = append(f.lastAskedTags, rest)
			spec, exists := f.releases[rest]
			if !exists {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.toGitHubRelease(spec, r.URL.Path != path))
			return
		}
		if rest, ok := strings.CutPrefix(path, prefix+"/assets/"); ok {
			parts := strings.SplitN(rest, ":", 2)
			if len(parts) != 2 {
				http.NotFound(w, r)
				return
			}
			spec, exists := f.releases[parts[0]]
			if !exists {
				http.NotFound(w, r)
				return
			}
			data, exists := spec.assets[parts[1]]
			if !exists {
				http.NotFound(w, r)
				return
			}
			if spec.redirectBlobURL != "" && strings.HasSuffix(parts[1], ".tar.gz") {
				http.Redirect(w, r, fmt.Sprintf("%s/blob?tag=%s&name=%s", spec.redirectBlobURL, parts[0], parts[1]), http.StatusFound)
				return
			}
			if strings.HasSuffix(parts[1], ".tar.gz") {
				f.tarDownloads.Add(1)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeForgeServer) toGitHubRelease(spec *fakeReleaseSpec, enterprisePrefix bool) githubRelease {
	apiRoot := f.srv.URL
	if enterprisePrefix {
		apiRoot += "/api/v3"
	}
	var assets []githubReleaseAsset
	var id int64 = 1
	for name, data := range spec.assets {
		assets = append(assets, githubReleaseAsset{
			ID:   id,
			Name: name,
			Size: int64(len(data)),
			URL:  fmt.Sprintf("%s/repos/%s/%s/releases/assets/%s:%s", apiRoot, f.owner, f.repo, spec.tag, name),
		})
		id++
	}
	return githubRelease{
		ID:              spec.id,
		TagName:         spec.tag,
		TargetCommitish: spec.targetCommitish,
		Prerelease:      spec.prerelease,
		CreatedAt:       spec.createdAt,
		Body:            spec.body,
		Assets:          assets,
	}
}

func (f *fakeForgeServer) clientFunc(token, conn, hint string) func(context.Context, string, string, string) (*ReleaseHostClient, error) {
	return func(_ context.Context, host, _, _ string) (*ReleaseHostClient, error) {
		apiBase := f.srv.URL
		if !strings.EqualFold(hostNameOnly(host), "github.com") {
			apiBase = f.srv.URL + "/api/v3"
		}
		return &ReleaseHostClient{
			Client:      f.srv.Client(),
			Token:       token,
			Connection:  conn,
			MissingHint: hint,
			APIBase:     apiBase,
		}, nil
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func addV1Release(f *fakeForgeServer, tag, repoName, commit, version, daemonRef, daemonID string, includeGuest bool) {
	daemonTar := []byte("fake-gzip-tarball-daemon-" + commit)
	daemonSHA := sha256Hex(daemonTar)
	images := map[string]any{
		"grain": map[string]any{
			"asset":   "grain-edition-image.tar.gz",
			"sha256":  daemonSHA,
			"imageId": daemonID,
			"ref":     daemonRef,
		},
	}
	assets := map[string][]byte{
		"grain-edition-image.tar.gz":        daemonTar,
		"grain-edition-image.tar.gz.sha256": []byte(daemonSHA + "  grain-edition-image.tar.gz\n"),
	}
	if includeGuest {
		guestTar := []byte("fake-gzip-tarball-guest-" + commit)
		guestSHA := sha256Hex(guestTar)
		k8sTar := []byte("fake-gzip-tarball-k8s-" + commit)
		k8sSHA := sha256Hex(k8sTar)
		images["guest"] = map[string]any{
			"asset":   "guest-image.tar.gz",
			"sha256":  guestSHA,
			"imageId": testGuestID1,
			"ref":     "edition-local/guest:sha-" + commit[:12],
		}
		images["k8s-sandbox"] = map[string]any{
			"asset":   "k8s-sandbox-image.tar.gz",
			"sha256":  k8sSHA,
			"imageId": testK8sID1,
			"ref":     "edition-local/k8s-sandbox:sha-" + commit[:12],
		}
		images["future-role"] = map[string]any{"asset": "ignored.tar.gz"}
		assets["guest-image.tar.gz"] = guestTar
		assets["guest-image.tar.gz.sha256"] = []byte(guestSHA + "  guest-image.tar.gz\n")
		assets["k8s-sandbox-image.tar.gz"] = k8sTar
		assets["k8s-sandbox-image.tar.gz.sha256"] = []byte(k8sSHA + "  k8s-sandbox-image.tar.gz\n")
	}
	manifest, _ := json.Marshal(map[string]any{
		"schema":       1,
		"repository":   repoName,
		"commit":       commit,
		"version":      version,
		"createdAt":    "2026-11-04T02:00:00Z",
		"unknownField": "tolerated",
		"images":       images,
	})
	assets["images.json"] = manifest
	f.releases[tag] = &fakeReleaseSpec{
		tag:             tag,
		targetCommitish: commit,
		assets:          assets,
	}
}

// fakeDockerEnv builds hermetic shell stand-ins for LoadCmd, InspectIDCmd,
// TagCmd and RunCmd that track image tags -> image IDs in a directory on disk.
type fakeDockerEnv struct {
	dir        string
	tagsDir    string
	loadedLog  string
	taggedLog  string
	sandboxOut string
	healthFail bool
}

func newFakeDockerEnv(t *testing.T) *fakeDockerEnv {
	t.Helper()
	dir := t.TempDir()
	tagsDir := filepath.Join(dir, "tags")
	if err := os.MkdirAll(tagsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeDockerEnv{
		dir:       dir,
		tagsDir:   tagsDir,
		loadedLog: filepath.Join(dir, "loaded.log"),
		taggedLog: filepath.Join(dir, "tagged.log"),
	}
}

func (d *fakeDockerEnv) setTag(ref, id string) {
	key := strings.ReplaceAll(strings.ReplaceAll(ref, "/", "__"), ":", "--")
	_ = os.WriteFile(filepath.Join(d.tagsDir, key), []byte(id+"\n"), 0o644)
}

func (d *fakeDockerEnv) getTag(ref string) string {
	key := strings.ReplaceAll(strings.ReplaceAll(ref, "/", "__"), ":", "--")
	b, err := os.ReadFile(filepath.Join(d.tagsDir, key))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (d *fakeDockerEnv) loadCmd(refForTar map[string][2]string) []string {
	mapFile := filepath.Join(d.dir, "load-map.txt")
	var lines []string
	for substr, pair := range refForTar {
		lines = append(lines, fmt.Sprintf("%s|%s|%s", substr, pair[0], pair[1]))
	}
	_ = os.WriteFile(mapFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	script := `
set -eu
tarpath="$1"
content="$(cat "$tarpath")"
printf '%s\n' "$tarpath" >> "` + d.loadedLog + `"
while IFS='|' read -r match ref imgid; do
  [ -n "$match" ] || continue
  case "$content" in
    *"$match"*)
      if [ -n "$ref" ]; then
        key="$(printf '%s' "$ref" | sed 's|/|__|g; s|:|--|g')"
        printf '%s\n' "$imgid" > "` + d.tagsDir + `/$key"
        printf 'Loaded image: %s\n' "$ref"
      else
        printf 'Loaded image ID: %s\n' "$imgid"
      fi
      exit 0
      ;;
  esac
done < "` + mapFile + `"
echo "unexpected tarball payload: $content" >&2
exit 1
`
	return []string{"sh", "-c", script, "sh"}
}

func (d *fakeDockerEnv) inspectIDCmd() []string {
	script := `
set -eu
ref="$1"
case "$ref" in
  sha256:*)
    printf '%s\n' "$ref"
    exit 0
    ;;
esac
key="$(printf '%s' "$ref" | sed 's|/|__|g; s|:|--|g')"
if [ -f "` + d.tagsDir + `/$key" ]; then
  cat "` + d.tagsDir + `/$key"
  exit 0
fi
echo "No such image: $ref" >&2
exit 1
`
	return []string{"sh", "-c", script, "sh"}
}

func (d *fakeDockerEnv) tagCmd() []string {
	script := `
set -eu
src="$1"
dst="$2"
printf '%s -> %s\n' "$src" "$dst" >> "` + d.taggedLog + `"
case "$src" in
  sha256:*)
    id="$src"
    ;;
  *)
    skey="$(printf '%s' "$src" | sed 's|/|__|g; s|:|--|g')"
    id="$(cat "` + d.tagsDir + `/$skey")"
    ;;
esac
dkey="$(printf '%s' "$dst" | sed 's|/|__|g; s|:|--|g')"
printf '%s\n' "$id" > "` + d.tagsDir + `/$dkey"
`
	return []string{"sh", "-c", script, "sh"}
}

func (d *fakeDockerEnv) runCmd() []string {
	healthExit := "0"
	if d.healthFail {
		healthExit = "1"
	}
	script := `
set -eu
# Skip leading --entrypoint <bin> if present
if [ "${1:-}" = "--entrypoint" ]; then
  shift 2
fi
img="$1"
shift
case "${1:-}" in
  schema-version)
    if [ "` + healthExit + `" != "0" ]; then
      echo "simulated health check crash" >&2
      exit 1
    fi
    echo "7"
    ;;
  sandbox-image|k8s-sandbox-image)
    printf '%s\n' "` + d.sandboxOut + `"
    ;;
  *)
    echo "ok"
    ;;
esac
`
	return []string{"sh", "-c", script, "sh"}
}

func TestParseDockerLoadOutput(t *testing.T) {
	t.Parallel()
	ref, id, err := ParseDockerLoadOutput("8f2a1c: Loading layer [===>]\nLoaded image: edition-local/grain:sha-111111111111\n")
	if err != nil || ref != "edition-local/grain:sha-111111111111" || id != "" {
		t.Fatalf("ParseDockerLoadOutput(tag) = (%q, %q, %v)", ref, id, err)
	}
	ref, id, err = ParseDockerLoadOutput("Loaded image ID: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")
	if err != nil || ref != "" || id != testDaemonID1 {
		t.Fatalf("ParseDockerLoadOutput(id) = (%q, %q, %v)", ref, id, err)
	}
	if _, _, err := ParseDockerLoadOutput("nothing useful printed\n"); err == nil {
		t.Fatal("expected error on unparseable docker load output")
	}
}

func TestReleaseTagsForBranch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		branch string
		want   []string
	}{
		{"main", []string{"channel-main", "main"}},
		{"candidate", []string{"channel-candidate", "candidate"}},
		{"rc", []string{"channel-candidate", "candidate"}},
		{"release/3.1", []string{"channel-release-3.1", "release-3.1"}},
		{"rc/v3.2", []string{"channel-rc-v3.2", "rc-v3.2"}},
		{"channel-rc-v3.2", []string{"channel-rc-v3.2"}},
		{"latest", []string{"latest"}},
		{"v3.1.0", []string{"v3.1.0"}},
		{"v3.1.0-rc.2", []string{"v3.1.0-rc.2"}},
		{"sha-111111111111", []string{"sha-111111111111"}},
	}
	for _, tc := range cases {
		got, err := ReleaseTagsForBranch(tc.branch)
		if err != nil {
			t.Fatalf("ReleaseTagsForBranch(%q): %v", tc.branch, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("ReleaseTagsForBranch(%q) = %v, want %v", tc.branch, got, tc.want)
		}
	}
}

func TestReleaseRepositoryForImage(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"docker.io/grainai/grain":           "github.com/ai-on-gke/grain",
		"docker.io/grainai/grain:main":      "github.com/ai-on-gke/grain",
		"index.docker.io/grainai/grain":     "github.com/ai-on-gke/grain",
		"grainai/grain":                     "github.com/ai-on-gke/grain",
		"ghcr.io/bwsalmon/grain/grain":      "github.com/ai-on-gke/grain",
		"ghcr.io/bwsalmon/grain/grain:main": "github.com/ai-on-gke/grain",
		"ghcr.io/ai-on-gke/grain/grain":     "github.com/ai-on-gke/grain",
		"ghcr.io/acme-org/grain-fork/grain": "github.com/acme-org/grain-fork",
		"ghcr.io/acme-org/grain-fork":       "github.com/acme-org/grain-fork",
		"registry.example.com/team/grain":   "",
	}
	for in, want := range cases {
		if got := ReleaseRepositoryForImage(in); got != want {
			t.Errorf("ReleaseRepositoryForImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultImageSourceLoadsWhenManifestPresentAndFallsBackWhenAbsent(t *testing.T) {
	shortRestartGrace(t)

	t.Run("manifest present loads from release without registry pull", func(t *testing.T) {
		forge := newFakeForgeServer(t, "ai-on-gke", "grain")
		const daemonRef = "docker.io/grainai/grain:sha-111111111111"
		addV1Release(forge, "channel-main", "github.com/ai-on-gke/grain", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, false)

		dk := newFakeDockerEnv(t)
		dir := t.TempDir()
		refFile := filepath.Join(dir, "image.env")
		pulledFile := filepath.Join(dir, "pulled")

		// Source is intentionally left empty ("") to exercise the default path.
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:   "docker.io/grainai/grain",
				RefFile:      refFile,
				PullCmd:      []string{"sh", "-c", `echo "$1" >> ` + pulledFile + `; exit 1`, "sh"},
				LoadCmd:      dk.loadCmd(map[string][2]string{testCommit1: {daemonRef, testDaemonID1}}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				RemoveCmd:    []string{"true"},
				ListCmd:      []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					ClientForHost: forge.clientFunc("", "oss-gh", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})

		if got := u.EffectiveImageSource(); got != ImageSourceReleaseAssets {
			t.Fatalf("EffectiveImageSource() = %q, want %q", got, ImageSourceReleaseAssets)
		}
		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if status.Source != string(ImageSourceReleaseAssets) {
			t.Errorf("status.Source = %q, want %q", status.Source, ImageSourceReleaseAssets)
		}
		if status.FallbackNote != "" {
			t.Errorf("status.FallbackNote = %q, want empty", status.FallbackNote)
		}
		if _, err := os.Stat(pulledFile); !os.IsNotExist(err) {
			t.Errorf("registry PullCmd was called when release had images.json")
		}
		if got := readFile(t, refFile); got != "GRAIN_IMAGE=docker.io/grainai/grain:main\n" {
			t.Errorf("RefFile = %q", got)
		}
		if ch := ChannelForRef(strings.TrimPrefix(strings.TrimSpace(readFile(t, refFile)), "GRAIN_IMAGE=")); ch != "main" {
			t.Errorf("ChannelForRef = %q, want main", ch)
		}
	})

	t.Run("release without images.json falls back to registry and says so", func(t *testing.T) {
		forge := newFakeForgeServer(t, "ai-on-gke", "grain")
		// Release exists on GitHub, but has no images.json yet (today's OSS state).
		forge.releases["v3.1.0"] = &fakeReleaseSpec{
			tag:             "v3.1.0",
			targetCommitish: testCommit1,
			assets:          map[string][]byte{"notes.txt": []byte("release notes only")},
		}

		dir := t.TempDir()
		refFile := filepath.Join(dir, "image.env")
		pulledFile := filepath.Join(dir, "pulled")

		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "docker.io/grainai/grain",
				RefFile:    refFile,
				PullCmd:    []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulledFile, "sh"},
				DigestCmd:  []string{"echo", "docker.io/grainai/grain@" + testDigest},
				RunCmd:     []string{"echo", "7"},
				RemoveCmd:  []string{"true"},
				ListCmd:    []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})

		status := runUpgradeToCompletion(t, u, "v3.1.0")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if status.Source != string(ImageSourceRegistry) {
			t.Errorf("status.Source = %q, want %q", status.Source, ImageSourceRegistry)
		}
		wantNote := "release has no image assets; pulled from docker.io/grainai/grain"
		if status.FallbackNote != wantNote {
			t.Errorf("status.FallbackNote = %q, want %q", status.FallbackNote, wantNote)
		}
		if !strings.Contains(status.Detail, wantNote) {
			t.Errorf("status.Detail = %q, want it to contain %q", status.Detail, wantNote)
		}
		if got := strings.TrimSpace(readFile(t, pulledFile)); got != "docker.io/grainai/grain:v3.1.0" {
			t.Errorf("pulled = %q, want docker.io/grainai/grain:v3.1.0", got)
		}
	})

	t.Run("missing release on default source falls back to registry and says so", func(t *testing.T) {
		forge := newFakeForgeServer(t, "ai-on-gke", "grain")
		dir := t.TempDir()
		refFile := filepath.Join(dir, "image.env")

		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "docker.io/grainai/grain",
				RefFile:    refFile,
				PullCmd:    []string{"true"},
				DigestCmd:  []string{"echo", "docker.io/grainai/grain@" + testDigest},
				RunCmd:     []string{"echo", "7"},
				RemoveCmd:  []string{"true"},
				ListCmd:    []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})

		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if status.Source != string(ImageSourceRegistry) {
			t.Errorf("status.Source = %q, want registry", status.Source)
		}
		wantNote := "release has no image assets; pulled from docker.io/grainai/grain"
		if !strings.Contains(status.Detail, wantNote) || status.FallbackNote != wantNote {
			t.Errorf("detail=%q fallbackNote=%q, want %q", status.Detail, status.FallbackNote, wantNote)
		}
	})

	// The publisher deletes a moving release's images.json first and
	// uploads it last, so a channel release without one is being replaced:
	// the upgrade waits for the next check rather than pulling, with or
	// without an explicit registry fallback.
	for _, explicitFallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("moving release mid-update does not fall back (explicit fallback %v)", explicitFallback), func(t *testing.T) {
			forge := newFakeForgeServer(t, "ai-on-gke", "grain")
			forge.releases["channel-main"] = &fakeReleaseSpec{
				tag:             "channel-main",
				targetCommitish: testCommit1,
				// Mid-move: the publisher has uploaded images.json.next and
				// deleted images.json but not yet renamed .next into place.
				// .next is never read as the manifest.
				assets: map[string][]byte{"images.json.next": []byte(`{"schema":2}`)},
			}
			dir := t.TempDir()
			pulledFile := filepath.Join(dir, "pulled")
			img := &ImageConfig{
				Repository: "docker.io/grainai/grain",
				RefFile:    filepath.Join(dir, "image.env"),
				PullCmd:    []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulledFile, "sh"},
				DigestCmd:  []string{"echo", "docker.io/grainai/grain@" + testDigest},
				RunCmd:     []string{"echo", "7"},
				RemoveCmd:  []string{"true"},
				ListCmd:    []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					ClientForHost: forge.clientFunc("", "", ""),
				},
			}
			if explicitFallback {
				img.Fallback, img.FallbackExplicit = ImageSourceRegistry, true
			}
			u := newUpgrader(t, Config{
				Image:           img,
				HealthCheckArgs: []string{"schema-version"},
				StatusFile:      filepath.Join(dir, "status.json"),
			})
			status := runUpgradeToCompletion(t, u, "main")
			if status.Phase != PhaseFailed {
				t.Fatalf("status = %+v, want a failure that waits for the release", status)
			}
			if !strings.Contains(status.Detail, "try again later") {
				t.Errorf("status.Detail = %q, want it to say try again later", status.Detail)
			}
			if _, err := os.Stat(pulledFile); !os.IsNotExist(err) {
				t.Errorf("registry PullCmd ran for a moving release caught mid-update")
			}
		})
	}

	t.Run("explicit release-assets disables missing-manifest fallback", func(t *testing.T) {
		forge := newFakeForgeServer(t, "ai-on-gke", "grain")
		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "docker.io/grainai/grain",
				Source:     ImageSourceReleaseAssets,
				RefFile:    filepath.Join(dir, "image.env"),
				PullCmd:    []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			StatusFile: filepath.Join(dir, "status.json"),
		})
		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseFailed {
			t.Fatalf("expected failure when Source is explicit release-assets and release is missing, got %+v", status)
		}
	})

	t.Run("explicit registry skips release lookup", func(t *testing.T) {
		forge := newFakeForgeServer(t, "ai-on-gke", "grain")
		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "docker.io/grainai/grain",
				Source:     ImageSourceRegistry,
				RefFile:    filepath.Join(dir, "image.env"),
				PullCmd:    []string{"true"},
				DigestCmd:  []string{"echo", "docker.io/grainai/grain@" + testDigest},
				RunCmd:     []string{"echo", "7"},
				RemoveCmd:  []string{"true"},
				ListCmd:    []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})
		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseOK || status.Source != string(ImageSourceRegistry) {
			t.Fatalf("status = %+v", status)
		}
		if forge.tagLookups.Load() != 0 {
			t.Errorf("explicit registry source queried release API %d times, want 0", forge.tagLookups.Load())
		}
	})
}

func TestReleaseLookupChannelTagPrecedenceAndLegacyFallback(t *testing.T) {
	shortRestartGrace(t)

	t.Run("prefers channel-main over legacy main release tag", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", "edition-local/grain:sha-111111111111", testDaemonID1, false)
		addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit2, "sha-222222222222", "edition-local/grain:sha-222222222222", testDaemonID2, false)

		dk := newFakeDockerEnv(t)
		dir := t.TempDir()
		refFile := filepath.Join(dir, "image.env")
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "edition-local/grain",
				Source:     ImageSourceReleaseAssets,
				RefFile:    refFile,
				LoadCmd: dk.loadCmd(map[string][2]string{
					testCommit1: {"edition-local/grain:sha-111111111111", testDaemonID1},
					testCommit2: {"edition-local/grain:sha-222222222222", testDaemonID2},
				}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				RemoveCmd:    []string{"true"},
				ListCmd:      []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					ClientForHost: forge.clientFunc("tok-ok", "forge-conn", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})
		forge.wantAuthHeader = "Bearer tok-ok"

		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if dk.getTag("edition-local/grain:main") != testDaemonID2 {
			t.Errorf("edition-local/grain:main points at %q, want %q (channel-main)", dk.getTag("edition-local/grain:main"), testDaemonID2)
		}
		if strings.Join(forge.lastAskedTags, ",") != "channel-main" {
			t.Errorf("asked release tags = %v, want [channel-main]", forge.lastAskedTags)
		}
	})

	t.Run("falls back to legacy main release tag via REST API when channel-main is 404", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", "edition-local/grain:sha-111111111111", testDaemonID1, false)

		dk := newFakeDockerEnv(t)
		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:   "edition-local/grain",
				Source:       ImageSourceReleaseAssets,
				RefFile:      filepath.Join(dir, "image.env"),
				LoadCmd:      dk.loadCmd(map[string][2]string{testCommit1: {"edition-local/grain:sha-111111111111", testDaemonID1}}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				RemoveCmd:    []string{"true"},
				ListCmd:      []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})

		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if strings.Join(forge.lastAskedTags, ",") != "channel-main,main" {
			t.Errorf("asked release tags = %v, want [channel-main main]", forge.lastAskedTags)
		}
	})
}

func TestReleaseManifestAndIntegrityChecksRefuseTamperedOrMissingAssets(t *testing.T) {
	shortRestartGrace(t)

	if _, err := ParseReleaseManifest([]byte(`{"schema":3}`)); err == nil || !strings.Contains(err.Error(), "unsupported images.json schema 3") {
		t.Fatalf("schema 3 err = %v", err)
	}
	if _, err := ParseReleaseManifest([]byte(`{"schema":1,"repository":"github.com/x/y","commit":"bad","version":"v1","createdAt":"2026-11-04T00:00:00Z","images":{}}`)); err == nil {
		t.Fatal("expected malformed commit in images.json to fail")
	}

	t.Run("tarball sha256 mismatch refuses cutover and never falls back to registry", func(t *testing.T) {
		forge := newFakeForgeServer(t, "ai-on-gke", "grain")
		addV1Release(forge, "channel-main", "github.com/ai-on-gke/grain", testCommit1, "sha-111111111111", "docker.io/grainai/grain:sha-111111111111", testDaemonID1, false)
		// Corrupt the tarball bytes while leaving images.json and .sha256 intact.
		forge.releases["channel-main"].assets["grain-edition-image.tar.gz"] = []byte("corrupted-tarball-bytes")

		dk := newFakeDockerEnv(t)
		dir := t.TempDir()
		refFile := filepath.Join(dir, "image.env")
		_ = os.WriteFile(refFile, []byte("GRAIN_IMAGE=docker.io/grainai/grain:previous-good\n"), 0o644)

		// Even on default Source ("") where missing-manifest fallback is enabled,
		// a SHA-256 mismatch must FAIL rather than falling back to registry.
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:   "docker.io/grainai/grain",
				RefFile:      refFile,
				PullCmd:      []string{"true"},
				LoadCmd:      dk.loadCmd(nil),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				ReleaseAssets: ReleaseAssetsConfig{
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			StatusFile: filepath.Join(dir, "status.json"),
		})

		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseFailed || !strings.Contains(status.Detail, "sha256 mismatch") {
			t.Fatalf("expected sha256 mismatch failure, got %+v", status)
		}
		if got := readFile(t, refFile); got != "GRAIN_IMAGE=docker.io/grainai/grain:previous-good\n" {
			t.Errorf("RefFile modified on failed checksum: %q", got)
		}
	})

	t.Run("missing checksum sidecar refused", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", "edition-local/grain:sha-111111111111", testDaemonID1, false)
		delete(forge.releases["channel-main"].assets, "grain-edition-image.tar.gz.sha256")

		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "edition-local/grain",
				Source:     ImageSourceReleaseAssets,
				RefFile:    filepath.Join(dir, "image.env"),
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			StatusFile: filepath.Join(dir, "status.json"),
		})
		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseFailed || !strings.Contains(status.Detail, "missing checksum asset") {
			t.Fatalf("expected missing checksum asset error, got %+v", status)
		}
	})

	t.Run("missing tarball asset refused", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", "edition-local/grain:sha-111111111111", testDaemonID1, false)
		delete(forge.releases["channel-main"].assets, "grain-edition-image.tar.gz")

		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "edition-local/grain",
				Source:     ImageSourceReleaseAssets,
				RefFile:    filepath.Join(dir, "image.env"),
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			StatusFile: filepath.Join(dir, "status.json"),
		})
		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseFailed || !strings.Contains(status.Detail, "missing asset") {
			t.Fatalf("expected missing asset error, got %+v", status)
		}
	})

	t.Run("imageId mismatch between images.json and docker load refused", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", "edition-local/grain:sha-111111111111", testDaemonID1, false)

		dk := newFakeDockerEnv(t)
		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:   "edition-local/grain",
				Source:       ImageSourceReleaseAssets,
				RefFile:      filepath.Join(dir, "image.env"),
				LoadCmd:      dk.loadCmd(map[string][2]string{testCommit1: {"edition-local/grain:sha-111111111111", testDaemonID2}}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			StatusFile: filepath.Join(dir, "status.json"),
		})
		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseFailed || !strings.Contains(status.Detail, "expects "+testDaemonID1) {
			t.Fatalf("expected imageId mismatch error, got %+v", status)
		}
	})

	t.Run("insufficient disk space refused before download", func(t *testing.T) {
		saved := statfsAvailableBytes
		statfsAvailableBytes = func(string) (uint64, error) { return 1024, nil }
		defer func() { statfsAvailableBytes = saved }()

		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", "edition-local/grain:sha-111111111111", testDaemonID1, false)

		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "edition-local/grain",
				Source:     ImageSourceReleaseAssets,
				RefFile:    filepath.Join(dir, "image.env"),
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					StagingDir:    filepath.Join(dir, "staging"),
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			StatusFile: filepath.Join(dir, "status.json"),
		})
		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseFailed || !strings.Contains(status.Detail, "insufficient disk space") {
			t.Fatalf("expected disk space refusal, got %+v", status)
		}
		if forge.tarDownloads.Load() != 0 {
			t.Errorf("tarDownloads = %d, want 0 when disk space check fails", forge.tarDownloads.Load())
		}
	})
}

func TestReleaseAssetsUnchangedShortCircuitsWithoutDownloadingTarball(t *testing.T) {
	shortRestartGrace(t)
	forge := newFakeForgeServer(t, "team", "edition")
	addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", "edition-local/grain:sha-111111111111", testDaemonID1, false)

	dk := newFakeDockerEnv(t)
	dk.setTag("edition-local/grain:main", testDaemonID1)
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	_ = os.WriteFile(refFile, []byte("GRAIN_IMAGE=edition-local/grain:main\n"), 0o644)

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository:   "edition-local/grain",
			Source:       ImageSourceReleaseAssets,
			RefFile:      refFile,
			LoadCmd:      dk.loadCmd(nil),
			InspectIDCmd: dk.inspectIDCmd(),
			TagCmd:       dk.tagCmd(),
			RunCmd:       dk.runCmd(),
			ReleaseAssets: ReleaseAssetsConfig{
				Repository:    "depot.code.forge.test/team/edition",
				ClientForHost: forge.clientFunc("", "", ""),
			},
		},
		RunningCommit: func() string { return testCommit1 },
		StatusFile:    filepath.Join(dir, "status.json"),
	})

	same, err := u.AlreadyRunning(context.Background(), "main")
	if err != nil || !same {
		t.Fatalf("AlreadyRunning = (%v, %v), want (true, nil)", same, err)
	}
	if forge.tarDownloads.Load() != 0 {
		t.Fatalf("AlreadyRunning downloaded %d tarballs, want 0", forge.tarDownloads.Load())
	}

	status := runUpgradeToCompletion(t, u, "main")
	if status.Phase != PhaseOK || !status.Unchanged {
		t.Fatalf("status = %+v, want unchanged PhaseOK", status)
	}
	if forge.tarDownloads.Load() != 0 {
		t.Fatalf("stageImage downloaded %d tarballs on unchanged release, want 0", forge.tarDownloads.Load())
	}
}

func TestReleaseAssetsSandboxImageLoadVerifyAndUnreachableRegistryError(t *testing.T) {
	shortRestartGrace(t)

	t.Run("loads daemon and guest sandbox from images.json and runs VerifyCmd", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		const daemonRef = "edition-local/grain:sha-111111111111"
		const guestRef = "edition-local/guest:sha-111111111111"
		addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, true)

		dk := newFakeDockerEnv(t)
		dk.sandboxOut = guestRef
		dir := t.TempDir()
		refFile := filepath.Join(dir, "image.env")
		verifyLog := filepath.Join(dir, "verified.log")

		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:       "edition-local/grain",
				Source:           ImageSourceReleaseAssets,
				RefFile:          refFile,
				SandboxImageArgs: []string{"sandbox-image"},
				// cosign's answer, over the image IDs the release's
				// tarballs load as: verifyLoaded passes a loaded image
				// only on a signature over its own ID.
				VerifyCmd: []string{"sh", "-c",
					`printf '%s\n' "$1" >> ` + verifyLog + `
					case "$1" in
					` + daemonRef + `) id=` + testDaemonID1 + ` ;;
					` + guestRef + `) id=` + testGuestID1 + ` ;;
					*) exit 1 ;;
					esac
					printf '[{"critical":{"image":{"docker-manifest-digest":"%s"}}}]\n' "$id"`, "sh"},
				LoadCmd: dk.loadCmd(map[string][2]string{
					"daemon-" + testCommit1: {daemonRef, testDaemonID1},
					"guest-" + testCommit1:  {guestRef, testGuestID1},
				}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				RemoveCmd:    []string{"true"},
				ListCmd:      []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})

		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if dk.getTag(guestRef) != testGuestID1 {
			t.Errorf("guest image %s ID = %q, want %q", guestRef, dk.getTag(guestRef), testGuestID1)
		}
		verified := readFile(t, verifyLog)
		if !strings.Contains(verified, daemonRef) || !strings.Contains(verified, guestRef) {
			t.Errorf("VerifyCmd log = %q, want both daemon and guest verified", verified)
		}
	})

	t.Run("unreachable sandbox registry fails clearly before cutover naming the sandbox image", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		const daemonRef = "edition-local/grain:sha-111111111111"
		const unreachableSandbox = "private-registry.example.invalid/proj/guest:sha-111111111111"
		addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, false)

		dk := newFakeDockerEnv(t)
		dk.sandboxOut = unreachableSandbox
		dk.setTag("edition-local/grain:main", testDaemonID2)
		dir := t.TempDir()
		refFile := filepath.Join(dir, "image.env")
		_ = os.WriteFile(refFile, []byte("GRAIN_IMAGE=edition-local/grain:main\n"), 0o644)

		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:       "edition-local/grain",
				Source:           ImageSourceReleaseAssets,
				RefFile:          refFile,
				SandboxImageArgs: []string{"sandbox-image"},
				PullCmd:          []string{"sh", "-c", `echo "unauthorized: authentication required" >&2; exit 1`, "sh"},
				LoadCmd:          dk.loadCmd(map[string][2]string{testCommit1: {daemonRef, testDaemonID1}}),
				InspectIDCmd:     dk.inspectIDCmd(),
				TagCmd:           dk.tagCmd(),
				RunCmd:           dk.runCmd(),
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})

		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseFailed {
			t.Fatalf("expected failure when sandbox registry is unreachable, got %+v", status)
		}
		if !strings.Contains(status.Detail, unreachableSandbox) {
			t.Errorf("error detail = %q, want it to name %q", status.Detail, unreachableSandbox)
		}
		// Atomic pre-cutover guarantee: edition-local/grain:main still points at testDaemonID2.
		if got := dk.getTag("edition-local/grain:main"); got != testDaemonID2 {
			t.Errorf("edition-local/grain:main moved to %q despite sandbox failure; want %q", got, testDaemonID2)
		}
	})

	// A manifest-style local ref the release does not carry is never handed
	// to docker pull: with no registry host, docker would fetch whatever
	// Docker Hub serves under that name.
	t.Run("local sandbox ref missing from the release is refused, not pulled", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		const daemonRef = "edition-local/grain:sha-111111111111"
		const localSandbox = "edition-local/guest:sha-111111111111"
		addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, false)

		dk := newFakeDockerEnv(t)
		dk.sandboxOut = localSandbox
		dir := t.TempDir()
		pulledFile := filepath.Join(dir, "pulled")
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:       "edition-local/grain",
				Source:           ImageSourceReleaseAssets,
				RefFile:          filepath.Join(dir, "image.env"),
				SandboxImageArgs: []string{"sandbox-image"},
				PullCmd:          []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulledFile, "sh"},
				LoadCmd:          dk.loadCmd(map[string][2]string{testCommit1: {daemonRef, testDaemonID1}}),
				InspectIDCmd:     dk.inspectIDCmd(),
				TagCmd:           dk.tagCmd(),
				RunCmd:           dk.runCmd(),
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})

		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseFailed {
			t.Fatalf("status = %+v, want a failure naming %s", status, localSandbox)
		}
		if !strings.Contains(status.Detail, localSandbox) || !strings.Contains(status.Detail, "no registry host") {
			t.Errorf("status.Detail = %q, want it to name %s and say it has no registry host", status.Detail, localSandbox)
		}
		if _, err := os.Stat(pulledFile); !os.IsNotExist(err) {
			t.Errorf("PullCmd ran for %s: %q", localSandbox, readFile(t, pulledFile))
		}
	})
}

func TestReleaseAssetsCredentialsRedirectGuardAndAmbientHostCLI(t *testing.T) {
	shortRestartGrace(t)

	t.Run("strips Authorization on cross-host blob redirect and names connection on 401", func(t *testing.T) {
		forge := newFakeForgeServer(t, "org", "private-grain")
		forge.wantAuthHeader = "Bearer secret-pat-123"
		const daemonRef = "ghcr.io/org/private-grain/grain:sha-111111111111"
		addV1Release(forge, "channel-main", "github.com/org/private-grain", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, false)
		forge.releases["channel-main"].redirectBlobURL = forge.blobSrv.URL

		dk := newFakeDockerEnv(t)
		dir := t.TempDir()

		// 1. Wrong/missing token names the connection to fix.
		badU := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "ghcr.io/org/private-grain/grain",
				Source:     ImageSourceReleaseAssets,
				RefFile:    filepath.Join(dir, "bad.env"),
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "github.com/org/private-grain",
					ClientForHost: forge.clientFunc("expired-tok", "corp-github-conn", ""),
				},
			},
			StatusFile: filepath.Join(dir, "bad-status.json"),
		})
		badStatus := runUpgradeToCompletion(t, badU, "main")
		if badStatus.Phase != PhaseFailed || !strings.Contains(badStatus.Detail, "corp-github-conn") {
			t.Fatalf("expected error naming connection corp-github-conn, got %+v", badStatus)
		}

		// 2. Valid token reaches API and is stripped on redirect to blobSrv.
		goodU := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:   "ghcr.io/org/private-grain/grain",
				Source:       ImageSourceReleaseAssets,
				RefFile:      filepath.Join(dir, "good.env"),
				LoadCmd:      dk.loadCmd(map[string][2]string{testCommit1: {daemonRef, testDaemonID1}}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				RemoveCmd:    []string{"true"},
				ListCmd:      []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "github.com/org/private-grain",
					ClientForHost: forge.clientFunc("secret-pat-123", "corp-github-conn", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "good-status.json"),
		})
		goodStatus := runUpgradeToCompletion(t, goodU, "main")
		if goodStatus.Phase != PhaseOK {
			t.Fatalf("goodStatus = %+v", goodStatus)
		}
		if forge.blobAuthSeen.Load() {
			t.Error("Authorization header leaked onto cross-host blob storage redirect")
		}
	})

	t.Run("ambient CLI host connection streams release assets via CommandForgeTransport", func(t *testing.T) {
		gitproxytest.Install()
		forge := newFakeForgeServer(t, "team", "edition")
		const daemonRef = "edition-local/grain:sha-111111111111"
		addV1Release(forge, "channel-main", gitproxytest.Host+"/team/edition", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, false)
		// Rewrite asset URLs in the release response to use gitproxytest.Host so
		// CommandForgeTransport validates the host guard on both metadata and downloads.
		spec := forge.releases["channel-main"]
		// Clear custom URLs so assetDownloadURL builds https://depot.code.forge.test/api/v3/repos/.../releases/assets/<id>
		// OR serve /repos/team/edition/releases/assets/<id> on forge.srv:
		_ = spec

		// Build a fake `forgecli` executable that forwards `forgecli api <endpoint>`
		// to our hermetic httptest server, translating https://depot.code.forge.test to forge.srv.URL.
		binDir := t.TempDir()
		cliPath := filepath.Join(binDir, gitproxytest.Command)
		cliScript := "#!/bin/sh\n" +
			"set -eu\n" +
			"endpoint=''\n" +
			"while [ $# -gt 0 ]; do\n" +
			"  case \"$1\" in\n" +
			"    api) shift ;;\n" +
			"    --hostname|-X|-H) shift 2 ;;\n" +
			"    -i|--include) shift ;;\n" +
			"    *) endpoint=\"$1\"; shift ;;\n" +
			"  esac\n" +
			"done\n" +
			"case \"$endpoint\" in\n" +
			"  https://" + gitproxytest.Host + "*) url=\"" + forge.srv.URL + "${endpoint#https://" + gitproxytest.Host + "}\" ;;\n" +
			"  http://*|https://*) url=\"$endpoint\" ;;\n" +
			"  /api/v3/*) url=\"" + forge.srv.URL + "$endpoint\" ;;\n" +
			"  *) url=\"" + forge.srv.URL + "/api/v3/${endpoint#/}\" ;;\n" +
			"esac\n" +
			"body=$(curl -fsS \"$url\" | sed 's|" + forge.srv.URL + "|https://" + gitproxytest.Host + "|g')\n" +
			"printf 'HTTP/1.1 200 OK\\r\\nContent-Type: application/octet-stream\\r\\n\\r\\n%s' \"$body\"\n"
		if err := os.WriteFile(cliPath, []byte(cliScript), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		hostDef, err := gitproxy.NormalizeCredentialHost(gitproxy.CredentialHost{Host: gitproxytest.Host})
		if err != nil {
			t.Fatalf("NormalizeCredentialHost(%s): %v", gitproxytest.Host, err)
		}
		ambientClient, ok := gitproxy.AmbientHostHTTPClient(hostDef, gitproxytest.Source, 30*time.Second)
		if !ok {
			t.Fatal("AmbientHostHTTPClient returned false")
		}

		dk := newFakeDockerEnv(t)
		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:   "edition-local/grain",
				Source:       ImageSourceReleaseAssets,
				RefFile:      filepath.Join(dir, "image.env"),
				LoadCmd:      dk.loadCmd(map[string][2]string{testCommit1: {daemonRef, testDaemonID1}}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				RemoveCmd:    []string{"true"},
				ListCmd:      []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					Repository: gitproxytest.Host + "/team/edition",
					StagingDir: dir,
					ClientForHost: func(context.Context, string, string, string) (*ReleaseHostClient, error) {
						return &ReleaseHostClient{
							Client:     ambientClient,
							Connection: "ambient-forge",
						}, nil
					},
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})

		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseOK || status.Source != string(ImageSourceReleaseAssets) {
			t.Fatalf("ambient CLI upgrade failed: %+v", status)
		}
	})
}

func TestBidirectionalFallbackAndTrackDefaults(t *testing.T) {
	shortRestartGrace(t)

	t.Run("registry falls back to release-assets when configured", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		const daemonRef = "edition-local/grain:sha-111111111111"
		addV1Release(forge, "channel-main", "depot.code.forge.test/team/edition", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, false)

		dk := newFakeDockerEnv(t)
		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:       "edition-local/grain",
				Source:           ImageSourceRegistry,
				Fallback:         ImageSourceReleaseAssets,
				FallbackExplicit: true,
				RefFile:          filepath.Join(dir, "image.env"),
				PullCmd:          []string{"sh", "-c", `echo "registry down" >&2; exit 1`, "sh"},
				LoadCmd:          dk.loadCmd(map[string][2]string{testCommit1: {daemonRef, testDaemonID1}}),
				InspectIDCmd:     dk.inspectIDCmd(),
				TagCmd:           dk.tagCmd(),
				RunCmd:           dk.runCmd(),
				RemoveCmd:        []string{"true"},
				ListCmd:          []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "depot.code.forge.test/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})
		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseOK || status.Source != string(ImageSourceReleaseAssets) {
			t.Fatalf("status = %+v", status)
		}
		if !strings.Contains(status.Detail, "registry pull failed") {
			t.Errorf("detail = %q, want fallback reason recorded", status.Detail)
		}
	})

	t.Run("RegisterTrackDefaults sets explicit edition source and lists release tags", func(t *testing.T) {
		installtracktest.Register()
		RegisterTrackDefaults(installtracktest.Track, TrackImageDefaults{
			Source:            ImageSourceReleaseAssets,
			ReleaseRepository: "depot.code.forge.test/acme/grain",
		})
		t.Cleanup(func() { UnregisterTrackDefaults(installtracktest.Track) })

		forge := newFakeForgeServer(t, "acme", "grain")
		addV1Release(forge, "channel-main", "depot.code.forge.test/acme/grain", testCommit1, "sha-111111111111", installtracktest.Repository+":sha-111111111111", testDaemonID1, false)
		addV1Release(forge, "v3.2.0", "depot.code.forge.test/acme/grain", testCommit2, "v3.2.0", installtracktest.Repository+":v3.2.0", testDaemonID2, false)

		u := New(Config{
			Image: &ImageConfig{
				Repository: installtracktest.Repository,
				Track:      installtracktest.Track,
				ReleaseAssets: ReleaseAssetsConfig{
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
		})
		if !u.IsImageSourceExplicit() || u.EffectiveImageFallback() != "" {
			t.Fatalf("track defaults with explicit Source must disable automatic registry fallback: explicit=%v fb=%q",
				u.IsImageSourceExplicit(), u.EffectiveImageFallback())
		}
		latest, err := u.LatestRelease(context.Background())
		if err != nil || latest != "v3.2.0" {
			t.Fatalf("LatestRelease = (%q, %v), want (v3.2.0, nil)", latest, err)
		}
	})
}

func runUpgradeToCompletion(t *testing.T, u *Upgrader, branch string) Status {
	t.Helper()
	if err := u.Start(branch); err != nil {
		t.Fatalf("Start(%q): %v", branch, err)
	}
	<-u.Done()
	st, err := u.Status()
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	return st
}

// addV2Channel publishes channel as a contract-v2 pointer: a release
// holding only a schema-2 images.json whose images name immutable, the
// release that holds their assets.
func addV2Channel(t *testing.T, f *fakeForgeServer, channel, immutable string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(f.releases[immutable].assets["images.json"], &m); err != nil {
		t.Fatal(err)
	}
	m["schema"] = 2
	for role, v := range m["images"].(map[string]any) {
		if role == "future-role" {
			continue
		}
		v.(map[string]any)["release"] = immutable
	}
	raw, _ := json.Marshal(m)
	f.releases[channel] = &fakeReleaseSpec{
		tag:             channel,
		targetCommitish: f.releases[immutable].targetCommitish,
		assets:          map[string][]byte{"images.json": raw},
	}
}

func TestReleaseContractV2ChannelPointersAndLatestMarker(t *testing.T) {
	shortRestartGrace(t)
	const daemonRef = "edition-local/grain:sha-111111111111"
	run := func(t *testing.T, forge *fakeForgeServer, branch string) (Status, *fakeDockerEnv) {
		t.Helper()
		dk := newFakeDockerEnv(t)
		dir := t.TempDir()
		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository:   "edition-local/grain",
				Source:       ImageSourceReleaseAssets,
				RefFile:      filepath.Join(dir, "image.env"),
				PullCmd:      []string{"false"},
				LoadCmd:      dk.loadCmd(map[string][2]string{testCommit1: {daemonRef, testDaemonID1}}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				RemoveCmd:    []string{"true"},
				ListCmd:      []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "github.com/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})
		return runUpgradeToCompletion(t, u, branch), dk
	}

	t.Run("schema 2 channel loads each asset from the release it names", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "sha-111111111111", "github.com/team/edition", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, false)
		addV2Channel(t, forge, "channel-main", "sha-111111111111")
		status, _ := run(t, forge, "main")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if forge.tarDownloads.Load() != 1 {
			t.Errorf("tarball downloads = %d, want 1 (from sha-111111111111)", forge.tarDownloads.Load())
		}
		if !slices.Contains(forge.lastAskedTags, "sha-111111111111") {
			t.Errorf("asked tags %v, want the immutable release looked up by tag", forge.lastAskedTags)
		}
	})

	t.Run("schema 2 image naming a moving release is refused", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "sha-111111111111", "github.com/team/edition", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, false)
		addV2Channel(t, forge, "channel-main", "sha-111111111111")
		raw := strings.Replace(string(forge.releases["channel-main"].assets["images.json"]), `"release":"sha-111111111111"`, `"release":"channel-other"`, 1)
		forge.releases["channel-main"].assets["images.json"] = []byte(raw)
		status, _ := run(t, forge, "main")
		if status.Phase != PhaseFailed || !strings.Contains(status.Detail, "immutable") {
			t.Fatalf("status = %+v, want a refusal naming an immutable release", status)
		}
	})

	t.Run("slashed release branch resolves channel-rc-v3.2 pointer while keeping rc..v3.2 docker tag and channel", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "sha-111111111111", "github.com/team/edition", testCommit1, "sha-111111111111", daemonRef, testDaemonID1, false)
		addV2Channel(t, forge, "channel-rc-v3.2", "sha-111111111111")

		dk := newFakeDockerEnv(t)
		dir := t.TempDir()
		refFile := filepath.Join(dir, "image.env")
		u := newUpgrader(t, Config{
			AllowedBranches: []string{"main", "rc/v*"},
			Image: &ImageConfig{
				Repository:   "edition-local/grain",
				Source:       ImageSourceReleaseAssets,
				RefFile:      refFile,
				PullCmd:      []string{"false"},
				LoadCmd:      dk.loadCmd(map[string][2]string{testCommit1: {daemonRef, testDaemonID1}}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				RemoveCmd:    []string{"true"},
				ListCmd:      []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "github.com/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})

		status := runUpgradeToCompletion(t, u, "rc/v3.2")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if want := []string{"channel-rc-v3.2", "sha-111111111111"}; !slices.Equal(forge.lastAskedTags, want) {
			t.Errorf("GET .../releases/tags/* = %v, want %v", forge.lastAskedTags, want)
		}
		if got := dk.getTag("edition-local/grain:rc..v3.2"); got != testDaemonID1 {
			t.Errorf("edition-local/grain:rc..v3.2 = %q, want %q", got, testDaemonID1)
		}
		if got := readFile(t, refFile); got != "GRAIN_IMAGE=edition-local/grain:rc..v3.2\n" {
			t.Errorf("RefFile = %q, want GRAIN_IMAGE=edition-local/grain:rc..v3.2\\n", got)
		}
		if got := u.Channel(); got != "rc/v3.2" {
			t.Errorf("u.Channel() = %q, want %q", got, "rc/v3.2")
		}
	})

	t.Run("latest resolves through the releases/latest marker", func(t *testing.T) {
		forge := newFakeForgeServer(t, "team", "edition")
		addV1Release(forge, "v3.2.0", "github.com/team/edition", testCommit1, "v3.2.0", daemonRef, testDaemonID1, false)
		forge.latestTag = "v3.2.0"
		status, _ := run(t, forge, "latest")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if slices.Contains(forge.lastAskedTags, "latest") {
			t.Errorf("asked for a release tagged latest (%v); want GET .../releases/latest", forge.lastAskedTags)
		}
	})
}

// The upgrade pane may point release-asset upgrades only at a release
// repository this deployment could already boot images from: one its
// flags or track name, or the release repository of an image
// repository it may move onto. A release asset is checked only against
// the images.json beside it, so an arbitrary repository there would let
// anyone holding deployment:upgrade choose what the daemon boots.
func TestSetImageSourceHoldsTheReleaseRepositoryToTheAllowedOnes(t *testing.T) {
	dir := t.TempDir()
	u := New(Config{Image: &ImageConfig{
		Repository:         "ghcr.io/ai-on-gke/grain",
		SourceOverrideFile: filepath.Join(dir, "source.json"),
		ReleaseAssets: ReleaseAssetsConfig{
			Repository: "ghe.example.test/platform/grain",
			Asset:      "flag-asset.tar.gz",
		},
	}})
	for _, repo := range []string{"github.com/attacker/grain", "ghe.example.test/attacker/grain", "evil.example.test/ai-on-gke/grain"} {
		if err := u.SetImageSource(ImageSourceOverride{Mode: "release-assets", ReleaseRepository: repo}); err == nil {
			t.Errorf("SetImageSource accepted release repository %s", repo)
		}
	}
	if got := u.EffectiveReleaseAssets().Repository; got != "ghe.example.test/platform/grain" {
		t.Fatalf("a refused override moved the release repository to %q", got)
	}
	for _, repo := range []string{"github.com/ai-on-gke/grain", "GitHub.com/AI-on-GKE/grain", "ghe.example.test/platform/grain"} {
		if err := u.SetImageSource(ImageSourceOverride{Mode: "release-assets", ReleaseRepository: repo}); err != nil {
			t.Errorf("SetImageSource(%s): %v", repo, err)
		}
	}
	// An override that names no repository or asset goes back to the
	// daemon's flags rather than clearing them.
	if err := u.SetImageSource(ImageSourceOverride{Mode: "registry"}); err != nil {
		t.Fatal(err)
	}
	ra := u.EffectiveReleaseAssets()
	if ra.Repository != "ghe.example.test/platform/grain" || ra.Asset != "flag-asset.tar.gz" {
		t.Fatalf("an empty override left repository %q, asset %q; want the flags' values", ra.Repository, ra.Asset)
	}

	// A file written before the repository fell out of the allowed set
	// is not trusted on the next start either.
	raw, _ := json.Marshal(ImageSourceOverride{Mode: "release-assets", ReleaseRepository: "github.com/attacker/grain"})
	if err := os.WriteFile(filepath.Join(dir, "source.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	u = New(Config{Image: &ImageConfig{
		Repository:         "ghcr.io/ai-on-gke/grain",
		SourceOverrideFile: filepath.Join(dir, "source.json"),
	}})
	if got := u.EffectiveReleaseAssets().Repository; got != "github.com/ai-on-gke/grain" {
		t.Fatalf("release repository loaded from the override file = %q, want github.com/ai-on-gke/grain", got)
	}

	// A private or loopback image repository with no -upgrade-release-repo
	// derives no allowed release repository at all; refusing an override must
	// say so plainly instead of rendering an empty "allowed are  (...)".
	unmapped := New(Config{Image: &ImageConfig{
		Repository:         "127.0.0.1:4255/x/grain",
		SourceOverrideFile: filepath.Join(dir, "unmapped-source.json"),
	}})
	err := unmapped.SetImageSource(ImageSourceOverride{
		Mode:              "release-assets",
		ReleaseRepository: "github.com/acme/grain-fork",
	})
	if err == nil || !strings.Contains(err.Error(), "none is configured for this image repository") || strings.Contains(err.Error(), "allowed are ") {
		t.Fatalf("unmapped SetImageSource error = %v, want 'none is configured for this image repository'", err)
	}
}

// A deployment with -upgrade-verify-cmd defaults to the registry, and
// the upgrade pane may not move it onto release-asset tarballs, as its
// source or its fallback: that is the host operator's flag to set. A
// fallback the flags already set may be kept.
func TestSetImageSourceKeepsAVerifyingDeploymentOffReleaseAssets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "source.json")
	newUpgrader := func(fallback ImageSource) *Upgrader {
		return New(Config{Image: &ImageConfig{
			Repository:         "ghcr.io/ai-on-gke/grain",
			SourceOverrideFile: file,
			VerifyCmd:          []string{"cosign", "verify"},
			Fallback:           fallback,
			FallbackExplicit:   fallback != "",
		}})
	}
	u := newUpgrader("")
	for _, ov := range []ImageSourceOverride{
		{Mode: "release-assets"},
		{Mode: "registry", Fallback: "release-assets"},
	} {
		if err := u.SetImageSource(ov); !errors.Is(err, errReleaseAssetsNeedOperator) {
			t.Errorf("SetImageSource(%+v) = %v, want errReleaseAssetsNeedOperator", ov, err)
		}
	}
	if got, fb := u.EffectiveImageSource(), u.EffectiveImageFallback(); got != ImageSourceRegistry || fb != "" {
		t.Fatalf("after refused overrides: source %q, fallback %q; want registry and none", got, fb)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused override was written to %s (%v)", file, err)
	}
	if err := u.SetImageSource(ImageSourceOverride{Mode: "registry"}); err != nil {
		t.Fatalf("SetImageSource(registry): %v", err)
	}

	if err := newUpgrader(ImageSourceReleaseAssets).SetImageSource(ImageSourceOverride{Mode: "registry", Fallback: "release-assets"}); err != nil {
		t.Errorf("keeping the flags' own release-assets fallback: %v", err)
	}

	// Nor does a file written before the verifier was configured.
	raw, _ := json.Marshal(ImageSourceOverride{Mode: "release-assets", Fallback: "release-assets", FallbackExplicit: true})
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	u = newUpgrader("")
	if got, fb := u.EffectiveImageSource(), u.EffectiveImageFallback(); got != ImageSourceRegistry || fb != "" {
		t.Fatalf("override file loaded as source %q, fallback %q; want registry and none", got, fb)
	}
}

// The release host's token goes only to that host's API, never to an
// asset URL the API's answer names somewhere else.
func TestReleaseTokenStaysOnTheAPIHost(t *testing.T) {
	r := &resolvedRelease{apiBase: "https://api.github.com", hostClient: &ReleaseHostClient{Token: "tok"}}
	for target, want := range map[string]string{
		"https://api.github.com/repos/o/r/releases/assets/1": "Bearer tok",
		"https://elsewhere.example.test/asset":               "",
	} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		r.authorize(req)
		if got := req.Header.Get("Authorization"); got != want {
			t.Errorf("Authorization to %s = %q, want %q", target, got, want)
		}
	}
}

// publishRelease adds a v1 release carrying images.json and its daemon
// tarball, created at createdAt with id.
func publishRelease(f *fakeForgeServer, tag, commit, createdAt string, id int64) {
	addV1Release(f, tag, "github.com/team/edition", commit, tag, "edition-local/grain:sha-"+commit[:12], daemonIDForCommit(commit), false)
	f.releases[tag].createdAt, f.releases[tag].id = createdAt, id
}

// tagUnpublished adds the empty release the publisher writes while asset
// publishing is off: every tag, no assets, "tagged, not published".
func tagUnpublished(f *fakeForgeServer, tag, commit, createdAt string, id int64) {
	f.releases[tag] = &fakeReleaseSpec{
		tag:             tag,
		targetCommitish: commit,
		assets:          map[string][]byte{},
		id:              id,
		createdAt:       createdAt,
		body:            "Release " + tag + ": tagged, not published (PUBLISH_RELEASE_ASSETS is off).",
	}
}

func daemonIDForCommit(commit string) string {
	switch commit {
	case testCommit1:
		return testDaemonID1
	case testCommit2:
		return testDaemonID2
	}
	return "sha256:" + strings.Repeat("e", 64)
}

func (d *fakeDockerEnv) loads(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(d.loadedLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(raw)))
}

// TestReleaseAssetsStayOnTheLastPublishedRelease: while the publisher
// writes every tag but uploads no assets ("tagged, not published"), an
// explicit release-assets deployment keeps working against the last
// release that was published, refuses a tag that never was, and is never
// offered one.
func TestReleaseAssetsStayOnTheLastPublishedRelease(t *testing.T) {
	shortRestartGrace(t)
	const (
		commit3 = "3333333333333333333333333333333333333333"
		commit4 = "4444444444444444444444444444444444444444"
	)
	// v3.2.0 (published, commit1) < sha-222222222222 (published, newest
	// published) < v3.3.0 / sha-333333333333 / channel-main (tagged, not
	// published).
	setup := func(t *testing.T) *fakeForgeServer {
		forge := newFakeForgeServer(t, "team", "edition")
		publishRelease(forge, "v3.2.0", testCommit1, "2026-11-01T10:00:00Z", 10)
		publishRelease(forge, "sha-222222222222", testCommit2, "2026-11-02T10:00:00Z", 11)
		tagUnpublished(forge, "sha-333333333333", commit3, "2026-11-04T10:00:00Z", 12)
		tagUnpublished(forge, "v3.3.0", commit3, "2026-11-04T10:00:01Z", 13)
		tagUnpublished(forge, "channel-main", commit3, "2026-11-04T10:00:02Z", 14)
		return forge
	}
	newU := func(t *testing.T, forge *fakeForgeServer, dk *fakeDockerEnv) *Upgrader {
		t.Helper()
		dir := t.TempDir()
		return newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "edition-local/grain",
				Source:     ImageSourceReleaseAssets,
				RefFile:    filepath.Join(dir, "image.env"),
				PullCmd:    []string{"false"},
				LoadCmd: dk.loadCmd(map[string][2]string{
					testCommit1: {"edition-local/grain:sha-111111111111", testDaemonID1},
					testCommit2: {"edition-local/grain:sha-222222222222", testDaemonID2},
				}),
				InspectIDCmd: dk.inspectIDCmd(),
				TagCmd:       dk.tagCmd(),
				RunCmd:       dk.runCmd(),
				RemoveCmd:    []string{"true"},
				ListCmd:      []string{"true"},
				ReleaseAssets: ReleaseAssetsConfig{
					Repository:    "github.com/team/edition",
					ClientForHost: forge.clientFunc("", "", ""),
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "status.json"),
		})
	}

	t.Run("main resolves to the newest published release when channel-main is tagged, not published", func(t *testing.T) {
		forge := setup(t)
		dk := newFakeDockerEnv(t)
		u := newU(t, forge, dk)
		if !u.releaseAssetsOnly() {
			t.Fatal("explicit release-assets with no fallback should load release assets only")
		}
		status := runUpgradeToCompletion(t, u, "main")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if got := dk.getTag("edition-local/grain:main"); got != testDaemonID2 {
			t.Errorf("edition-local/grain:main = %q, want %q (sha-222222222222)", got, testDaemonID2)
		}
	})

	t.Run("main resolves to the newest published release when channel-main is missing", func(t *testing.T) {
		forge := setup(t)
		delete(forge.releases, "channel-main")
		dk := newFakeDockerEnv(t)
		status := runUpgradeToCompletion(t, newU(t, forge, dk), "main")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if got := dk.getTag("edition-local/grain:main"); got != testDaemonID2 {
			t.Errorf("edition-local/grain:main = %q, want %q (sha-222222222222)", got, testDaemonID2)
		}
	})

	t.Run("latest on an empty release resolves to the newest published version", func(t *testing.T) {
		forge := setup(t)
		forge.latestTag = "v3.3.0"
		// A newer published prerelease is passed over for latest.
		publishRelease(forge, "v3.2.1", commit4, "2026-11-03T10:00:00Z", 15)
		forge.releases["v3.2.1"].prerelease = true
		dk := newFakeDockerEnv(t)
		status := runUpgradeToCompletion(t, newU(t, forge, dk), "latest")
		if status.Phase != PhaseOK {
			t.Fatalf("status = %+v", status)
		}
		if got := dk.getTag("edition-local/grain:latest"); got != testDaemonID1 {
			t.Errorf("edition-local/grain:latest = %q, want %q (v3.2.0)", got, testDaemonID1)
		}
	})

	t.Run("a channel mid-swap still waits for its publisher", func(t *testing.T) {
		forge := setup(t)
		forge.releases["channel-main"] = &fakeReleaseSpec{
			tag:             "channel-main",
			targetCommitish: commit3,
			assets:          map[string][]byte{"images.json.next": []byte(`{"schema":2}`)},
		}
		dk := newFakeDockerEnv(t)
		status := runUpgradeToCompletion(t, newU(t, forge, dk), "main")
		if status.Phase != PhaseFailed || !strings.Contains(status.Detail, "try again later") {
			t.Fatalf("status = %+v, want a failure that waits for the swap", status)
		}
		if n := dk.loads(t); n != 0 {
			t.Errorf("docker load ran %d times for a channel mid-swap, want 0", n)
		}
	})

	for _, tc := range []struct {
		name, target, want string
		remove             bool
	}{
		{"unpublished version", "v3.3.0", "release v3.3.0 on github.com/team/edition is not published; the newest published release is v3.2.0", false},
		{"unpublished sha", "sha-333333333333", "release sha-333333333333 on github.com/team/edition is not published; the newest published release is sha-222222222222", false},
		{"missing version", "v3.4.0", "release v3.4.0 on github.com/team/edition is not published; the newest published release is v3.2.0", true},
		{"missing sha", "sha-444444444444", "release sha-444444444444 on github.com/team/edition is not published; the newest published release is sha-222222222222", true},
	} {
		t.Run(tc.name+" is refused naming the newest published release", func(t *testing.T) {
			forge := setup(t)
			if _, exists := forge.releases[tc.target]; exists == tc.remove {
				t.Fatalf("fixture: release %s exists=%v", tc.target, exists)
			}
			dk := newFakeDockerEnv(t)
			u := newU(t, forge, dk)
			status := runUpgradeToCompletion(t, u, tc.target)
			if status.Phase != PhaseFailed || !strings.Contains(status.Detail, tc.want) {
				t.Fatalf("status = %+v, want a refusal saying %q", status, tc.want)
			}
			if n := dk.loads(t); n != 0 {
				t.Errorf("docker load ran %d times for %s, want 0", n, tc.target)
			}
			if forge.tarDownloads.Load() != 0 {
				t.Errorf("downloaded %d tarballs for %s, want 0", forge.tarDownloads.Load(), tc.target)
			}
		})
	}

	t.Run("an unpublished target is nothing for the automatic upgrade to install", func(t *testing.T) {
		forge := setup(t)
		dk := newFakeDockerEnv(t)
		u := newU(t, forge, dk)
		if err := os.WriteFile(u.cfg.Image.RefFile, []byte("GRAIN_IMAGE=edition-local/grain:v3.2.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		same, err := u.AlreadyRunning(context.Background(), "v3.3.0")
		if err != nil || !same {
			t.Errorf("AlreadyRunning(v3.3.0) = (%v, %v), want (true, nil)", same, err)
		}
	})

	for _, tc := range []struct{ name, asset string }{
		{"missing sandbox tarball", "guest-image.tar.gz"},
		{"missing sandbox checksum", "guest-image.tar.gz.sha256"},
		{"corrupt sandbox checksum", ""},
	} {
		t.Run(tc.name+" is refused before anything is loaded", func(t *testing.T) {
			forge := newFakeForgeServer(t, "team", "edition")
			addV1Release(forge, "v3.2.0", "github.com/team/edition", testCommit1, "v3.2.0", "edition-local/grain:sha-111111111111", testDaemonID1, true)
			if tc.asset != "" {
				delete(forge.releases["v3.2.0"].assets, tc.asset)
			} else {
				forge.releases["v3.2.0"].assets["guest-image.tar.gz.sha256"] = []byte(strings.Repeat("0", 64) + "  guest-image.tar.gz\n")
			}
			dk := newFakeDockerEnv(t)
			status := runUpgradeToCompletion(t, newU(t, forge, dk), "v3.2.0")
			if status.Phase != PhaseFailed || !strings.Contains(status.Detail, "guest") {
				t.Fatalf("status = %+v, want a refusal naming the guest image", status)
			}
			if n := dk.loads(t); n != 0 {
				t.Errorf("docker load ran %d times, want 0 (nothing loaded before every asset checks out)", n)
			}
		})
	}

	t.Run("Targets and LatestRelease never offer an unpublished version", func(t *testing.T) {
		forge := setup(t)
		forge.releases["v3.4.0"] = &fakeReleaseSpec{tag: "v3.4.0", assets: map[string][]byte{"notes.txt": []byte("notes")}}
		u := newU(t, forge, newFakeDockerEnv(t))
		latest, err := u.LatestRelease(context.Background())
		if err != nil || latest != "v3.2.0" {
			t.Fatalf("LatestRelease = (%q, %v), want (v3.2.0, nil)", latest, err)
		}
		var versions []string
		for _, tg := range u.Targets(context.Background()).Targets {
			if tg.Kind == TargetVersion {
				versions = append(versions, tg.Name)
			}
		}
		if !slices.Equal(versions, []string{"v3.2.0"}) {
			t.Errorf("Targets versions = %v, want [v3.2.0]", versions)
		}
	})

	t.Run("a registry fallback still lists every tagged version", func(t *testing.T) {
		forge := setup(t)
		u := newU(t, forge, newFakeDockerEnv(t))
		u.cfg.Image.Fallback, u.cfg.Image.FallbackExplicit = ImageSourceRegistry, true
		if u.releaseAssetsOnly() {
			t.Fatal("a registry fallback should not load release assets only")
		}
		tags, err := u.listReleaseTags(context.Background())
		if err != nil || !slices.Contains(tags, "v3.3.0") {
			t.Errorf("listReleaseTags = (%v, %v), want v3.3.0 listed", tags, err)
		}
	})
}

func TestPublishedReleasesSortsNewestFirstAndSkipsPointers(t *testing.T) {
	t.Parallel()
	manifest := []githubReleaseAsset{{Name: "images.json"}}
	got := publishedReleases([]githubRelease{
		{ID: 1, TagName: "v1.0.0", CreatedAt: "2026-10-01T00:00:00Z", Assets: manifest},
		{ID: 2, TagName: "channel-main", CreatedAt: "2026-11-05T00:00:00Z", Assets: manifest},
		{ID: 3, TagName: "sha-aaaaaaaaaaaa", CreatedAt: "2026-11-03T00:00:00Z", Assets: manifest},
		{ID: 4, TagName: "sha-bbbbbbbbbbbb", CreatedAt: "2026-11-03T00:00:00Z", Assets: manifest},
		{ID: 5, TagName: "v1.1.0", CreatedAt: "2026-11-04T00:00:00Z", Draft: true, Assets: manifest},
		{ID: 6, TagName: "v1.2.0", CreatedAt: "2026-11-04T00:00:00Z", Body: "tagged, not published"},
		{ID: 7, TagName: "latest", CreatedAt: "2026-11-05T00:00:00Z", Assets: manifest},
	}, "images.json")
	var tags []string
	for _, r := range got {
		tags = append(tags, r.TagName)
	}
	if want := []string{"sha-bbbbbbbbbbbb", "sha-aaaaaaaaaaaa", "v1.0.0"}; !slices.Equal(tags, want) {
		t.Errorf("publishedReleases = %v, want %v", tags, want)
	}
	if rel, ok := pickPublishedRelease(got, true); !ok || rel.TagName != "v1.0.0" {
		t.Errorf("pickPublishedRelease(preferVersion) = %q, %v; want v1.0.0", rel.TagName, ok)
	}
	if rel, ok := pickPublishedRelease(got, false); !ok || rel.TagName != "sha-bbbbbbbbbbbb" {
		t.Errorf("pickPublishedRelease = %q, %v; want sha-bbbbbbbbbbbb", rel.TagName, ok)
	}
	if _, ok := pickPublishedRelease(nil, true); ok {
		t.Error("pickPublishedRelease(nil) found a release")
	}
}

// A deployment told to load one named tarball (-upgrade-release-asset)
// rather than an images.json manifest counts a release carrying that
// tarball as published. It used to hold every release to images.json, so
// such a deployment listed no versions at all and could not upgrade
// (docs/test-runs/2026-11-07-releases-upgrades-ci.md, Bug F2).
func TestSingleReleaseAssetCountsAsPublished(t *testing.T) {
	forge := newFakeForgeServer(t, "team", "edition")
	forge.releases["v3.2.0"] = &fakeReleaseSpec{
		tag: "v3.2.0", targetCommitish: testCommit1, id: 10, createdAt: "2026-11-01T10:00:00Z",
		assets: map[string][]byte{"grain-image.tar.gz": []byte("not really a tarball")},
	}
	tagUnpublished(forge, "v3.2.1", testCommit2, "2026-11-02T10:00:00Z", 11)
	dir := t.TempDir()
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "edition-local/grain",
			Source:     ImageSourceReleaseAssets,
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"false"},
			ReleaseAssets: ReleaseAssetsConfig{
				Repository:    "github.com/team/edition",
				Asset:         "grain-image.tar.gz",
				ClientForHost: forge.clientFunc("", "", ""),
			},
		},
		StatusFile: filepath.Join(dir, "status.json"),
	})
	if !u.releaseAssetsOnly() {
		t.Fatal("explicit release-assets with no fallback should load release assets only")
	}
	tags, err := u.listReleaseTags(context.Background())
	if err != nil {
		t.Fatalf("listReleaseTags: %v", err)
	}
	if !slices.Contains(tags, "v3.2.0") {
		t.Errorf("release tags = %v, want v3.2.0 (it carries the configured grain-image.tar.gz)", tags)
	}
	if slices.Contains(tags, "v3.2.1") {
		t.Errorf("release tags = %v, want v3.2.1 left out (tagged, not published)", tags)
	}
}
