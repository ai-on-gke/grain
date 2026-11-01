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

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// mirroredSandboxImageFile is where, under the data directory, the
// daemon records the mirrored sandbox images it has run: the one in
// force and the one before it.
const mirroredSandboxImageFile = "sandbox-image-mirrored.json"

// mirroredSandboxRecord is that file's content. Both are tag references
// (`<mirror>:<tag>`, no digest), which is what local docker lists.
type mirroredSandboxRecord struct {
	Current  string `json:"current"`
	Previous string `json:"previous,omitempty"`
}

var mirroredSandboxCleanMu sync.Mutex

// cleanOldMirroredSandboxImages removes the local docker tags that
// earlier mirrors left behind, after a startup or live-config mirror of
// sourceRef into mirrorRepo (as target) has succeeded. An upgrade cleans
// up after its own mirror in pkg/upgrade's cleanOldImages; this is the
// same thing for the mirrors that happen outside one -- a restart on a
// new build, or a mirror changed in Settings -- which otherwise leave
// one tag per build and per mirror repository on the host for good.
//
// Like cleanOldImages it keeps the current image and the previous one,
// so going back a build does not have to pull again. "Previous" is the
// image this daemon ran before the current one, which only the daemon
// itself knows, so it is recorded in the data directory
// (mirroredSandboxImageFile). With no record yet -- the first mirror on
// this data directory, or the first after this cleanup existed -- the
// record is written and nothing is removed: what came before is unknown.
//
// The repositories cleaned are those of the current and previous images
// and, when the record moves on, of the image that falls out of it, so a
// mirror changed twice does not strand the first repository's tag. Only
// tags are removed (`docker image rm repo:tag`, never by ID or with -f),
// and never the source image, nor an image a setting names: docker
// refuses a tag a container still uses, and that refusal is logged and
// left. The mirror registry itself is not pruned; see
// docs/sandbox-image-mirror.md for the cleanup policy that does that.
func cleanOldMirroredSandboxImages(ctx context.Context, cfg Config, sourceRef, mirrorRepo, target string) {
	if cfg.DataDir == "" || !sandboxMirrorUsesDocker() {
		return
	}
	target = tagRef(target)
	sourceRepo := imageRepository(sourceRef)
	if target == "" || imageRepository(target) == sourceRepo {
		return
	}
	mirroredSandboxCleanMu.Lock()
	defer mirroredSandboxCleanMu.Unlock()

	path := filepath.Join(cfg.DataDir, mirroredSandboxImageFile)
	rec, known, err := readMirroredSandboxRecord(path)
	if err != nil {
		log.Printf("WARNING: not cleaning up old mirrored sandbox images: reading %s: %v", path, err)
		return
	}
	var evicted string
	if rec.Current != target {
		evicted = rec.Previous
		rec = mirroredSandboxRecord{Current: target, Previous: rec.Current}
		if err := writeMirroredSandboxRecord(path, rec); err != nil {
			log.Printf("WARNING: not cleaning up old mirrored sandbox images: recording %s: %v", target, err)
			return
		}
	}
	if !known {
		return
	}

	keep := map[string]bool{}
	for _, ref := range []string{rec.Current, rec.Previous, sourceRef, cfg.K8sGranuleImage, cfg.CustomSandboxImage, cfg.SandboxImage} {
		if ref = tagRef(ref); ref != "" {
			keep[ref] = true
		}
	}
	var repos []string
	for _, ref := range []string{rec.Current, rec.Previous, evicted} {
		repo := imageRepository(tagRef(ref))
		if repo != "" && repo != sourceRepo && !slices.Contains(repos, repo) {
			repos = append(repos, repo)
		}
	}
	for _, repo := range repos {
		out, err := runSandboxMirrorDocker(ctx, nil, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}", repo)
		if err != nil {
			log.Printf("grain daemon: listing old mirrored sandbox images in %s to remove: %v", repo, err)
			continue
		}
		for _, ref := range strings.Fields(out) {
			if imageRepository(ref) != repo || imageTag(ref) == "" || imageTag(ref) == "<none>" || keep[ref] {
				continue
			}
			if _, err := runSandboxMirrorDocker(ctx, nil, "image", "rm", ref); err != nil {
				log.Printf("grain daemon: leaving old mirrored sandbox image %s in place: %v", ref, err)
				continue
			}
			log.Printf("grain daemon: removed old mirrored sandbox image %s (keeping %s and the previous %s)",
				ref, rec.Current, orUnknownImage(rec.Previous))
		}
	}
}

// tagRef is ref without a digest: `repo:tag@sha256:...` is listed by
// docker as `repo:tag`.
func tagRef(ref string) string {
	ref, _, _ = strings.Cut(strings.TrimSpace(ref), "@")
	return ref
}

// readMirroredSandboxRecord reads the record, reporting known=false when
// there is none yet.
func readMirroredSandboxRecord(path string) (rec mirroredSandboxRecord, known bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rec, false, nil
	}
	if err != nil {
		return rec, false, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		// A record that does not parse says nothing about what came
		// before, the same as no record: start a new one.
		return mirroredSandboxRecord{}, false, nil
	}
	return rec, rec.Current != "", nil
}

func writeMirroredSandboxRecord(path string, rec mirroredSandboxRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
