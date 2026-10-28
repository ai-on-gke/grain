package daemon

import (
	"log"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// captureLog collects what a call logs, so a test can assert on the line
// an operator actually sees at startup.
func captureLog(t *testing.T, f func()) string {
	t.Helper()
	var logs strings.Builder
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
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
