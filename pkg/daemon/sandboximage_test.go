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
	"errors"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

// captureLog collects what a call logs, so a test can assert on the line
// an operator actually sees at startup.
func captureLog(t *testing.T, f func()) string {
	t.Helper()
	var logs strings.Builder
	prevLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prevLog) })
	f()
	return logs.String()
}

// The default is what an upgrade actually moves: a deployment that named
// no sandbox image gets this build's own stamp, resolved now rather than
// whenever scripts/setup.sh last ran on the host.
func TestSandboxImageCreateArgsFillsInThisBuildsOwnStamp(t *testing.T) {
	t.Parallel()
	args := sandboxImageCreateArgs(Config{GranuleCreateArgs: []string{"-disk-mode=overlay"}})

	want := []string{"-disk-mode=overlay", "-kontur-image", DefaultSandboxImage}
	if !slices.Equal(args, want) {
		t.Errorf("create args = %q, want %q", args, want)
	}
}

// A deployment that pinned one on purpose keeps it -- and -sandbox-image
// is where it says so, since that is the one form scripts/setup.sh never
// wrote for a deployment that pinned nothing.
func TestSandboxImageCreateArgsKeepsADeliberatePin(t *testing.T) {
	t.Parallel()
	cfg := Config{
		GranuleSandboxes:  true,
		GranuleCreateArgs: []string{"-disk-mode=overlay"},
		SandboxImage:      "example.test/mirror/guest:pinned",
	}
	args := sandboxImageCreateArgs(cfg)

	want := []string{"-disk-mode=overlay", "-kontur-image", "example.test/mirror/guest:pinned"}
	if !slices.Equal(args, want) {
		t.Errorf("create args = %q, want %q", args, want)
	}
	if got := sandboxImagePin(cfg); got != "example.test/mirror/guest:pinned" {
		t.Errorf("sandboxImagePin = %q, want the deliberate pin", got)
	}
}

// A -kontur-create-arg pin naming an image from somewhere other than the
// stamp's own repository is kept too, in both of konturctl's spellings: a
// mirror or a private copy is not a shape setup.sh ever wrote by itself,
// and the stamp it would be replaced with may not even be pullable from
// such a host.
func TestSandboxImageCreateArgsLeavesAnImageFromElsewhereAlone(t *testing.T) {
	t.Parallel()
	for _, pinned := range [][]string{
		{"-kontur-image", "example.test/mirror/guest:pinned"},
		{"-kontur-image=example.test/mirror/guest:pinned"},
	} {
		args := sandboxImageCreateArgs(Config{GranuleCreateArgs: slices.Clone(pinned)})

		if !slices.Equal(args, pinned) {
			t.Errorf("create args = %q, want %q untouched", args, pinned)
		}
		if got, ok := pinnedSandboxImage(args); !ok || got != "example.test/mirror/guest:pinned" {
			t.Errorf("pinnedSandboxImage = %q, %v, want the pinned reference", got, ok)
		}
	}
}

// The pin winning is fine; the pin winning *silently* is the bug. A
// deployment whose deliberately pinned image is not this build's own
// stamp is running a daemon and sandboxes from different commits, and
// pays for a sandbox pull on every upgrade that nothing then runs. It
// gets a WARNING naming both tags (grain/task-306).
func TestSandboxImageCreateArgsWarnsWhenTheDeliberatePinIsNotThisBuildsStamp(t *testing.T) {
	got := captureLog(t, func() {
		sandboxImageCreateArgs(Config{SandboxImage: "example.test/mirror/guest:sha-2f89890"})
	})

	for _, want := range []string{
		"WARNING",
		"example.test/mirror/guest:sha-2f89890",
		"-sandbox-image",
		DefaultSandboxImage,
		"pulled and never run",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("startup log = %q, want it to say %q", got, want)
		}
	}
}

// The case this deployment was actually in (grain/task-373): the unit
// still carries the -kontur-image scripts/setup.sh once wrote for every
// kontur host, naming the guest of the commit the host was *installed*
// at. That is not a pin anybody chose, and honouring it overrode three
// consecutive upgrades -- each of which had pulled the guest it then did
// not run -- so the stamp is adopted over it.
func TestSandboxImageCreateArgsAdoptsTheStampOverAnInstallTimePin(t *testing.T) {
	stale := imageRepository(DefaultSandboxImage) + ":sha-2f89890"
	cfg := Config{
		GranuleSandboxes:  true,
		GranuleCreateArgs: []string{"-disk-mode=overlay", "-kontur-image", stale},
	}

	var args []string
	got := captureLog(t, func() { args = sandboxImageCreateArgs(cfg) })

	want := []string{"-disk-mode=overlay", "-kontur-image", DefaultSandboxImage}
	if !slices.Equal(args, want) {
		t.Errorf("create args = %q, want %q -- the stamp, in place of the install-time pin", args, want)
	}
	// Both tags again, and the way back: an operator who did mean to hold
	// that guest has one flag to add.
	for _, wantSaid := range []string{DefaultSandboxImage, stale, "-sandbox-image"} {
		if !strings.Contains(got, wantSaid) {
			t.Errorf("startup log = %q, want it to say %q", got, wantSaid)
		}
	}
	// Nothing for the pane or the upgrade to flag: the image in force is
	// the image this build expects, which is the point of adopting it.
	if pin := sandboxImagePin(cfg); pin != "" {
		t.Errorf("sandboxImagePin = %q, want empty once the stamp is adopted", pin)
	}
	image := uiSandboxImage(cfg)
	if image == nil || image.Image != DefaultSandboxImage || image.Pinned {
		t.Errorf("uiSandboxImage = %+v, want the stamp, unpinned", image)
	}
}

// The other spelling of the same flag, and a pin the daemon has to leave
// where it is: konturctl's own -kontur-image=<ref> form is rewritten in
// place rather than appended to, so a VM is created with one image and
// not two.
func TestSandboxImageCreateArgsRewritesTheJoinedFormInPlace(t *testing.T) {
	t.Parallel()
	stale := imageRepository(DefaultSandboxImage) + ":sha-2f89890"
	args := sandboxImageCreateArgs(Config{GranuleCreateArgs: []string{"-kontur-image=" + stale, "-guest-port", "22"}})

	want := []string{"-kontur-image=" + DefaultSandboxImage, "-guest-port", "22"}
	if !slices.Equal(args, want) {
		t.Errorf("create args = %q, want %q", args, want)
	}
}

// A deployment that pinned exactly what this build is stamped for has no
// drift to report: a mirror or a private copy of the same tag is a
// perfectly good reason to pin, and warning about it every start would
// train an operator to ignore the line that matters.
func TestSandboxImageCreateArgsDoesNotWarnWhenThePinMatchesTheStamp(t *testing.T) {
	got := captureLog(t, func() {
		sandboxImageCreateArgs(Config{GranuleCreateArgs: []string{"-kontur-image=" + DefaultSandboxImage}})
	})

	if strings.Contains(got, "WARNING") {
		t.Errorf("startup log = %q, want no warning for a pin that agrees with the stamp", got)
	}
	if !strings.Contains(got, DefaultSandboxImage) {
		t.Errorf("startup log = %q, want it to still name the image in force", got)
	}
}

// A tag is what comes after the last colon, and only when that colon
// comes after the last slash: a registry carries a port, and a reference
// pinned by digest carries a colon of its own.
func TestImageRepositoryStripsTagsAndDigestsAndNothingElse(t *testing.T) {
	t.Parallel()
	for ref, want := range map[string]string{
		"ghcr.io/bwsalmon/grain/guest:sha-2f89890": "ghcr.io/bwsalmon/grain/guest",
		"ghcr.io/bwsalmon/grain/guest":             "ghcr.io/bwsalmon/grain/guest",
		"127.0.0.1:5000/grain-sandbox:old":         "127.0.0.1:5000/grain-sandbox",
		"127.0.0.1:5000/grain-sandbox":             "127.0.0.1:5000/grain-sandbox",
		"ghcr.io/bwsalmon/grain/guest@sha256:0000000000000000000000000000000000000000000000000000000000000000":             "ghcr.io/bwsalmon/grain/guest",
		"ghcr.io/bwsalmon/grain/guest:sha-2f89890@sha256:0000000000000000000000000000000000000000000000000000000000000000": "ghcr.io/bwsalmon/grain/guest",
		"127.0.0.1:5000/grain-sandbox:old@sha256:0000000000000000000000000000000000000000000000000000000000000000":         "127.0.0.1:5000/grain-sandbox",
	} {
		if got := imageRepository(ref); got != want {
			t.Errorf("imageRepository(%q) = %q, want %q", ref, got, want)
		}
	}
}

// The same two facts the warning carries, for the pane an operator is
// actually looking at when a sandbox starts behaving like an older
// commit: what sandboxes are built from, and what this build expects.
func TestUISandboxImageReportsBothTheImageInForceAndTheStamp(t *testing.T) {
	t.Parallel()
	pinned := uiSandboxImage(Config{
		GranuleSandboxes: true,
		SandboxImage:     "example.test/mirror/guest:sha-2f89890",
	})
	want := &ui.SandboxImage{
		Image:    "example.test/mirror/guest:sha-2f89890",
		Expected: DefaultSandboxImage,
		Pinned:   true,
	}
	if pinned == nil || *pinned != *want {
		t.Errorf("uiSandboxImage = %+v, want %+v", pinned, want)
	}

	// Unpinned, the daemon fills in its own stamp, so the pane has
	// nothing to flag: the two are the same reference by construction.
	unpinned := uiSandboxImage(Config{GranuleSandboxes: true})
	if unpinned == nil || unpinned.Image != DefaultSandboxImage || unpinned.Expected != DefaultSandboxImage || unpinned.Pinned {
		t.Errorf("uiSandboxImage = %+v, want this build's own stamp, unpinned", unpinned)
	}

	// A host-directory sandbox is a directory, and there is no image to
	// report at all.
	if got := uiSandboxImage(Config{}); got != nil {
		t.Errorf("uiSandboxImage = %+v for host sandboxing, want nil", got)
	}
}

// And the same two facts again for the upgrade that pulls one of them:
// the pin is handed to pkg/upgrade so its sandbox step can say the image
// it just spent a pull on is not the one this deployment will run.
func TestSandboxImagePinIsHandedToTheUpgrader(t *testing.T) {
	t.Parallel()
	cfg := Config{
		GranuleSandboxes:  true,
		GranuleCreateArgs: []string{"-disk-mode=overlay", "-kontur-image=example.test/mirror/guest:sha-2f89890"},
	}
	if got, want := sandboxImagePin(cfg), "example.test/mirror/guest:sha-2f89890"; got != want {
		t.Errorf("sandboxImagePin = %q, want %q", got, want)
	}
	// Nothing pinned, nothing to disagree with: the sandbox step reports
	// its pull exactly as it did before.
	if got := sandboxImagePin(Config{GranuleSandboxes: true, GranuleCreateArgs: []string{"-disk-mode=overlay"}}); got != "" {
		t.Errorf("sandboxImagePin = %q for an unpinned deployment, want empty", got)
	}
	if got := sandboxImagePin(Config{GranuleCreateArgs: cfg.GranuleCreateArgs}); got != "" {
		t.Errorf("sandboxImagePin = %q for host sandboxing, want empty", got)
	}
}

// A -kontur-image with no value at all is konturctl's to complain about,
// not this daemon's to guess at: the list goes through untouched, and the
// pane and the warning say "?" rather than inventing a reference.
func TestSandboxImageCreateArgsLeavesAValuelessPinAlone(t *testing.T) {
	in := []string{"-disk-mode=overlay", "-kontur-image"}
	cfg := Config{GranuleSandboxes: true, GranuleCreateArgs: in}

	var args []string
	got := captureLog(t, func() { args = sandboxImageCreateArgs(cfg) })

	if !slices.Equal(args, in) {
		t.Errorf("create args = %q, want %q untouched", args, in)
	}
	if !strings.Contains(got, "sandbox image ?") {
		t.Errorf("startup log = %q, want it to name the missing value as unknown", got)
	}
	if image := uiSandboxImage(cfg); image == nil || image.Image != "?" || !image.Pinned {
		t.Errorf("uiSandboxImage = %+v, want an unknown, pinned image", image)
	}
}

// The caller's slice is never appended to in place: cfg.granuleCreateArgs
// is read by more than this, and a flag list that grew a sandbox image
// behind its owner's back would be a surprise well away from here.
func TestSandboxImageCreateArgsDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()
	in := make([]string, 1, 8)
	in[0] = "-disk-mode=overlay"

	sandboxImageCreateArgs(Config{GranuleCreateArgs: in})

	if len(in) != 1 || in[0] != "-disk-mode=overlay" {
		t.Errorf("input = %q, want it untouched", in)
	}
}

// And not in place through the rewrite either: an install-time pin is
// replaced in the copy the daemon passes konturctl, while the list the
// rest of the daemon reads still says what the unit says.
func TestSandboxImageCreateArgsDoesNotMutateAPinItAdoptsOver(t *testing.T) {
	t.Parallel()
	stale := imageRepository(DefaultSandboxImage) + ":sha-2f89890"
	in := []string{"-kontur-image", stale}

	sandboxImageCreateArgs(Config{GranuleCreateArgs: in})

	if in[1] != stale {
		t.Errorf("input = %q, want the pin left where it was", in)
	}
}

// A release stamps its sandbox by digest (scripts/release-images.sh), and
// a stamp so pinned is still this build's own guest: an install-time
// `sha-` tag of the same repository is adopted over exactly as it is for
// a stamp by tag, and what VMs are created from is the pinned reference,
// unchanged -- the digest an upgrade verified.
func TestSandboxImageCreateArgsAdoptsADigestPinnedStamp(t *testing.T) {
	stamped := "ghcr.io/bwsalmon/grain/guest:sha-1234567@sha256:5a4db0c55a4db0c55a4db0c55a4db0c55a4db0c55a4db0c55a4db0c55a4db0c5"
	saved := DefaultSandboxImage
	DefaultSandboxImage = stamped
	t.Cleanup(func() { DefaultSandboxImage = saved })
	cfg := Config{
		GranuleSandboxes:  true,
		GranuleCreateArgs: []string{"-kontur-image", "ghcr.io/bwsalmon/grain/guest:sha-2f89890"},
	}

	var args []string
	captureLog(t, func() { args = sandboxImageCreateArgs(cfg) })

	if want := []string{"-kontur-image", stamped}; !slices.Equal(args, want) {
		t.Errorf("create args = %q, want %q", args, want)
	}
	if image := uiSandboxImage(cfg); image == nil || image.Image != stamped || image.Pinned {
		t.Errorf("uiSandboxImage = %+v, want the pinned stamp, unpinned", image)
	}
}

func TestUpgradeSandboxImageArgsIncludesK8sGranuleAndMirror(t *testing.T) {
	t.Parallel()
	if got := upgradeSandboxImageArgs(Config{}); got != nil {
		t.Fatalf("upgradeSandboxImageArgs(empty) = %v, want nil", got)
	}
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{
			name: "GranuleSandboxes",
			cfg:  Config{GranuleSandboxes: true},
		},
		{
			name: "K8sGranuleSandboxes",
			cfg:  Config{K8sGranuleSandboxes: true},
		},
		{
			name: "SandboxImageMirror",
			cfg:  Config{SandboxImageMirror: "us-docker.pkg.dev/cluster-project/grain/guest"},
		},
	} {
		if got := upgradeSandboxImageArgs(tc.cfg); !slices.Equal(got, []string{"sandbox-image"}) {
			t.Errorf("upgradeSandboxImageArgs(%s) = %v, want [sandbox-image]", tc.name, got)
		}
	}
}

func TestSandboxImageMirrorRepoAndEffectiveK8sGranuleImageMirrorsStampedGuest(t *testing.T) {
	ResetMirroredSandboxImageCacheForTest()
	t.Cleanup(ResetMirroredSandboxImageCacheForTest)

	stamped := "us-docker.pkg.dev/source-project/grain/guest:sha-31d0973@sha256:053664f0a62cf642aff3b59530eaee5b22441cdfd9ba74ff6af2632a50f8891a"
	savedStamp := DefaultSandboxImage
	DefaultSandboxImage = stamped
	t.Cleanup(func() { DefaultSandboxImage = savedStamp })

	mirrorDigest := "sha256:d210757e35832d3c8f47f11abd84c3d2f82fa0b7b2c88a13fd021d6999018a83"
	dir := t.TempDir()
	logFile := dir + "/docker.log"
	dockerScript := dir + "/docker"
	script := `#!/bin/sh
echo "$@" >> "` + logFile + `"
if [ "$1" = "image" ] && [ "$2" = "inspect" ]; then
  ref=""
  for arg in "$@"; do ref="$arg"; done
  case "$ref" in
    us-docker.pkg.dev/cluster-project/grain/guest:sha-31d0973)
      if grep -q "^push us-docker.pkg.dev/cluster-project/grain/guest:sha-31d0973$" "` + logFile + `" 2>/dev/null; then
        echo '["us-docker.pkg.dev/cluster-project/grain/guest@` + mirrorDigest + `"]'
        exit 0
      fi
      exit 1
      ;;
    *)
      exit 1
      ;;
  esac
fi
exit 0
`
	if err := os.WriteFile(dockerScript, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	savedDocker := sandboxMirrorDockerCmd
	savedToken := sandboxMirrorTokenSource
	sandboxMirrorDockerCmd = []string{dockerScript}
	sandboxMirrorTokenSource = nil
	t.Cleanup(func() {
		sandboxMirrorDockerCmd = savedDocker
		sandboxMirrorTokenSource = savedToken
	})

	// Explicit SandboxImageMirror setting mirrors DefaultSandboxImage and
	// resolves the mirror's RepoDigest, ignoring stale sha- pins in that
	// same mirror repository.
	cfg := Config{
		K8sGranuleSandboxes: true,
		SandboxImageMirror:  "us-docker.pkg.dev/cluster-project/grain/guest",
		K8sGranuleImage:     "us-docker.pkg.dev/cluster-project/grain/guest:sha-0000000@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		CustomSandboxImage:  "us-docker.pkg.dev/cluster-project/grain/guest:sha-0000000@sha256:1111111111111111111111111111111111111111111111111111111111111111",
	}
	wantMirrored := "us-docker.pkg.dev/cluster-project/grain/guest:sha-31d0973@" + mirrorDigest
	if got := customSandboxImageFor(cfg); got != "" {
		t.Fatalf("customSandboxImageFor(mirrored guest ref) = %q, want empty so stock mirrored image is used without drift warning", got)
	}
	if got := activeSandboxImageFor(cfg); got != wantMirrored {
		t.Fatalf("activeSandboxImageFor = %q, want %q", got, wantMirrored)
	}
	loggedBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	logged := string(loggedBytes)
	for _, wantCmd := range []string{
		"pull " + stamped,
		"tag " + stamped + " us-docker.pkg.dev/cluster-project/grain/guest:sha-31d0973",
		"push us-docker.pkg.dev/cluster-project/grain/guest:sha-31d0973",
	} {
		if !strings.Contains(logged, wantCmd) {
			t.Errorf("docker log missing %q:\n%s", wantCmd, logged)
		}
	}

	// Automatic derivation when K8sGranuleImage names a cross-project guest
	// sha- tag even if SandboxImageMirror is not explicitly set.
	derivedCfg := Config{
		K8sGranuleSandboxes: true,
		K8sGranuleImage:     "us-docker.pkg.dev/cluster-project/grain/guest:sha-0000000@sha256:1111111111111111111111111111111111111111111111111111111111111111",
	}
	if gotRepo := sandboxImageMirrorRepo(derivedCfg); gotRepo != "us-docker.pkg.dev/cluster-project/grain/guest" {
		t.Fatalf("sandboxImageMirrorRepo(derived) = %q, want us-docker.pkg.dev/cluster-project/grain/guest", gotRepo)
	}
	if got := EffectiveK8sGranuleImage(derivedCfg); got != wantMirrored {
		t.Fatalf("EffectiveK8sGranuleImage(derived) = %q, want %q", got, wantMirrored)
	}
}

// fakeMirrorDocker installs a fake docker for the mirror path whose
// `image inspect` of any ref answers repoDigests and whose push exits
// pushExit, and returns the file it logs its argv to.
func fakeMirrorDocker(t *testing.T, repoDigests string, pushExit int) string {
	t.Helper()
	ResetMirroredSandboxImageCacheForTest()
	t.Cleanup(ResetMirroredSandboxImageCacheForTest)
	dir := t.TempDir()
	logFile := dir + "/docker.log"
	script := `#!/bin/sh
echo "$@" >> "` + logFile + `"
case "$1 $2" in
  "image inspect") echo '` + repoDigests + `'; exit 0 ;;
esac
if [ "$1" = push ]; then
  echo "The push refers to repository [$2]"
  echo "denied: Permission \"artifactregistry.repositories.uploadArtifacts\" denied" >&2
  exit ` + strconv.Itoa(pushExit) + `
fi
exit 0
`
	if err := os.WriteFile(dir+"/docker", []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	savedDocker, savedToken, savedRemote := sandboxMirrorDockerCmd, sandboxMirrorTokenSource, sandboxMirrorRegistryDigest
	sandboxMirrorDockerCmd = []string{dir + "/docker"}
	sandboxMirrorTokenSource = nil
	sandboxMirrorRegistryDigest = func(context.Context, string) (string, error) {
		return "", errors.New("HTTP 404 (fake registry)")
	}
	t.Cleanup(func() {
		sandboxMirrorDockerCmd, sandboxMirrorTokenSource, sandboxMirrorRegistryDigest = savedDocker, savedToken, savedRemote
	})
	return logFile
}

// The -k8s-granule-image flag defaults to the stamp, so a daemon started
// with only -sandbox-image-mirror has the stamp in K8sGranuleImage on its
// first start. That must still be mirrored rather than read as a pin:
// the walkthrough in docs/test-runs/2026-10-29-sandbox-image-mirror-registry-auth.md
// found the daemon creating pods from the source image instead.
func TestEffectiveK8sGranuleImageMirrorsWhenTheFlagDefaultIsTheStamp(t *testing.T) {
	stamped := "127.0.0.1:5000/src/guest:sha-aaaa111"
	savedStamp := DefaultSandboxImage
	DefaultSandboxImage = stamped
	t.Cleanup(func() { DefaultSandboxImage = savedStamp })
	digest := "sha256:f97baa533a26513c453a362be331a43eb60214302f449c16571b23cea141dce4"
	logFile := fakeMirrorDocker(t, `["127.0.0.1:5001/mirror/guest@`+digest+`"]`, 0)

	cfg := Config{
		K8sGranuleSandboxes: true,
		K8sGranuleImage:     stamped,
		SandboxImageMirror:  "127.0.0.1:5001/mirror/guest",
	}
	want := "127.0.0.1:5001/mirror/guest:sha-aaaa111@" + digest
	if got := EffectiveK8sGranuleImage(cfg); got != want {
		t.Fatalf("EffectiveK8sGranuleImage = %q, want %q", got, want)
	}
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("the mirror never asked docker anything: %v", err)
	}

	// A pin of the operator's own, from elsewhere, still wins.
	cfg.K8sGranuleImage = "registry.example/own/guest:v1"
	if got := EffectiveK8sGranuleImage(cfg); got != cfg.K8sGranuleImage {
		t.Fatalf("EffectiveK8sGranuleImage(own pin) = %q, want %q", got, cfg.K8sGranuleImage)
	}
}

// A `docker tag` carries the source repository's RepoDigest over to the
// mirror tag before anything is pushed. After a push that failed, the
// next start must push again rather than pin to that digest in a mirror
// that never received it.
func TestMirrorSandboxImageDoesNotPinASourceDigestThatWasNeverPushed(t *testing.T) {
	source := "127.0.0.1:5000/src/guest:sha-aaaa111"
	target := "127.0.0.1:5001/mirror/guest:sha-aaaa111"
	logFile := fakeMirrorDocker(t, `["127.0.0.1:5000/src/guest@sha256:f97baa533a26513c453a362be331a43eb60214302f449c16571b23cea141dce4"]`, 1)

	pinned, err := defaultMirrorSandboxImage(t.Context(), source, "127.0.0.1:5001/mirror/guest", target)
	if err == nil {
		t.Fatalf("defaultMirrorSandboxImage = %q, nil; want the failed push reported", pinned)
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error %q does not carry docker's own reason", err)
	}
	logged, _ := os.ReadFile(logFile)
	if !strings.Contains(string(logged), "push "+target) {
		t.Errorf("no push was attempted:\n%s", logged)
	}
}

// A RepoDigest for the mirror is only docker's memory of an earlier
// push. When the mirror no longer answers it -- the repository was
// emptied or recreated -- the image is pushed again rather than pinned
// to a digest the cluster cannot pull; when it does, nothing is pushed.
func TestMirrorSandboxImageChecksTheRegistryBeforeTrustingALocalRepoDigest(t *testing.T) {
	source := "127.0.0.1:5000/src/guest:sha-aaaa111"
	target := "127.0.0.1:5001/mirror/guest:sha-aaaa111"
	digest := "sha256:f97baa533a26513c453a362be331a43eb60214302f449c16571b23cea141dce4"
	logFile := fakeMirrorDocker(t, `["127.0.0.1:5001/mirror/guest@`+digest+`"]`, 0)

	pinned, err := defaultMirrorSandboxImage(t.Context(), source, "127.0.0.1:5001/mirror/guest", target)
	if err != nil || pinned != target+"@"+digest {
		t.Fatalf("defaultMirrorSandboxImage = %q, %v; want %q", pinned, err, target+"@"+digest)
	}
	logged, _ := os.ReadFile(logFile)
	if !strings.Contains(string(logged), "push "+target) {
		t.Fatalf("a mirror answering 404 was not pushed to again:\n%s", logged)
	}

	if err := os.Remove(logFile); err != nil {
		t.Fatal(err)
	}
	sandboxMirrorRegistryDigest = func(_ context.Context, ref string) (string, error) {
		if ref != target {
			t.Errorf("registry asked for %q, want %q", ref, target)
		}
		return digest, nil
	}
	if pinned, err := defaultMirrorSandboxImage(t.Context(), source, "127.0.0.1:5001/mirror/guest", target); err != nil || pinned != target+"@"+digest {
		t.Fatalf("defaultMirrorSandboxImage(present) = %q, %v", pinned, err)
	}
	logged, _ = os.ReadFile(logFile)
	if strings.Contains(string(logged), "push") {
		t.Errorf("pushed although the mirror already has the digest:\n%s", logged)
	}
}

// A daemon started with -sandbox-image-mirror resolves its stock image to
// the mirrored one; clearing the mirror in Settings must hand the backend
// the unmirrored stamp again, not the mirrored image it started with.
// And the line logged for it must not claim a custom image was cleared
// when none was ever set.
func TestApplySandboxImageClearingTheMirrorReturnsToTheStamp(t *testing.T) {
	stamped := "127.0.0.1:5000/src/guest:sha-aaaa111"
	savedStamp := DefaultSandboxImage
	DefaultSandboxImage = stamped
	t.Cleanup(func() { DefaultSandboxImage = savedStamp })
	digest := "sha256:f97baa533a26513c453a362be331a43eb60214302f449c16571b23cea141dce4"
	fakeMirrorDocker(t, `["127.0.0.1:5001/mirror/guest@`+digest+`"]`, 0)

	cfg := Config{K8sGranuleSandboxes: true, SandboxImageMirror: "127.0.0.1:5001/mirror/guest"}
	mirrored := "127.0.0.1:5001/mirror/guest:sha-aaaa111@" + digest
	stock := stockSandboxImageFor(cfg)
	if stock != mirrored {
		t.Fatalf("stock image with a mirror = %q, want %q", stock, mirrored)
	}
	backend := &fakeImageBackend{image: stock}
	live := (&LiveConfig{sandboxes: backend, Applied: cfg}).WithSandboxImages(nil, stock, nil)

	cfg.SandboxImageMirror = ""
	logged := captureLog(t, func() { live.applySandboxImage(t.Context(), cfg) })
	if backend.image != stamped {
		t.Fatalf("after clearing the mirror new sandboxes come from %q, want the stamp %q", backend.image, stamped)
	}
	if strings.Contains(logged, "custom sandbox image cleared") {
		t.Errorf("logged a custom image cleared when none was set:\n%s", logged)
	}
	if !strings.Contains(logged, "new sandboxes are created from "+stamped) {
		t.Errorf("no line saying which image new sandboxes use:\n%s", logged)
	}
}

// The Settings pane's Granule VM image warning probes the image the daemon
// really creates kubernetes-granule pods from: this build's stamp,
// mirrored, and not the UI's version-derived default mirrored instead --
// a tag the mirror never received, which read as "did not resolve (HTTP
// 404)" beside a deployment running happily (task 1802).
func TestSettingsProbesTheDaemonsMirroredK8sGranuleImage(t *testing.T) {
	stamped := "127.0.0.1:5000/src/guest:sha-aaaa111"
	savedStamp := DefaultSandboxImage
	DefaultSandboxImage = stamped
	t.Cleanup(func() { DefaultSandboxImage = savedStamp })
	if versionDefault := ui.DefaultSandboxImageFor(installtrack.TrackOSS, version.Get()); versionDefault == stamped {
		t.Fatalf("the version default is the stamp %q; the test needs them to differ", stamped)
	}
	mirror := "127.0.0.1:5001/mirror/guest"
	digest := "sha256:f97baa533a26513c453a362be331a43eb60214302f449c16571b23cea141dce4"
	fakeMirrorDocker(t, `["`+mirror+`@`+digest+`"]`, 0)
	want := mirror + ":sha-aaaa111@" + digest

	running := Config{K8sGranuleSandboxes: true, K8sGranuleImage: stamped, SandboxImageMirror: mirror}
	// What startup does: mirror the stamp, and create pods from the result.
	if got := EffectiveK8sGranuleImage(running); got != want {
		t.Fatalf("EffectiveK8sGranuleImage = %q, want %q", got, want)
	}

	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	ctx := context.Background()
	if err := store.PutConfig(ctx, model.Config{SandboxImageMirror: mirror}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	var probed []string
	client := ui.NewClient(ui.Config{
		K8sGranuleImage: func(mc model.Config) string { return k8sGranuleImageForSettings(running, mc) },
		CheckImageManifest: func(_ context.Context, ref string) error {
			probed = append(probed, ref)
			if ref != want {
				return errors.New("HTTP 404")
			}
			return nil
		},
	}, store)
	settings, err := client.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !slices.Contains(probed, want) {
		t.Errorf("probed %q, want the daemon's mirrored stamp %q among them", probed, want)
	}
	if settings.K8sGranuleImageWarning != "" {
		t.Errorf("K8sGranuleImageWarning = %q, want none for the image the daemon runs", settings.K8sGranuleImageWarning)
	}
}
