package guestverify

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestimage"
)

var fakedockerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakedocker-verify")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	fakedockerPath = filepath.Join(dir, "fakedocker")
	cmd := exec.Command("go", "build", "-o", fakedockerPath, "../guestboot/testdata/fakedocker")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("building fakedocker test helper: " + err.Error())
	}

	os.Exit(m.Run())
}

// testOptions returns Options pointed at fakedocker, plus a function
// reading back the calls it recorded as one []string per invocation.
func testOptions(t *testing.T) (Options, func() [][]string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKEDOCKER_LOG", logPath)
	t.Setenv(dockercli.EnvBinary, fakedockerPath)

	opts := Options{
		Image:    "ghcr.io/bwsalmon/granule:debian12-customized",
		Checks:   []Check{{Name: "docker starts and answers", Script: "systemctl start docker\ndocker info >/dev/null\n"}},
		CacheDir: t.TempDir(),
		Stdout:   io.Discard,
		Stderr:   io.Discard,
	}
	return opts, func() [][]string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatalf("reading fakedocker log: %v", err)
		}
		var calls [][]string
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if line != "" {
				call := strings.Split(line, "\x1f")
				for i := range call {
					call[i] = strings.ReplaceAll(call[i], "\x1e", "\n")
				}
				calls = append(calls, call)
			}
		}
		return calls
	}
}

// find returns the first recorded call whose args contain every one of
// want, in order but not necessarily adjacent.
func find(calls [][]string, want ...string) []string {
	for _, call := range calls {
		rest := strings.Join(call, " ")
		ok := true
		for _, w := range want {
			i := strings.Index(rest, w)
			if i < 0 {
				ok = false
				break
			}
			rest = rest[i+len(w):]
		}
		if ok {
			return call
		}
	}
	return nil
}

// count returns how many recorded calls begin with the given args.
func count(calls [][]string, prefix ...string) int {
	n := 0
	for _, call := range calls {
		if len(call) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if call[i] != p {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

// failing is a check the fake guest runs and fails, saying what it said
// on the way out.
func failing(name, output string) Check {
	return Check{Name: name, Script: "#fake-out: " + output + "\n#fake-exit: 1\n"}
}

func TestVerifyBootsRunsTheChecksAndProducesNoArtifact(t *testing.T) {
	opts, calls := testOptions(t)
	var out strings.Builder
	opts.Stdout = &out

	res, err := Verify(context.Background(), opts)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	got := calls()

	if find(got, "run", "-d", "--device", "/dev/kvm", "CHV_NET_MODE=inline", opts.Image, "run") == nil {
		t.Errorf("no VM container booting the image under test; calls were %v", got)
	}
	// The check reaches the guest as a staged script and is run by path,
	// the same way a setup script is.
	if find(got, "exec", "granule", "exec", "--", checkPath) == nil {
		t.Errorf("the check was never run in the guest; calls were %v", got)
	}
	// The whole premise: nothing is committed, tagged or pushed. A verb
	// that produced an image would be judging something other than what
	// it emitted.
	for _, forbidden := range []string{"commit", "tag", "push"} {
		if n := count(got, forbidden); n != 0 {
			t.Errorf("verify ran `docker %s` %d times; it must produce no artifact: %v", forbidden, n, got)
		}
	}
	if res.Failed() {
		t.Errorf("Failed() = true for a passing check set: %+v", res.Checks)
	}
	if !strings.Contains(out.String(), "1 of 1 checks passed") {
		t.Errorf("output = %q, want a verdict in it", out.String())
	}
}

// A check writes into a throwaway overlay, never into the image it is
// judging: persistent mode is what `guest build` needs and is exactly
// what would let a check change its own subject.
func TestVerifyBootsIntoAThrowawayOverlay(t *testing.T) {
	opts, calls := testOptions(t)
	if _, err := Verify(context.Background(), opts); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, call := range calls() {
		for _, a := range call {
			if a == "CHV_DISK_MODE=persistent" {
				t.Fatalf("the guest was booted writing through to the image's own disk: %v", call)
			}
		}
	}
	if find(calls(), "CHV_DISK_MODE=overlay") == nil {
		t.Errorf("the guest was not booted in overlay mode; calls were %v", calls())
	}
}

func TestVerifyFailsTheImageAndSaysWhichCheckAndWhatItPrinted(t *testing.T) {
	opts, _ := testOptions(t)
	var out strings.Builder
	opts.Stdout = &out
	opts.Checks = []Check{
		{Name: "the guest boots", Script: "true\n"},
		failing("docker starts and answers", "Cannot connect to the Docker daemon"),
	}

	res, err := Verify(context.Background(), opts)
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("Verify err = %v, want it to wrap ErrFailed", err)
	}
	if !res.Failed() {
		t.Error("Failed() = false for a failing check set")
	}
	if !strings.Contains(err.Error(), "docker starts and answers") {
		t.Errorf("error = %v, want it to name the check that failed", err)
	}
	if strings.Contains(err.Error(), "the guest boots") {
		t.Errorf("error = %v, want it to name only the check that failed", err)
	}
	// "failed" with no detail is barely better than no verdict: the
	// report has to carry what the check printed.
	if !strings.Contains(out.String(), "Cannot connect to the Docker daemon") {
		t.Errorf("output = %q, want the failing check's own output in it", out.String())
	}
	if !strings.Contains(out.String(), "exit status 1") {
		t.Errorf("output = %q, want the failing check's exit status in it", out.String())
	}
}

// Every check runs, not just the ones up to the first failure: a report
// that names one broken thing when three are broken sends its reader
// round the loop three times.
func TestVerifyRunsEveryCheckAndReportsAllTheFailures(t *testing.T) {
	opts, _ := testOptions(t)
	opts.Checks = []Check{
		failing("docker starts and answers", "no docker"),
		{Name: "the guest has a default route", Script: "true\n"},
		failing("the clock is set", "1970"),
	}

	res, err := Verify(context.Background(), opts)
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("Verify err = %v, want ErrFailed", err)
	}
	if len(res.Checks) != 3 {
		t.Fatalf("ran %d checks, want all 3: %+v", len(res.Checks), res.Checks)
	}
	if !strings.Contains(err.Error(), "2 of 3 checks failed") {
		t.Errorf("error = %v, want it to count the failures", err)
	}
	for _, name := range []string{"docker starts and answers", "the clock is set"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error = %v, want it to name %q", err, name)
		}
	}
}

// A failing verdict is never cached: it must not be able to pin an image
// as broken, and the image is not in service anyway.
func TestVerifyCachesAPassAndNotAFailure(t *testing.T) {
	opts, calls := testOptions(t)
	opts.Checks = []Check{failing("docker starts and answers", "no docker")}
	if _, err := Verify(context.Background(), opts); !errors.Is(err, ErrFailed) {
		t.Fatalf("Verify err = %v, want ErrFailed", err)
	}
	entries, err := os.ReadDir(opts.CacheDir)
	if err != nil {
		t.Fatalf("reading the cache dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failed verdict was cached: %v", entries)
	}

	opts.Checks = []Check{{Name: "the guest boots", Script: "true\n"}}
	if _, err := Verify(context.Background(), opts); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if entries, err := os.ReadDir(opts.CacheDir); err != nil || len(entries) != 1 {
		t.Fatalf("a passing verdict was not cached: %v %v", entries, err)
	}

	// And the second run of the same checks against the same image boots
	// nothing at all -- which is the point: a consumer that verifies
	// before use would otherwise boot a VM on every sandbox start.
	before := len(calls())
	var out strings.Builder
	opts.Stdout = &out
	res, err := Verify(context.Background(), opts)
	if err != nil {
		t.Fatalf("Verify (cached): %v", err)
	}
	if !res.Cached {
		t.Error("the second verify was not answered from the cache")
	}
	for _, call := range calls()[before:] {
		if call[0] == "run" {
			t.Errorf("a cached verdict booted a VM anyway: %v", call)
		}
	}
	if !strings.Contains(out.String(), "nothing booted") {
		t.Errorf("output = %q, want it to say nothing was booted", out.String())
	}
}

// The check set is part of the key, so adding a check cannot be answered
// by yesterday's pass -- otherwise the first thing a consumer does after
// tightening its checks is skip them.
func TestVerifyDoesNotReuseAVerdictForADifferentCheckSet(t *testing.T) {
	opts, calls := testOptions(t)
	if _, err := Verify(context.Background(), opts); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	before := len(calls())

	opts.Checks = append(opts.Checks, Check{Name: "the clock is set", Script: "true\n"})
	res, err := Verify(context.Background(), opts)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Cached {
		t.Fatal("a verdict for a different check set was reused")
	}
	if count(calls()[before:], "run") == 0 {
		t.Error("the second verify booted nothing despite a new check")
	}
}

// Nor can a tag that has moved be answered by the verdict on what it used
// to point at: the key is the digest, not the name.
func TestVerifyDoesNotReuseAVerdictForMovedBytes(t *testing.T) {
	opts, _ := testOptions(t)
	first, err := Verify(context.Background(), opts)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	t.Setenv("FAKEDOCKER_IMAGE_SALT", "moved")
	second, err := Verify(context.Background(), opts)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if second.Cached {
		t.Fatal("the verdict on the bytes a tag used to name was reused after it moved")
	}
	if first.ImageID == second.ImageID {
		t.Fatal("the test did not actually move the tag")
	}
}

// -no-cache both skips the lookup and records nothing, so that a caller
// checking a suspect image cannot leave a verdict behind for the next
// one to trust.
func TestVerifyNoCacheNeitherReadsNorWrites(t *testing.T) {
	opts, _ := testOptions(t)
	if _, err := Verify(context.Background(), opts); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	opts.NoCache = true
	res, err := Verify(context.Background(), opts)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Cached {
		t.Error("-no-cache read the cache anyway")
	}

	opts.CacheDir = t.TempDir()
	if _, err := Verify(context.Background(), opts); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if entries, _ := os.ReadDir(opts.CacheDir); len(entries) != 0 {
		t.Errorf("-no-cache wrote a verdict: %v", entries)
	}
}

// The image is booted under the VMM half that will actually run it: a
// verdict about the published artifact is a verdict about a machine
// nobody boots.
func TestVerifyRebasesOntoTheVMMHalfBeforeBooting(t *testing.T) {
	opts, calls := testOptions(t)
	opts.GranuleImage = "ghcr.io/bwsalmon/granule:v9"

	if _, err := Verify(context.Background(), opts); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	got := calls()
	build := find(got, "build", "--tag", "granule-verify-rebase:")
	if build == nil {
		t.Fatalf("no rebase build; calls were %v", got)
	}
	dockerfile := build[len(build)-1]
	if !strings.Contains(dockerfile, "FROM "+opts.GranuleImage) {
		t.Errorf("the rebase is not based on the VMM half: %q", dockerfile)
	}
	if !strings.Contains(dockerfile, "COPY --from="+opts.Image+" "+guestimage.DiskImage) {
		t.Errorf("the rebase does not take the guest disk from the image under test: %q", dockerfile)
	}
	// vmlinux belongs to the VMM half. A rebase that copied it would pin
	// the kernel the deployment has just replaced, which is the whole
	// thing a rebase is for. The generated build explains that in a
	// comment, so what is asserted is the absence of the COPY rather than
	// the absence of the word.
	if strings.Contains(dockerfile, "COPY --from="+opts.Image+" "+guestimage.RuntimeKernel) {
		t.Errorf("the rebase copied the VMM half's own kernel out of the guest image: %q", dockerfile)
	}
	// And what boots is the rebase, not the image as published.
	booted := find(got, "run", "-d", "granule-verify-rebase:")
	if booted == nil {
		t.Fatalf("the rebased image was built and not booted; calls were %v", got)
	}
	// It is not an artifact either: the rebase is removed whatever the
	// verdict, so nothing that looks like a granule image is left behind.
	if find(got, "image", "rm", "-f", "granule-verify-rebase:") == nil {
		t.Errorf("the throwaway rebase was left in the daemon; calls were %v", got)
	}
}

// A guest that brings its own kernel brings its initramfs with it, and a
// guest that brings neither is the ordinary case -- so the rebase has to
// find out which rather than assume either.
func TestVerifyRebaseCarriesTheKernelPairOnlyWhenItIsThere(t *testing.T) {
	opts, calls := testOptions(t)
	opts.GranuleImage = "ghcr.io/bwsalmon/granule:v9"
	// What the image carries is not something the digest changes under
	// this fake, so the second verify below would otherwise be answered
	// by the first one's cached pass.
	opts.NoCache = true
	t.Setenv("FAKEDOCKER_MISSING_GUEST_FILES", guestimage.GuestKernel+","+guestimage.GuestInitramfs)

	if _, err := Verify(context.Background(), opts); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	build := find(calls(), "build", "--tag", "granule-verify-rebase:")
	if build == nil {
		t.Fatalf("no rebase build; calls were %v", calls())
	}
	if dockerfile := build[len(build)-1]; strings.Contains(dockerfile, "COPY --from="+opts.Image+" "+guestimage.GuestKernel) {
		t.Errorf("the rebase copies a kernel the image does not carry: %q", dockerfile)
	}

	// The other half of the same rule: half a kernel pair is refused
	// rather than assembled into a guest that boots without the modules
	// it needs and never answers.
	t.Setenv("FAKEDOCKER_MISSING_GUEST_FILES", guestimage.GuestInitramfs)
	_, err := Verify(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), guestimage.GuestInitramfs) {
		t.Errorf("err = %v, want it to name the missing half of the kernel pair", err)
	}
}

// A guest half with no disk in it is reported as what it is, before
// anything boots -- not as a VM that comes up to nothing.
func TestVerifyRefusesAnImageWithNoGuestInIt(t *testing.T) {
	opts, calls := testOptions(t)
	opts.GranuleImage = "ghcr.io/bwsalmon/granule:v9"
	t.Setenv("FAKEDOCKER_MISSING_GUEST_FILES", guestimage.DiskImage)

	_, err := Verify(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), guestimage.DiskImage) {
		t.Fatalf("err = %v, want it to name the disk the image has not got", err)
	}
	if count(calls(), "run") != 0 {
		t.Errorf("something was booted anyway; calls were %v", calls())
	}
}

// A granule image with a bundled guest kernel of its own, under a guest
// that brings none, is the one pair the rebase cannot express its way out
// of: the base's vmlinuz and initrd.img would stay beside somebody else's
// disk and the guest would boot on a kernel built for a different rootfs
// -- which is a VM that never answers rather than an error. Verify
// refuses it before anything boots, because the rebase it delegates to
// does.
func TestVerifyRefusesAGranuleImageWithABundledGuestKernel(t *testing.T) {
	opts, calls := testOptions(t)
	opts.GranuleImage = "ghcr.io/bwsalmon/granule:v9"
	// Only the image under test is without the kernel pair; the granule
	// image is a full one and carries a guest of its own.
	t.Setenv("FAKEDOCKER_MISSING_GUEST_FILES",
		opts.Image+"="+guestimage.GuestKernel+","+opts.Image+"="+guestimage.GuestInitramfs)

	_, err := Verify(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "--target runtime") {
		t.Fatalf("err = %v, want it to refuse the bundled guest kernel and name the image to rebase onto instead", err)
	}
	if count(calls(), "run") != 0 {
		t.Errorf("something was booted anyway; calls were %v", calls())
	}
}

// The rebase is tagged by the build and only then checked for being
// bootable, so a failure of that check comes back with the throwaway
// image already in the daemon. It has to go even then -- an image that
// nothing can boot, under a granule-ish name, is the worst of the
// artifacts this verb is not supposed to leave behind, and it is the one
// a later run would find already there.
func TestVerifyRemovesTheRebaseWhenItsOwnCheckFails(t *testing.T) {
	opts, calls := testOptions(t)
	opts.GranuleImage = "ghcr.io/bwsalmon/granule:v9"
	tag := rebaseTag(opts.Image, opts.GranuleImage)
	// The disk is missing from the built image alone: the image under
	// test still has one, so the rebase is generated and built, and what
	// fails is the check on what came out rather than anything before
	// it.
	t.Setenv("FAKEDOCKER_MISSING_GUEST_FILES", tag+"="+guestimage.DiskImage)

	_, err := Verify(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), guestimage.DiskImage) {
		t.Fatalf("err = %v, want the rebase's post-build check to fail naming the disk that is missing", err)
	}
	got := calls()
	if find(got, "build", "--tag", tag) == nil {
		t.Fatalf("the rebase was never built, so this is not the failure under test; calls were %v", got)
	}
	if find(got, "image", "rm", "-f", tag) == nil {
		t.Errorf("the rebase was left in the daemon after its own check failed; calls were %v", got)
	}
}

// A guest that never becomes reachable is the characteristic symptom of
// a guest-contract mismatch between the two halves, and verify is the
// first place that should show up as a sentence: the console log is
// where the reason is written down, so it has to reach the caller.
func TestVerifyReportsAGuestThatNeverAnswers(t *testing.T) {
	opts, _ := testOptions(t)
	opts.ReadyTimeout = 2 * time.Second
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "granule exec")

	_, err := Verify(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error for a guest that never answered, got nil")
	}
	if errors.Is(err, ErrFailed) {
		t.Errorf("err = %v, want a boot failure rather than a verdict: no check ran", err)
	}
	if !strings.Contains(err.Error(), "guest console") {
		t.Errorf("err = %v, want the guest's console log with it", err)
	}
}

// A VM that dies mid-run has failed no check: reporting that as "the
// image failed verification" would be a false verdict on an image that
// may be perfectly good.
func TestVerifyDoesNotBlameTheImageForAVMThatDied(t *testing.T) {
	opts, _ := testOptions(t)
	opts.Checks = []Check{failing("docker starts and answers", "")}
	t.Setenv("FAKEDOCKER_RUNNING", "false")

	_, err := Verify(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if errors.Is(err, ErrFailed) {
		t.Errorf("err = %v, want it not to be a verdict on the image", err)
	}
	if !strings.Contains(err.Error(), "stopped while running check") {
		t.Errorf("err = %v, want it to say the VM stopped", err)
	}
}

// A check that hangs is a guest that is subtly useless, which is the
// whole failure mode being designed against -- so it fails with a
// verdict naming it rather than hanging the run.
func TestVerifyGivesUpOnACheckThatNeverReturns(t *testing.T) {
	opts, _ := testOptions(t)
	opts.CheckTimeout = 2 * time.Second
	t.Setenv("FAKEDOCKER_HANG_CONTAINS", checkPath)

	_, err := Verify(context.Background(), opts)
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want a failed verdict", err)
	}
	if !strings.Contains(err.Error(), "docker starts and answers") {
		t.Errorf("err = %v, want it to name the check that hung", err)
	}
}

func TestVerifyRefusesAnEmptyCheckSet(t *testing.T) {
	opts, calls := testOptions(t)
	opts.Checks = nil
	if _, err := Verify(context.Background(), opts); err == nil {
		t.Fatal("expected an error for a verify with no checks, got nil")
	}
	if len(calls()) != 0 {
		t.Errorf("an empty check set booted something: %v", calls())
	}
}

// The VM is removed whatever happened, or every failed verify leaks a
// container and a tap device.
func TestVerifyRemovesTheVMAfterAFailedCheck(t *testing.T) {
	opts, calls := testOptions(t)
	opts.Checks = []Check{failing("docker starts and answers", "no")}
	if _, err := Verify(context.Background(), opts); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
	// Create force-removes stale containers of its own before starting,
	// so two `rm -f` calls are its; the cleanup adds the VM and the
	// namespace holder.
	if n := count(calls(), "rm", "-f"); n != 4 {
		t.Errorf("`rm -f` calls = %d, want 4 (2 from Create, 2 cleaning up); calls were %v", n, calls())
	}
}

func TestVerifyKeepsTheVMOnFailureWhenAsked(t *testing.T) {
	opts, calls := testOptions(t)
	opts.KeepOnFailure = true
	opts.Checks = []Check{failing("docker starts and answers", "no")}

	_, err := Verify(context.Background(), opts)
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
	if !strings.Contains(err.Error(), "docker logs") {
		t.Errorf("err = %v, want it to name the VM left behind", err)
	}
	if n := count(calls(), "rm", "-f"); n != 2 {
		t.Errorf("`rm -f` calls = %d, want only Create's own 2 under -keep-on-failure; calls were %v", n, calls())
	}
}

// A verdict is only believed from a file this user wrote and nobody else
// could have: its key is computable by anyone, so in a cache directory
// another user can write to, a planted entry would otherwise be a pass no
// boot ever earned.
func TestVerifyIgnoresAVerdictOthersCouldHaveWritten(t *testing.T) {
	opts, calls := testOptions(t)
	opts.Checks = []Check{{Name: "the guest boots", Script: "true\n"}}
	if _, err := Verify(context.Background(), opts); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	entries, err := os.ReadDir(opts.CacheDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("a passing verdict was not cached: %v %v", entries, err)
	}
	entry := filepath.Join(opts.CacheDir, entries[0].Name())
	info, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		t.Fatalf("the cache wrote an entry other users can write: %v", perm)
	}

	booted := func() bool {
		before := len(calls())
		res, err := Verify(context.Background(), opts)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		for _, call := range calls()[before:] {
			if call[0] == "run" {
				return true
			}
		}
		if !res.Cached {
			t.Fatal("no boot and no cached verdict")
		}
		return false
	}
	if booted() {
		t.Fatal("a verdict this user wrote was not believed")
	}

	// The same entry, writable by anyone: a file that could have been
	// put there by someone else is no verdict.
	if err := os.Chmod(entry, 0o666); err != nil {
		t.Fatal(err)
	}
	if !booted() {
		t.Fatal("a world-writable cache entry was believed")
	}

	// And a FIFO planted under the entry's name is neither believed nor
	// able to hang the read.
	entries, _ = os.ReadDir(opts.CacheDir)
	entry = filepath.Join(opts.CacheDir, entries[0].Name())
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(entry, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if !booted() {
		t.Fatal("a FIFO in the cache was believed")
	}
}
