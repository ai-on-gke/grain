package forge

// Where a commit's checks are read from, when it is not GitHub.
//
// The point of this seam is one number: grain polls GitHub for every
// pull request it is watching, every thirty seconds, and almost every
// one of those reads returns what the last one did. A source that
// already knows how the build ended -- because the build told it
// (pkg/checkpost) -- answers the same question without spending a
// request against anybody's rate limit.
//
// # Completeness is the whole of the safety argument
//
// A reporting service holds verdicts for checks that have *finished*.
// Three jobs into a twelve-job build it holds three, all green, and a
// reader that treated that as the answer would merge a change nine
// checks had not looked at. There is no way to tell that apart from a
// finished build with three checks by counting, so counting is not what
// this does.
//
// Instead a Verdicts source answers a second question -- is this the
// whole answer -- and grain acts on the checks only when it is. Anything
// else falls through to GitHub, which is what grain did before this
// existed. The rule is therefore that a source can only ever *save* a
// GitHub read; it can never be the reason grain believes something it
// would not otherwise have believed.
//
// That is deliberately conservative in the direction that costs
// requests rather than the one that costs correctness. A build in
// flight is polled from GitHub exactly as it always was -- nine minutes
// of it, on this deployment -- and the hours a pull request then spends
// waiting for its turn in the queue, which is where the volume actually
// is, are answered from the source.
//
// # And an error is silence, not an answer
//
// A source that is unreachable, unconfigured or wrong falls through to
// GitHub too. Nothing here may take a merge queue down: the failure mode
// of the whole mechanism is "grain goes back to polling", which is
// merely expensive.

import "github.com/bwsalmon/grain/pkg/github"

// Verdicts is somewhere other than the forge that knows how a commit's
// checks ended.
//
// For answers three things, and the middle one is what makes it safe to
// use at all:
//
//   - the checks, shaped as GitHub's own, so that everything downstream
//     -- HealthFrom, FailingChecks, the prose a repair task carries --
//     judges them by exactly one code path;
//   - whether that is the *whole* answer for this commit. False is not
//     an error and not an empty repository; it is "ask the forge";
//   - an error, which the caller treats the same as false.
//
// It is deliberately not given the ref's head branch, only the commit: a
// verdict is about a tree, and a source that resolved a branch would be
// answering about whatever that branch points at now rather than about
// the commit the caller is deciding on.
type Verdicts interface {
	For(ref Ref, headSHA string) (checks []github.CheckRun, complete bool, err error)
}
