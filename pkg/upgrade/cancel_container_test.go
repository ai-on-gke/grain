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
const cancelTestDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestCancelDuringHealthCheckRemovesTheCheckContainer(t *testing.T) {
	u, dir, dockerLog, container := cancelDuringHealthCheck(t, "")
	refFile := filepath.Join(dir, "image.env")
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

// cancelDuringHealthCheck starts an image upgrade from v3.1.0 to v3.2.0
// against a fake docker, cancels it while its health check's container
// runs, and checks the cancel took. prePullID, when non-empty, puts the
// target tag on the fake host at that image ID before the upgrade pulls it.
func cancelDuringHealthCheck(t *testing.T, prePullID string) (u *Upgrader, dir, dockerLog, container string) {
	t.Helper()
	dir = t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerLog = filepath.Join(dir, "docker.log")
	container = filepath.Join(dir, "container")
	entered := filepath.Join(dir, "entered")
	pulled := filepath.Join(dir, "pulled")
	present := filepath.Join(dir, "present")
	if prePullID != "" {
		if err := os.WriteFile(present, []byte(prePullID+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fakeDocker := `#!/bin/sh
printf '%s\n' "$*" >> "` + dockerLog + `"
case "$1" in
  pull) touch "` + pulled + `"; exit 0 ;;
  image)
    case "$2" in
      inspect)
        if [ "$4" = '{{.Id}}' ]; then
          if [ ! -f "` + pulled + `" ]; then
            if [ ! -f "` + present + `" ]; then
              echo "Error: No such image: $5" >&2
              exit 1
            fi
            cat "` + present + `"
            exit 0
          fi
          printf '%s\n' "` + cancelTestDigest + `"
          exit 0
        fi
        printf 'docker.io/grainai/grain@` + cancelTestDigest + `\n' ;;
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
	u = New(Config{
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
	return u, dir, dockerLog, container
}

// TestCancelKeepsATargetTagThatWasAlreadyHere is the regression test for
// docs/test-runs/2026-11-13-upgrade-cancel.md F7: a cancel removed the
// target tag even when it had been on this host at the same image ID before
// the upgrade pulled it (an earlier rollback's target, or an image the
// operator pulled), not only what the cancelled upgrade itself brought.
func TestCancelKeepsATargetTagThatWasAlreadyHere(t *testing.T) {
	u, _, dockerLog, container := cancelDuringHealthCheck(t, cancelTestDigest)
	if _, err := os.Stat(container); err == nil {
		t.Errorf("the health check's container is still there after the cancel")
	}
	raw, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	if !strings.Contains(log, "image inspect --format {{.Id}} docker.io/grainai/grain:v3.2.0\npull ") {
		t.Errorf("want the target tag looked for before the pull; docker was asked:\n%s", log)
	}
	if !strings.Contains(log, "rm -f c0ffee") {
		t.Errorf("want the check container removed; docker was asked:\n%s", log)
	}
	if strings.Contains(log, "image rm docker.io/grainai/grain:v3.2.0") {
		t.Errorf("the cancel removed a target tag that was on the host before the upgrade; docker was asked:\n%s", log)
	}
	_ = u
}

// TestCancelRemovesATargetTagThatMovedDuringPull checks that when a mutable
// tag (:main, :latest, or a re-pushed tag) already existed on the host at an
// older image ID and `docker pull` moved it onto a newly fetched image,
// cancelling before cut-over still removes the newly fetched image rather
// than mistaking it for the pre-existing one.
func TestCancelRemovesATargetTagThatMovedDuringPull(t *testing.T) {
	const olderID = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	_, _, dockerLog, container := cancelDuringHealthCheck(t, olderID)
	if _, err := os.Stat(container); err == nil {
		t.Errorf("the health check's container is still there after the cancel")
	}
	raw, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	rmAt := strings.Index(log, "rm -f c0ffee")
	imageRmAt := strings.Index(log, "image rm docker.io/grainai/grain:v3.2.0")
	if rmAt < 0 || imageRmAt < 0 || imageRmAt < rmAt {
		t.Errorf("want the check container removed, then the moved target tag; docker was asked:\n%s", log)
	}
}

// TestCancelDuringPrePullInspectKeepsPreExistingTargetTag checks that a
// cancel arriving while pre-pull `docker image inspect` is running (so the
// inspect is killed by context cancellation and `docker pull` never starts)
// does not read the killed inspect as "absent before pull" and delete a
// pre-existing tag.
func TestCancelDuringPrePullInspectKeepsPreExistingTargetTag(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerLog := filepath.Join(dir, "docker.log")
	entered := filepath.Join(dir, "entered")
	fakeDocker := `#!/bin/sh
printf '%s\n' "$*" >> "` + dockerLog + `"
if [ "$1" = image ] && [ "$2" = inspect ]; then
  touch "` + entered + `"
  exec sleep 30
fi
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

	if cancelled, err := u.Cancel("alice@example.com", "aborted early"); err != nil || !cancelled {
		t.Fatalf("Cancel = (%v, %v), want (true, nil)", cancelled, err)
	}
	st, err := u.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != PhaseCancelled {
		t.Fatalf("Phase = %q (%q), want %q", st.Phase, st.Detail, PhaseCancelled)
	}
	raw, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	if strings.Contains(log, "pull ") {
		t.Errorf("pull ran after cancel during pre-pull inspect; docker was asked:\n%s", log)
	}
	if strings.Contains(log, "image rm") {
		t.Errorf("cancel deleted a tag when pull never started; docker was asked:\n%s", log)
	}
}
