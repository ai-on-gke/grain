package guestdockerfile

// Tests for deploy/guest-rootfs-key.sh and the rootfs tags
// deploy/publish-images.sh builds out of it -- the naming of the images
// DefaultBase points at, which is why they live in this package rather
// than beside a shell script no Go code calls.
//
// What they are for: the key decides when a published rootfs is
// republished and when a pinned tag keeps meaning what it meant. Both
// halves of that are silent when they break. A key that stopped moving
// with the recipe would leave a stale rootfs under a tag that claims to
// be current; a key that moved with anything else -- a reworded comment,
// a Go file, granule's own guest-side half -- would mint a new
// "immutable" rootfs per commit, which is the thing this replaced.
// Neither shows up in a build.
//
// Nothing here needs docker: every one of these runs is a hash of a text
// file.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	keyScript      = "../../deploy/guest-rootfs-key.sh"
	publishScript  = "../../deploy/publish-images.sh"
	dockerfilePath = "../../Dockerfile"
)

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed, so there is nothing here to run the publish scripts with")
	}
}

// runScript runs one of the deploy scripts and returns its stdout, with
// the Dockerfile it reads and any extra environment named by the caller.
func runScript(t *testing.T, script, dockerfile string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(script, args...)
	cmd.Env = os.Environ()
	// publish-images.sh builds this repository's own Dockerfile and says
	// so to the key script itself, so only the key script is asked to
	// read a copy.
	if dockerfile != "" {
		cmd.Env = append(cmd.Env, "DOCKERFILE="+dockerfile)
	}
	cmd.Env = append(cmd.Env, env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		t.Logf("%s said: %s", filepath.Base(script), strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), err
}

func rootfsKey(t *testing.T, dockerfile string, args ...string) string {
	t.Helper()
	out, err := runScript(t, keyScript, dockerfile, nil, args...)
	if err != nil {
		t.Fatalf("guest-rootfs-key.sh %v: %v", args, err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(out) {
		t.Fatalf("guest-rootfs-key.sh %v printed %q, want twelve hex characters", args, out)
	}
	return out
}

// editedDockerfile writes a copy of the real Dockerfile with one
// substitution made, so that "does this move the key?" is asked about
// the file the publish actually builds rather than about a fixture that
// could drift away from it.
func editedDockerfile(t *testing.T, old, new string) string {
	t.Helper()
	src, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("reading the Dockerfile: %v", err)
	}
	if !strings.Contains(string(src), old) {
		t.Fatalf("the Dockerfile no longer contains %q, so this test is editing nothing -- update it to a line that is still there", old)
	}
	path := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(path, []byte(strings.Replace(string(src), old, new, 1)), 0o644); err != nil {
		t.Fatalf("writing the edited Dockerfile: %v", err)
	}
	return path
}

// The tags the publish script would push, which is the only form of the
// key anybody outside this repository ever sees.
func TestPublishedRootfsTagsAreContentKeys(t *testing.T) {
	requireBash(t)
	out, err := runScript(t, publishScript, "", nil, "--print-rootfs-tags", "guests")
	if err != nil {
		t.Fatalf("publish-images.sh --print-rootfs-tags: %v", err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("want a tag for each of the three guest variants, got:\n%s", out)
	}
	keys := map[string]string{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("want %q to read \"<variant> <moving image> <keyed image>\"", line)
		}
		variant, moving, keyed := fields[0], fields[1], fields[2]
		// The moving tag is the consumer-facing name and does not
		// change: :latest is what each variant's package resolves.
		wantMoving := "ghcr.io/gke-labs/granule/" + variant + ".rootfs:latest"
		if moving != wantMoving {
			t.Fatalf("the %s rootfs moves at %q, want %q", variant, moving, wantMoving)
		}
		want := regexp.MustCompile(`^ghcr\.io/gke-labs/granule/` + regexp.QuoteMeta(variant) + `\.rootfs:os-([0-9a-f]{12})$`)
		m := want.FindStringSubmatch(keyed)
		if m == nil {
			t.Fatalf("the %s rootfs is pinned as %q, want the .rootfs package under os-<key>", variant, keyed)
		}
		keys[variant] = m[1]
	}
	for _, variant := range []string{"debian", "alpine", "debian12"} {
		if keys[variant] == "" {
			t.Fatalf("no tag printed for the %s rootfs; got:\n%s", variant, out)
		}
	}
	// Three variants, three keys: debian12 is the debian rootfs with a
	// kernel package installed into it, so a key that ignored the build
	// args would publish it over the plain debian one.
	if keys["debian"] == keys["debian12"] || keys["debian"] == keys["alpine"] || keys["alpine"] == keys["debian12"] {
		t.Fatalf("two variants share a rootfs key, so one would be published over the other: %v", keys)
	}

	// The same tree, asked twice, has to answer the same thing: a key
	// that drifted (a timestamp, the order of a map) would republish the
	// rootfs on every run and pin nothing.
	again, err := runScript(t, publishScript, "", nil, "--print-rootfs-tags", "guests")
	if err != nil {
		t.Fatalf("publish-images.sh --print-rootfs-tags, second run: %v", err)
	}
	if again != out {
		t.Fatalf("two runs over one tree printed different tags:\n%s\n---\n%s", out, again)
	}
}

// The property the whole change rests on: granule moving does not
// republish an operating system.
func TestRootfsKeyIgnoresWhatDoesNotReachTheRootfs(t *testing.T) {
	requireBash(t)
	base := rootfsKey(t, dockerfilePath)

	for _, tc := range []struct {
		name     string
		old, new string
	}{
		{
			// The stages after the recipe's end marker are granule's own
			// guest-side half, laid over a rootfs at pack time.
			name: "granule's guest-side parts",
			old:  "COPY deploy/guest-image/pack-guest /pack-guest",
			new:  "COPY deploy/guest-image/pack-guest /pack-guest\nCOPY deploy/guest-image/pack-guest /pack-guest-again",
		},
		{
			name: "the packing stage's own arguments",
			old:  "ARG GUEST_DNS=8.8.8.8",
			new:  "ARG GUEST_DNS=9.9.9.9",
		},
		{
			// This repository explains itself at length inside those
			// stages, and a reworded paragraph is not a new OS.
			name: "a comment inside the recipe",
			old:  "# Builds the Debian variant of the guest rootfs baked into the final",
			new:  "# Builds the Debian guest rootfs -- reworded, and nothing else.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rootfsKey(t, editedDockerfile(t, tc.old, tc.new)); got != base {
				t.Fatalf("editing %s moved the rootfs key (%s -> %s), so a commit that changes nothing about the OS republishes it", tc.name, base, got)
			}
		})
	}

	// A build arg that configures the disk the rootfs is packed into,
	// passed the way publish-images.sh passes it: accepted, and not part
	// of the key.
	if got := rootfsKey(t, dockerfilePath, "GUEST_DISK_EXTRA_MB=2048"); got != base {
		t.Fatalf("GUEST_DISK_EXTRA_MB moved the rootfs key (%s -> %s), but it configures the packed disk and not the rootfs", base, got)
	}
}

// And the other half of it: a change that does reach the rootfs has to
// move the key, or a pinned tag silently stops meaning what it said.
func TestRootfsKeyFollowsTheRecipe(t *testing.T) {
	requireBash(t)
	base := rootfsKey(t, dockerfilePath)

	for _, tc := range []struct {
		name     string
		old, new string
	}{
		{
			name: "what debootstrap installs",
			old:  "--include=systemd-sysv,iproute2,acpid,udev,kmod",
			new:  "--include=systemd-sysv,iproute2,acpid,udev,kmod,e2fsprogs",
		},
		{
			name: "the base image a rootfs stage builds on",
			old:  "FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587 AS guest-rootfs-debian",
			new:  "FROM debian:trixie-slim AS guest-rootfs-debian",
		},
		{
			// An ARG's default is part of the recipe even though it is
			// declared above the region: changing it changes what every
			// caller who does not override it gets.
			name: "a build arg's default",
			old:  "ARG GUEST_SUITE=bookworm",
			new:  "ARG GUEST_SUITE=trixie",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rootfsKey(t, editedDockerfile(t, tc.old, tc.new)); got == base {
				t.Fatalf("editing %s left the rootfs key at %s, so the published rootfs would not be rebuilt", tc.name, got)
			}
		})
	}

	for _, arg := range []string{
		"GUEST_SUITE=trixie",
		"GUEST_DISTRO=alpine",
		"GUEST_KERNEL_PACKAGE=linux-image-amd64",
		"GUEST_SETUP_SCRIPT=apt-get install -y nginx",
		"GUEST_READY_FILE=/run/granule/ready",
	} {
		if got := rootfsKey(t, dockerfilePath, arg); got == base {
			t.Fatalf("building with %s left the rootfs key at %s, so two different rootfs images would share a tag", arg, got)
		}
	}

	// The escape hatch for the case the recipe cannot see: the same
	// instructions, run when upstream has moved.
	out, err := runScript(t, keyScript, dockerfilePath, []string{"ROOTFS_REVISION=2026-09-21"})
	if err != nil {
		t.Fatalf("guest-rootfs-key.sh with ROOTFS_REVISION: %v", err)
	}
	if out == base {
		t.Fatalf("ROOTFS_REVISION did not move the key, so there is no way to republish a rootfs whose recipe has not changed")
	}
}

// Two ways the key could come to cover less than it claims, both of them
// refused rather than absorbed.
func TestRootfsKeyRefusesWhatItCannotAccountFor(t *testing.T) {
	requireBash(t)

	// A build arg nobody has classified: it might be a rootfs input, and
	// guessing that it is not is how an input falls out of the key.
	if _, err := runScript(t, keyScript, dockerfilePath, nil, "GUEST_SOMETHING_NEW=1"); err == nil {
		t.Fatal("an unclassified build arg was accepted; a new input has to be declared as one, or it is silently left out of the key")
	}

	// The markers that say which part of the Dockerfile the key covers.
	src, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("reading the Dockerfile: %v", err)
	}
	unmarked := filepath.Join(t.TempDir(), "Dockerfile")
	var kept []string
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(line, "# >>> guest rootfs recipe") {
			continue
		}
		kept = append(kept, line)
	}
	if err := os.WriteFile(unmarked, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("writing the unmarked Dockerfile: %v", err)
	}
	if _, err := runScript(t, keyScript, unmarked, nil); err == nil {
		t.Fatal("a Dockerfile with no recipe marker produced a key anyway, which would be a hash of whatever happened to be found")
	}
}
