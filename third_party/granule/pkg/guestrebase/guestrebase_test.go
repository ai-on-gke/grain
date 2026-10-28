package guestrebase

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestimage"
)

var fakedockerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakedocker-rebase")
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

const (
	theirGuest   = "ghcr.io/example/their-guest:v9"
	granuleImage = "ghcr.io/example/kontur-runtime:2026-09"
	outputTag    = "example/guest:rebased"
)

// noKernel is the ordinary case: neither the guest being rebased nor the
// granule image underneath it brings a kernel of its own, so both boot
// the deployment's vmlinux.
func noKernel(refs ...string) string {
	if len(refs) == 0 {
		refs = []string{theirGuest, granuleImage, outputTag}
	}
	var entries []string
	for _, ref := range refs {
		entries = append(entries,
			ref+"="+guestimage.GuestKernel,
			ref+"="+guestimage.GuestInitramfs)
	}
	return strings.Join(entries, ",")
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
	t.Setenv("FAKEDOCKER_ABSENT", noKernel())

	opts := Options{
		From:        theirGuest,
		Tag:         outputTag,
		KonturImage: granuleImage,
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
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

// The property the cheap form exists to have: it builds one image, it
// never starts a container from the image it is rebasing, and the build
// it runs has no RUN in it at all.
func TestCheapRebaseRunsNothingFromTheImageItRebases(t *testing.T) {
	opts, calls, dockerfile := testOptions(t)
	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}

	var builds int
	for _, call := range calls() {
		switch call[0] {
		case "build":
			builds++
		case "run", "start", "exec":
			t.Errorf("a rebase ran something: docker %s", strings.Join(call, " "))
		}
	}
	if builds != 1 {
		t.Errorf("docker build ran %d times, want 1", builds)
	}

	file := dockerfile()
	if strings.Contains(file, "\nRUN ") || strings.HasPrefix(file, "RUN ") {
		t.Errorf("the generated build has a RUN in it, so it could execute something out of %s:\n%s", theirGuest, file)
	}
	// The granule image is the base, and the guest half is what comes
	// across -- not the other way around, which would keep the
	// consumer's entrypoint and environment.
	if !strings.Contains(file, "FROM "+granuleImage+"\n") {
		t.Errorf("the generated build is not FROM the granule image:\n%s", file)
	}
	want := "COPY --from=" + theirGuest + " " + guestimage.DiskImage + " " + guestimage.DiskImage
	if !strings.Contains(file, want) {
		t.Errorf("the generated build does not copy the guest disk (%q):\n%s", want, file)
	}
	// vmlinux lives in the guest directory and belongs to the VMM half:
	// copying it across is how an image pins a kernel the deployment has
	// since replaced.
	if strings.Contains(file, "COPY --from="+theirGuest+" "+guestimage.RuntimeKernel) {
		t.Errorf("the generated build copies %s out of the image being rebased:\n%s", guestimage.RuntimeKernel, file)
	}
	if !strings.Contains(file, RebasedFromLabel+"=\""+theirGuest+"\"") {
		t.Errorf("the result does not record what it was rebased from:\n%s", file)
	}
}

// A `docker build --target runtime` granule image brings no kernel, and
// the ordinary guest brings none either: rebasing one onto the other
// produces an image whose boot stops at a missing CHV_KERNEL on whatever
// node runs it. A build is the cheap place to find that out, so the
// refusal happens here and names both halves.
func TestCheapRebaseRefusesWhenNothingWouldBootTheResult(t *testing.T) {
	opts, calls, _ := testOptions(t)
	// The guest brings no kernel (the default) and the granule image has
	// no vmlinux either, which is what a --target runtime build is.
	t.Setenv("FAKEDOCKER_ABSENT", noKernel()+","+granuleImage+"="+guestimage.RuntimeKernel)

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("Rebase succeeded, want a refusal naming the kernel neither half carries")
	}
	for _, want := range []string{guestimage.RuntimeKernel, "--target runtime", granuleImage, theirGuest} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	for _, call := range calls() {
		if call[0] == "build" {
			t.Errorf("an image was built anyway: docker %s", strings.Join(call, " "))
		}
	}
}

// The same pair the other way round is fine: a guest that brings its own
// kernel is exactly what a granule image without one is for.
func TestCheapRebaseAllowsAGuestKernelToStandInForTheRuntimeKernel(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	t.Setenv("FAKEDOCKER_ABSENT", noKernel(granuleImage, outputTag)+","+granuleImage+"="+guestimage.RuntimeKernel)

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if file := dockerfile(); !strings.Contains(file, "COPY --from="+theirGuest+" "+guestimage.GuestKernel) {
		t.Errorf("the generated build does not carry the guest's own kernel across:\n%s", file)
	}
}

// A guest that brings its own kernel brings it as a pair, and the pair
// has to travel with the disk it belongs to.
func TestCheapRebaseCarriesTheGuestsOwnKernelPair(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	// Only the granule image and the result lack a kernel now; the guest
	// being rebased has one.
	t.Setenv("FAKEDOCKER_ABSENT", noKernel(granuleImage))

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	file := dockerfile()
	for _, path := range []string{guestimage.GuestKernel, guestimage.GuestInitramfs} {
		if !strings.Contains(file, "COPY --from="+theirGuest+" "+path+" "+path) {
			t.Errorf("the generated build does not carry %s across:\n%s", path, file)
		}
	}
}

// What a guest declares it wants reachable belongs to the guest, so it
// has to survive an operation that keeps the machine and replaces
// everything around it. A rebase is FROM the granule image, so nothing of
// the rebased image's config comes across unless this build says so --
// and a declaration silently dropped here would undo what `guest build`
// and `guest pack` carried that far.
//
// Declared, not granted: the EXPOSE lands in the result's config and
// opens nothing.
func TestCheapRebaseCarriesWhatTheGuestDeclares(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	// Unsorted and undecorated, the way an image's config hands them
	// over, and with one value that is not a port at all -- these come
	// from an image somebody else authored and are written into a
	// generated Dockerfile.
	t.Setenv("FAKEDOCKER_EXPOSED", "8080/tcp 53/udp notaport")

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	file := dockerfile()
	if !strings.Contains(file, "\nEXPOSE 53/udp 8080/tcp\n") {
		t.Errorf("the guest's declared ports were not carried across, sorted and without the value that is not one:\n%s", file)
	}
	if strings.Contains(file, "notaport") {
		t.Errorf("a value that is not a port reached the generated build:\n%s", file)
	}
	// After the FROM, or it would be a declaration on nothing.
	if strings.Index(file, "\nEXPOSE ") < strings.Index(file, "\nFROM ") {
		t.Errorf("EXPOSE is above the FROM:\n%s", file)
	}
}

// An image that declares nothing produces no EXPOSE and no noise.
func TestCheapRebaseWithNothingDeclared(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	out := &bytes.Buffer{}
	opts.Stdout = out
	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if strings.Contains(dockerfile(), "\nEXPOSE ") {
		t.Errorf("an image that declares no ports produced an EXPOSE:\n%s", dockerfile())
	}
	if strings.Contains(out.String(), "EXPOSE") {
		t.Errorf("a rebase with nothing to carry talked about carrying:\n%s", out)
	}
}

func TestRebaseRefusesAnImageWithNoDisk(t *testing.T) {
	opts, calls, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_ABSENT", noKernel()+","+theirGuest+"="+guestimage.DiskImage)

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("Rebase succeeded on an image with no guest disk in it")
	}
	if !strings.Contains(err.Error(), guestimage.DiskImage) {
		t.Errorf("the error does not name the missing disk: %v", err)
	}
	for _, call := range calls() {
		if call[0] == "build" {
			t.Errorf("a rebase that cannot work still ran a build: docker %s", strings.Join(call, " "))
		}
	}
}

// granuleImageUnresolvable is Docker 29.8.1's own output, pasted from a
// real build, for a rebase whose granule image cannot be resolved --
// here a tag that is not in the registry. Every line of this build is a
// COPY of something under guestimage.Dir, so that directory is in the
// log whatever the build fails on; what failed here is the FROM.
const granuleImageUnresolvable = `#3 [internal] load metadata for ` + granuleImage + `
#3 ERROR: ` + granuleImage + `: not found
------
 > [internal] load metadata for ` + granuleImage + `:
------
Dockerfile:16
--------------------
  14 |     FROM ` + granuleImage + `
  15 |     
  16 | >>> COPY --from=` + theirGuest + ` ` + guestimage.DiskImage + ` ` + guestimage.DiskImage + `
  17 |     
--------------------
ERROR: failed to build: failed to solve: ` + granuleImage + `: failed to resolve source metadata for ` + granuleImage + `: ` + granuleImage + `: not found`

// A rebase explains one build failure of its own: the image being
// rebased lost the guest half between the read that checked it and the
// build that copies it, which is what a moving tag republished
// underneath a rebase looks like. It has to be that failure and not
// merely a log with the words in it -- this build copies nothing but
// guest-half paths, so the directory is in every line of it.
func TestRebaseDoesNotBlameTheGuestHalfForAnUnrelatedFailure(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_WITH", granuleImageUnresolvable)

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("Rebase succeeded, want the build's failure to come back as an error")
	}
	if strings.Contains(err.Error(), "no longer carries") {
		t.Errorf("an unresolvable granule image was explained as the guest half disappearing mid-rebase: %v", err)
	}
	if !strings.Contains(err.Error(), "failed to resolve source metadata") {
		t.Errorf("the build's own message did not survive: %v", err)
	}
}

// Neither reference a rebase builds from reaches the build when it will
// not resolve: both images are read -- `docker create`, then `docker cp`
// -- to decide what the build should say, so a registry that will not
// hand one over stops that read instead, and says only "denied" while
// doing it.
func TestRebaseExplainsAnUnresolvableReference(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  string
		want []string
	}{
		{"the guest being rebased", theirGuest, []string{theirGuest + " could not be resolved", "moving tag", "docker login"}},
		{"the granule image it is rebased onto", granuleImage, []string{granuleImage + " could not be resolved", "-kontur-image", "docker login"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, _ := testOptions(t)
			t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "create "+tc.ref)
			t.Setenv("FAKEDOCKER_FAIL_MESSAGE", "Unable to find image '"+tc.ref+"' locally\nError response from daemon: error from registry: denied\ndenied")

			err := Rebase(context.Background(), opts)
			if err == nil {
				t.Fatal("Rebase succeeded, want an error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the explanation does not mention %q: %v", want, err)
				}
			}
			if !strings.Contains(err.Error(), "error from registry: denied") {
				t.Errorf("docker's own message did not survive: %v", err)
			}
		})
	}
}

// A read that failed for a reason of the daemon's is not a registry
// refusing the reference, and gets none of the advice above.
func TestRebaseDoesNotBlameTheRegistryForAReadThatFailed(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "create "+theirGuest)
	t.Setenv("FAKEDOCKER_FAIL_MESSAGE", "Error response from daemon: no space left on device")

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("Rebase succeeded, want an error")
	}
	if strings.Contains(err.Error(), "could not be resolved") {
		t.Errorf("a full disk was explained as a reference a registry would not hand over: %v", err)
	}
}

// And the failure it is for, as a real daemon prints it: the COPY of
// the guest half is the step that fails.
func TestRebaseExplainsAGuestHalfThatDisappearedMidRebase(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_FAIL_WITH", `#6 [stage-0 2/4] COPY --from=`+theirGuest+` `+guestimage.DiskImage+` `+guestimage.DiskImage+`
#6 ERROR: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::l1rpiujg5nrz9cig19962b25m: "`+guestimage.DiskImage+`": not found
------
 > [stage-0 2/4] COPY --from=`+theirGuest+` `+guestimage.DiskImage+` `+guestimage.DiskImage+`:
------
ERROR: failed to build: failed to solve: failed to compute cache key: failed to calculate checksum of ref hxrqy3v1bp6qxltqwr9lh8e54::l1rpiujg5nrz9cig19962b25m: "`+guestimage.DiskImage+`": not found`)

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("Rebase succeeded, want the build's failure to come back as an error")
	}
	if !strings.Contains(err.Error(), "no longer carries") {
		t.Errorf("the guest half vanishing mid-rebase was not explained: %v", err)
	}
}

// Half a kernel pair is guestimage.Check's other failure, and it reaches
// a rebase through the image it is reading rather than through a
// directory.
func TestRebaseRefusesHalfAKernelPair(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_ABSENT", noKernel(granuleImage, outputTag)+","+theirGuest+"="+guestimage.GuestInitramfs)

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("Rebase succeeded on a guest with a kernel and no initramfs")
	}
	if !strings.Contains(err.Error(), guestimage.GuestInitramfs) {
		t.Errorf("the error does not name the missing initramfs: %v", err)
	}
}

// A granule image with a bundled guest kernel would leave that pair
// beside somebody else's disk, and there is no RUN in this build to
// delete it with -- so the combination is refused rather than built.
func TestRebaseRefusesAGranuleImageWhoseKernelWouldOutliveIt(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_ABSENT", noKernel(theirGuest, outputTag))

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("Rebase succeeded onto a granule image carrying a guest kernel of its own")
	}
	if !strings.Contains(err.Error(), "--target runtime") {
		t.Errorf("the error does not say what to rebase onto instead: %v", err)
	}
}

// The check that matters after the build: an image that came out with no
// disk in it fails here, naming the disk, rather than at the first boot
// of it.
func TestRebaseChecksTheImageItProduced(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_ABSENT", noKernel()+","+outputTag+"="+guestimage.DiskImage)

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("Rebase succeeded having produced an image with no guest disk in it")
	}
	if !strings.Contains(err.Error(), "not a bootable guest image") || !strings.Contains(err.Error(), guestimage.DiskImage) {
		t.Errorf("the error does not say what the built image is missing: %v", err)
	}
}

// What the cheap form did not do is the thing a caller most needs told,
// and an image packed by granule says where its rootfs came from.
func TestCheapRebaseSaysWhatItDidNotRefresh(t *testing.T) {
	opts, _, _ := testOptions(t)
	t.Setenv("FAKEDOCKER_LABELS", PackedFromLabel+"=ghcr.io/example/their-rootfs:v9")
	out := &bytes.Buffer{}
	opts.Stdout = out

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if !strings.Contains(out.String(), guestimage.Dir+" is untouched") {
		t.Errorf("a cheap rebase did not say the guest half is untouched:\n%s", out)
	}
	if !strings.Contains(out.String(), "-rootfs ghcr.io/example/their-rootfs:v9") {
		t.Errorf("a cheap rebase did not name the rootfs image that would refresh it:\n%s", out)
	}
}

func TestCheapRebaseOffersBothFormsWhenNothingRecordsARootfs(t *testing.T) {
	opts, _, _ := testOptions(t)
	out := &bytes.Buffer{}
	opts.Stdout = out

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if !strings.Contains(out.String(), "-rootfs <image>") || !strings.Contains(out.String(), "-boot") {
		t.Errorf("a cheap rebase did not name the flags that refresh the guest side:\n%s", out)
	}
	// Every image published before granule started stamping a version is
	// this one, and it must be a report rather than a refusal.
	if !strings.Contains(out.String(), "declares no guest-side contract version") {
		t.Errorf("a cheap rebase did not say the image declares no contract version:\n%s", out)
	}
}

// A guest that declares a contract this granule supports has been checked
// rather than skipped, and the output says so in one line instead of the
// paragraph that offers to refresh it.
func TestCheapRebaseIsQuietAboutACurrentGuest(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	t.Setenv("FAKEDOCKER_LABELS", guestimage.ContractLabel+"="+guestimage.ContractValue())
	out := &bytes.Buffer{}
	opts.Stdout = out

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if !strings.Contains(out.String(), "which this granule supports") {
		t.Errorf("a cheap rebase did not say the guest's contract is one it supports:\n%s", out)
	}
	for _, unwanted := range []string{"-rootfs <image>", "is untouched"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("a cheap rebase warned about a guest it had just found current (%q):\n%s", unwanted, out)
		}
	}
	// The result carries the guest's own declaration forward. Inheriting
	// the granule image's would have the result claiming its disk is as
	// current as the binaries outside it.
	want := guestimage.ContractLabel + `="` + guestimage.ContractValue() + `"`
	if !strings.Contains(dockerfile(), want) {
		t.Errorf("the generated build does not carry %s across:\n%s", want, dockerfile())
	}
}

// A label is text off an image somebody else built, and it reaches
// docker by being written into a Dockerfile. Anything that is not a
// version is not written at all.
func TestCheapRebaseDoesNotCarryAJunkContractForward(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	// A comma would split fakedocker's own list, and a quote is what this
	// is really about: a value that closes the LABEL and opens an
	// instruction of its own.
	t.Setenv("FAKEDOCKER_LABELS", guestimage.ContractLabel+`=1" `+"\nRUN whoami")
	out := &bytes.Buffer{}
	opts.Stdout = out

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	file := dockerfile()
	if strings.Contains(file, "\nRUN ") || strings.HasPrefix(file, "RUN ") {
		t.Errorf("a label off the image being rebased put a RUN in the generated build:\n%s", file)
	}
	if !strings.Contains(file, guestimage.ContractLabel+`=""`) {
		t.Errorf("an unreadable declaration did not come out as no declaration:\n%s", file)
	}
	if !strings.Contains(out.String(), "not a version this granule can compare") {
		t.Errorf("a cheap rebase did not say the declaration was unreadable:\n%s", out)
	}
	// An image carrying a label granule cannot read is not an image from
	// before granule wrote labels, and saying so would tell the reader the
	// opposite of what happened to their image.
	if strings.Contains(out.String(), "published before granule wrote one down") {
		t.Errorf("a cheap rebase called an unreadable declaration an absent one:\n%s", out)
	}
}

// tooOld puts every declared version below the floor. The floor is at
// the first version anything has ever declared, so no image can be too
// old for this build yet -- and the refusal that backstop exists for is
// worth testing before the day it has to work.
func tooOld(t *testing.T) {
	t.Helper()
	previous := contractState
	contractState = func(declared string) (guestimage.ContractState, int) {
		state, version := previous(declared)
		if state == guestimage.ContractSupported {
			return guestimage.ContractTooOld, version
		}
		return state, version
	}
	t.Cleanup(func() { contractState = previous })
}

// The backstop: a guest whose declared contract predates what this
// granule requires is refused rather than warned about, before anything
// is built, and the refusal names what would fix it.
func TestCheapRebaseRefusesAGuestBelowTheFloor(t *testing.T) {
	opts, calls, _ := testOptions(t)
	tooOld(t)
	t.Setenv("FAKEDOCKER_LABELS", guestimage.ContractLabel+"="+guestimage.ContractValue()+","+PackedFromLabel+"=ghcr.io/example/their-rootfs:v9")

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("a cheap rebase accepted a guest older than this granule can drive")
	}
	for _, want := range []string{
		"contract version",
		"-rootfs ghcr.io/example/their-rootfs:v9",
		"-boot",
		"-force",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	for _, call := range calls() {
		if call[0] == "build" {
			t.Errorf("a rebase that was going to refuse still ran a build: docker %s", strings.Join(call, " "))
		}
	}
}

// Cheap makes the same refusal, because its caller is the one a guest
// too old to answer hurts most: `granule guest verify` boots what this
// builds, and without the refusal it learns by a timeout what the label
// says outright.
func TestCheapRefusesAGuestBelowTheFloor(t *testing.T) {
	opts, calls, _ := testOptions(t)
	tooOld(t)
	t.Setenv("FAKEDOCKER_LABELS", guestimage.ContractLabel+"="+guestimage.ContractValue())

	err := Cheap(context.Background(), opts)
	if err == nil {
		t.Fatal("Cheap accepted a guest older than this granule can drive")
	}
	if !strings.Contains(err.Error(), "contract version") {
		t.Errorf("the refusal does not name the contract: %v", err)
	}
	for _, call := range calls() {
		if call[0] == "build" {
			t.Errorf("Cheap refused but still ran a build: docker %s", strings.Join(call, " "))
		}
	}
}

// -force is the caller saying they know something the label does not.
// It rebases, and it says plainly what the result carries.
func TestForceRebasesBelowTheFloorAnyway(t *testing.T) {
	opts, calls, _ := testOptions(t)
	tooOld(t)
	t.Setenv("FAKEDOCKER_LABELS", guestimage.ContractLabel+"="+guestimage.ContractValue())
	opts.Force = true
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	opts.Stdout, opts.Stderr = out, errOut

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase with -force: %v", err)
	}
	if find(calls(), "build") == nil {
		t.Errorf("-force refused anyway: no build ran")
	}
	if !strings.Contains(errOut.String(), "-force was given") {
		t.Errorf("-force did not warn about the guest it let through:\n%s", errOut)
	}
	if !strings.Contains(out.String(), "rebased anyway") {
		t.Errorf("the report does not say the guest side is still the old one:\n%s", out)
	}
}

// The complete forms are the answer to the question the floor asks, so
// they never ask it: -boot lays the current guest side down whatever the
// old one declared, and stamps what the result now implements -- in the
// label a rebase reads and in the sidecar a boot reads, since a commit
// would otherwise carry the base's copy of the second one forward to
// describe a guest side that has just been replaced under it.
func TestBootRebaseRestampsTheContract(t *testing.T) {
	opts, calls, _ := testOptions(t)
	tooOld(t)
	t.Setenv("FAKEDOCKER_LABELS", guestimage.ContractLabel+"="+guestimage.ContractValue())
	opts.Boot = true
	parts := t.TempDir()
	if err := os.WriteFile(filepath.Join(parts, "granule-agent"), []byte("ELF\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKEDOCKER_PARTS", parts)
	t.Setenv("FAKEDOCKER_ABSENT", noKernel(theirGuest, granuleImage, outputTag, bootBaseTag(outputTag)))
	placed := t.TempDir()
	t.Setenv("FAKEDOCKER_PLACED", placed)

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase -boot: %v", err)
	}
	commit := find(calls(), "commit")
	if commit == nil {
		t.Fatalf("nothing was committed; calls were %v", calls())
	}
	want := "LABEL " + guestimage.ContractLabel + "=" + guestimage.ContractValue()
	if !strings.Contains(strings.Join(commit, " "), want) {
		t.Errorf("the boot form committed without %q: docker %s", want, strings.Join(commit, " "))
	}

	sidecar, err := os.ReadFile(filepath.Join(placed, guestimage.ContractSidecarName))
	if err != nil {
		t.Fatalf("the boot form committed without restating the sidecar beside the disk: %v", err)
	}
	if got := strings.TrimSpace(string(sidecar)); got != guestimage.ContractValue() {
		t.Errorf("the sidecar beside the rebased disk says %q, want %q", got, guestimage.ContractValue())
	}
}

// The complete form by repack is a pack: the disk is rebuilt from the
// rootfs with the current agents in it, and the guest image being
// rebased is not a source of anything.
func TestRebaseByRepackIsAPackOfTheRootfs(t *testing.T) {
	opts, _, dockerfile := testOptions(t)
	opts.Rootfs = "ghcr.io/example/their-rootfs:v9"
	out := &bytes.Buffer{}
	opts.Stdout = out

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	file := dockerfile()
	if !strings.Contains(file, "COPY --from="+opts.Rootfs+" / /rootfs") {
		t.Errorf("the repack does not pack the rootfs image:\n%s", file)
	}
	if strings.Contains(file, theirGuest) {
		t.Errorf("the repack reads the guest image it is replacing:\n%s", file)
	}
	if !strings.Contains(out.String(), "replaced rather than carried over") {
		t.Errorf("the repack did not say the old disk is being replaced:\n%s", out)
	}
}

func TestRebaseRejectsBothCompleteForms(t *testing.T) {
	opts, _, _ := testOptions(t)
	opts.Rootfs = "ghcr.io/example/their-rootfs:v9"
	opts.Boot = true

	if err := Rebase(context.Background(), opts); err == nil {
		t.Fatal("Rebase accepted -rootfs and -boot together")
	}
}

// A variant only means something to a form that lays an overlay down,
// and silently ignoring it would leave a caller believing a cheap rebase
// had replaced their agents.
func TestRebaseRejectsAVariantWithNothingToLayDown(t *testing.T) {
	opts, _, _ := testOptions(t)
	opts.Variant = "alpine"

	err := Rebase(context.Background(), opts)
	if err == nil {
		t.Fatal("Rebase accepted -variant with neither -rootfs nor -boot")
	}
	if !strings.Contains(err.Error(), "leaves the disk alone") {
		t.Errorf("the error does not say why the variant has nothing to do: %v", err)
	}
}

// Every reference here is interpolated into a Dockerfile, so one
// carrying a newline is one that can append instructions to a file whose
// whole point is that it has none.
func TestRebaseRefusesAReferenceThatCouldWriteInstructions(t *testing.T) {
	opts, calls, _ := testOptions(t)
	opts.From = "example/guest:v1\nRUN echo pwned"

	if err := Rebase(context.Background(), opts); err == nil {
		t.Fatal("Rebase accepted an image reference containing a newline")
	}
	if len(calls()) != 0 {
		t.Errorf("a rebase with an unusable reference still called docker: %v", calls())
	}
}

// The boot form is the only one that runs the guest's code, and the
// thing it must not do is run the *consumer's granule* to do it: it
// builds the cheap rebase first and boots that, so the VMM half running
// the guest is already the operator's. The intermediate image is not an
// artifact and does not outlive the call.
func TestBootRebaseBootsTheRebasedImageRatherThanTheOriginal(t *testing.T) {
	opts, calls, _ := testOptions(t)
	opts.Boot = true
	parts := t.TempDir()
	if err := os.WriteFile(filepath.Join(parts, "granule-agent"), []byte("ELF\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKEDOCKER_PARTS", parts)
	intermediate := bootBaseTag(outputTag)
	t.Setenv("FAKEDOCKER_ABSENT", noKernel(theirGuest, granuleImage, outputTag, intermediate))

	if err := Rebase(context.Background(), opts); err != nil {
		t.Fatalf("Rebase: %v", err)
	}

	recorded := calls()
	var booted, removed bool
	for _, call := range recorded {
		if call[0] == "run" {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, theirGuest) {
				t.Errorf("the boot form ran the image being rebased: docker %s", joined)
			}
			if strings.Contains(joined, intermediate) {
				booted = true
			}
		}
		if call[0] == "image" && len(call) > 1 && call[1] == "rm" {
			removed = true
		}
	}
	if !booted {
		t.Errorf("nothing booted the rebased image %s; calls were %v", intermediate, recorded)
	}
	if !removed {
		t.Errorf("the intermediate image %s was left behind; calls were %v", intermediate, recorded)
	}
	if find(recorded, "commit") == nil {
		t.Errorf("nothing was committed; calls were %v", recorded)
	}
	// The parts laid over the guest come out of the granule image, which
	// is what keeps the agents in a guest and the granule that talks to
	// them from skewing.
	if find(recorded, "cp", "c-"+base64.RawURLEncoding.EncodeToString([]byte(granuleImage))+":"+guestimage.PartsDir+"/.") == nil {
		t.Errorf("the guest-side parts were not taken out of %s; calls were %v", granuleImage, recorded)
	}
}

// find returns the first recorded call containing every one of want.
func find(calls [][]string, want ...string) []string {
	for _, call := range calls {
		joined := strings.Join(call, " ")
		found := true
		for _, w := range want {
			if !strings.Contains(joined, w) {
				found = false
				break
			}
		}
		if found {
			return call
		}
	}
	return nil
}

// The boot form's setup script is one instruction, and it is pack-guest's
// own: what a refresh means -- which overlay, which files, what to say
// about replacing one the consumer edited -- stays in one place.
func TestBootSetupScriptLaysDownWithPackGuest(t *testing.T) {
	script := setupScript("alpine")
	for _, want := range []string{
		"GRANULE_GUEST_VARIANT=alpine",
		stageDir + "/pack-guest -lay-only / " + stageDir,
		"rm -rf " + stageDir,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the setup script does not contain %q:\n%s", want, script)
		}
	}
	if !strings.Contains(setupScript(""), "GRANULE_GUEST_VARIANT=auto") {
		t.Errorf("an unset variant does not ask the guest which init system it runs:\n%s", setupScript(""))
	}
}

// The parts reach the guest as a tar, because a 5MB static binary cannot
// travel on a command line the way the setup script does.
func TestTarDirCarriesTheTreeItWasGiven(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "overlay-common/etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "overlay-common/etc/thing.conf"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "granule-agent"), []byte("ELF\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := tarDir(dir)
	if err != nil {
		t.Fatalf("tarDir: %v", err)
	}
	found := map[string]int64{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading the archive: %v", err)
		}
		found[hdr.Name] = hdr.Mode
	}
	if _, ok := found["overlay-common/etc/thing.conf"]; !ok {
		t.Errorf("the archive is missing the overlay tree: %v", found)
	}
	if mode, ok := found["granule-agent"]; !ok || mode&0o111 == 0 {
		t.Errorf("the archive lost the agent or its executable bit: %v", found)
	}
}
