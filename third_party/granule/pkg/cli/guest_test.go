package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/qcow2"
)

// fakeDocker puts a `docker` on PATH that records every argv it is
// called with, one call per line, and the Dockerfile it was handed on
// stdin, and returns a function reading both back. A command that never
// invoked docker reads as ("", "").
//
// Every call rather than the last, because a Dockerfile build is now two
// docker builds and a size query: the rootfs build, then the pack of it.
// A helper that kept only the last would say a build had happened when
// what it had seen was the packing.
func fakeDocker(t *testing.T) func() (argv string, stdin string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "call")
	script := "#!/bin/sh\n" +
		"[ -t 0 ] || cat >> \"$FAKE_DOCKER_OUT.stdin\"\n" +
		"printf '%s\\n' \"$*\" >> \"$FAKE_DOCKER_OUT\"\n" +
		// A size query docker cannot answer turns the pack's size report
		// off, which is noise here; answer it. The other inspect a pack
		// makes asks what the image declares with EXPOSE, and an image
		// that declares nothing answers with nothing.
		"case \"$1 $2\" in 'image inspect')\n" +
		"  case \"$*\" in *ExposedPorts*) : ;; *) echo 1000000;; esac\n" +
		";; esac\n" +
		// Reading a path out of an image, which a pack does to find out
		// whether the granule image brings a kernel: `docker create`
		// answers with a container id and starts nothing, and
		// `docker cp <id>:<path> -` answers with a tar whose first
		// header is a regular file. The tar is written by the Go below
		// rather than by a `tar` this script would have to find.
		"case \"$1\" in\n" +
		"  create) echo c-fake; exit 0;;\n" +
		"  cp) cat \"$FAKE_DOCKER_TAR\"; exit 0;;\n" +
		"  rm) exit 0;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DOCKER_TAR", writeOneFileTar(t, filepath.Join(dir, "file.tar")))
	t.Setenv("FAKE_DOCKER_OUT", out)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() (string, string) {
		argv, _ := os.ReadFile(out)
		stdin, _ := os.ReadFile(out + ".stdin")
		return string(argv), string(stdin)
	}
}

// writeOneFileTar writes the archive `docker cp <id>:<path> -` answers
// with for a path an image does carry: one regular-file header, which is
// all the reader looks at.
func writeOneFileTar(t *testing.T, path string) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("fake\n")
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "file", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// guestDir writes a Dockerfile into a new directory and returns both.
func guestDir(t *testing.T, contents string) (dir, file string) {
	t.Helper()
	dir = t.TempDir()
	file = filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

func runCLI(t *testing.T, args ...string) (code int, out string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code = Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

const guestfile = "RUN apt-get update && apt-get install -y nginx\nRUN systemctl enable nginx\n"

// A bare `granule guest build` is the Dockerfile route, reading
// ./Dockerfile -- the decision this command had to make, since the two
// routes now overlap for most of what people do with them. The booting
// one is asked for by name with -setup, because it is the one with the
// requirements (a KVM host, a boot per build).
func TestGuestBuild_BareCommandBuildsTheDockerfileInTheCurrentDirectory(t *testing.T) {
	read := fakeDocker(t)
	dir, _ := guestDir(t, guestfile)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	code, out := runCLI(t, "guest", "build", "-t", "my-guest:dev")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	argv, stdin := read()
	if !strings.Contains(argv, "build -f - -t my-guest:dev-rootfs .") {
		t.Errorf("docker argv = %q", argv)
	}
	if !strings.Contains(stdin, "FROM ghcr.io/gke-labs/granule/debian.rootfs:latest") || !strings.Contains(stdin, "systemctl enable nginx") {
		t.Errorf("the file docker was handed:\n%s", stdin)
	}
}

// -f names the file, and the context defaults to the directory holding
// it, so the short form in the documentation means what it looks like.
func TestGuestBuild_ContextDefaultsToTheDockerfilesDirectory(t *testing.T) {
	read := fakeDocker(t)
	dir, file := guestDir(t, guestfile)

	code, out := runCLI(t, "guest", "build", "-f", file, "-t", "my-guest:dev")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	argv, _ := read()
	if !strings.Contains(firstLine(argv), " "+dir) {
		t.Errorf("the rootfs build's argv = %q, want it to end with the context %s", firstLine(argv), dir)
	}
}

// What -t names is a bootable image on both routes, so the Dockerfile
// route is a rootfs build followed by a pack of it. Neither half is
// optional and neither is implicit in the other: the assertion is that
// both docker builds happen, in that order, joined by the rootfs tag.
func TestGuestBuild_PacksWhatTheDockerfileBuilt(t *testing.T) {
	read := fakeDocker(t)
	_, file := guestDir(t, guestfile)

	code, out := runCLI(t, "guest", "build", "-f", file, "-t", "my-guest:dev",
		"-kontur-image", "ghcr.io/example/granule:2026-09", "-variant", "alpine")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	calls := strings.Split(strings.TrimSpace(argvLog(read)), "\n")
	if len(calls) < 2 {
		t.Fatalf("docker was called %d time(s), want a build and a pack:\n%s", len(calls), argvLog(read))
	}
	if !strings.Contains(calls[0], "-t my-guest:dev-rootfs") {
		t.Errorf("the first call is not the rootfs build: %q", calls[0])
	}
	// The pack's own build, which is not necessarily the second call:
	// packing also asks the rootfs image what it declares with EXPOSE,
	// so it can carry the declaration across.
	var packed bool
	for _, call := range calls[1:] {
		packed = packed || strings.Contains(call, "--tag my-guest:dev")
	}
	if !packed {
		t.Errorf("nothing packed onto the output tag: %q", calls[1:])
	}
	// The packing's own knobs have to reach it, and the only place they
	// show up is the build it generates.
	if !strings.Contains(out, "packing my-guest:dev-rootfs onto ghcr.io/example/granule:2026-09") {
		t.Errorf("the pack did not take the rootfs and the named granule image:\n%s", out)
	}
}

// A Dockerfile whose only instruction is a FROM of a guest image is the
// complete form of a rebase, end to end: the image's own payload is
// rebuilt into a rootfs unchanged, and the packing re-lays granule's half
// over it out of the granule image named here. See docs/guest-images.md.
func TestGuestBuild_AFromOnlyFileIsARepack(t *testing.T) {
	read := fakeDocker(t)
	_, file := guestDir(t, "FROM ghcr.io/example/my-guest:v1\n")

	code, out := runCLI(t, "guest", "build", "-f", file, "-t", "my-guest:v2",
		"-kontur-image", "ghcr.io/example/granule:2026-09")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	_, stdin := read()
	if !strings.HasSuffix(stdin, "FROM ghcr.io/example/my-guest:v1\n") {
		t.Errorf("the rootfs build is not the file's own base and nothing else:\n%s", stdin)
	}
	if strings.Contains(stdin, "granule-guest-rootfs:latest") {
		t.Errorf("granule supplied a base over the file's own:\n%s", stdin)
	}
	for _, want := range []string{
		"repack of ghcr.io/example/my-guest:v1",
		"packing my-guest:v2-rootfs onto ghcr.io/example/granule:2026-09",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the build does not say %q:\n%s", want, out)
		}
	}
}

// Two bases is a build where one of them is not happening.
func TestGuestBuild_RefusesAFileWithItsOwnBaseUnderFrom(t *testing.T) {
	read := fakeDocker(t)
	_, file := guestDir(t, "FROM ghcr.io/example/my-guest:v1\n")

	code, out := runCLI(t, "guest", "build", "-f", file, "-t", "my-guest:v2", "-from", "granule-guest-rootfs:latest")
	if code == 0 {
		t.Fatalf("exit code = 0, want a refusal:\n%s", out)
	}
	if !strings.Contains(out, "names its own base") {
		t.Errorf("the refusal does not say what the conflict is:\n%s", out)
	}
	if argv, _ := read(); argv != "" {
		t.Errorf("docker was invoked anyway: %q", argv)
	}
}

// -rootfs-tag names the rootfs image, because that -- not the packed
// disk -- is what a consumer publishes for somebody else to pack.
func TestGuestBuild_RootfsTagNamesTheIntermediate(t *testing.T) {
	read := fakeDocker(t)
	_, file := guestDir(t, guestfile)

	code, out := runCLI(t, "guest", "build", "-f", file, "-t", "my-guest:dev", "-rootfs-tag", "ghcr.io/example/rootfs:v3")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	calls := strings.Split(strings.TrimSpace(argvLog(read)), "\n")
	if !strings.Contains(calls[0], "-t ghcr.io/example/rootfs:v3") {
		t.Errorf("the rootfs build did not use the name given: %q", calls[0])
	}
	if !strings.Contains(out, "packing ghcr.io/example/rootfs:v3 onto ") {
		t.Errorf("the pack did not take the named rootfs:\n%s", out)
	}
}

// -rootfs-only stops at the rootfs image, and there -t names that
// instead -- so nothing is packed and the output says what to do next.
func TestGuestBuild_RootfsOnlyPacksNothing(t *testing.T) {
	read := fakeDocker(t)
	_, file := guestDir(t, guestfile)

	code, out := runCLI(t, "guest", "build", "-f", file, "-t", "example/rootfs:v3", "-rootfs-only")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	calls := strings.Split(strings.TrimSpace(argvLog(read)), "\n")
	if len(calls) != 1 {
		t.Errorf("docker was called %d time(s), want only the rootfs build:\n%s", len(calls), argvLog(read))
	}
	if !strings.Contains(calls[0], "-t example/rootfs:v3") {
		t.Errorf("-t did not name the rootfs image: %q", calls[0])
	}
	if !strings.Contains(out, "granule guest pack -from example/rootfs:v3") {
		t.Errorf("the output does not say how to make it bootable:\n%s", out)
	}
}

// A flag that configures a packing this build will not do is an error
// rather than something quietly ignored -- the same rule the two routes
// already follow, one step further in.
func TestGuestBuild_PackFlagsWithRootfsOnly(t *testing.T) {
	read := fakeDocker(t)
	_, file := guestDir(t, guestfile)

	code, out := runCLI(t, "guest", "build", "-f", file, "-t", "example/rootfs:v3", "-rootfs-only", "-variant", "alpine")
	if code == 0 {
		t.Fatalf("exit code = 0, want a failure:\n%s", out)
	}
	for _, want := range []string{"-variant", "-rootfs-only"} {
		if !strings.Contains(out, want) {
			t.Errorf("message does not carry %q:\n%s", want, out)
		}
	}
	if argv, _ := read(); argv != "" {
		t.Errorf("docker was invoked anyway: %q", argv)
	}
}

func TestRootfsTagFor(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"my-guest:dev", "my-guest:dev-rootfs"},
		{"my-guest", "my-guest:latest-rootfs"},
		{"ghcr.io/example/guest:v1", "ghcr.io/example/guest:v1-rootfs"},
		{"registry.local:5000/example/guest", "registry.local:5000/example/guest:latest-rootfs"},
		{"registry.local:5000/example/guest:v1", "registry.local:5000/example/guest:v1-rootfs"},
	} {
		got, err := rootfsTagFor(tc.in)
		if err != nil {
			t.Errorf("rootfsTagFor(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("rootfsTagFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// A digest cannot be tagged, so there is no name to derive and the
	// message has to say which flag supplies one.
	if _, err := rootfsTagFor("example/guest@sha256:" + strings.Repeat("a", 64)); err == nil {
		t.Error("rootfsTagFor accepted a digest")
	} else if !strings.Contains(err.Error(), "-rootfs-tag") {
		t.Errorf("the message does not name the flag that fixes it: %v", err)
	}
}

// argvLog is every docker call the fake recorded, and firstLine is the
// first of them.
func argvLog(read func() (string, string)) string { argv, _ := read(); return argv }

func firstLine(s string) string { return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0] }

// Nothing to build, and the message says what both routes want, since a
// caller who typed a bare `guest build` in the wrong directory and one
// who meant `-setup` make the same mistake.
func TestGuestBuild_NoDockerfile(t *testing.T) {
	read := fakeDocker(t)
	dir := t.TempDir()

	code, out := runCLI(t, "guest", "build", "-t", "my-guest:dev", dir)
	if code == 0 {
		t.Fatalf("exit code = 0, want a failure:\n%s", out)
	}
	for _, want := range []string{filepath.Join(dir, "Dockerfile"), "-f", "-setup"} {
		if !strings.Contains(out, want) {
			t.Errorf("message does not carry %q:\n%s", want, out)
		}
	}
	if argv, _ := read(); argv != "" {
		t.Errorf("docker was invoked anyway: %q", argv)
	}
}

// A refused instruction fails the command before docker is invoked, and
// names the file, the line and the instruction.
func TestGuestBuild_RefusedInstruction(t *testing.T) {
	read := fakeDocker(t)
	_, file := guestDir(t, guestfile+"VOLUME /var/log/nginx\n")

	code, out := runCLI(t, "guest", "build", "-f", file, "-t", "my-guest:dev")
	if code == 0 {
		t.Fatalf("exit code = 0, want a failure:\n%s", out)
	}
	if !strings.Contains(out, file+":3: VOLUME") {
		t.Errorf("message does not name the file, line and instruction:\n%s", out)
	}
	if argv, _ := read(); argv != "" {
		t.Errorf("docker was invoked anyway: %q", argv)
	}
}

// -print-dockerfile shows exactly what docker would be given, and gives
// docker nothing.
func TestGuestBuild_PrintDockerfile(t *testing.T) {
	read := fakeDocker(t)
	_, file := guestDir(t, guestfile)

	code, out := runCLI(t, "guest", "build", "-f", file, "-t", "my-guest:dev", "-print-dockerfile")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "FROM ghcr.io/gke-labs/granule/debian.rootfs:latest") || !strings.Contains(out, guestfile) {
		t.Errorf("printed file:\n%s", out)
	}
	if argv, _ := read(); argv != "" {
		t.Errorf("docker was invoked: %q", argv)
	}
}

// A flag belonging to the other route is refused rather than quietly
// ignored: a build told -no-cache and then given -setup would boot a
// guest and honour none of it.
func TestGuestBuild_FlagsFromTheOtherRoute(t *testing.T) {
	_, file := guestDir(t, guestfile)
	setup := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(setup, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{{
		name: "a Dockerfile flag with -setup",
		args: []string{"guest", "build", "-setup", setup, "-from", "granule:base", "-t", "g:dev", "-f", file},
		want: []string{"-f", "-setup"},
	}, {
		name: "a booting flag without -setup",
		args: []string{"guest", "build", "-f", file, "-t", "g:dev", "-ready-timeout", "5m"},
		want: []string{"-ready-timeout", "-setup"},
	}, {
		name: "-snapshot without -setup",
		args: []string{"guest", "build", "-f", file, "-t", "g:dev", "-snapshot"},
		want: []string{"-snapshot", "-setup"},
	}, {
		// -dns names the resolver of a VM, and a Dockerfile build
		// boots none: what a docker build resolves through is the
		// daemon's business, and the guest image's own baseline is
		// the GUEST_DNS build arg.
		name: "-dns without -setup",
		args: []string{"guest", "build", "-f", file, "-t", "g:dev", "-dns", "10.0.0.53"},
		want: []string{"-dns", "-setup"},
	}, {
		name: "-setup without a base to boot",
		args: []string{"guest", "build", "-setup", setup, "-t", "g:dev"},
		want: []string{"-from"},
	}, {
		name: "no tag",
		args: []string{"guest", "build", "-f", file},
		want: []string{"-t is required"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			read := fakeDocker(t)
			code, out := runCLI(t, tc.args...)
			if code == 0 {
				t.Fatalf("exit code = 0, want a failure:\n%s", out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("message does not carry %q:\n%s", want, out)
				}
			}
			if argv, _ := read(); argv != "" {
				t.Errorf("docker was invoked anyway: %q", argv)
			}
		})
	}
}

// -print-dockerfile is the one way to see what a pack would do without
// a daemon, and it is also how this test checks the flags reach
// guestpack at all: every value below has to show up in the build.
func TestGuestPack_PrintDockerfile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"guest", "pack",
		"-from", "ghcr.io/example/guest-rootfs:v7",
		"-t", "example/guest:packed",
		"-kontur-image", "ghcr.io/example/kontur-runtime:2026-09",
		"-packer-image", "alpine:3.20",
		"-variant", "alpine",
		"-disk-extra-mb", "512",
		"-print-dockerfile",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"COPY --from=ghcr.io/example/guest-rootfs:v7 / /rootfs",
		"FROM alpine:3.20 AS pack",
		"FROM ghcr.io/example/kontur-runtime:2026-09",
		"GUEST_DISK_EXTRA_MB=512 GRANULE_GUEST_VARIANT=alpine",
		"/var/lib/granule/guest/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the printed build does not contain %q:\n%s", want, out)
		}
	}
	// Printing is not building: what comes back is the file, with none
	// of the progress a pack prints around it.
	if !strings.HasPrefix(out, "# Generated by") {
		t.Errorf("-print-dockerfile printed something other than the build:\n%s", out)
	}
	if strings.Contains(out, "onto ghcr.io/example/kontur-runtime:2026-09\n") {
		t.Errorf("-print-dockerfile ran a build:\n%s", out)
	}
}

func TestGuestPack_RequiresFromAndTag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"guest", "pack", "-from", "rootfs:v1"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "-from and -t are both required") {
		t.Errorf("stderr does not say what is missing:\n%s", stderr.String())
	}
}

func TestGuest_UnknownSubcommandNamesTheOnesThereAre(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"guest"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "(build, pack, rebase, verify, flatten)") {
		t.Errorf("stderr does not list the subcommands:\n%s", stderr.String())
	}
}

func TestGuestFlatten_CLI(t *testing.T) {
	dir := t.TempDir()
	baseRaw := filepath.Join(dir, "base.raw")
	if err := os.WriteFile(baseRaw, []byte("BASE ROOTFS DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(baseRaw, 10*1024*1024); err != nil {
		t.Fatal(err)
	}

	layer1 := filepath.Join(dir, "layer1.qcow2")
	if err := qcow2.WriteOverlay(layer1, baseRaw, 10*1024*1024); err != nil {
		t.Fatal(err)
	}

	dstRaw := filepath.Join(dir, "flattened.raw")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"guest", "flatten", "-base", baseRaw, layer1, dstRaw}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guest flatten failed with exit code %d, stderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(dstRaw); err != nil {
		t.Fatalf("flattened output raw missing: %v", err)
	}
}

func TestGuestRebase_RequiresFromAndTag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"guest", "rebase", "-from", "example/guest:v1"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "-from and -t are both required") {
		t.Errorf("stderr does not say what is missing:\n%s", stderr.String())
	}
}

// The two complete forms are alternatives rather than steps, and asking
// for both is a mistake worth catching before anything is built --
// especially since one of them boots the guest and the other explicitly
// does not.
func TestGuestRebase_RootfsAndBootAreExclusive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"guest", "rebase",
		"-from", "example/guest:v1",
		"-t", "example/guest:rebased",
		"-rootfs", "example/guest-rootfs:v1",
		"-boot",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "-rootfs and -boot") {
		t.Errorf("stderr does not say the two forms are exclusive:\n%s", stderr.String())
	}
}
