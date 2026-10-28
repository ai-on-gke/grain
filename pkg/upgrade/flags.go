package upgrade

// The unit-flag check, which every cut-over path that can ask runs and
// none of them enforces (grain/task-233 for the image path,
// grain/task-244 for the Kubernetes one).
//
// The drift it catches is the same on both shapes, and it is drift by
// design. What starts grain is written down once, by whoever installed
// this deployment -- an ExecStart scripts/setup.sh wrote, or the `args:`
// of a Deployment an operator applied -- and an upgrade rewrites only
// which image goes with it. So the command line stays as the installer
// of the day left it while the build behind it moves with every upgrade
// and every rollback: a build that dropped a flag meets a command line
// that still passes it, and a rollback meets one written for something
// newer. That is what crash-looped this deployment 52 times on
// 2026-09-18, and under Kubernetes it would be a CrashLoopBackOff of the
// one replica instead, which with strategy Recreate is an outage.
//
// The daemon itself no longer dies of it (pkg/cli/daemon_unknown_flags.go
// ignores a flag it does not define and says so once), so what is left
// for this package to do is make sure nobody is surprised: ask the
// candidate build which flags it accepts, while the old one is still
// running, and name the ones that are about to start being ignored.
//
// Reported, never refused -- see checkImageFlags for the whole of that
// argument. The upgrade that *removes* a flag is exactly the one every
// already-installed deployment needs.

import (
	"errors"
	"fmt"
	"strings"
)

// flagsNotDefined returns the flags in have that out -- a build's answer
// to `grain daemon -flag-names` -- does not name, each with a single
// leading dash however it was written on the command line.
//
// The answer is one flag per line: the name, a space, and "bool" or
// "value" (pkg/cli/daemon_unknown_flags.go's printDaemonFlagNames).
// Anything that is not shaped like one of those lines is not counted as
// a flag, which matters more here than it looks. What a build too old to
// know the question prints is a usage error and then its whole flag
// dump, indented and with dashes and Go's own type names -- and on the
// Kubernetes path the answer arrives through `kubectl logs`, which merges
// that stderr into the same stream as the answer. Parsing it loosely
// would read those lines as a flag list and quietly report that
// everything is fine. A line this cannot make sense of is left out
// instead, and an answer with nothing left in it is no answer at all
// (the error below), which the step reports as "not checked".
//
// A bare name with nothing after it is still taken, so that a build
// predating the second column -- or a test standing in for one -- is read
// rather than discarded.
func flagsNotDefined(ref, out string, have []string) ([]string, error) {
	defined := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		name, kind, _ := strings.Cut(strings.TrimSpace(line), " ")
		if name == "" || strings.HasPrefix(name, "-") {
			continue
		}
		switch strings.TrimSpace(kind) {
		case "", "bool", "value":
			defined[name] = true
		}
	}
	// A build that named nothing at all has not answered the question,
	// whatever its exit status: reporting every flag the deployment
	// passes as missing would be a false alarm of exactly the kind that
	// teaches operators to ignore this step.
	if len(defined) == 0 {
		return nil, fmt.Errorf("%s named no daemon flags at all", ref)
	}
	var missing []string
	for _, flag := range have {
		if !defined[strings.TrimLeft(flag, "-")] {
			missing = append(missing, "-"+strings.TrimLeft(flag, "-"))
		}
	}
	return missing, nil
}

// reportFlags writes stepFlags's outcome from what the check found, and
// hands back the note worth carrying into the upgrade's own Detail --
// empty unless there is something an operator has to act on.
//
// fix is the one path-specific part: where the command line this
// deployment is running with is written down, and what to do to it. The
// rest is deliberately identical on both paths, since an operator
// reading the Upgrade pane should not have to learn two vocabularies for
// the same disagreement.
func reportFlags(p *progress, ref string, have []string, fix string, missing []string, err error) string {
	switch {
	case errors.Is(err, errCouldNotAsk):
		// The same "not checked" as below, said differently because it
		// is a fact about the machinery rather than about the build: the
		// image never got the chance to answer. Still not fatal -- see
		// checkImageFlags for why nothing here refuses an upgrade over a
		// check it could not run.
		p.done(stepFlags, fmt.Sprintf("not checked: %s was never asked which flags it accepts (%v)", ref, err))
	case err != nil:
		// The reason leads, because Step.Detail is capped
		// (maxStepDetail) and what an operator needs off the end of a
		// long command's error is "this was not checked".
		p.done(stepFlags, fmt.Sprintf("not checked: %s could not say which flags it accepts, "+
			"which a build older than the question cannot (%v)", ref, err))
	case len(missing) == 0:
		p.done(stepFlags, fmt.Sprintf("%s accepts all %d flags this deployment was started with",
			ref, len(have)))
	default:
		note := fmt.Sprintf("%s does not define %s, which this deployment is running with: "+
			"it will start and ignore them. %s", ref, strings.Join(missing, " "), fix)
		p.done(stepFlags, note)
		return note
	}
	return ""
}
