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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCancelDuringHealthCheckRemovesTheCheckContainer is the regression test
// for docs/test-runs/2026-11-13-upgrade-cancel.md F1. Cancelling an image
// upgrade while its health check ran killed the `docker run` client and
// left the container behind, so the cancel's own `docker image rm` of the
// image it had just pulled was refused ("container … is using its
// referenced image") and the image stayed on the host. The fake docker
// below models exactly that: a container exists from `run` until `rm -f`,
// and `image rm` is refused while it does.
func TestCancelDuringHealthCheckRemovesTheCheckContainer(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerLog := filepath.Join(dir, "docker.log")
	container := filepath.Join(dir, "container")
	entered := filepath.Join(dir, "entered")
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fakeDocker := `#!/bin/sh
printf '%s\n' "$*" >> "` + dockerLog + `"
case "$1" in
  pull) exit 0 ;;
  image)
    case "$2" in
      inspect) printf 'docker.io/grainai/grain@` + digest + `\n' ;;
      rm)
        if [ -f "` + container + `" ]; then
          echo "conflict: unable to delete $3 (must be forced) - container c0ffee is using its referenced image" >&2
          exit 1
        fi
        ;;
    esac
    exit 0
    ;;
  run)
    label=""
    for a in "$@"; do case "$a" in --label=*) label="${a#--label=}" ;; esac; done
    printf '%s\n' "$label" > "` + container + `"
    touch "` + entered + `"
    exec sleep 30
    ;;
  ps)
    for a in "$@"; do
      case "$a" in label=*)
        [ -f "` + container + `" ] && [ "$(cat "` + container + `")" = "${a#label=}" ] && echo c0ffee
      ;; esac
    done
    exit 0
    ;;
  rm)
    [ "$2" = -f ] && [ "$3" = c0ffee ] && rm -f "` + container + `"
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	refFile := filepath.Join(dir, "image.env")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE=docker.io/grainai/grain:v3.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := New(Config{
		StatusFile:      filepath.Join(dir, "status.json"),
		HealthCheckArgs: []string{"schema-version"},
		Image: &ImageConfig{
			Repository: "docker.io/grainai/grain",
			RefFile:    refFile,
		},
	})
	if err := u.Start("v3.2.0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFileExists(t, entered)

	if cancelled, err := u.Cancel("alice@example.com", "wrong tag"); err != nil || !cancelled {
		t.Fatalf("Cancel = (%v, %v), want (true, nil)", cancelled, err)
	}
	st, err := u.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != PhaseCancelled {
		t.Fatalf("Phase = %q (%q), want %q", st.Phase, st.Detail, PhaseCancelled)
	}
	if _, err := os.Stat(container); err == nil {
		t.Errorf("the health check's container is still there after the cancel")
	}
	raw, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	if !strings.Contains(log, "run --rm --pull=never --label="+u.checkContainerLabel()+" ") {
		t.Errorf("the health check ran without its label; docker was asked:\n%s", log)
	}
	rmAt := strings.Index(log, "rm -f c0ffee")
	imageRmAt := strings.Index(log, "image rm docker.io/grainai/grain:v3.2.0")
	if rmAt < 0 || imageRmAt < 0 || imageRmAt < rmAt {
		t.Errorf("want the check container removed, then the pulled image; docker was asked:\n%s", log)
	}
	if strings.Contains(log, "image rm docker.io/grainai/grain:v3.1.0") {
		t.Errorf("the cancel removed the image this deployment runs; docker was asked:\n%s", log)
	}
	if got, _ := os.ReadFile(refFile); !strings.Contains(string(got), ":v3.1.0") {
		t.Errorf("ref file = %q after the cancel, want it untouched", got)
	}
	// Another deployment's checks (another ref file) carry another label.
	other := New(Config{Image: &ImageConfig{Repository: "docker.io/grainai/grain", RefFile: refFile + ".other"}})
	if other.checkContainerLabel() == u.checkContainerLabel() {
		t.Errorf("two ref files share check label %q", u.checkContainerLabel())
	}
}
