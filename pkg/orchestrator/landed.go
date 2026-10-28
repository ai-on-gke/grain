package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// Work that already landed (gke-labs/granule task 147).
//
// A branch can carry commits its base does not have and still have
// nothing left to propose: a human squash-merged its pull request by
// hand, which gives every commit on the base a different SHA, and GitHub
// goes on reading the branch as "ahead" for ever after. grain used to
// read it the same way. Task 147's run opened a second pull request for
// a branch whose first had been squash-merged minutes earlier, and a
// merge-queue repair later resolved that second one to an empty diff
// and merged it as a no-op.
//
// So "is there anything to open (or merge)?" is asked by content here,
// never by commit: for every path the branch changes against its fork
// point, does the base already hold that same blob (or that same
// deletion)? That is exactly "the tree of merge(base, branch) equals the
// base tree" -- a path only the branch changed takes the branch's blob
// in the merge, and that is the base's own blob precisely when the base
// already has it -- and it is a question two of GitHub's compares can
// answer without a clone: base...branch for what the branch changes, and
// branch...base for what the base changed since the two forked, each
// with the blob it leaves behind (github.Comparison.Files).

// landedWork is what that check found about one branch against one base.
type landedWork struct {
	Base   string
	Branch string
	// Known is false when the check could not be made -- a compare that
	// failed, a file list GitHub truncated, a branch with no commits
	// over its base at all (which is noCommitsError's case, not this
	// one's). Nothing is ever refused on an unknown answer.
	Known bool
	// Landed and Pending are the paths the branch changes that the base
	// already has, and those it does not, each sorted.
	Landed  []string
	Pending []string
	// BaseCommits is what the base gained since the branch forked,
	// oldest first: where the landed part must have arrived.
	BaseCommits []github.Commit
	// MergedPR is a pull request of this task's own that already merged,
	// and MergedSHA the commit it left on the base, if GitHub said.
	MergedPR  *model.PullRequestRef
	MergedSHA string
	// MergedHead is true when that merged pull request's head was this
	// branch's current tip -- nothing has been pushed since it merged.
	MergedHead bool
}

// all reports whether nothing the branch changes is missing from the
// base: everything landed, or the diff was empty to begin with.
//
// An empty diff is not enough on its own: a branch that never changed
// anything has one too, and that is noCommitsError's business (or a
// harmless empty change), not work that landed. So it also takes
// evidence that there was work -- paths whose content the base already
// has, or a merged pull request of the task's own.
func (w landedWork) all() bool {
	return w.MergedHead || (w.Known && len(w.Pending) == 0 && (len(w.Landed) > 0 || w.MergedPR != nil))
}

// partial reports whether some of the branch landed and some did not.
func (w landedWork) partial() bool {
	return w.Known && len(w.Landed) > 0 && len(w.Pending) > 0
}

// where names the base commit(s) the work landed in, as a phrase that
// follows "already landed on <base>": " as abc1234 (owner/repo#69)", or
// the commits the base gained since the fork when no merged pull request
// of this task's own is known.
func (w landedWork) where() string {
	if w.MergedPR != nil {
		if w.MergedSHA != "" {
			return fmt.Sprintf(" as %s (%s)", shortSHA(w.MergedSHA), w.MergedPR)
		}
		return fmt.Sprintf(" through %s", w.MergedPR)
	}
	switch n := len(w.BaseCommits); {
	case n == 0:
		return ""
	case n <= 5:
		names := make([]string, 0, n)
		for i := n - 1; i >= 0; i-- {
			c := w.BaseCommits[i]
			names = append(names, fmt.Sprintf("%s (%s)", shortSHA(c.SHA), subjectOf(c.Message)))
		}
		if n == 1 {
			return " as " + names[0]
		}
		return " in one of " + strings.Join(names, ", ")
	default:
		return fmt.Sprintf(" in one of the %d commits it gained since this branch forked (newest %s)",
			n, shortSHA(w.BaseCommits[n-1].SHA))
	}
}

// landedSentence is the whole finding for a branch that landed in full.
func (w landedWork) landedSentence() string {
	return fmt.Sprintf("This branch's changes already landed on %s%s; nothing to open. "+
		"Every file %s changes already has the same content on %s, so a pull request "+
		"for it would merge nothing.", w.Base, w.where(), w.Branch, w.Base)
}

// partialSentence is what a pull request opened for a partly landed
// branch says about the part that is already there.
func (w landedWork) partialSentence() string {
	return fmt.Sprintf("Part of this branch already landed on %s%s: %s already %s the same "+
		"content there. What is left to review is %s.",
		w.Base, w.where(), pathList(w.Landed), hasOrHave(len(w.Landed)), pathList(w.Pending))
}

func hasOrHave(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

// pathList names up to ten paths, in backticks.
func pathList(paths []string) string {
	const most = 10
	quoted := make([]string, 0, most)
	for i, p := range paths {
		if i == most {
			break
		}
		quoted = append(quoted, "`"+p+"`")
	}
	out := strings.Join(quoted, ", ")
	if len(paths) > most {
		out += fmt.Sprintf(" and %d more", len(paths)-most)
	}
	return out
}

func subjectOf(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// fileEffects is a compare's file list as "what the tree holds at each
// path afterwards": the blob SHA, or "" for a path removed. A rename is
// its new path's blob plus its old path's removal. A path whose blob
// GitHub did not name gets a value no other entry can equal, so it is
// never taken for landed.
func fileEffects(files []github.ComparedFile) map[string]string {
	out := make(map[string]string, len(files))
	for _, f := range files {
		switch f.Status {
		case "removed":
			out[f.Filename] = ""
			continue
		case "renamed":
			if f.PreviousFilename != "" {
				out[f.PreviousFilename] = ""
			}
		}
		if f.SHA == "" {
			out[f.Filename] = "\x00unknown:" + f.Filename
			continue
		}
		out[f.Filename] = f.SHA
	}
	return out
}

// workOnBase makes the content check for branch against base in repo.
// Failures are an unknown answer, never an error: the check exists to
// stop a duplicate, and a check that cannot be made must not stop a
// pull request that is real.
func workOnBase(client github.Client, repo model.RepoRef, base, branch string) landedWork {
	w := landedWork{Base: base, Branch: branch}
	ahead, err := client.CompareRefs(repo.Owner, repo.Name, base, branch)
	if err != nil || len(ahead.Commits) == 0 || ahead.FilesTruncated {
		return w
	}
	mine := fileEffects(ahead.Files)
	if len(mine) == 0 {
		// Commits, and no diff. Either they never changed anything, or
		// the base was merged into the branch after the base had already
		// gained the same changes -- what a repair or a refresh of a
		// branch whose work was squash-merged elsewhere leaves behind.
		// The second is told apart by what the branch changed *before*
		// the last of those merges: if that was something, it is
		// something the base now holds.
		w.Known = true
		if behind, err := client.CompareRefs(repo.Owner, repo.Name, branch, base); err == nil {
			w.BaseCommits = behind.Commits
		}
		for i := len(ahead.Commits) - 1; i >= 0; i-- {
			c := ahead.Commits[i]
			if !c.Merge || len(c.Parents) == 0 {
				continue
			}
			before, err := client.CompareRefs(repo.Owner, repo.Name, base, c.Parents[0])
			if err != nil || before.FilesTruncated {
				w.Known = false
				return w
			}
			for path := range fileEffects(before.Files) {
				w.Landed = append(w.Landed, path)
			}
			// Where it landed: what the base gained since the branch first
			// forked from it, not since that merge (which is nothing).
			if since, err := client.CompareRefs(repo.Owner, repo.Name, c.Parents[0], base); err == nil {
				w.BaseCommits = since.Commits
			}
			if len(w.Landed) > 0 {
				sort.Strings(w.Landed)
				break
			}
		}
		return w
	}
	if ahead.Status == "ahead" && !hasMergeCommit(ahead.Commits) {
		for path := range mine {
			w.Pending = append(w.Pending, path)
		}
		sort.Strings(w.Pending)
		w.Known = true
		return w
	}
	behind, err := client.CompareRefs(repo.Owner, repo.Name, branch, base)
	if err != nil {
		return w
	}
	theirs := fileEffects(behind.Files)
	for path, blob := range mine {
		if got, ok := theirs[path]; ok && got == blob {
			w.Landed = append(w.Landed, path)
		} else {
			w.Pending = append(w.Pending, path)
		}
	}
	if len(w.Pending) > 0 && allPortedCommitsOnBase(ahead.Commits, behind.Commits) {
		w.Landed = append(w.Landed, w.Pending...)
		w.Pending = nil
	}
	if behind.FilesTruncated && len(w.Pending) > 0 {
		return landedWork{Base: base, Branch: branch}
	}
	sort.Strings(w.Landed)
	sort.Strings(w.Pending)
	w.Known = true
	w.BaseCommits = behind.Commits
	return w
}

func hasMergeCommit(commits []github.Commit) bool {
	for _, c := range commits {
		if c.Merge {
			return true
		}
	}
	return false
}

// allPortedCommitsOnBase reports whether ahead contains at least one
// non-merge commit and every non-merge commit in ahead is a port
// (Backported-from or cherry-picked-from) whose source commit is already
// in behind (the commits base gained since the branch forked, or ported
// there by another commit in behind).
func allPortedCommitsOnBase(ahead, behind []github.Commit) bool {
	nonMerge := 0
	for _, c := range ahead {
		if c.Merge {
			continue
		}
		nonMerge++
		matches := portedFooter.FindAllStringSubmatch(c.Message, -1)
		if len(matches) == 0 {
			return false
		}
		for _, m := range matches {
			sha := strings.ToLower(m[1] + m[2])
			if !commitOrPortIn(behind, sha) {
				return false
			}
		}
	}
	return nonMerge > 0
}

func commitOrPortIn(commits []github.Commit, sha string) bool {
	if sha == "" {
		return false
	}
	for _, c := range commits {
		cSHA := strings.ToLower(c.SHA)
		if cSHA != "" && (strings.HasPrefix(cSHA, sha) || strings.HasPrefix(sha, cSHA)) {
			return true
		}
		for _, m := range portedFooter.FindAllStringSubmatch(c.Message, -1) {
			named := strings.ToLower(m[1] + m[2])
			if named != "" && (strings.HasPrefix(named, sha) || strings.HasPrefix(sha, named)) {
				return true
			}
		}
	}
	return false
}

// portedCommitsOnBase reports whether every non-merge commit head carries
// over base is a port (a Backported-from or cherry-picked-from footer)
// of a commit base itself now contains (onBranch), and there is at least
// one such commit. It returns the source commits the footers name, in
// the order they were found. The merge commits a refresh or a re-arm
// added are skipped: they bring base's own content, not the port's.
//
// allPortedCommitsOnBase asks the same of the commits base gained since
// the fork; this asks base itself, which is what still answers once the
// fork point has moved past where the source landed -- a re-arm that
// merged base in before the paired branches were synced (grain/task-189).
func portedCommitsOnBase(client github.Client, ref model.PullRequestRef, base, head string) ([]string, bool) {
	ahead, err := client.CompareCommits(ref.Repo.Owner, ref.Repo.Name, base, head)
	if err != nil || len(ahead) == 0 {
		return nil, false
	}
	var sources []string
	seen := map[string]bool{}
	for _, c := range ahead {
		if c.Merge {
			continue
		}
		matches := portedFooter.FindAllStringSubmatch(c.Message, -1)
		if len(matches) == 0 {
			return nil, false
		}
		for _, m := range matches {
			sha := strings.ToLower(m[1] + m[2])
			if seen[sha] {
				continue
			}
			if !onBranch(client, ref, base, sha) {
				return nil, false
			}
			seen[sha] = true
			sources = append(sources, sha)
		}
	}
	return sources, len(sources) > 0
}

func isPortTask(task model.Task) bool {
	for _, l := range task.Links {
		if l.Kind == model.LinkPortOf {
			return true
		}
	}
	return false
}

// mergedPortSourceOnBase checks whether task is a port (LinkPortOf) whose
// source task's merged pull request has already landed on base.
func mergedPortSourceOnBase(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, skip model.PullRequestRef, base string, baseCommits []github.Commit) (
	*model.PullRequestRef, github.PullRequestDetail, bool) {

	if store == nil {
		return nil, github.PullRequestDetail{}, false
	}
	for _, l := range task.Links {
		if l.Kind != model.LinkPortOf || l.Target == "" {
			continue
		}
		source, err := store.GetTask(ctx, l.Target)
		if err != nil || source == nil {
			continue
		}
		merged, mdetail, ok := mergedPullRequestOf(client, *source, &skip)
		if !ok {
			continue
		}
		if mdetail.MergeCommitSHA != "" &&
			(commitOrPortIn(baseCommits, strings.ToLower(mdetail.MergeCommitSHA)) ||
				onBranch(client, *merged, base, mdetail.MergeCommitSHA)) {
			return merged, mdetail, true
		}
		if mdetail.HeadSHA != "" &&
			(commitOrPortIn(baseCommits, strings.ToLower(mdetail.HeadSHA)) ||
				onBranch(client, *merged, base, mdetail.HeadSHA)) {
			return merged, mdetail, true
		}
		if own, err := client.ListPullRequestCommits(merged.Repo.Owner, merged.Repo.Name, merged.Number); err == nil &&
			len(own) > 0 && len(lackedBy(client, *merged, mdetail, base, own)) == 0 {
			return merged, mdetail, true
		}
	}
	return nil, github.PullRequestDetail{}, false
}

// mergedPullRequestOf finds a pull request linked to task that already
// merged, other than skip, and the detail GitHub gave for it.
func mergedPullRequestOf(client github.Client, task model.Task, skip *model.PullRequestRef) (
	*model.PullRequestRef, github.PullRequestDetail, bool) {

	for _, l := range task.Links {
		if l.Kind != model.LinkFixes {
			continue
		}
		ref, err := model.ParsePullRequestRef(l.Target)
		if err != nil || (skip != nil && ref == *skip) {
			continue
		}
		detail, err := client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
		if err != nil || !detail.Merged {
			continue
		}
		return &ref, detail, true
	}
	return nil, github.PullRequestDetail{}, false
}

// landedForTask is workOnBase for task's own branch, with the task's own
// merged pull request (if it has one) folded in -- both to name where the
// work landed, and as the first of the two checks: a merged pull request
// whose head is still the branch's tip is the same work, whatever the
// content check could or could not read.
func landedForTask(client github.Client, task model.Task, base string, skip *model.PullRequestRef) landedWork {
	branch := task.BranchName()
	w := workOnBase(client, *task.Target, base, branch)
	if ref, detail, ok := mergedPullRequestOf(client, task, skip); ok {
		w.MergedPR, w.MergedSHA = ref, detail.MergeCommitSHA
		if detail.HeadSHA != "" {
			if head, err := client.GetBranchHead(task.Target.Owner, task.Target.Name, branch); err == nil &&
				head != nil && head.SHA == detail.HeadSHA {
				w.MergedHead = true
			}
		}
	}
	return w
}

// alreadyLandedError is a branch with nothing left to propose: every
// change it makes is already on its base. It is the reason no pull
// request was opened, and its words are what the run is told.
type alreadyLandedError struct {
	Work landedWork
}

func (e *alreadyLandedError) Error() string { return e.Work.landedSentence() }

// NothingToPropose marks this as an answer about the branch rather than a
// failure of anything: the daemon's open_pull_request route reports it as
// 409 Conflict (pkg/ui's handleOpenPullRequest) instead of the 500 an
// agent would read as grain itself having broken.
func (e *alreadyLandedError) NothingToPropose() bool { return true }

// alreadyLanded reports whether err is that, anywhere in its chain.
func alreadyLanded(err error) (*alreadyLandedError, bool) {
	var e *alreadyLandedError
	ok := errors.As(err, &e)
	return e, ok
}

// noteOnTask leaves body on taskID as grain's own comment.
func noteOnTask(ctx context.Context, store *model.Store, taskID, body string, now time.Time) error {
	_, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      body,
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: noting on %s: %w", taskID, err)
	}
	return nil
}

// finishAlreadyLanded is the finish path's answer to a branch that
// already landed: no pull request, the finding on the task, and the task
// done -- completed and closed, since there is nothing left for anybody
// to merge or review.
func finishAlreadyLanded(ctx context.Context, store *model.Store, task model.Task,
	landed *alreadyLandedError, now time.Time) error {

	log.Printf("orchestrator: task %s: %s", task.ID, landed.Error())
	if err := noteOnTask(ctx, store, task.ID, landed.Error()+
		"\n\ngrain has marked this task done rather than opening a duplicate pull request.", now); err != nil {
		return err
	}
	return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.CompletedAt = &now
		o.ClosedAt = &now
		o.RetryRequestedAt = nil
	})
}

// closeIfLanded is the merge queue's half: an open pull request whose
// every change is already on its base -- what a repair or a base-merge
// refresh leaves behind when the work landed some other way -- is closed
// with a comment naming where the work landed, rather than merged as an
// empty merge commit. It reports whether it closed it; the caller records
// the close on the task (recordPullRequestEvents), which is what marks
// the task done.
func closeIfLanded(ctx context.Context, store *model.Store, client github.Client, task model.Task,
	ref model.PullRequestRef, detail github.PullRequestDetail, now time.Time) (bool, error) {

	if detail.State != "open" || detail.Merged || detail.HeadRef == "" || detail.BaseRef == "" {
		return false, nil
	}
	w := workOnBase(client, ref.Repo, detail.BaseRef, detail.HeadRef)
	if w.Known {
		if merged, mdetail, ok := mergedPortSourceOnBase(ctx, store, client, task, ref, detail.BaseRef, w.BaseCommits); ok {
			w.MergedPR, w.MergedSHA = merged, mdetail.MergeCommitSHA
			if len(w.Pending) > 0 {
				w.Landed = append(w.Landed, w.Pending...)
				w.Pending = nil
				sort.Strings(w.Landed)
			}
			if len(w.Landed) == 0 {
				w.MergedHead = true
			}
		}
	}
	// A port whose every own commit is a cherry-pick of a commit the base
	// now has: the paired branches were synced (an RC cut) after the port
	// was opened, and GitHub's three-dot diff goes on listing the port's
	// files until the base is merged into its branch -- or for good, where
	// the base changed those files again since. Judged by commit, not by
	// content, which is what the port's own footers say it is
	// (grain/task-189).
	var ported []string
	if (!w.Known || len(w.Pending) != 0) && isPortTask(task) {
		if srcs, ok := portedCommitsOnBase(client, ref, detail.BaseRef, detail.HeadRef); ok {
			ported = srcs
			w.Known = true
			w.Landed = append(w.Landed, w.Pending...)
			w.Pending = nil
			sort.Strings(w.Landed)
		}
	}
	if !w.Known || len(w.Pending) != 0 {
		return false, nil
	}
	if w.MergedPR == nil {
		if merged, mdetail, ok := mergedPullRequestOf(client, task, &ref); ok {
			w.MergedPR, w.MergedSHA = merged, mdetail.MergeCommitSHA
		}
	}
	if !w.all() && len(ported) == 0 {
		return false, nil
	}
	body := fmt.Sprintf("grain is closing %s instead of merging it: its diff against `%s` is empty. "+
		"Everything this branch changes already landed on %s%s, so merging it would only add an "+
		"empty merge commit.\n\nTask %s is marked done.", ref, w.Base, w.Base, w.where(), task.ID)
	if len(ported) > 0 {
		short := make([]string, len(ported))
		for i, sha := range ported {
			short[i] = shortSHA(sha)
		}
		body = fmt.Sprintf("grain is closing %s instead of merging it: it is a port, and every commit it "+
			"carries is a cherry-pick of a commit `%s` already has (%s)%s. Merging it would only add "+
			"the same change a second time.\n\nTask %s is marked done.", ref, w.Base,
			strings.Join(short, ", "), w.where(), task.ID)
	}
	if _, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, body); err != nil {
		log.Printf("orchestrator: could not say why %s is being closed: %v", ref, err)
	}
	if err := client.ClosePullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number); err != nil {
		return false, fmt.Errorf("orchestrator: closing %s, whose work already landed: %w", ref, err)
	}
	if err := noteOnTask(ctx, store, task.ID, body, now); err != nil {
		return true, err
	}
	return true, nil
}

// ReattachPullRequest re-links task to the pull request still open from
// its own branch, if there is one -- what reopening a closed task does,
// and what every run of it does as it starts, so that the close-time
// note's promise ("reopening task N is what puts it back under grain's
// watch") is true and a run continues on that pull request rather than
// opening a second one beside it.
//
// It returns the pull request it found, or nil. Finding one is said on
// the task and on the pull request when the link is new, or when
// reopened says this is a reopen -- the moment the close-time note
// pointed at -- and is otherwise silent, since a run starting on a task
// that is already attached has nothing to announce.
func ReattachPullRequest(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, reopened bool, now time.Time) (*model.PullRequestRef, error) {

	if task.Target == nil || client == nil {
		return nil, nil
	}
	if _, inPlace := reviewsInPlace(task); inPlace {
		// The pull request open off a review's branch, when it works on
		// the reviewed change's own (reviewsInPlace), is that change's:
		// linked here, it would be the auto-merging review's to merge,
		// around the very hold the review is there to settle.
		return nil, nil
	}
	pr, err := client.FindOpenPullRequestForBranch(task.Target.Owner, task.Target.Name, task.BranchName())
	if err != nil {
		return nil, fmt.Errorf("orchestrator: looking for %s's open pull request: %w", task.ID, err)
	}
	if pr == nil {
		return nil, nil
	}
	ref := model.PullRequestRef{Repo: *task.Target, Number: pr.Number}
	linked := false
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes && l.Target == ref.String() {
			linked = true
		}
	}
	if err := linkPullRequest(ctx, store, task.ID, ref); err != nil {
		return nil, err
	}
	if linked && !reopened {
		return &ref, nil
	}
	body := fmt.Sprintf("Task %s's branch `%s` still has %s open, so grain has attached it to the "+
		"task again and is watching it: the next run continues on it, and it merges through "+
		"the task as usual.", task.ID, task.BranchName(), ref)
	if _, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, body); err != nil {
		log.Printf("orchestrator: could not say on %s that task %s took it back: %v", ref, task.ID, err)
	}
	return &ref, noteOnTask(ctx, store, task.ID, body, now)
}
