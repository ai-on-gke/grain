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
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// containerGrainSourceMount is where a self-debug sandbox container has
// its grain-src volume mounted, read-only. Outside the workspace on
// purpose: the volume holds the tree one directory down (see
// grainSourceVolume), and placeGrainSource links <workspace>/grain-src
// to that directory, so nothing about the mount depends on where the
// run's checkout happens to be cloned.
const containerGrainSourceMount = "/var/lib/grain-src"

// grainSourceVolumeTree is the directory inside a grain-src volume that
// holds the tree. It only ever appears complete: a populating container
// extracts beside it and renames into place last, so its presence is
// what says a volume is ready, and a volume a crash left half-written is
// filled again rather than mounted.
const grainSourceVolumeTree = "tree"

// GrainSourceVolumePrefix names the Docker volume holding one revision
// of the deployment's baked source: grain-src-<revision>.
const GrainSourceVolumePrefix = "grain-src-"

// GrainSourceMounter is a Sandbox whose backend mounted the deployment's
// baked source into it when the sandbox was created, so placeGrainSource
// has only to point grain-src/ at it rather than stream a copy in.
// MountedGrainSource reports the in-sandbox path of that tree and the
// revision it holds, and ok false for a sandbox created without one.
type GrainSourceMounter interface {
	MountedGrainSource() (path, revision string, ok bool)
}

// grainSourceVolume returns the volume holding the baked source at
// ContainerConfig.GrainSourceDir and the revision it holds, populating it
// first if this is the first sandbox to need that revision -- once per
// revision for as long as the Docker daemon keeps the volume, not once
// per run. "" with a nil error means there is nothing to mount (no baked
// copy, or one whose revision is unknown and so cannot name a volume),
// and placeGrainSource falls back on streaming.
//
// The tree goes in from this process's side, as the same cached archive
// placeGrainSource would stream: in container mode the daemon's own
// /usr/local/share/grain/src is a path inside the daemon's container,
// not one the Docker host could bind-mount.
func (c *ContainerSandboxes) grainSourceVolume(ctx context.Context) (string, string, error) {
	src, ok := readBakedSource(c.cfg.GrainSourceDir)
	if !ok || src.Revision == "" {
		return "", "", nil
	}
	volume := GrainSourceVolumePrefix + src.Revision
	c.srcMu.Lock()
	defer c.srcMu.Unlock()
	if c.srcReady[volume] {
		return volume, src.Revision, nil
	}
	if _, err := c.run(ctx, "volume", "inspect", volume); err != nil {
		// Two daemons sharing one Docker may race to create it; either
		// one's volume is as good as the other's.
		if _, err := c.run(ctx, "volume", "create", "--label", "grain.grain-src=true", volume); err != nil {
			if _, inspectErr := c.run(ctx, "volume", "inspect", volume); inspectErr != nil {
				return "", "", fmt.Errorf("creating volume %s: %w", volume, err)
			}
		}
	}
	archive, err := bakedSourceArchive(src)
	if err != nil {
		return "", "", err
	}
	// Root inside a throwaway container with no network and no
	// capabilities, so the tree is root's and the sandbox user -- who
	// gets the volume read-only anyway -- owns none of it. A volume an
	// earlier daemon already filled is left exactly as it is.
	script := fmt.Sprintf(`set -e
cd /v
[ -d '%[1]s' ] && exit 0
tmp=".partial.$$"
rm -rf "$tmp"
mkdir "$tmp"
tar --no-same-owner -xzf - -C "$tmp"
find "$tmp" -type f -exec chmod a-w {} +
[ -d '%[1]s' ] || mv "$tmp" '%[1]s'
rm -rf "$tmp"`, grainSourceVolumeTree)
	cmd := exec.CommandContext(ctx, c.docker(), "run", "--rm", "-i", "-u", "0",
		"--network", "none", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--label", "grain.grain-src=true",
		"--mount", "type=volume,src="+volume+",dst=/v",
		"--entrypoint", "sh", c.cfg.Image, "-c", script)
	// The docker CLI's own environment (DOCKER_HOST, DOCKER_CONFIG),
	// which is all it reads: nothing passes into the container but the
	// archive on stdin.
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(string(archive))
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("filling volume %s: %w: %s", volume, err, strings.TrimSpace(string(out)))
	}
	c.srcReady[volume] = true
	return volume, src.Revision, nil
}

// grainSourceMountArgs is the `docker run` mount of volume, read-only at
// containerGrainSourceMount.
func grainSourceMountArgs(volume string) []string {
	return []string{"--mount", "type=volume,src=" + volume + ",dst=" + containerGrainSourceMount + ",readonly"}
}

// MountedGrainSource implements GrainSourceMounter.
func (s *containerSandbox) MountedGrainSource() (string, string, bool) {
	if s.srcVolume == "" {
		return "", "", false
	}
	return containerGrainSourceMount + "/" + grainSourceVolumeTree, s.srcRevision, true
}
