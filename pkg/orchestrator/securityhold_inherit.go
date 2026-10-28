package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// A clearance is about one commit (LinkTrustBoundaryCleared), and a new
// head is classified again before it merges. Read naively that costs a
// review per head, and two kinds of change paid it for nothing (task
// 2935, three reviews in a row of code already reviewed once):
//
//   - a port (LinkPortOf) of a change that was cleared on the branch it
//     came from, re-reviewed on the paired branch;
//   - a change whose clearance a later commit moved past without touching
//     a single trust-boundary file -- a CI repair, a formatting fix.
//
// Both are carried here rather than reviewed again, on one test: every
// trust-boundary file the new head's diff touches, and every one the
// cleared diff touched, carries the same patch in both, compared the way
// git patch-id compares them (line numbers and blob ids ignored). What a
// review read is then exactly what would merge on the trust boundary; the
// rest of the change is off the list, which is where no review was ever
// asked for.
//
// Everything else fails closed into an ordinary hold: a list or rules
// identity that differs from the cleared one, a path only a human or an
// owner may release, a diff GitHub would not serve or that holds a binary
// or a path it quotes, a source with a hold still on it or a review that
// pushed, and a client that cannot serve raw diffs at all.

// trustBoundaryDiffer is the part of the GitHub client a carried clearance
// reads. Not part of github.Client (github.DiffReader's own doc comment),
// so it is asserted for, and a client without it carries nothing.
type trustBoundaryDiffer = github.DiffReader

// carriedClearance is a clearance a new head keeps without a review.
type carriedClearance struct {
	// source is the port's source task, or nil when the clearance is
	// task's own, carried from the head it was given for.
	source *model.Task
	// sourcePR and review are, for a port, the source's pull request and
	// the security review that cleared it ("" if none had to).
	sourcePR model.PullRequestRef
	review   string
}

// carryClearance reports whether task's new head, headSHA, may keep a
// clearance without another review, and from where. Called by
// holdForTrustBoundary with a file list it could read in full that
// touches the trust boundary.
func carryClearance(ctx context.Context, store *model.Store, client github.Client, current model.Task,
	ref model.PullRequestRef, headSHA string, matches []trustboundary.Match, rules model.RepoRules) (carriedClearance, bool) {

	if headSHA == "" || rules.Broken != "" || current.Target == nil {
		return carriedClearance{}, false
	}
	for _, m := range matches {
		// Neither is a review's to give, so neither is one to carry: an
		// owner's approval and a human's Submit are about the head they
		// read.
		if m.Category.RequiresHuman || m.Category.OwnerApproval {
			return carriedClearance{}, false
		}
	}
	version := strings.TrimSuffix(clearedTarget("", rules), "@")
	recordedVersion, recordedSHA, hasRecorded := strings.Cut(linkTarget(current, model.LinkTrustBoundaryCleared), "@")
	if hasRecorded && recordedVersion == version && recordedSHA == headSHA &&
		!hasSecurityHold(current) && !hasBrokenRulesHold(current) {
		return carriedClearance{}, true
	}
	differ, ok := client.(trustBoundaryDiffer)
	if !ok {
		return carriedClearance{}, false
	}
	base := current.Base
	if base == "" {
		detail, err := client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
		if err != nil || detail.BaseRef == "" {
			return carriedClearance{}, false
		}
		base = detail.BaseRef
	}
	diff, err := differ.CompareDiff(ref.Repo.Owner, ref.Repo.Name, base, headSHA)
	if err != nil {
		log.Printf("orchestrator: reading %s's diff at %s to compare with what was cleared: %v", ref, shortSHA(headSHA), err)
		return carriedClearance{}, false
	}
	patches, ok := filePatches(diff)
	if !ok {
		return carriedClearance{}, false
	}

	if carried, ok := inheritedClearance(ctx, store, client, differ, current, version, patches, matches); ok {
		return carried, true
	}

	// Its own clearance, given for an earlier head: one a review (or
	// nothing on the list) gave, still standing -- findings remove it. A
	// human-only hold records one too, but no head touching a path only a
	// human may release gets this far (the loop above).
	if !hasRecorded || recordedVersion != version || recordedSHA == "" || recordedSHA == headSHA || hasBrokenRulesHold(current) {
		return carriedClearance{}, false
	}
	before, err := differ.CompareDiff(ref.Repo.Owner, ref.Repo.Name, base, recordedSHA)
	if err != nil {
		log.Printf("orchestrator: reading %s's diff at its cleared head %s: %v", ref, shortSHA(recordedSHA), err)
		return carriedClearance{}, false
	}
	cleared, ok := filePatches(before)
	if !ok {
		return carriedClearance{}, false
	}
	if sameTrustBoundaryPatches(ctx, store, *current.Target, cleared, patches, matches) ||
		reviewFixClearance(ctx, store, client, differ, current, ref, recordedSHA, headSHA, cleared, patches, matches) {
		return carriedClearance{}, true
	}
	return carriedClearance{}, false
}

// inheritedClearance is a port's clearance carried from its source: the
// change it ports, cleared under the same list and rules at the head its
// pull request ended on, with no hold left on it and no review of its own
// commits standing in for one.
func inheritedClearance(ctx context.Context, store *model.Store, client github.Client, differ trustBoundaryDiffer,
	current model.Task, version string, patches map[string]string, matches []trustboundary.Match) (carriedClearance, bool) {

	sourceID := linkTarget(current, model.LinkPortOf)
	if sourceID == "" {
		return carriedClearance{}, false
	}
	source, err := store.GetTask(ctx, sourceID)
	if err != nil || source == nil {
		if err != nil {
			log.Printf("orchestrator: reading %s, the source of port %s: %v", sourceID, current.ID, err)
		}
		return carriedClearance{}, false
	}
	if hasSecurityHold(*source) || hasBrokenRulesHold(*source) {
		// Never released by a review: what merged, merged on a human's
		// say-so, which says nothing about this branch.
		return carriedClearance{}, false
	}
	recordedVersion, sourceSHA, ok := strings.Cut(linkTarget(*source, model.LinkTrustBoundaryCleared), "@")
	if !ok || recordedVersion != version || sourceSHA == "" {
		return carriedClearance{}, false
	}
	review := linkTarget(*source, model.LinkSecurityReviewTask)
	if review != "" {
		r, err := store.GetTask(ctx, review)
		if err != nil || r == nil || hasPullRequest(*r) {
			return carriedClearance{}, false
		}
		obs, err := store.GetObservation(ctx, review)
		if err != nil || obs == nil || obs.CompletedAt == nil {
			return carriedClearance{}, false
		}
	}
	sourcePR := model.TaskPullRequest(*source)
	if sourcePR == nil {
		return carriedClearance{}, false
	}
	// The pull request's own diff is of its last head, so it is only the
	// cleared diff if that head is the cleared one.
	detail, err := client.GetPullRequest(sourcePR.Repo.Owner, sourcePR.Repo.Name, sourcePR.Number)
	if err != nil || detail.HeadSHA != sourceSHA {
		if err != nil {
			log.Printf("orchestrator: reading %s, the source of port %s: %v", sourcePR, current.ID, err)
		}
		return carriedClearance{}, false
	}
	diff, err := differ.PullRequestDiff(sourcePR.Repo.Owner, sourcePR.Repo.Name, sourcePR.Number)
	if err != nil {
		log.Printf("orchestrator: reading %s's diff, the source of port %s: %v", sourcePR, current.ID, err)
		return carriedClearance{}, false
	}
	cleared, ok := filePatches(diff)
	if !ok || !sameTrustBoundaryPatches(ctx, store, *current.Target, cleared, patches, matches) {
		return carriedClearance{}, false
	}
	return carriedClearance{source: source, sourcePR: *sourcePR, review: review}, true
}

// sameTrustBoundaryPatches reports whether every trust-boundary path in
// either diff -- matches, for the new one; the list and repo's own rules
// applied to the cleared one's paths -- carries the same patch in both.
// A path one of them leaves alone is a difference: a port that drops part
// of what was reviewed is not what was reviewed.
func sameTrustBoundaryPatches(ctx context.Context, store *model.Store, repo model.RepoRef,
	cleared, next map[string]string, matches []trustboundary.Match) bool {

	paths := map[string]bool{}
	for _, m := range matches {
		for _, p := range m.Paths {
			paths[p] = true
		}
	}
	clearedPaths := make([]string, 0, len(cleared))
	for p := range cleared {
		clearedPaths = append(clearedPaths, p)
	}
	clearedMatches := trustboundary.Classify(clearedPaths)
	rules, guarded := repoGuard(ctx, store, repo, clearedPaths, true)
	if rules.Broken != "" {
		return false
	}
	for _, m := range append(clearedMatches, guarded...) {
		for _, p := range m.Paths {
			paths[p] = true
		}
	}
	if len(paths) == 0 {
		return false
	}
	for p := range paths {
		a, okA := cleared[p]
		b, okB := next[p]
		if !okA || !okB || a != b {
			return false
		}
	}
	return true
}

// reviewFixClearance reports whether the only trust-boundary changes between
// recordedSHA and headSHA came from the security review's own cleanly merged
// stacked pull request (without introducing any RequiresHuman or OwnerApproval
// category).
func reviewFixClearance(ctx context.Context, store *model.Store, client github.Client, differ trustBoundaryDiffer,
	current model.Task, ref model.PullRequestRef, recordedSHA, headSHA string,
	cleared, next map[string]string, matches []trustboundary.Match) bool {

	reviewID := linkTarget(current, model.LinkSecurityReviewTask)
	if reviewID == "" {
		return false
	}
	if _, hasQuestions := model.ReviewQuestionsOf(current); hasQuestions {
		return false
	}
	review, err := store.GetTask(ctx, reviewID)
	if err != nil || review == nil {
		return false
	}
	obs, err := store.GetObservation(ctx, reviewID)
	if err != nil {
		return false
	}
	reviewPR := model.TaskPullRequest(*review)
	if reviewPR == nil {
		return false
	}
	pushed, landed, ok := reviewOutcome(*review, obs)
	if pushed && !landed && !ok && obs != nil && obs.CompletedAt != nil {
		if detail, err := client.GetPullRequest(reviewPR.Repo.Owner, reviewPR.Repo.Name, reviewPR.Number); err == nil && detail.Merged {
			landed, ok = true, true
		}
	}
	if !pushed || !landed || !ok {
		return false
	}
	reviewDiff, err := differ.PullRequestDiff(reviewPR.Repo.Owner, reviewPR.Repo.Name, reviewPR.Number)
	if err != nil || strings.TrimSpace(reviewDiff) == "" {
		reviewDiff, err = differ.CompareDiff(ref.Repo.Owner, ref.Repo.Name, recordedSHA, review.BranchName())
		if err != nil {
			return false
		}
	}
	reviewPatches, ok := filePatches(reviewDiff)
	if !ok || len(reviewPatches) == 0 {
		return false
	}
	deltaDiff, err := differ.CompareDiff(ref.Repo.Owner, ref.Repo.Name, recordedSHA, headSHA)
	if err != nil {
		return false
	}
	deltaPatches, ok := filePatches(deltaDiff)
	if !ok || len(deltaPatches) == 0 {
		return false
	}

	deltaMatches, ok := classifyPatchPaths(ctx, store, *current.Target, deltaPatches)
	if !ok {
		return false
	}
	reviewMatches, ok := classifyPatchPaths(ctx, store, *current.Target, reviewPatches)
	if !ok {
		return false
	}

	tbDelta := map[string]bool{}
	for _, m := range append(deltaMatches, reviewMatches...) {
		if m.Category.RequiresHuman || m.Category.OwnerApproval {
			return false
		}
		for _, p := range m.Paths {
			tbDelta[p] = true
		}
	}
	if len(tbDelta) == 0 {
		return false
	}
	for p := range tbDelta {
		dp, okD := deltaPatches[p]
		rp, okR := reviewPatches[p]
		if !okD || !okR || dp != rp {
			return false
		}
	}

	clearedMatches, ok := classifyPatchPaths(ctx, store, *current.Target, cleared)
	if !ok {
		return false
	}
	allTB := map[string]bool{}
	for _, m := range append(matches, clearedMatches...) {
		for _, p := range m.Paths {
			allTB[p] = true
		}
	}
	for p := range allTB {
		if tbDelta[p] {
			continue
		}
		a, okA := cleared[p]
		b, okB := next[p]
		if !okA || !okB || a != b {
			return false
		}
	}
	return true
}

func classifyPatchPaths(ctx context.Context, store *model.Store, repo model.RepoRef,
	patches map[string]string) ([]trustboundary.Match, bool) {

	paths := make([]string, 0, len(patches))
	for p := range patches {
		paths = append(paths, p)
	}
	matches := trustboundary.Classify(paths)
	rules, guarded := repoGuard(ctx, store, repo, paths, true)
	if rules.Broken != "" {
		return nil, false
	}
	return append(matches, guarded...), true
}

// filePatches splits a raw unified diff into its files' patches, each
// keyed by every path it touches (a rename by both), normalised the way
// git patch-id normalises them: hunk headers lose their line numbers, and
// blob ids and similarity scores go, so the same change made on two
// branches reads the same. The second return is false when any file's
// patch cannot be compared on its text -- a binary file, whose patch
// GitHub elides to a line that reads the same whatever changed, or a path
// git had to quote -- since that file could be the trust-boundary one.
func filePatches(diff string) (map[string]string, bool) {
	out := map[string]string{}
	var lines []string
	flush := func() bool {
		if len(lines) == 0 {
			return true
		}
		paths, text, ok := filePatch(lines)
		if !ok {
			return false
		}
		for _, p := range paths {
			if prev, dup := out[p]; dup && prev != text {
				return false
			}
			out[p] = text
		}
		lines = nil
		return true
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") && !flush() {
			return nil, false
		}
		if len(lines) == 0 && !strings.HasPrefix(line, "diff --git ") {
			if strings.TrimSpace(line) != "" {
				return nil, false
			}
			continue
		}
		lines = append(lines, line)
	}
	if !flush() {
		return nil, false
	}
	return out, true
}

func filePatch(lines []string) ([]string, string, bool) {
	header := strings.TrimPrefix(lines[0], "diff --git ")
	if strings.Contains(header, `"`) {
		return nil, "", false
	}
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		p = strings.TrimSuffix(p, "\t")
		if p != "" && p != "/dev/null" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	// "a/<path> b/<path>": unambiguous only when both halves are the same
	// path, which is every file that was not renamed or copied.
	if n := len(header) - len("a/ b/"); n > 0 && n%2 == 0 {
		half := n / 2
		if strings.HasPrefix(header, "a/") && header[2+half:5+half] == " b/" && header[2:2+half] == header[5+half:] {
			add(header[2 : 2+half])
		}
	}
	var b strings.Builder
	inHunk := false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
			line = "@@"
		case inHunk:
		case i > 0 && (strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "similarity index ") ||
			strings.HasPrefix(line, "dissimilarity index ")):
			continue
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
			return nil, "", false
		case strings.HasPrefix(line, "--- a/"):
			add(strings.TrimPrefix(line, "--- a/"))
		case strings.HasPrefix(line, "+++ b/"):
			add(strings.TrimPrefix(line, "+++ b/"))
		case strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "copy from "):
			add(line[strings.Index(line, "from ")+len("from "):])
		case strings.HasPrefix(line, "rename to "), strings.HasPrefix(line, "copy to "):
			add(line[strings.Index(line, "to ")+len("to "):])
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if len(paths) == 0 {
		return nil, "", false
	}
	return paths, strings.TrimRight(b.String(), "\n"), true
}

// keepClearance records cleared for task's new head on the strength of
// carried, in place of the hold holdForTrustBoundary would otherwise put
// on it.
//
// A port inheriting its source's clearance is released from any hold an
// earlier head of it was put on -- the review that hold filed is closed
// unread -- and the port and its pull request are told where the
// clearance came from, once per source. Carrying a change's own
// clearance to a head that changed nothing on the list leaves the rest of
// its state as it was: released stays released, and a hold still waiting
// on its review goes on waiting on that same review, which now releases
// this head, rather than on a fresh one filed for a commit that changed
// none of what it reads.
func keepClearance(ctx context.Context, store *model.Store, client github.Client, current model.Task,
	ref model.PullRequestRef, headSHA string, cleared model.Link, carried carriedClearance,
	matches []trustboundary.Match, now time.Time) error {

	if carried.source == nil {
		if err := store.UpdateTask(ctx, current.ID, func(t *model.Task) error {
			t.Links = append(withoutLinks(t.Links, model.LinkTrustBoundaryCleared, model.LinkHumanSubmitted), cleared)
			return nil
		}); err != nil {
			return fmt.Errorf("orchestrator: carrying %s's clearance to %s: %w", current.ID, shortSHA(headSHA), err)
		}
		log.Printf("orchestrator: %s moved to %s without changing a trust-boundary patch; its clearance carries over",
			ref, shortSHA(headSHA))
		return nil
	}

	var prevReview string
	told := false
	if err := store.UpdateTask(ctx, current.ID, func(t *model.Task) error {
		prevReview = ""
		told = linkTarget(*t, model.LinkClearanceInheritedFrom) == carried.source.ID
		if hasSecurityHold(*t) {
			prevReview = linkTarget(*t, model.LinkSecurityReviewTask)
			if !hasPathTriggerHold(*t) && len(model.PendingApprovals(*t)) == 0 {
				t.AutoMerge = true
			}
		}
		// A port carrying its source's clearance touches nothing only a
		// human may release (carryable), so a human-only hold an earlier
		// head was put on no longer applies. Auto-merge stays as the
		// human left it.
		t.Links = withoutLinks(t.Links, model.LinkSecurityHold, model.LinkSecurityReviewTask,
			model.LinkTrustBoundaryCleared, model.LinkBrokenRulesHold, model.LinkClearanceInheritedFrom,
			model.LinkHumanSubmitted, model.LinkHumanOnlyHold)
		t.Links = model.WithoutApprovalRules(t.Links, model.ApprovalRuleBuiltin)
		t.Links = append(t.Links, cleared, model.Link{Kind: model.LinkClearanceInheritedFrom, Target: carried.source.ID})
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: carrying %s's clearance to its port %s: %w", carried.source.ID, current.ID, err)
	}
	log.Printf("orchestrator: %s is a port of %s carrying the same trust-boundary patches; its clearance is inherited",
		ref, carried.source.ID)
	if prevReview != "" {
		if err := closeUnneededSecurityReview(ctx, store, prevReview, current.ID, carried.source.ID, now); err != nil {
			log.Printf("orchestrator: %v", err)
		}
	}
	if told {
		return nil
	}
	body := inheritedClearanceNote(ref, headSHA, carried, matches)
	if err := reviewComment(ctx, store, current.ID, body, now); err != nil {
		return err
	}
	if client != nil {
		prBody := fmt.Sprintf("**grain security review** (task %s)\n\n%s", current.ID, body)
		if _, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, prBody); err != nil {
			log.Printf("orchestrator: noting %s's inherited clearance on %s: %v -- it is on the task only", current.ID, ref, err)
		}
	}
	return nil
}

func inheritedClearanceNote(ref model.PullRequestRef, headSHA string, carried carriedClearance,
	matches []trustboundary.Match) string {

	var b strings.Builder
	fmt.Fprintf(&b, "%s is a port of task %s (%s), ", ref, carried.source.ID, carried.sourcePR)
	if carried.review != "" {
		fmt.Fprintf(&b, "whose security review (task %s) finished without findings. ", carried.review)
	} else {
		b.WriteString("which grain cleared under the same trust boundary. ")
	}
	fmt.Fprintf(&b, "Every trust-boundary file this change touches carries exactly the patch %s did, "+
		"so that clearance carries over to `%s` rather than another review being filed, and this change "+
		"goes on merging automatically.\n\n", carried.sourcePR, shortSHA(headSHA))
	b.WriteString(matchList(matches, true))
	b.WriteString("\nA later commit that changes any of these patches is held for a security review of its own.")
	return b.String()
}

// closeUnneededSecurityReview closes reviewID, filed for an earlier head
// of taskID, now that taskID has inherited its source's clearance. A
// review already running is left to finish, and its verdict counts for
// nothing: settleSecurityHold only listens to the review a hold names, and
// the hold is gone.
func closeUnneededSecurityReview(ctx context.Context, store *model.Store, reviewID, taskID, sourceID string,
	now time.Time) error {

	state, err := store.State(ctx, reviewID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading security review %s's state: %w", reviewID, err)
	}
	if state == "" || state.Settled() || state == model.StateRunning {
		return nil
	}
	if err := observeField(ctx, store, reviewID, now, func(o *model.Observation) {
		o.ClosedAt = &now
	}); err != nil {
		return fmt.Errorf("orchestrator: closing security review %s: %w", reviewID, err)
	}
	return reviewComment(ctx, store, reviewID, fmt.Sprintf(
		"Closing this review: task %s now carries exactly the trust-boundary patches of task %s, "+
			"which was already cleared, so it inherits that clearance and has nothing left for this "+
			"review to read.", taskID, sourceID), now)
}
