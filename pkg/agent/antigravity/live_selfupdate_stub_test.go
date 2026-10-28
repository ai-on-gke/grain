package antigravity

// The measurement selfupdate.go rests on: agy replaces its own binary
// during an ordinary dispatch, and AGY_CLI_DISABLE_AUTO_UPDATE=true is
// what stops it.
//
// This is the probe that decides whether the Dockerfile's pin means
// anything. That pin fetches an immutable artifact and checks its
// sha256, docs/agy-surface.md records what that release says about
// itself, and every capability claim in this package was measured
// against it -- all of which a binary that rewrites itself on first exec
// quietly turns into claims about a release the deployment has stopped
// running. selfupdate_test.go asserts that grain asks for the updater to
// be off in all three places it starts agy; nothing there says agy
// listens, and nothing but a real binary can.
//
// It needs an agy and no credential, like its two neighbours, and it
// needs egress to Google's updater -- which the control half establishes
// rather than assumes, because a probe that cannot reach the updater
// proves nothing by finding the binary unchanged.
//
// By hand, wherever an agy binary is available:
//
//	GRAIN_LIVE_AGENT_TEST=1 go test ./pkg/agent/antigravity/ \
//	  -run TestLiveAgyDoesNotReplaceItsOwnBinary -count=1 -v
//
// docs/hand-runs/2026-09-17-agy-self-update.md is the run this was
// written from, against agy 1.1.27.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// How long the control exec is given to replace its own copy, and how
// much longer than the control took the pinned exec's copy then has to
// stay put. The update lands out of band -- a background download that
// outlives the exec -- so both halves are polled rather than read once:
// on 1.1.27 the replacement appeared between 8 and 30 seconds after the
// exec returned.
const (
	selfUpdateControlWindow = 3 * time.Minute
	selfUpdatePatienceRatio = 3
	selfUpdateMinPatience   = 60 * time.Second
	selfUpdatePollInterval  = 2 * time.Second
)

// TestLiveAgyDoesNotReplaceItsOwnBinary runs the same dispatch twice, on
// two writable copies of the installed agy, and watches the files.
//
// The control comes first and deliberately drops the switch: it is what
// says this agy, today, on this network, does replace itself -- and
// therefore what gives the second half its meaning. A control that stays
// put is not a pass, it is a probe with nothing to measure (a release
// that stopped self-updating, an egress hole, a manifest already naming
// the installed version), so it skips and says which.
//
// Copies rather than the installed binary because the images unpack agy
// as root and the pin holds *there* by permission as well -- the case
// this test is about is the one permissions do not cover: agy running as
// root, or from a writable GRAIN_AGY_PATH in somebody's home directory.
func TestLiveAgyDoesNotReplaceItsOwnBinary(t *testing.T) {
	installed := liveAgyPath(t)

	control := writableAgyCopy(t, installed)
	controlBefore := fileDigest(t, control)
	runOneDispatch(t, control, true)
	elapsed, updated := waitForBinaryChange(t, control, controlBefore, selfUpdateControlWindow)
	if !updated {
		skipUnlessLiveRequired(t, fmt.Sprintf("agy %s left its own binary alone through a dispatch with "+
			"AGY_CLI_DISABLE_AUTO_UPDATE unset, after %s: either this release no longer self-updates, or "+
			"its updater is unreachable from here, and either way there is nothing here for "+
			"AGY_CLI_DISABLE_AUTO_UPDATE to prevent", agyVersion(t), selfUpdateControlWindow))
	}
	t.Logf("with the updater enabled, agy %s replaced its own binary %s after the exec returned: %s",
		agyVersion(t), elapsed.Round(time.Second), describeBinary(t, control, controlBefore))

	patience := elapsed * selfUpdatePatienceRatio
	if patience < selfUpdateMinPatience {
		patience = selfUpdateMinPatience
	}

	pinned := writableAgyCopy(t, installed)
	pinnedBefore := fileDigest(t, pinned)
	runOneDispatch(t, pinned, false)
	if _, updated := waitForBinaryChange(t, pinned, pinnedBefore, patience); updated {
		t.Fatalf("AGY_CLI_DISABLE_AUTO_UPDATE=true did not stop agy %s replacing its own binary "+
			"(%s).\nThe Dockerfile's pinned release, docs/agy-surface.md's captures and every "+
			"measurement in this package are then statements about a binary this deployment no longer "+
			"runs. Find what this release calls the switch (selfupdate.go records that the value is "+
			"compared as a string, so a bare 1 has never worked), or take the pin out and say so.",
			agyVersion(t), describeBinary(t, pinned, pinnedBefore))
	}
	t.Logf("with AGY_CLI_DISABLE_AUTO_UPDATE=true, the same dispatch left the binary alone for %s: %s",
		patience.Round(time.Second), describeBinary(t, pinned, pinnedBefore))
}

// runOneDispatch execs a copy of agy once the way Framework.Run does --
// the production argv, the private HOME, the turn on stdin, the stub
// provider answering it -- which is the shape the update was first seen
// on. A bare `agy --version` does not update; `agy models` does, even
// when it refuses the listing (docs/hand-runs/2026-09-17-agy-self-update.md).
//
// A failed exec is not fatal here. What is being measured is what the
// process did to its own file, and a refused turn has still run the
// binary; the two halves are compared on the same terms either way.
func runOneDispatch(t *testing.T, agyPath string, allowSelfUpdate bool) {
	t.Helper()
	p := newAgyProbe(t, agyPath)
	p.allowSelfUpdate = allowSelfUpdate
	run := p.exec(t, p.sandboxA, "", "GRAIN-AGY-SELF-UPDATE-PROBE: reply with nothing but the word ok.")
	if run.err != nil {
		t.Logf("the exec failed (%v); the file is still the measurement.\nstdout:\n%s\nstderr:\n%s",
			run.err, run.stdout, run.stderr)
	}
}

// writableAgyCopy copies the installed binary somewhere this test process
// owns it. ~200MB of copy per call, which is why there are two.
func writableAgyCopy(t *testing.T, agyPath string) string {
	t.Helper()
	// Named "antigravity", the name the release archive carries and the
	// name the symlink on $PATH points at: the updater rewrites the file
	// it is running, and nothing here should depend on that, but a probe
	// should not be the one place agy is called something else.
	dst := filepath.Join(t.TempDir(), "antigravity")
	data, err := os.ReadFile(agyPath)
	if err != nil {
		t.Fatalf("reading %s: %v", agyPath, err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("writing a writable copy of agy: %v", err)
	}
	return dst
}

// binaryState is a file read the way the Dockerfile's pin and
// docs/agy-surface.md's header describe one: its size and its digest.
type binaryState struct {
	size   int64
	digest string
}

func fileDigest(t *testing.T, path string) binaryState {
	t.Helper()
	state, err := statDigest(path)
	if err != nil {
		t.Fatalf("reading %s before the exec: %v", path, err)
	}
	return state
}

// waitForBinaryChange polls until the file is not the one it was, and
// returns how long after the exec returned that took. It polls because
// the replacement is a background download that outlives the process
// that started it: reading the file once, immediately, is how this went
// unnoticed for as long as it did.
//
// A file that is briefly unreadable or mid-write is not a change: the
// digest is compared, so a partial write is only reported once it has
// settled into something different from what was there.
func waitForBinaryChange(t *testing.T, path string, before binaryState, window time.Duration) (time.Duration, bool) {
	t.Helper()
	start := time.Now()
	for {
		if now, err := statDigest(path); err == nil && now != before {
			return time.Since(start), true
		}
		if time.Since(start) >= window {
			return time.Since(start), false
		}
		time.Sleep(selfUpdatePollInterval)
	}
}

// statDigest is fileDigest without a *testing.T: the poll above must
// tolerate a file that is being rewritten underneath it rather than fail
// the test on the first unreadable read.
func statDigest(path string) (binaryState, error) {
	f, err := os.Open(path)
	if err != nil {
		return binaryState{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return binaryState{}, err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return binaryState{}, err
	}
	return binaryState{size: info.Size(), digest: hex.EncodeToString(sum.Sum(nil))}, nil
}

// describeBinary is the before-and-after both messages above want: the
// two sizes and the two digests, in the form docs/agy-surface.md records
// them, so a reader can compare a failure with the pin in the Dockerfile
// without running anything.
func describeBinary(t *testing.T, path string, before binaryState) string {
	t.Helper()
	now, err := statDigest(path)
	if err != nil {
		return fmt.Sprintf("%d bytes, sha256 %s -> unreadable (%v)", before.size, before.digest, err)
	}
	if now == before {
		return fmt.Sprintf("%d bytes, sha256 %s, unchanged", before.size, before.digest)
	}
	return fmt.Sprintf("%d bytes, sha256 %s -> %d bytes, sha256 %s",
		before.size, before.digest, now.size, now.digest)
}
