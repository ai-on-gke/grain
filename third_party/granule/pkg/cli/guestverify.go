package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/guestverify"
)

// runGuestVerify boots a guest image, runs checks inside it, and prints a
// verdict. It produces no image: see pkg/guestverify for why that is
// the whole point of it being a verb of its own rather than a flag on
// "guest build".
func runGuestVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("granule guest verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	image := fs.String("image", "", "guest image to verify (required)")
	konturImage := fs.String("kontur-image", "", "image whose VMM half to boot the guest under -- the granule, cloud-hypervisor and entrypoint that will actually run it. Without it the image is booted as it stands, which verifies a machine nobody boots whenever the deployment supplies its own half")
	checksFile := fs.String("checks", "", "path to a check file: a [name] line, then the shell that tests it, repeated")
	var inline repeatedFlag
	fs.Var(&inline, "check", "one check as a shell `command`, named after itself (repeatable) -- the one-liner form of -checks")
	checkTimeout := fs.Duration("check-timeout", guestverify.DefaultCheckTimeout, "how long any one check may run before it is given up on")
	readyTimeout := fs.Duration("ready-timeout", 3*time.Minute, "how long to wait for the guest to become reachable and finish booting before running the checks")
	shutdownTimeout := fs.Duration("shutdown-timeout", 60*time.Second, "how long to wait for the guest to power off before giving up. Left alone, a machine that records a shutdown-timeout of its own (VM SET, docs/granulefile.md §5.5) is verified with that budget instead of this one -- which is the budget it will run with")
	cacheDir := fs.String("cache-dir", "", "where verdicts are cached (default: /var/lib/granule/verify as root, $XDG_CACHE_HOME/granule/verify otherwise)")
	noCache := fs.Bool("no-cache", false, "boot and check even if this image has already passed these checks, and record nothing")
	keep := fs.Bool("keep-on-failure", false, "leave the VM in place when a check fails, for inspection")
	verbose := fs.Bool("v", false, "print what every check printed, not only the ones that failed")
	var runArgs repeatedFlag
	fs.Var(&runArgs, "docker-run-arg", "extra argument for the underlying `docker run` (repeatable) -- how a checked guest reaches a proxy, a CA bundle or a private registry, none of which it inherits on its own")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *image == "" {
		fs.Usage()
		return fmt.Errorf("-image is required")
	}

	checks, err := collectChecks(*checksFile, inline)
	if err != nil {
		return err
	}

	// A flag's default is not the operator asking for it. Passed on only
	// when it was actually typed, so that an untouched -shutdown-timeout
	// leaves the machine's own recorded budget in place (guestboot falls
	// back to this same minute for a machine that records none) rather
	// than overriding it with a number nobody chose -- the way a command
	// line of granule's own used to hide the one a machine records.
	shutdown := time.Duration(0)
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "shutdown-timeout" {
			shutdown = *shutdownTimeout
		}
	})

	_, err = guestverify.Verify(ctx, guestverify.Options{
		Image:           *image,
		GranuleImage:    *konturImage,
		Checks:          checks,
		CheckTimeout:    *checkTimeout,
		CacheDir:        *cacheDir,
		NoCache:         *noCache,
		ExtraRunArgs:    runArgs,
		ReadyTimeout:    *readyTimeout,
		ShutdownTimeout: shutdown,
		KeepOnFailure:   *keep,
		Verbose:         *verbose,
		Stdout:          stdout,
		Stderr:          stderr,
	})
	if errors.Is(err, guestverify.ErrFailed) {
		// The checks ran and the image did not pass. Reported as the
		// verdict it is, and as a non-zero status, which is the whole of
		// what "a failure blocks rather than warns" comes to for a caller
		// that is a shell script: everything upstream of it branches on
		// this number.
		fmt.Fprintf(stderr, "granule: %v\n", err)
		return &exitStatusError{code: 1}
	}
	return err
}

// collectChecks assembles the check set from -checks and -check, in that
// order: a file of the checks an image is always held to, then whatever
// this particular invocation wanted to add.
func collectChecks(path string, inline []string) ([]guestverify.Check, error) {
	var checks []guestverify.Check
	if path != "" {
		// Read here rather than inside guestverify: a check file that is
		// not there should fail before anything boots, not after a VM has
		// come up and the caller has waited for it.
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading the check file: %w", err)
		}
		parsed, err := guestverify.ParseChecks(string(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		checks = parsed
	}
	for _, cmd := range inline {
		if strings.TrimSpace(cmd) == "" {
			return nil, fmt.Errorf("-check was given an empty command")
		}
		// Named after itself: a one-liner is its own best description,
		// and the verdict has to name something a reader recognizes.
		checks = append(checks, guestverify.Check{Name: cmd, Script: cmd})
	}
	if len(checks) == 0 {
		return nil, fmt.Errorf("no checks: pass -checks with a check file, or -check with a command (a verify with no checks would pass any image that booted)")
	}
	return checks, nil
}
