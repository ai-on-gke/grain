// Package guestverify boots a granule guest image, runs checks inside it,
// and produces a verdict. It commits nothing, tags nothing and emits no
// image: the point of the verb is that nothing it does can change what it
// is judging.
//
// # What it is for
//
// A guest image can be built without booting -- the Dockerfile's
// GUEST_SETUP_SCRIPT hook does exactly that -- and most of what a
// consumer wants to assert about the result cannot be asserted that way:
//
//	systemctl enable docker   # works on the container path -- it is symlinks
//	systemctl start docker    # does not: nothing is running to ask
//	docker info >/dev/null    # does not
//
// The last two lines prove that the machine works rather than that its
// files are in place, and they need a booted machine to run in. Until
// there was somewhere to put them they lived inside a `guest build`,
// where they were provisioning steps that happened to assert something --
// which means the thing that judged the image was also the thing that
// produced it, and a check that passed proved nothing about the artifact
// anyone else would get.
//
// # Three properties, built in rather than added later
//
// It boots the image as it will actually run. A deployment runs somebody
// else's guest half under its own VMM half (see docs/guest-images.md), so
// GranuleImage says which granule, cloud-hypervisor and entrypoint to boot
// the guest under and the boot happens *after* that rebase. Verifying the
// published artifact instead would prove something about an image nobody
// boots -- and this is the first place a guest-contract mismatch turns
// into a sentence rather than into a VM that never answers.
//
// Its verdict is keyed and cached, on the image's digest and the check
// set (see key). A consumer that verifies before use -- which is the
// point -- would otherwise boot a VM on every sandbox start to re-learn
// something about an image that has not changed.
//
// A failure blocks. Verify returns an error wrapping ErrFailed when any
// check fails, so the caller that ignores the verdict is the caller that
// wrote no error handling at all, rather than the caller that forgot to
// read a field.
//
// # What it deliberately does not do
//
// It does not write to the image being verified: the guest boots in the
// overlay disk mode, so everything the checks do lands in a throwaway
// overlay and is discarded with the VM. A verify that could change its
// subject would be a build.
package guestverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/guestboot"
	"github.com/bwsalmon/granule/pkg/guestrebase"
)

// checkPath is where a check's script is staged inside the guest, for
// the same reasons guestbuild's setupPath is where it is: not under
// /tmp, which the guest's own boot empties a second or two in, and on
// the root filesystem rather than a noexec /run, because the script is
// executed by path so that its shebang decides what interprets it.
//
// One path reused by every check rather than one per check: the guest is
// thrown away at the end, nothing here is committed, and a fixed path
// keeps the exec that fails a check the check's own rather than the
// staging's.
const checkPath = "/root/.granule-guest-check.sh"

// ErrFailed is what a verdict of "failed" is reported as. It is an error
// rather than a field so that a caller has to go out of its way to run
// an image that did not pass -- see the package comment.
var ErrFailed = errors.New("the image failed verification")

// DefaultCheckTimeout bounds one check. Generous, because a check that
// installs something or waits for a service legitimately takes a while,
// and bounded at all because the failure this verb is designed against
// includes a guest that accepts a command and never answers it.
const DefaultCheckTimeout = 5 * time.Minute

// Options configures one Verify.
type Options struct {
	// Image is the guest image under test. Its guest half is what boots;
	// its VMM half is only used when GranuleImage is empty.
	Image string

	// GranuleImage is the image whose VMM half the guest is booted under:
	// this deployment's own granule, cloud-hypervisor and entrypoint. When
	// it is set, Verify assembles the two halves into a throwaway image
	// and boots that, which is the cheap form of a rebase
	// (pkg/guestrebase, whose Cheap does the assembling) and the
	// state the image will actually run in.
	//
	// Which image to name is the same question a rebase asks, because
	// the assembly *is* a rebase, and the rebase refuses the two ways of
	// getting it wrong rather than booting something nothing can reach.
	// A full granule image is right for an ordinary guest: it supplies
	// the kernel that boots one (guestimage.RuntimeKernel), which a
	// `docker build --target runtime` build deliberately does not --
	// name that one only for a guest that brings a kernel of its own.
	// The other refusal is a granule image carrying a bundled guest
	// *kernel* (one built with GUEST_KERNEL_PACKAGE) under a guest that
	// brings none, which would boot somebody else's disk on a stranger's
	// kernel.
	//
	// Empty means "boot Image as it stands", which is right when the two
	// halves come from the same build -- granule verifying its own image
	// -- and wrong whenever the deployment supplies the VMM half, where
	// it would verify a combination nobody runs.
	GranuleImage string

	// Checks are run in order, all of them: a failing check does not stop
	// the ones after it, because a report that names every broken thing
	// is worth more than one that names the first. They run against the
	// same booted guest, so a check may depend on an earlier one having
	// started something -- and the report says which failed, so it is
	// visible when one did.
	Checks []Check

	// CheckTimeout bounds each check individually. Zero means
	// DefaultCheckTimeout.
	CheckTimeout time.Duration

	// CacheDir holds verdicts keyed by image digest and check set. Empty
	// means DefaultCacheDir(). NoCache skips both the lookup and the
	// write.
	CacheDir string
	NoCache  bool

	// DockerBinary, ExtraRunArgs, ReadyTimeout and ShutdownTimeout are
	// guestboot's, and mean there what they mean here -- including a
	// zero ShutdownTimeout, which is how the machine's own recorded
	// shutdown-timeout comes to be the one it is verified with.
	DockerBinary    string
	ExtraRunArgs    []string
	ReadyTimeout    time.Duration
	ShutdownTimeout time.Duration

	// KeepOnFailure leaves the VM in place when a check fails or the boot
	// goes wrong, so the guest can be inspected with the checks' own
	// changes still in it. The report names the container.
	KeepOnFailure bool

	// Verbose prints every check's output, not just that of the ones that
	// failed.
	Verbose bool

	Stdout io.Writer
	Stderr io.Writer
}

// CheckResult is one check's verdict.
type CheckResult struct {
	Name string `json:"name"`

	// Passed is the verdict. Output and Err are what it was based on.
	Passed bool `json:"passed"`

	// Output is everything the check printed, stdout and stderr
	// interleaved as the guest produced them. Kept even for a check that
	// passed, because -v prints it and because "passed, and said
	// something alarming" is a thing a reader wants to be able to see.
	Output string `json:"-"`

	// Err is why a failing check failed: the guest command's own exit
	// status, or the timeout, or the reason it could not be run.
	Err error `json:"-"`

	Duration time.Duration `json:"duration"`
}

// Result is the whole verdict, whether it came from a boot or from the
// cache.
type Result struct {
	// Image is the reference asked for, ImageID the content digest it
	// resolved to. The digest is what the cache is keyed on, so a
	// floating tag that has moved is a different verdict rather than a
	// stale one.
	Image   string `json:"image"`
	ImageID string `json:"imageID"`

	// GranuleImage and GranuleImageID are the VMM half the guest was booted
	// under, empty when the image was booted as it stands. Part of the
	// key: the same guest under a different granule is a different
	// machine, and the guest contract between the two halves is exactly
	// what a mismatch here breaks.
	GranuleImage   string `json:"granuleImage,omitempty"`
	GranuleImageID string `json:"granuleImageID,omitempty"`

	// Booted is the image reference that was actually booted: Image, or
	// the throwaway rebase assembled from both halves.
	Booted string `json:"-"`

	// Key identifies this verdict: the digests above and the check set.
	Key string `json:"key"`

	// Checks is one entry per check, in the order they ran. Empty for a
	// cached verdict, which records that the checks passed and not what
	// each of them printed.
	Checks []CheckResult `json:"-"`

	// CheckNames is the check set this verdict is about, for a cached
	// entry to be readable by a human who finds it.
	CheckNames []string `json:"checks"`

	// Cached says the verdict came from a previous run and nothing was
	// booted. VerifiedAt is when the boot that produced it happened.
	Cached     bool      `json:"-"`
	VerifiedAt time.Time `json:"verifiedAt"`
}

// Failed reports whether any check failed.
func (r Result) Failed() bool {
	for _, c := range r.Checks {
		if !c.Passed {
			return true
		}
	}
	return false
}

// Verify boots opts.Image -- under opts.GranuleImage's VMM half, if one
// was given -- runs opts.Checks inside it, and returns the verdict.
//
// The returned error wraps ErrFailed when the image was verified and did
// not pass; any other error means the verdict could not be reached at
// all, which is equally a reason not to put the image into service but a
// different thing to report. The Result is returned in both cases.
func Verify(ctx context.Context, opts Options) (Result, error) {
	if opts.Image == "" {
		return Result{}, fmt.Errorf("an image to verify is required")
	}
	if len(opts.Checks) == 0 {
		// Nothing to prove, so nothing is proved: an empty check set
		// would otherwise pass every image and cache that answer.
		return Result{}, fmt.Errorf("at least one check is required: a verify with no checks would pass any image that booted")
	}
	for i, c := range opts.Checks {
		if strings.TrimSpace(c.Name) == "" {
			return Result{}, fmt.Errorf("check %d has no name; the name is what the verdict is reported as", i+1)
		}
	}
	if opts.CheckTimeout <= 0 {
		opts.CheckTimeout = DefaultCheckTimeout
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.CacheDir == "" {
		opts.CacheDir = DefaultCacheDir()
	}

	cli := &guestboot.CLI{Binary: opts.DockerBinary}

	res := Result{Image: opts.Image, GranuleImage: opts.GranuleImage}
	var err error
	if res.ImageID, err = imageID(ctx, cli, opts.Image); err != nil {
		return res, err
	}
	if opts.GranuleImage != "" {
		if res.GranuleImageID, err = imageID(ctx, cli, opts.GranuleImage); err != nil {
			return res, err
		}
	}
	res.Key = key(res.ImageID, res.GranuleImageID, opts.Checks)
	for _, c := range opts.Checks {
		res.CheckNames = append(res.CheckNames, c.Name)
	}

	fmt.Fprintf(opts.Stdout, "verifying %s (%s)\n", opts.Image, short(res.ImageID))
	if opts.GranuleImage != "" {
		fmt.Fprintf(opts.Stdout, "under the VMM half of %s (%s)\n", opts.GranuleImage, short(res.GranuleImageID))
	} else {
		// Said out loud rather than left to be inferred: an operator who
		// runs their own granule under somebody else's guest and verifies
		// without this has verified a machine nobody boots.
		fmt.Fprintf(opts.Stdout, "under this image's own VMM half (pass -kontur-image to boot it under the one that will run it)\n")
	}

	if !opts.NoCache {
		if cached, ok := readCache(opts.CacheDir, res.Key); ok {
			cached.Cached = true
			cached.Booted = ""
			fmt.Fprintf(opts.Stdout, "verified %s on %s; nothing booted (pass -no-cache to check again)\n",
				short(res.ImageID), cached.VerifiedAt.Format(time.RFC3339))
			return cached, nil
		}
	}

	// The image to boot: the one under test, or both halves assembled
	// into the machine that will actually run. That assembly is the cheap
	// form of `granule guest rebase` -- a generated build of `COPY
	// --from` lines with no RUN in it, which executes nothing from either
	// image -- and it is the rebase package's rather than a second
	// implementation here, so that what is verified is what a rebase
	// produces.
	res.Booted = opts.Image
	if opts.GranuleImage != "" {
		tag := rebaseTag(opts.Image, opts.GranuleImage)
		// Removed even when everything passes, and especially then: an
		// image left in the daemon that boots and has a granule-ish name
		// is exactly the artifact this verb is not supposed to produce.
		//
		// Registered before the rebase rather than after it, because a
		// rebase tags the image it built and *then* checks that what
		// came out can boot: a failure of that check returns an error
		// with the tag already in the daemon, which is the one case
		// where the image left behind is also one nobody wants. The tag
		// is this function's own and deterministic, so removing one that
		// was never created is a docker call that fails and is ignored,
		// and never somebody else's image.
		defer func() {
			_ = cli.Run(context.WithoutCancel(ctx), io.Discard, "image", "rm", "-f", tag)
		}()
		if err := guestrebase.Cheap(ctx, guestrebase.Options{
			From:         opts.Image,
			Tag:          tag,
			KonturImage:  opts.GranuleImage,
			DockerBinary: opts.DockerBinary,
			Stdout:       opts.Stdout,
			Stderr:       opts.Stderr,
		}); err != nil {
			return res, err
		}
		res.Booted = tag
	}

	s, bootErr := guestboot.Boot(ctx, guestboot.Options{
		Image:      res.Booted,
		NamePrefix: "gv-",
		// The overlay, not persistent: a check writes into a throwaway
		// overlay that is discarded with the VM, so nothing a check does
		// can reach the image it is judging. `guest build` needs the
		// opposite, which is why guestboot leaves this to its caller.
		DiskMode:        config.DiskModeOverlay,
		DockerBinary:    opts.DockerBinary,
		ExtraRunArgs:    opts.ExtraRunArgs,
		ReadyTimeout:    opts.ReadyTimeout,
		ShutdownTimeout: opts.ShutdownTimeout,
		Stdout:          opts.Stdout,
		Stderr:          opts.Stderr,
	})
	if s == nil {
		return res, bootErr
	}
	kept := false
	defer func() {
		if kept {
			return
		}
		_ = s.Remove(context.WithoutCancel(ctx))
	}()

	// keptFor annotates an error with the VM it left behind, and stops
	// the deferred removal from taking it away.
	keptFor := func(err error) error {
		kept = true
		return fmt.Errorf("%w (left running for inspection: docker logs %s; docker exec %s granule exec)", err, s.Container(), s.Container())
	}
	fail := func(err error) error {
		if opts.KeepOnFailure {
			return keptFor(err)
		}
		return s.WithConsole(ctx, err)
	}
	if bootErr != nil {
		// A guest that never answers is a verdict too, and the most
		// common one a mismatched pair of halves produces -- so it is
		// reported with the console log, which is the only place the
		// reason for it is written down.
		return res, fail(bootErr)
	}

	for _, c := range opts.Checks {
		cr := runCheck(ctx, s, c, opts.CheckTimeout)
		res.Checks = append(res.Checks, cr)
		report(opts, cr)
		if cr.Passed {
			continue
		}
		// A check can only fail on the guest's own account if the guest
		// is still there to have answered. A VM that has gone -- the
		// supervisor died, the container was killed -- fails every
		// remaining check for a reason that has nothing to do with the
		// image, and reporting that as "the image failed verification"
		// would be a false verdict on an image that may be fine.
		if running, err := s.Running(ctx); err == nil && !running {
			return res, fail(fmt.Errorf("the VM stopped while running check %q, so no verdict was reached: %w", c.Name, cr.Err))
		}
	}

	if res.Failed() {
		// No console log on this path, unlike the boot failures above: a
		// check that ran and said no has already reported the evidence
		// that matters -- its own output, printed under its name -- and
		// forty lines of kernel messages on top of it would bury it.
		verdict := fmt.Errorf("%w: %s", ErrFailed, summary(res))
		if opts.KeepOnFailure {
			return res, keptFor(verdict)
		}
		return res, verdict
	}

	// Cached only on success, deliberately. A cached pass saves a boot on
	// every sandbox start, which is what the cache is for; a cached
	// failure would pin a verdict that a re-pulled image or a fixed
	// environment should be able to clear, on an image that is not in
	// service anyway because it did not pass.
	if !opts.NoCache {
		res.VerifiedAt = time.Now().UTC()
		if err := writeCache(opts.CacheDir, res); err != nil {
			// Not fatal: the verdict is still a verdict. A cache that
			// cannot be written costs a boot next time and nothing else.
			fmt.Fprintf(opts.Stderr, "note: the verdict could not be cached: %v\n", err)
		}
	}
	fmt.Fprintf(opts.Stdout, "%s\n", summary(res))
	return res, nil
}

// rebaseTag names the throwaway image the two halves are assembled
// into.
//
// Deterministic in both halves, so that repeating a verify reuses
// docker's build cache rather than rebuilding a multi-gigabyte copy, and
// so that two verifies running at once on one host do not collide on a
// name that means something different to each of them. It is a function
// rather than four lines inline so that the removal of the image and a
// test asserting the removal name the same tag the build does.
func rebaseTag(image, granuleImage string) string {
	sum := sha256.Sum256([]byte(image + "\x00" + granuleImage))
	return fmt.Sprintf("granule-verify-rebase:%x", sum[:6])
}

// runCheck stages one check in the guest and runs it, capturing
// everything it printed.
func runCheck(ctx context.Context, s *guestboot.Session, c Check, timeout time.Duration) CheckResult {
	// One buffer for both streams: a check's output is read by a human
	// deciding what went wrong, and a diagnostic printed on stderr
	// between two lines of stdout belongs between them.
	var out bytes.Buffer
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	err := s.Script(cctx, c.shell(), checkPath, &out, &out)
	cr := CheckResult{Name: c.Name, Output: out.String(), Duration: time.Since(start)}
	switch {
	case err == nil:
		cr.Passed = true
	case cctx.Err() != nil && ctx.Err() == nil:
		cr.Err = fmt.Errorf("the check was still running after %s and was given up on", timeout)
	default:
		cr.Err = err
	}
	return cr
}

// report prints one check's verdict as it happens, so that a long run
// says what it is doing rather than going quiet for ten minutes.
func report(opts Options, cr CheckResult) {
	verdict := "ok  "
	if !cr.Passed {
		verdict = "FAIL"
	}
	fmt.Fprintf(opts.Stdout, "%s  %s (%s)\n", verdict, cr.Name, cr.Duration.Round(time.Millisecond))
	if cr.Passed && !opts.Verbose {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(cr.Output, "\n"), "\n") {
		if line == "" {
			continue
		}
		fmt.Fprintf(opts.Stdout, "      %s\n", line)
	}
	if cr.Err != nil {
		fmt.Fprintf(opts.Stdout, "      %v\n", cr.Err)
	}
}

// summary is the verdict in one line, naming the checks that failed --
// "failed" on its own is barely better than no verdict at all.
func summary(r Result) string {
	var failed []string
	for _, c := range r.Checks {
		if !c.Passed {
			failed = append(failed, c.Name)
		}
	}
	if len(failed) == 0 {
		return fmt.Sprintf("%d of %d checks passed: %s verified", len(r.Checks), len(r.Checks), r.Image)
	}
	return fmt.Sprintf("%d of %d checks failed: %s", len(failed), len(r.Checks), strings.Join(failed, "; "))
}

// short is a digest as it is printed: enough to tell two images apart,
// short enough to sit in a line of output.
func short(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
