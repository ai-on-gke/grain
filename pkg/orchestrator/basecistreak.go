package orchestrator

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
)

// # Red on a moving tip (grain/task-1034)
//
// A red base branch is confirmed by a second, uncached run of the jobs
// that failed (startBaseCIConfirmation), and the confirmation is what
// wakes the pipeline steward. Both are about one commit, so neither
// survives the branch moving: a run that concludes red after the tip has
// moved on is about a tree that has gone, and collectBaseBranchCI used
// to drop it and judge the tip instead. On a busy branch that is every
// run. `bwsalmon/grain` main failed tests/frontend and tests/ui e2e on
// three consecutive tips on 2026-10-23, each dropped this way, and
// nothing was woken for any of them.
//
// So a superseded tip's verdict is kept, as a streak carried from row to
// row (model.BaseBranchCI.RedStreak). The same job failing on consecutive
// judged tips is itself a second, independent sample -- a different
// tree each time, and not a cached replay -- so once it has failed on
// model.Config.BaseCIRedStreakOr() tips in a row the branch is confirmed
// red at the newest of them, through confirmBaseBranchCI exactly as a
// confirmation re-run would have confirmed it.
//
// What counts:
//
//   - a job whose conclusion is a failure, whether it ran or was replayed
//     from grain CI's cache: a cached failure is the verdict grain CI
//     reports for that job on that tree, and nothing here second-guesses
//     it;
//   - a run that judged nothing (errored, cancelled, lost) neither
//     extends a streak nor ends one;
//   - a tip that passes the streak's jobs ends it -- a green run, or a
//     confirmation re-run that cleared the failure -- and one that fails
//     only other jobs starts a new streak on those;
//   - where consecutive tips fail overlapping sets, the streak goes on
//     about the jobs common to all of them, since those are the ones
//     with a run of reds to their name.

// noteBaseCIStreak folds the verdict collectBaseBranchCI has just
// recorded on row into its streak. run is the run that produced it.
func noteBaseCIStreak(row *model.BaseBranchCI, run ci.Run) {
	switch row.Conclusion {
	case string(ci.ConclusionSuccess):
		clearBaseCIStreak(row)
		row.LastGreenCommit = row.Commit
	case model.BaseBranchCIFailure:
		failed := baseCIStreakJobs(run)
		if len(failed) == 0 {
			// Red without a job to name -- a workflow that would not
			// parse, a run that failed before it had jobs. Nothing to
			// compare the next tip against, so nothing to carry.
			clearBaseCIStreak(row)
			return
		}
		if row.RedStreak > 0 && row.RedStreakTip == row.Commit {
			// The same commit asked again: one tip, however many runs.
			if common := intersectJobs(row.RedStreakJobs, failed); len(common) > 0 {
				row.RedStreakJobs = common
			}
			return
		}
		if common := intersectJobs(row.RedStreakJobs, failed); row.RedStreak > 0 && len(common) > 0 {
			row.RedStreak++
			row.RedStreakJobs = common
		} else {
			row.RedStreak, row.RedStreakJobs = 1, failed
			row.RedStreakSince, row.RedStreakWoken = row.Commit, ""
		}
		row.RedStreakTip = row.Commit
	}
}

// clearBaseCIStreak ends row's streak, keeping the last green tip.
func clearBaseCIStreak(row *model.BaseBranchCI) {
	row.RedStreak, row.RedStreakJobs = 0, nil
	row.RedStreakSince, row.RedStreakTip, row.RedStreakWoken = "", "", ""
}

// carryBaseCIStreak copies the streak from the row being replaced onto
// the fresh one a new run writes (startBaseBranchCI, refuseBaseBranchCI):
// the streak is the one thing on the row that is about more than the
// commit the row names.
func carryBaseCIStreak(fresh *model.BaseBranchCI, row model.BaseBranchCI) {
	fresh.RedStreak, fresh.RedStreakJobs = row.RedStreak, row.RedStreakJobs
	fresh.RedStreakSince, fresh.RedStreakTip = row.RedStreakSince, row.RedStreakTip
	fresh.LastGreenCommit, fresh.RedStreakWoken = row.LastGreenCommit, row.RedStreakWoken
}

// baseCIStreakJobs names the jobs of run that failed, cached replays
// included, in the words the rest of grain names them ("tests/frontend").
// Sorted, so two runs' sets compare the same whatever order they ran in.
func baseCIStreakJobs(run ci.Run) []string {
	var out []string
	for _, j := range run.Jobs {
		if j.Conclusion == ci.ConclusionFailure {
			out = append(out, ciJobName(j))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func intersectJobs(a, b []string) []string {
	var out []string
	for _, j := range a {
		if slices.Contains(b, j) {
			out = append(out, j)
		}
	}
	return out
}

// baseCIStreakConfirms reports whether row's streak is long enough to
// confirm the branch red at row.Commit: the newest tip of the streak is
// the commit the row is about, and it has run for at least n tips.
func baseCIStreakConfirms(row model.BaseBranchCI, n int) bool {
	return row.Broken() && row.RedStreak >= n && row.RedStreakTip == row.Commit
}

// baseCIStreakSummary is the streak in a phrase, for the row, the log and
// the steward: "tests/frontend red on 3 consecutive tips since b1e0068".
// Empty for a branch with no streak worth naming -- fewer than two tips,
// which is just a red.
func baseCIStreakSummary(row model.BaseBranchCI) string {
	if row.RedStreak < 2 || len(row.RedStreakJobs) == 0 {
		return ""
	}
	return fmt.Sprintf("%s red on %d consecutive tips since %s",
		strings.Join(row.RedStreakJobs, ", "), row.RedStreak, shortSHA(row.RedStreakSince))
}

// baseCIBisectRange is where a red stretch began, for whoever has to find
// the commit that started it: the last tip judged green and the first one
// judged red.
func baseCIBisectRange(row model.BaseBranchCI) string {
	if row.RedStreakSince == "" {
		return ""
	}
	green := "no green tip recorded before it"
	if row.LastGreenCommit != "" {
		green = "last green " + shortSHA(row.LastGreenCommit)
	}
	return fmt.Sprintf("%s, first red %s", green, shortSHA(row.RedStreakSince))
}
