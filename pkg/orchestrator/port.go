package orchestrator

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/grainci"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/portpick"
)

// porter is who files a port: the merge queue, which is the one thing
// that sees every merge land (syncEntry's PrMerged arm).
var porter = model.Principal{Kind: model.PrincipalAutomation, ID: "merge-queue"}

// PortTag is the idempotency marker a port carries: one port of one
// source task onto one branch, ever. A port that ended on nothing_needed
// because the change was already there is not filed a second time by a
// retried close-out.
func PortTag(repo model.RepoRef, sourceTaskID, target string) string {
	return fmt.Sprintf("port:%s:%s:%s", repo, sourceTaskID, target)
}

var (
	// portOptOut is the `Port: no` footer: a line of its own in one of the
	// change's commit messages, read from the pull request's own commits
	// (portSkip) rather than from the description grain builds out of them.
	portOptOut = regexp.MustCompile(`(?mi)^[ \t]*Port:[ \t]*no[ \t]*$`)
	// portOptIn is the `Port: yes` footer: a change ported in a direction
	// whose port-types would otherwise leave it out.
	portOptIn = regexp.MustCompile(`(?mi)^[ \t]*Port:[ \t]*yes[ \t]*$`)
	// conventionalSubject is a conventional-commit summary line's type:
	// `fix: ...`, `fix(scope): ...`, `fix!: ...`, `fix(scope)!: ...`.
	conventionalSubject = regexp.MustCompile(`^[ \t]*([A-Za-z][A-Za-z0-9-]*)(?:\([^)]*\))?!?:`)
	// gitRevertSubject is the summary line `git revert` writes.
	gitRevertSubject = regexp.MustCompile(`^[ \t]*Revert "`)
	// portedFooter is what a change that is already a port says about
	// itself: the Backported-from footer port tasks are asked to write, or
	// the line `git cherry-pick -x` writes on its own.
	portedFooter = regexp.MustCompile(`(?mi)^[ \t]*(?:Backported-from:[ \t]*([0-9a-f]{7,40})|\(cherry picked from commit ([0-9a-f]{7,40})\))`)
	// revertedLine names the commit a revert undoes: the line `git revert`
	// writes, or the Reverts-Commit footer grain's own revert tool writes
	// (pkg/mcp/git_reverter.go).
	revertedLine = regexp.MustCompile(`(?mi)^[ \t]*(?:This reverts commit ([0-9a-f]{7,40})|Reverts-Commit:[ \t]*([0-9a-f]{7,40}))`)
)

// portDecision is where a merge is ported to, or why it is not.
type portDecision struct {
	targets []string
	// pending is, per target, the change's own commits it lacks
	// (lackedBy): what a mechanical port picks.
	pending map[string][]github.Commit
	skip    string
	// quiet is a skip not worth a log line: a repository, or a branch,
	// that pairs nothing, which is almost every merge.
	quiet bool
	// notes are the skips worth saying on the merged task itself
	// (notePortSkips), not only in the log: the ones that leave a
	// change off a paired branch on purpose, which somebody reading the
	// task later cannot otherwise tell from a port grain dropped.
	notes []portNote
}

// portNote is one paired branch a change was deliberately not ported to,
// and why.
type portNote struct {
	target, why string
}

// decidePort works out where the change task's pull request just landed
// should be ported to, if anywhere.
//
// Every refusal here is a way a port would be wrong rather than merely
// redundant:
//
//   - the repository pairs no branches, or not this one (PortTargets);
//   - the task said it is not to be ported (Task.NoPort), or the
//     repository's merge rules did for the paths it touches
//     (`port: false`, grain/task-1660);
//   - the task is itself a port (LinkPortOf) -- the rule that stops a
//     change bouncing back to the branch it came from;
//   - a port of this task onto that branch was already filed (PortTag);
//   - per target, the pull request's own commits that branch lacks
//     (portSkip): none, so the change is already there; one says
//     `Port: no`; every one says it is itself a port (Backported-from /
//     cherry picked from); or the direction ports only some
//     conventional-commit types (RepoConfig.PortTypeFilter, `port-types`)
//     and none of them is one, nor says `Port: yes`.
//
// The commits are GitHub's record of the pull request
// (ListPullRequestCommits), not its description: the description is a
// summary grain rewrites at every refresh and truncates, and it cannot
// say which commits the target already has.
//
// Patch-id equivalence -- the same change landed on both branches under
// different commits -- is not something GitHub's API answers, so it is
// the port task's own first step (portBody), which has the repository
// checked out.
func decidePort(ctx context.Context, store *model.Store, client github.Client, task model.Task,
	ref model.PullRequestRef, detail github.PullRequestDetail) (portDecision, error) {

	rc, err := store.GetRepoConfig(ctx, ref.Repo)
	if err != nil {
		return portDecision{}, err
	}
	var targets []string
	if rc != nil {
		targets = rc.PortTargets(detail.BaseRef)
	}
	if len(targets) == 0 {
		return portDecision{skip: "not a paired branch", quiet: true}, nil
	}
	// noteAll is a refusal of the whole change: every target is told
	// the same reason.
	noteAll := func(why string) portDecision {
		d := portDecision{skip: why}
		for _, target := range targets {
			d.notes = append(d.notes, portNote{target: target, why: why})
		}
		return d
	}
	if task.NoPort {
		return noteAll("the task is marked not to be ported"), nil
	}
	// The policy as recorded now: the cycle that merged this change may
	// have classified it after its caller read the task.
	ruled := task
	if fresh, err := store.GetTask(ctx, task.ID); err == nil && fresh != nil {
		ruled = *fresh
	}
	if why := portRefusal(ruled); why != "" {
		return noteAll(why), nil
	}
	if slices.ContainsFunc(task.Links, func(l model.Link) bool { return l.Kind == model.LinkPortOf }) {
		return portDecision{skip: "the task is itself a port"}, nil
	}
	// The pull request's own commits, as GitHub recorded them: what the
	// footers are read from, rather than the description grain builds
	// out of them.
	own, err := client.ListPullRequestCommits(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return portDecision{}, fmt.Errorf("listing %s's commits: %w", ref, err)
	}
	var out, skips []string
	var notes []portNote
	pending := map[string][]github.Commit{}
	for _, target := range targets {
		filed, err := store.HasTaskWithTag(ctx, PortTag(ref.Repo, task.ID, target))
		if err != nil {
			return portDecision{}, err
		}
		if filed {
			skips = append(skips, fmt.Sprintf("a port to `%s` was already filed", target))
			continue
		}
		why, noted, lacked := portSkip(client, ref, detail, target, own, rc.PortTypeFilter(detail.BaseRef, target))
		if why != "" {
			skips = append(skips, fmt.Sprintf("`%s`: %s", target, why))
			if noted {
				notes = append(notes, portNote{target: target, why: why})
			}
			continue
		}
		out = append(out, target)
		pending[target] = lacked
	}
	if len(out) == 0 {
		return portDecision{skip: strings.Join(skips, "; "), notes: notes}, nil
	}
	return portDecision{targets: out, pending: pending, notes: notes}, nil
}

// compareCap is how many commits GitHub's compare endpoint lists before
// it stops: a comparison that long may be missing the commit asked about.
const compareCap = 250

// portSkip is why the change should not be ported onto target, or "" if
// it should. It is decided on the change's own commits that target lacks
// (lackedBy), not on all of them: a head that merged target in carries
// target's own commits too, and a `Port: no` or a port footer on one of
// those says nothing about the change (grain/task-1869's pull request,
// 10-04, which merged candidate into a main change).
//
//   - none of them: the change is already on target, which is what a
//     branch cut from target looks like;
//   - any of them carries `Port: no`: the change opted out;
//   - every one of them carries a Backported-from / cherry-picked-from
//     footer naming a commit the merged-onto branch does not have: the
//     change is itself a port, and porting it would bounce it back. A
//     footer naming a commit that branch has is not a port onto it;
//   - types is set (the direction's port-types) and no commit that is not
//     itself a port is of one of those types or says `Port: yes`. One
//     that is ports the whole change, so a feature with a fix inside it is
//     ported, and a squash-merged pull request is decided by its one
//     commit. A revert counts as `revert` only if target may have what it
//     reverts (revertsAbsent): the revert of a feature the filter kept
//     off target has nothing there to undo.
//
// A pull request GitHub lists no commits for is ported, and the port
// task checks again with git.
//
// noted is whether the skip is worth a comment on the merged task
// (portDecision.notes): `Port: no` and the port-types filter, revert or
// not, leave the change off target on purpose, and say so; a change
// already on target, or itself a port, needs no saying -- the second
// would put a comment on every port.
//
// Alongside a port it should make, it returns the commits that port is
// of, for a mechanical attempt at it (filePorts).
func portSkip(client github.Client, ref model.PullRequestRef, detail github.PullRequestDetail,
	target string, own []github.Commit, types []string) (why string, noted bool, pending []github.Commit) {

	if len(own) == 0 {
		return "", false, nil
	}
	pending = lackedBy(client, ref, detail, target, own)
	if len(pending) == 0 {
		return "the change is already on it", false, nil
	}
	if slices.ContainsFunc(pending, func(c github.Commit) bool { return portOptOut.MatchString(c.Message) }) {
		return "the change carries a `Port: no` footer", true, nil
	}
	if !slices.ContainsFunc(pending, func(c github.Commit) bool { return !portedOnto(client, ref, detail.BaseRef, c) }) {
		return "the change is itself a port (Backported-from / cherry picked from)", false, nil
	}
	if len(types) > 0 {
		revertsOnlyAbsent := false
		if !slices.ContainsFunc(pending, func(c github.Commit) bool {
			if portOptIn.MatchString(c.Message) {
				return true
			}
			t := commitType(c.Message)
			if !slices.Contains(types, t) || portedOnto(client, ref, detail.BaseRef, c) {
				return false
			}
			if t == "revert" && revertsAbsent(client, ref, detail.BaseRef, target, c) {
				revertsOnlyAbsent = true
				return false
			}
			return true
		}) {
			if revertsOnlyAbsent {
				return fmt.Sprintf("a revert of a change `%s` does not have, and no `Port: yes` footer "+
					"(`%s` ports only %s changes to `%s`); there is nothing on `%s` to revert",
					target, detail.BaseRef, strings.Join(types, "/"), target, target), true, nil
			}
			return fmt.Sprintf("not a %s change and no `Port: yes` footer (`%s` ports only those types to `%s`); "+
				"it reaches `%s` some other way, such as the next release cut",
				strings.Join(types, "/"), detail.BaseRef, target, target), true, nil
		}
	}
	return "", false, pending
}

// revertsAbsent reports whether c is a revert of a change target is known
// not to have -- a feature the direction's port-types kept off target,
// say -- so that porting it would leave target nothing to revert. c names
// what it reverts in git's `This reverts commit <sha>.` line or grain's
// `Reverts-Commit: <sha>` footer; a revert of a merge commit reverts the
// commits that merge brought in.
//
// Target has the change when it contains a reverted commit, or carries a
// port of one (a Backported-from / cherry-picked-from footer naming it,
// among target's commits source lacks). Anything GitHub will not answer --
// a commit it cannot resolve, a comparison long enough to be cut off --
// reads as "target may have it" and the revert is ported, the answer that
// costs a port task that finds nothing to do rather than a revert lost
// from target. So does a revert naming no commit at all.
func revertsAbsent(client github.Client, ref model.PullRequestRef, source, target string, c github.Commit) bool {
	matches := revertedLine.FindAllStringSubmatch(c.Message, -1)
	if len(matches) == 0 {
		return false
	}
	var ported []github.Commit
	portedRead := false
	for _, m := range matches {
		sha := strings.ToLower(m[1] + m[2])
		change, ok := lackedChange(client, ref, target, sha)
		if !ok {
			return false
		}
		if !portedRead {
			commits, err := client.CompareCommits(ref.Repo.Owner, ref.Repo.Name, source, target)
			if err != nil || len(commits) >= compareCap {
				return false
			}
			ported, portedRead = commits, true
		}
		for _, p := range ported {
			for _, f := range portedFooter.FindAllStringSubmatch(p.Message, -1) {
				named := strings.ToLower(f[1] + f[2])
				if slices.ContainsFunc(change, func(s string) bool { return strings.HasPrefix(s, named) }) {
					return false
				}
			}
		}
	}
	return true
}

// lackedChange is the commits of the change sha is -- sha itself, or the
// commits a merge sha brought in -- with ok true only when GitHub says
// target has none of them.
func lackedChange(client github.Client, ref model.PullRequestRef, target, sha string) ([]string, bool) {
	missing, err := client.CompareCommits(ref.Repo.Owner, ref.Repo.Name, target, sha)
	if err != nil || len(missing) == 0 || len(missing) >= compareCap {
		return nil, false
	}
	i := slices.IndexFunc(missing, func(c github.Commit) bool { return strings.HasPrefix(c.SHA, sha) })
	if i < 0 {
		return nil, false
	}
	reverted := missing[i]
	if !reverted.Merge {
		return []string{reverted.SHA}, true
	}
	if len(reverted.Parents) < 2 {
		return nil, false
	}
	brought, err := client.CompareCommits(ref.Repo.Owner, ref.Repo.Name, reverted.Parents[0], reverted.Parents[1])
	if err != nil || len(brought) >= compareCap {
		return nil, false
	}
	var change []string
	for _, b := range brought {
		if b.Merge {
			continue
		}
		if !slices.ContainsFunc(missing, func(m github.Commit) bool { return m.SHA == b.SHA }) {
			return nil, false // target has one of them
		}
		change = append(change, b.SHA)
	}
	if len(change) == 0 {
		return nil, false
	}
	return change, true
}

// commitType is a commit message's conventional-commit type, lower-case
// -- `fix` for `fix(port)!: ...` -- with `git revert`'s own summary line
// read as `revert`, or "" for a summary line that has none.
func commitType(message string) string {
	subject, _, _ := strings.Cut(message, "\n")
	if gitRevertSubject.MatchString(subject) {
		return "revert"
	}
	if m := conventionalSubject.FindStringSubmatch(subject); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// lackedBy is own's commits that target does not have, merges left out:
// a merge says how the branch was put together, not what changed.
//
// One comparison of target with the head answers it for every commit at
// once. A comparison GitHub will not answer -- or one long enough to
// have been cut off -- is asked commit by commit instead (onBranch).
func lackedBy(client github.Client, ref model.PullRequestRef, detail github.PullRequestDetail,
	target string, own []github.Commit) []github.Commit {

	head := detail.HeadSHA
	if head == "" {
		head = detail.HeadRef
	}
	var lacking map[string]bool
	if head != "" {
		commits, err := client.CompareCommits(ref.Repo.Owner, ref.Repo.Name, target, head)
		if err == nil && len(commits) < compareCap {
			lacking = make(map[string]bool, len(commits))
			for _, c := range commits {
				lacking[c.SHA] = true
			}
		}
	}
	var out []github.Commit
	for _, c := range own {
		if c.Merge {
			continue
		}
		if lacking != nil && !lacking[c.SHA] {
			continue
		}
		if lacking == nil && onBranch(client, ref, target, c.SHA) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// portedOnto reports whether c says it is a port onto branch: a
// Backported-from or cherry-picked-from footer naming a commit branch
// does not otherwise contain. A footer naming a commit branch already has
// is the other paired branch's port of it, not a port onto this one.
func portedOnto(client github.Client, ref model.PullRequestRef, branch string, c github.Commit) bool {
	for _, m := range portedFooter.FindAllStringSubmatch(c.Message, -1) {
		if !onBranch(client, ref, branch, m[1]+m[2]) {
			return true
		}
	}
	return false
}

// onBranch reports whether branch contains the commit sha. A comparison
// GitHub will not answer -- a commit since deleted, an abbreviated sha it
// does not resolve -- reads as "no", which is the answer that keeps a
// footer counting as a port and a commit counting as one to port.
func onBranch(client github.Client, ref model.PullRequestRef, branch, sha string) bool {
	commits, err := client.CompareCommits(ref.Repo.Owner, ref.Repo.Name, branch, sha)
	return err == nil && len(commits) == 0
}

// filePorts files the port tasks a merge of task's pull request calls
// for -- one per other paired branch (RepoConfig.PortTargets) -- and
// links each to task both ways (LinkPortOf / LinkPortedBy).
//
// Ports are filed approved: they carry onto another branch work a human
// already approved, under the same team, capabilities, reads and writes,
// and they land the ordinary way, through the merge queue and grain CI.
// They inherit the source's AutoMerge, so a change somebody wanted to
// read before it merged is read again before its port does.
//
// # Mechanically first (grain/task-2116)
//
// Most ports are a clean cherry-pick, and a sandbox and a model run spent
// on one is waste. So where picker is set, grain first makes the port
// itself (portMechanically, pkg/portpick) and only asks an agent when that
// does not work out:
//
//   - every commit picked cleanly: the port task is filed already owning
//     its pull request, opened the way an agent's would be
//     (fileMechanicalPort), and goes to the merge queue with no agent run.
//     If its grain CI goes red the merge queue sends it to an agent the way
//     it sends every red change, on that same branch (requeueForRepair);
//   - nothing left to port (already there by patch-id or by footer):
//     no task, a note on the source;
//   - a conflict, or anything that stopped the attempt: today's agent port
//     task, told what the attempt found.
//
// Its caller logs a failure rather than holding the close-out on it: the
// merge has happened either way, and a port this misses is left for a
// person (or a repository's own triage) to catch.
func filePorts(ctx context.Context, store *model.Store, client github.Client, picker Porter, task model.Task,
	ref model.PullRequestRef, detail github.PullRequestDetail, now time.Time) error {

	// No automatic port into a repository whose ports this deployment
	// does not run (model.PortsItem, grain/task-3332): a person can still
	// file one by hand.
	if runs, err := runsStandingWork(ctx, store, ref.Repo, model.PortsItem); err != nil {
		return err
	} else if !runs {
		log.Printf("orchestrator: not porting %s (task %s): %s", ref, task.ID, model.StandingWorkOffReason(ref.Repo))
		return nil
	}
	decision, err := decidePort(ctx, store, client, task, ref, detail)
	if err != nil {
		return fmt.Errorf("orchestrator: deciding whether to port %s: %w", ref, err)
	}
	if err := notePortSkips(ctx, store, task, ref, detail, decision.notes, now); err != nil {
		log.Printf("orchestrator: noting why %s (task %s) was not ported: %v", ref, task.ID, err)
	}
	if len(decision.targets) == 0 {
		if !decision.quiet {
			log.Printf("orchestrator: not porting %s (task %s): %s", ref, task.ID, decision.skip)
		}
		return nil
	}
	var ported []model.Link
	for _, target := range decision.targets {
		port, err := newPortTask(ctx, store, task, ref, detail, target, now)
		if err != nil {
			return err
		}
		mech := portMechanically(ctx, store, picker, task, ref, detail, target, &port, decision.pending[target])
		if mech.outcome == portpick.NothingToPort {
			if _, err := store.AddComment(ctx, model.Comment{
				TaskID: task.ID,
				Author: model.Attribution{Actor: porter},
				Body: fmt.Sprintf("Merged onto `%s`, which `%s` keeps paired with `%s`, but there is nothing to port: "+
					"grain checked with git and %s.", detail.BaseRef, ref.Repo, target, mech.result.Summary(target)),
				CreatedAt: now,
			}); err != nil {
				log.Printf("orchestrator: noting the port of %s on %s: %v", ref, task.ID, err)
			}
			continue
		}
		how := "filed %s to port this change there."
		if mech.outcome == portpick.Picked {
			how = "%s ported this change there by cherry-picking it, with no agent run, and its pull request is in the merge queue."
			if err := fileMechanicalPort(ctx, store, client, port, now); err != nil {
				return err
			}
		} else if err := store.PutTask(ctx, port); err != nil {
			return fmt.Errorf("orchestrator: filing the port of %s to %s: %w", ref, target, err)
		}
		ported = append(ported, model.Link{Kind: model.LinkPortedBy, Target: port.ID})
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID: task.ID,
			Author: model.Attribution{Actor: porter},
			Body: fmt.Sprintf("Merged onto `%s`, which `%s` keeps paired with `%s`: "+how,
				detail.BaseRef, ref.Repo, target, port.ID),
			CreatedAt: now,
		}); err != nil {
			log.Printf("orchestrator: noting the port of %s on %s: %v", ref, task.ID, err)
		}
		log.Printf("orchestrator: filed %s to port %s (task %s) from %s to %s (%s)",
			port.ID, ref, task.ID, detail.BaseRef, target, mech.kind())
	}
	if len(ported) == 0 {
		return nil
	}
	// The source's half of the link. Re-read first: the row syncEntry
	// read may be older than the one stored.
	current, err := store.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("orchestrator: re-reading %s to link its ports: %w", task.ID, err)
	}
	if current == nil {
		return nil
	}
	for _, l := range ported {
		if !slices.Contains(current.Links, l) {
			current.Links = append(current.Links, l)
		}
	}
	return store.PutTask(ctx, *current)
}

// notePortSkips says on the merged task why it was deliberately not
// ported to each target in notes (portDecision.notes), which the log
// line alone left to whoever could read the daemon's journal: a `feat:`
// on main the port-types filter left for the next cut reads, without it,
// exactly like a port grain dropped.
//
// Once per merge: the comment names the pull request, and one already
// on the task saying the same thing is not said again, so a close-out
// that runs filePorts a second time -- its closing write failed, say --
// does not repeat it.
func notePortSkips(ctx context.Context, store *model.Store, task model.Task, ref model.PullRequestRef,
	detail github.PullRequestDetail, notes []portNote, now time.Time) error {

	if len(notes) == 0 {
		return nil
	}
	said, err := store.Comments(ctx, task.ID)
	if err != nil {
		return err
	}
	for _, n := range notes {
		body := portSkipComment(ref, detail.BaseRef, n)
		if slices.ContainsFunc(said, func(c model.Comment) bool {
			return c.Author.Actor == porter && c.Body == body
		}) {
			continue
		}
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID:    task.ID,
			Author:    model.Attribution{Actor: porter},
			Body:      body,
			CreatedAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// portSkipComment is notePortSkips' comment for one target.
func portSkipComment(ref model.PullRequestRef, base string, n portNote) string {
	return fmt.Sprintf("Merged %s onto `%s`, which `%s` keeps paired with `%s`, but grain did not port it there: %s.",
		ref, base, ref.Repo, n.target, n.why)
}

// Porter makes a port mechanically: pkg/portpick's *Porter in a
// deployment, which runs git on grain's own host.
type Porter interface {
	Port(ctx context.Context, req portpick.Request) (portpick.Result, error)
}

// mechanicalPort is what portMechanically's attempt came to. A zero
// outcome is an attempt not made (no porter, nothing GitHub listed) or
// one that failed (err): both fall back to an agent.
type mechanicalPort struct {
	outcome portpick.Outcome
	result  portpick.Result
	err     error
}

func (m mechanicalPort) kind() string {
	switch {
	case m.outcome == portpick.Picked:
		return "mechanically"
	case m.outcome == portpick.Conflicted:
		return "by agent, after a conflicting cherry-pick"
	case m.err != nil:
		return "by agent, after a failed cherry-pick"
	}
	return "by agent"
}

// portMechanically tries port's cherry-pick before it is filed, and
// rewrites port to say what came of it: a mechanical port's tag, branch
// and body, or the agent port's body told what the attempt found.
//
// Every attempt, whatever it came to, is logged with what it did: this
// is grain itself pushing to a repository, and the log is its audit
// trail beside the comments it leaves on both tasks.
func portMechanically(ctx context.Context, store *model.Store, picker Porter, source model.Task,
	ref model.PullRequestRef, detail github.PullRequestDetail, target string, port *model.Task,
	pending []github.Commit) mechanicalPort {

	if picker == nil || len(pending) == 0 || detail.HeadSHA == "" {
		return mechanicalPort{}
	}
	var m mechanicalPort
	defer func() {
		switch {
		case m.err != nil:
			log.Printf("orchestrator: mechanical port of %s to %s (task %s) did not run to the end: %v",
				ref, target, port.ID, m.err)
		default:
			log.Printf("orchestrator: mechanical port of %s to %s (task %s, branch %s): %s",
				ref, target, port.ID, port.BranchName(), m.result.Summary(target))
		}
	}()
	branch, err := store.BranchForNewTask(ctx, port.ID, port.Title)
	if err != nil {
		m.err = err
		return m
	}
	port.Branch = branch
	commits := make([]string, 0, len(pending))
	for _, c := range pending {
		commits = append(commits, c.SHA)
	}
	res, err := picker.Port(ctx, portpick.Request{
		Repo:           ref.Repo,
		Source:         detail.BaseRef,
		Target:         target,
		PullRequest:    ref.Number,
		Head:           detail.HeadSHA,
		Commits:        commits,
		Branch:         branch,
		CredentialTask: source.ID,
	})
	m.result, m.err = res, err
	switch {
	case err != nil:
		port.Body = portBody(source, ref, detail, target, fmt.Sprintf(
			"grain tried this port as a plain cherry-pick first and could not finish it (%v); nothing was pushed "+
				"to `%s`. Port it as above.", err, branch))
	case res.Outcome == portpick.Picked:
		m.outcome = res.Outcome
		port.Tags = append(port.Tags, model.TagMechanicalPort)
		port.Body = portBody(source, ref, detail, target, fmt.Sprintf(
			"grain has already made this port itself, with no agent: it %s, pushed the result as `%s`, and opened "+
				"the pull request from it. If you are reading this, the merge queue has sent that pull request back "+
				"to an agent: work on `%s` as it is, do not start the port over.",
			res.Summary(target), branch, branch))
	case res.Outcome == portpick.Conflicted:
		m.outcome = res.Outcome
		port.Body = portBody(source, ref, detail, target, fmt.Sprintf(
			"grain tried this port as a plain cherry-pick first and stopped on a conflict: %s. Nothing was pushed. "+
				"Resolve that conflict in step 3.", res.Summary(target)))
	default:
		m.outcome = res.Outcome
	}
	return m
}

// fileMechanicalPort files port, whose branch portMechanically has
// already pushed, as a task that owns its pull request from the start:
// opened, linked and marked completed by the same finish path an agent's
// run ends on (finishWithPullRequest), so the merge queue, grain CI, the
// trust-boundary and path-trigger holds, the origin label and the
// lineage all treat it exactly as they treat an agent-made port.
//
// It is stored unapproved until that is done, which is what keeps the
// dispatcher from handing it to an agent in between. If the pull request
// cannot be opened, it is approved anyway and becomes an ordinary agent
// port, whose body already says the branch holds the picks.
func fileMechanicalPort(ctx context.Context, store *model.Store, client github.Client, port model.Task, now time.Time) error {
	approval, approvedAt := port.Approval, port.ApprovedAt
	port.Approval, port.ApprovedAt = nil, nil
	if err := store.PutTask(ctx, port); err != nil {
		return fmt.Errorf("orchestrator: filing the mechanical port %s: %w", port.ID, err)
	}
	finishErr := finishWithPullRequest(ctx, store, client, port, now)
	if err := store.UpdateTask(ctx, port.ID, func(t *model.Task) error {
		t.Approval, t.ApprovedAt = approval, approvedAt
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: approving the port %s: %w", port.ID, err)
	}
	if finishErr != nil {
		log.Printf("orchestrator: mechanical port %s: %v -- left to an agent", port.ID, finishErr)
		if err := noteOnTask(ctx, store, port.ID, fmt.Sprintf(
			"grain pushed this port to `%s` but could not open its pull request (%v), so it is left to an agent.",
			port.BranchName(), finishErr), now); err != nil {
			log.Printf("orchestrator: %v", err)
		}
	}
	return nil
}

func newPortTask(ctx context.Context, store *model.Store, source model.Task, ref model.PullRequestRef,
	detail github.PullRequestDetail, target string, now time.Time) (model.Task, error) {

	id, err := store.NewTaskID(ctx)
	if err != nil {
		return model.Task{}, fmt.Errorf("orchestrator: allocating an id for the port of %s: %w", ref, err)
	}
	orderKey, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return model.Task{}, fmt.Errorf("orchestrator: allocating order key for the port of %s: %w", ref, err)
	}
	repo := ref.Repo
	grants := slices.Clone(source.Grants)
	if !grainci.Granted(grants) {
		grants = append(grants, model.Grant{Capability: grainci.CapabilityName, Via: model.GrantByGrain})
	}
	title := source.Title
	if title == "" {
		title = detail.Title
	}
	port := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  fmt.Sprintf("Port %s to %s: %s", ref, target, title),
		Body:   portBody(source, ref, detail, target, ""),
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: porter},
			Reason:      model.ReasonDirect,
		},
		Approval:       &model.Attribution{Actor: porter},
		ApprovedAt:     &now,
		Target:         &repo,
		Binding:        model.BindingDirective,
		Base:           target,
		AutoMerge:      source.AutoMerge,
		Tags:           []string{PortTag(repo, source.ID, target)},
		Links:          []model.Link{{Kind: model.LinkPortOf, Target: source.ID}},
		OrderKey:       orderKey,
		Reads:          slices.Clone(source.Reads),
		Writes:         model.WritesBesides(source.Writes, repo, target),
		Grants:         grants,
		CustomFields:   source.CustomFields,
		NetworkAccess:  source.NetworkAccess,
		AgentFramework: source.AgentFramework,
		CreatedAt:      &now,
	}
	// The source's requester, not porter's approval, decides whether
	// local policy runs it: a port of an external request needs an
	// approval of its own.
	model.InheritRequester(&port, source)
	return port, nil
}

// portBody is what the agent porting a change is told: which pull
// request, from which branch, onto which, and the steps that make the
// result recognisable as a port to everything that reads it afterwards
// (decidePort's footers, the nightly triage's `git cherry`).
//
// note, when there is one, is what grain's own mechanical attempt at the
// port found (portMechanically).
func portBody(source model.Task, ref model.PullRequestRef, detail github.PullRequestDetail, target, note string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s's pull request %s (%q) merged onto `%s`. `%s` keeps `%s` and `%s` paired, "+
		"so the same change belongs on `%s` too. Port it there.\n\n",
		source.ID, ref, detail.Title, detail.BaseRef, ref.Repo, detail.BaseRef, target, target)
	fmt.Fprintf(&b, "The pull request's head was `%s` at `%s`.\n\n", detail.HeadRef, detail.HeadSHA)
	b.WriteString("Do this:\n\n")
	fmt.Fprintf(&b, "1. `git fetch origin` and find the merge that landed it: "+
		"`git log --first-parent --merges --format='%%H %%s' origin/%s | grep -F 'pull request #%d '`.\n", detail.BaseRef, ref.Number)
	// The limit argument (<merge>^1) is what keeps this to the pull
	// request's own commits: without it `git cherry` also lists every
	// commit of the source branch the target lacks, and a rev-list of
	// <merge>^1..<merge>^2 also lists commits a head that merged the
	// target in already shares with it, which cherry-pick refuses as
	// empty.
	fmt.Fprintf(&b, "2. `git cherry -v origin/%s <merge>^2 <merge>^1` lists the pull request's own commits that `%s` "+
		"does not contain, oldest first. Port the ones marked `+`; a `-` is already on `%s` under another commit "+
		"(same patch-id). Commits `%s` already contains are not listed at all, and nor are merges. Also drop a `+` "+
		"commit that `git log origin/%s --grep '<sha>'` finds a `Backported-from:` or `cherry picked from` footer "+
		"naming. If nothing is left, end with nothing_needed and say so.\n", target, target, target, target, target)
	fmt.Fprintf(&b, "3. On your branch (based on `%s`), `git cherry-pick -x` each remaining commit in order, and add a "+
		"`Backported-from: <original sha>` footer to each message. Resolve conflicts so the change does on `%s` "+
		"what it did on `%s`, and say in the commit message what you had to adapt. If a commit cannot stand on `%s` "+
		"without an earlier change `%s` does not have, do not merge `%s` into your branch or `%s` to get it -- that "+
		"carries every unported commit across with it. Port just the change, adapted; if it cannot be adapted, stop "+
		"and ask_question, naming the commit it depends on.\n",
		target, target, detail.BaseRef, target, target, detail.BaseRef, target)
	b.WriteString("4. Push, get grain CI green, and let the merge queue land it like any other change.\n\n")
	if note != "" {
		b.WriteString(note + "\n\n")
	}
	b.WriteString("This task is itself a port, so its own merge is not ported back.\n")
	if body := strings.TrimSpace(source.Body); body != "" {
		b.WriteString("\n---\n\nThe original task, for context:\n\n")
		for _, line := range strings.Split(body, "\n") {
			b.WriteString(strings.TrimRight("> "+line, " ") + "\n")
		}
	}
	return b.String()
}
