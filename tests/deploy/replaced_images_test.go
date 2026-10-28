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

package deploy

import (
	"os"
	"strings"
	"testing"
)

// fakeDocker is a docker(1) stand-in over a tiny image store, enough of
// it for setup.sh's image bookkeeping: REF maps a ref or id to an image
// id, SUB maps "<id> <subcommand>" to the sandbox image that image names,
// LIST holds "<repo> <tag> <id>" rows for `docker images`, and INUSE the
// ids a container still holds. Every rmi is appended to $RMI_LOG.
const fakeDocker = `
declare -A REF SUB INUSE
LIST=()
docker() {
  case "$1 $2" in
    "container inspect") [ -n "${DAEMON_IMAGE:-}" ] && echo "$DAEMON_IMAGE" ;;
    "image inspect")
      local ref="${!#}" id
      id="${REF[$ref]:-}"
      [ -n "$id" ] || return 1
      [ "$3" = "--format" ] && echo "$id"
      return 0 ;;
    "ps -aq")
      local f="${!#}"
      [ -n "${INUSE[${f#ancestor=}]:-}" ] && echo container1
      return 0 ;;
    *)
      case "$1" in
        run)
          local image="${@: -2:1}" sub="${!#}" id
          id="${REF[$image]:-}"
          echo "${SUB[$id $sub]:-}" ;;
        rmi)
          echo "rmi $2" >>"$RMI_LOG"
          local id="${REF[$2]:-}" k
          [ -n "$id" ] || return 1
          for k in "${!REF[@]}"; do [ "${REF[$k]}" = "$id" ] && unset "REF[$k]"; done
          return 0 ;;
        images)
          local want="$2" repo tag id row
          for row in "${LIST[@]}"; do
            read -r repo tag id <<<"$row"
            [ "$repo" = "$want" ] && echo "$tag $id"
          done
          return 0 ;;
      esac ;;
  esac
}
`

// An upgrade re-run whose pull moved the tag (grain:main republished)
// removes the image it replaced and the sandbox images only that image
// named, and keeps everything the new image still uses -- and anything a
// container is still holding.
func TestSetupRerunPrunesTheImagesItReplaced(t *testing.T) {
	rmiLog := t.TempDir() + "/rmi"
	script := logFn + fakeDocker +
		shellFunction(t, "image_id") + shellFunction(t, "sandbox_image_refs") +
		shellFunction(t, "record_replaced_image") + shellFunction(t, "prune_replaced_images") + `
GRAIN_DOCKER_AVAILABLE=1
UNIT_IMAGE_REF_FILE=/nonexistent
GRAIN_IMAGE_REF=g/grain:main
DAEMON_IMAGE=idA
REF[idA]=idA
REF[g/k8s-sandbox:sha-a]=idKA; REF[idKA]=idKA
REF[g/container-sandbox:sha-a]=idCA; REF[idCA]=idCA
REF[g/guest:shared]=idG; REF[idG]=idG
REF[g/held:sha-a]=idH; REF[idH]=idH
SUB[idA k8s-sandbox-image]=g/k8s-sandbox:sha-a
SUB[idA container-sandbox-image]=g/container-sandbox:sha-a
SUB[idA sandbox-image]=g/guest:shared
record_replaced_image
echo "recorded $REPLACED_IMAGE_ID"
# pull_image moves grain:main to B, which shares the guest image.
REF[g/grain:main]=idB; REF[idB]=idB
REF[g/k8s-sandbox:sha-b]=idKB
SUB[idB k8s-sandbox-image]=g/k8s-sandbox:sha-b
SUB[idB sandbox-image]=g/guest:shared
DAEMON_IMAGE=idB
INUSE[idH]=1
SUB[idA sandbox-image]=g/guest:shared
GRAIN_GRANULE_OCI_IMAGE=g/held:sha-a
prune_replaced_images
`
	out := runBash(t, script, "RMI_LOG="+rmiLog)
	contains(t, out, "recorded idA")
	rmis := rmiLines(rmiLog)
	for _, want := range []string{"rmi g/k8s-sandbox:sha-a", "rmi g/container-sandbox:sha-a", "rmi idA"} {
		if !strings.Contains(rmis, want+"\n") {
			t.Errorf("prune_replaced_images did not run %q; it ran:\n%s\n%s", want, rmis, out)
		}
	}
	for _, kept := range []string{"g/guest:shared", "idG", "idB", "g/grain:main", "sha-b", "held"} {
		if strings.Contains(rmis, kept) {
			t.Errorf("prune_replaced_images removed %s, which this run's image still uses:\n%s", kept, rmis)
		}
	}

	// A re-run that pulled the image it already ran removes nothing.
	rmiLog = t.TempDir() + "/rmi"
	runBash(t, script+"\nREPLACED_IMAGE_ID=idB\nprune_replaced_images\n", "RMI_LOG="+rmiLog)
	if rmis := rmiLines(rmiLog); strings.Count(rmis, "\n") != 3 {
		t.Errorf("a re-run onto the image it already ran removed more:\n%s", rmis)
	}
}

// A container still holding the replaced image keeps it.
func TestSetupRerunKeepsAReplacedImageAContainerStillUses(t *testing.T) {
	rmiLog := t.TempDir() + "/rmi"
	out := runBash(t, logFn+fakeDocker+
		shellFunction(t, "image_id")+shellFunction(t, "sandbox_image_refs")+
		shellFunction(t, "prune_replaced_images")+`
GRAIN_DOCKER_AVAILABLE=1
GRAIN_IMAGE_REF=g/grain:main
REF[g/grain:main]=idB; REF[idB]=idB; REF[idA]=idA
INUSE[idA]=1
REPLACED_IMAGE_ID=idA
prune_replaced_images
`, "RMI_LOG="+rmiLog)
	contains(t, out, "kept idA: a container still uses it")
	if rmis := rmiLines(rmiLog); rmis != "" {
		t.Errorf("removed an image a container still uses:\n%s", rmis)
	}
}

// --uninstall names the untagged grain images a moved tag left behind,
// the sandbox images they name, and the untagged images of those
// sandbox repositories (task 1058, N3).
func TestUninstallNamesUntaggedImages(t *testing.T) {
	out := runBash(t, logFn+fakeDocker+
		shellFunction(t, "sandbox_image_refs")+shellFunction(t, "uninstall_images")+`
GRAIN_IMAGE=g/grain
REF[g/grain:main]=idB; REF[idA]=idA
LIST=("g/grain main idB" "g/grain <none> idA" "g/k8s-sandbox sha-b idKB" "g/k8s-sandbox <none> idKX" "g/other <none> idO")
SUB[idB k8s-sandbox-image]=g/k8s-sandbox:sha-b
SUB[idA k8s-sandbox-image]=g/k8s-sandbox@sha256:aaaa
uninstall_images
`)
	lines := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		lines[l] = true
	}
	for _, want := range []string{"g/grain:main", "idA", "g/k8s-sandbox:sha-b", "g/k8s-sandbox@sha256:aaaa", "idKX"} {
		if !lines[want] {
			t.Errorf("uninstall_images did not name %s:\n%s", want, out)
		}
	}
	if lines["idO"] {
		t.Errorf("uninstall_images named an image of a repository grain never pulled from:\n%s", out)
	}
}

// rmiLines is what the fake docker's rmi log holds; nothing if it never
// ran rmi at all.
func rmiLines(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}
