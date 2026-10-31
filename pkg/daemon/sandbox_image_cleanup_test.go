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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCleanupDocker is a docker that lists, for `image ls <repo>`, the
// lines of images whose repository is <repo>, forgets an image `image rm`
// names, and logs every argv.
func fakeCleanupDocker(t *testing.T, images ...string) (logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "docker.log")
	list := filepath.Join(dir, "images")
	if err := os.WriteFile(list, []byte(strings.Join(images, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$@" >> "` + logFile + `"
if [ "$1 $2" = "image ls" ]; then
  eval "repo=\${$#}"
  grep "^$repo:" "` + list + `"
fi
if [ "$1 $2" = "image rm" ]; then
  grep -vxF "$3" "` + list + `" > "` + list + `.new"; mv "` + list + `.new" "` + list + `"
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := sandboxMirrorDockerCmd
	sandboxMirrorDockerCmd = []string{filepath.Join(dir, "docker")}
	t.Cleanup(func() { sandboxMirrorDockerCmd = saved })
	return logFile
}

func removedImages(t *testing.T, logFile string) []string {
	t.Helper()
	data, _ := os.ReadFile(logFile)
	var removed []string
	for _, line := range strings.Split(string(data), "\n") {
		if ref, ok := strings.CutPrefix(line, "image rm "); ok {
			removed = append(removed, ref)
		}
	}
	return removed
}

// A startup or live mirror outside an upgrade removes the local tags
// older mirrors left behind, keeping the current and the previous one --
// the walkthrough in docs/test-runs/2026-10-29-sandbox-image-mirror-registry-auth.md
// found every earlier tag still on the host after a new build and two
// mirror changes.
func TestCleanOldMirroredSandboxImagesKeepsCurrentAndPrevious(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{DataDir: dataDir}
	logFile := fakeCleanupDocker(t,
		"127.0.0.1:5000/src/guest:sha-aaaa111",
		"127.0.0.1:5000/src/guest:sha-bbbb222",
		"127.0.0.1:5001/mirror/guest:sha-0000000",
		"127.0.0.1:5001/mirror/guest:sha-aaaa111",
		"127.0.0.1:5001/mirror/guest:sha-bbbb222",
		"127.0.0.1:5001/mirror/guest:<none>",
		"127.0.0.1:5001/live/guest:sha-bbbb222",
	)

	// The first mirror on this data directory knows nothing of what came
	// before, so it only records.
	cleanOldMirroredSandboxImages(t.Context(), cfg, "127.0.0.1:5000/src/guest:sha-aaaa111",
		"127.0.0.1:5001/mirror/guest", "127.0.0.1:5001/mirror/guest:sha-aaaa111@sha256:f97b")
	if got := removedImages(t, logFile); len(got) != 0 {
		t.Fatalf("first mirror removed %v; want nothing", got)
	}

	// A new build: sha-aaaa111 is now the previous one, and the older
	// sha-0000000 goes. Neither <none> nor the source repository is touched.
	cleanOldMirroredSandboxImages(t.Context(), cfg, "127.0.0.1:5000/src/guest:sha-bbbb222",
		"127.0.0.1:5001/mirror/guest", "127.0.0.1:5001/mirror/guest:sha-bbbb222@sha256:c266")
	if got := removedImages(t, logFile); strings.Join(got, ",") != "127.0.0.1:5001/mirror/guest:sha-0000000" {
		t.Fatalf("after a new build removed %v; want only mirror/guest:sha-0000000", got)
	}
	rec, known, err := readMirroredSandboxRecord(filepath.Join(dataDir, mirroredSandboxImageFile))
	if err != nil || !known || rec.Current != "127.0.0.1:5001/mirror/guest:sha-bbbb222" || rec.Previous != "127.0.0.1:5001/mirror/guest:sha-aaaa111" {
		t.Fatalf("record = %+v, %v, %v", rec, known, err)
	}

	// The mirror changes in Settings: mirror/guest:sha-bbbb222 becomes
	// the previous image, and sha-aaaa111, which falls out of the record,
	// is removed from the old repository.
	if err := os.Truncate(logFile, 0); err != nil {
		t.Fatal(err)
	}
	cleanOldMirroredSandboxImages(t.Context(), cfg, "127.0.0.1:5000/src/guest:sha-bbbb222",
		"127.0.0.1:5001/live/guest", "127.0.0.1:5001/live/guest:sha-bbbb222")
	if got := removedImages(t, logFile); strings.Join(got, ",") != "127.0.0.1:5001/mirror/guest:sha-aaaa111" {
		t.Fatalf("after a mirror change removed %v; want only mirror/guest:sha-aaaa111", got)
	}
}

// An image a setting names in the mirror repository is an operator's own
// pin, and stays.
func TestCleanOldMirroredSandboxImagesKeepsPinnedImages(t *testing.T) {
	dataDir := t.TempDir()
	if err := writeMirroredSandboxRecord(filepath.Join(dataDir, mirroredSandboxImageFile),
		mirroredSandboxRecord{Current: "127.0.0.1:5001/mirror/guest:sha-aaaa111"}); err != nil {
		t.Fatal(err)
	}
	logFile := fakeCleanupDocker(t,
		"127.0.0.1:5001/mirror/guest:custom",
		"127.0.0.1:5001/mirror/guest:sha-0000000",
		"127.0.0.1:5001/mirror/guest:sha-aaaa111",
		"127.0.0.1:5001/mirror/guest:sha-bbbb222",
	)
	cfg := Config{DataDir: dataDir, CustomSandboxImage: "127.0.0.1:5001/mirror/guest:custom@sha256:1234"}
	cleanOldMirroredSandboxImages(t.Context(), cfg, "127.0.0.1:5000/src/guest:sha-bbbb222",
		"127.0.0.1:5001/mirror/guest", "127.0.0.1:5001/mirror/guest:sha-bbbb222")
	if got := removedImages(t, logFile); strings.Join(got, ",") != "127.0.0.1:5001/mirror/guest:sha-0000000" {
		t.Fatalf("removed %v; want only sha-0000000", got)
	}
}

// Without a data directory there is nowhere to remember the previous
// image, so nothing is removed.
func TestCleanOldMirroredSandboxImagesNeedsADataDir(t *testing.T) {
	logFile := fakeCleanupDocker(t, "127.0.0.1:5001/mirror/guest:sha-0000000")
	cleanOldMirroredSandboxImages(t.Context(), Config{}, "127.0.0.1:5000/src/guest:sha-bbbb222",
		"127.0.0.1:5001/mirror/guest", "127.0.0.1:5001/mirror/guest:sha-bbbb222")
	if data, _ := os.ReadFile(logFile); len(data) != 0 {
		t.Fatalf("docker was run without a data directory:\n%s", data)
	}
}
