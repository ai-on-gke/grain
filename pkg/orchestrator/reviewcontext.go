// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// earlierReviewsHeader and parallelReviewsHeader introduce the two context
// sections appended to a pull request review's body so reviews of one pull
// request do not run blind to earlier heads' reviews or sibling reviews of
// the same head (grain/task-5323).
const (
	earlierReviewsHeader  = "\n\n## Earlier reviews of this pull request\n\n"
	parallelReviewsHeader = "\n\n## Parallel reviews of this head\n\n"

	// earlierReviewQuestionsCap caps the total bytes spent listing earlier
	// reviews' add_review_comment questions across the section (~2 KB).
	earlierReviewQuestionsCap = 2048

	// maxPerQuestionBodyBytes bounds a single question's inline text before
	// the section-wide cap is checked, so one multi-paragraph comment cannot
	// crowd out every sibling finding.
	maxPerQuestionBodyBytes = 400

	// maxListedReviewCommits caps how many pushed commits are named inline
	// for one earlier review before summarizing the remainder as (+N more).
	maxListedReviewCommits = 5

	// linkReviewPostedFindings records on a review task the structured
	// add_review_comment findings its run(s) produced, before GitHub draft
	// creation can fail or lose line anchors. Provenance only; blocks nothing.
	linkReviewPostedFindings model.LinkKind = "review-posted-findings"

	// linkReviewedHeadCompat matches LinkReviewedHead if present on a task.
	linkReviewedHeadCompat model.LinkKind = "reviewed-head"

	// linkReviewVerdictCompat matches LinkReviewVerdict if present on a task.
	linkReviewVerdictCompat model.LinkKind = "review-verdict"
)

type storedReviewFinding struct {
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Located bool   `json:"located,omitempty"`
	Body    string `json:"body"`
}

// recordPostedReviewFindings persists findings onto review task taskID so
// later reviews of the same pull request can list its open or answered
// questions even when GitHub rejected a duplicate draft review. Best-effort:
// an error is logged and never fails the run's finish.
func recordPostedReviewFindings(ctx context.Context, store *model.Store, taskID string, findings []reviewFinding) {
	if store == nil || taskID == "" || len(findings) == 0 {
		return
	}
	if err := store.UpdateTask(ctx, taskID, func(t *model.Task) error {
		existing := parseStoredReviewFindings(linkTarget(*t, linkReviewPostedFindings))
		for _, f := range findings {
			item := storedReviewFinding{
				Path:    f.Path,
				Line:    f.Line,
				Located: f.Located,
				Body:    strings.TrimSpace(f.Body),
			}
			if item.Body == "" || slices.Contains(existing, item) {
				continue
			}
			existing = append(existing, item)
		}
		raw, err := json.Marshal(existing)
		if err != nil {
			return err
		}
		t.Links = append(withoutLinks(t.Links, linkReviewPostedFindings),
			model.Link{Kind: linkReviewPostedFindings, Target: string(raw)})
		return nil
	}); err != nil {
		log.Printf("orchestrator: recording review %s's posted findings: %v", taskID, err)
	}
}

func parseStoredReviewFindings(raw string) []storedReviewFinding {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []storedReviewFinding
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// loadReviewFindings returns the add_review_comment findings recorded on
// review, falling back to parsing its relayed findings comment for reviews
// finished before linkReviewPostedFindings existed.
func loadReviewFindings(ctx context.Context, store *model.Store, review model.Task) []storedReviewFinding {
	if stored := parseStoredReviewFindings(linkTarget(review, linkReviewPostedFindings)); len(stored) > 0 {
		return stored
	}
	if store == nil {
		return nil
	}
	comments, err := store.Comments(ctx, review.ID)
	if err != nil {
		return nil
	}
	var out []storedReviewFinding
	for _, c := range comments {
		for _, f := range parseFindingsFromReviewComment(c.Body) {
			if !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	return out
}

func parseFindingsFromReviewComment(body string) []storedReviewFinding {
	markers := []string{
		"as a review on ",
		"as a draft review on ",
		"recorded here instead:\n\n",
		"The findings are here instead:\n\n",
	}
	matched := false
	for _, m := range markers {
		if strings.Contains(body, m) {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}
	idx := strings.Index(body, "\n\n- ")
	if idx < 0 {
		return nil
	}
	listText := body[idx+2:]
	var bullets []string
	for _, line := range strings.Split(listText, "\n") {
		switch {
		case strings.HasPrefix(line, "- "):
			bullets = append(bullets, strings.TrimPrefix(line, "- "))
		case strings.HasPrefix(line, "  ") && len(bullets) > 0:
			bullets[len(bullets)-1] += " " + strings.TrimSpace(line)
		}
	}
	var out []storedReviewFinding
	for _, b := range bullets {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		if strings.HasPrefix(b, "`") {
			if endTick := strings.Index(b[1:], "` -- "); endTick > 0 {
				loc := b[1 : 1+endTick]
				rest := strings.TrimSpace(b[1+endTick+len("` -- "):])
				if colon := strings.LastIndex(loc, ":"); colon > 0 {
					if lineNum, err := strconv.Atoi(loc[colon+1:]); err == nil && lineNum > 0 && rest != "" {
						out = append(out, storedReviewFinding{
							Path:    loc[:colon],
							Line:    lineNum,
							Located: true,
							Body:    rest,
						})
						continue
					}
				}
			}
		}
		out = append(out, storedReviewFinding{Body: b})
	}
	return out
}

// filedReviewHead returns the commit SHA review t was filed to read, or "".
func filedReviewHead(t model.Task) string {
	if sha := strings.ToLower(strings.TrimSpace(linkTarget(t, linkReviewedHeadCompat))); sha != "" {
		return sha
	}
	if sha := strings.ToLower(strings.TrimSpace(reviewHead(t.Tags))); sha != "" {
		return sha
	}
	if ev := linkTarget(t, model.LinkGitHubEvent); strings.HasPrefix(ev, "takeover-") {
		if _, sha, ok := strings.Cut(ev, "@"); ok && sha != "" {
			return strings.ToLower(strings.TrimSpace(sha))
		}
	}
	return ""
}

func addReviewHeadTag(tags []string, headSHA string) []string {
	headSHA = strings.ToLower(strings.TrimSpace(headSHA))
	if headSHA == "" || reviewHead(tags) != "" {
		return tags
	}
	return append(tags, reviewHeadTag(headSHA))
}

// reviewKindLabel returns a short human-readable template/kind name for a
// review task ("security-review", "pr-review", etc.).
func reviewKindLabel(t model.Task) string {
	for _, tag := range t.Tags {
		if rest, ok := strings.CutPrefix(tag, "path-trigger-review:"); ok {
			if kind := shortTemplateKind(rest); kind != "" && kind != rest {
				return kind
			}
		}
	}
	if t.SourceTemplateID != "" {
		return shortTemplateKind(t.SourceTemplateID)
	}
	if t.TemplateID != "" {
		return shortTemplateKind(t.TemplateID)
	}
	switch {
	case strings.HasPrefix(t.Title, "Security review"):
		return "security-review"
	case strings.HasPrefix(t.Title, "Design review"):
		return "design-review"
	default:
		return "review"
	}
}

func shortTemplateKind(id string) string {
	if idx := strings.LastIndex(id, ":"); idx >= 0 && id[idx+1:] != "" {
		return id[idx+1:]
	}
	return id
}

func reviewMatchesPullRequest(review model.Task, reviewed *model.Task, ref model.PullRequestRef) bool {
	if !isReviewTask(review) || ref == (model.PullRequestRef{}) {
		return false
	}
	if target := linkTarget(review, model.LinkReviewsPullRequest); target != "" {
		rRef, err := model.ParsePullRequestRef(target)
		return err == nil && model.SamePullRequest(rRef, ref)
	}
	if ext := linkTarget(review, model.LinkExternalPR); ext != "" {
		rRef, err := model.ParsePullRequestRef(ext)
		return err == nil && model.SamePullRequest(rRef, ref)
	}
	return reviewed != nil && review.Parent() == reviewed.ID && holdIsAbout(*reviewed, ref)
}

// reviewsOfPullRequest returns every review task filed for ref, oldest first.
func reviewsOfPullRequest(ctx context.Context, store *model.Store, reviewed *model.Task,
	ref model.PullRequestRef) ([]model.Task, error) {

	if store == nil || ref == (model.PullRequestRef{}) {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []model.Task
	addByID := func(id string) error {
		if id == "" || seen[id] {
			return nil
		}
		seen[id] = true
		t, err := store.GetTask(ctx, id)
		if err != nil || t == nil {
			return err
		}
		if reviewMatchesPullRequest(*t, reviewed, ref) {
			out = append(out, *t)
		}
		return nil
	}

	if ids, err := store.TasksWithLinkTarget(ctx, model.LinkReviewsPullRequest, ref.String()); err != nil {
		return nil, err
	} else {
		for _, id := range ids {
			if err := addByID(id); err != nil {
				return nil, err
			}
		}
	}
	if ids, err := store.TasksWithLinkTarget(ctx, model.LinkExternalPR, ref.String()); err != nil {
		return nil, err
	} else {
		for _, id := range ids {
			if err := addByID(id); err != nil {
				return nil, err
			}
		}
	}
	if reviewed != nil && reviewed.ID != "" {
		children, err := store.ChildTasks(ctx, reviewed.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range children {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			if reviewMatchesPullRequest(c, reviewed, ref) {
				out = append(out, c)
			}
		}
	}
	slices.SortFunc(out, compareTasksChronologically)
	return out, nil
}

func compareTasksChronologically(a, b model.Task) int {
	switch {
	case a.CreatedAt != nil && b.CreatedAt != nil && !a.CreatedAt.Equal(*b.CreatedAt):
		if a.CreatedAt.Before(*b.CreatedAt) {
			return -1
		}
		return 1
	case a.CreatedAt != nil && b.CreatedAt == nil:
		return -1
	case a.CreatedAt == nil && b.CreatedAt != nil:
		return 1
	}
	ai, aErr := strconv.ParseInt(a.ID, 10, 64)
	bi, bErr := strconv.ParseInt(b.ID, 10, 64)
	if aErr == nil && bErr == nil {
		return cmp.Compare(ai, bi)
	}
	return cmp.Compare(a.ID, b.ID)
}

func isSameHeadReview(r model.Task, obs *model.Observation, state model.State, headSHA string, supersedingIDs []string) bool {
	if slices.Contains(supersedingIDs, r.ID) {
		return false
	}
	rHead := filedReviewHead(r)
	if headSHA != "" && rHead != "" {
		return strings.EqualFold(rHead, headSHA)
	}
	if state == model.StateClosed {
		return false
	}
	if obs != nil && (obs.CompletedAt != nil || obs.ClosedAt != nil || obs.PrMergedAt != nil || obs.PrClosedAt != nil) {
		return false
	}
	return true
}

func isActiveParallelReview(r model.Task, obs *model.Observation, state model.State, supersedingIDs []string) bool {
	if slices.Contains(supersedingIDs, r.ID) || linkTarget(r, model.LinkSupersededBy) != "" {
		return false
	}
	return state != model.StateClosed || (obs != nil && obs.CompletedAt != nil)
}

type prQuestionAnswerState struct {
	loaded          bool
	reviewed        *model.Task
	reviewedReplies []model.Comment
	repliedComments map[int]bool
	resolvedComment map[int]bool
	ghRootByLocBody map[string]int
}

func (s *prQuestionAnswerState) ensureLoaded(ctx context.Context, store *model.Store, client github.Client,
	reviewed *model.Task, ref model.PullRequestRef) {

	if s.loaded {
		return
	}
	s.loaded = true
	s.repliedComments = map[int]bool{}
	s.resolvedComment = map[int]bool{}
	s.ghRootByLocBody = map[string]int{}

	if reviewed != nil && store != nil && reviewed.ID != "" {
		if fresh, err := store.GetTask(ctx, reviewed.ID); err == nil && fresh != nil {
			s.reviewed = fresh
		} else {
			s.reviewed = reviewed
		}
		if cs, err := store.Comments(ctx, reviewed.ID); err == nil {
			s.reviewedReplies = cs
		}
	}
	if client == nil || ref == (model.PullRequestRef{}) {
		return
	}
	if comments, err := client.ListReviewComments(ref.Repo.StoredOwner(), ref.Repo.Name, ref.Number); err == nil {
		for _, c := range comments {
			if c.InReplyTo != 0 {
				s.repliedComments[c.InReplyTo] = true
				continue
			}
			key := fmt.Sprintf("%s:%d:%s", c.Path, c.Line, oneLineText(c.Body))
			s.ghRootByLocBody[key] = c.ID
		}
	}
	if lister, ok := client.(reviewThreadLister); ok {
		if threads, err := lister.ListReviewThreads(ref.Repo.StoredOwner(), ref.Repo.Name, ref.Number); err == nil {
			for _, th := range threads {
				if len(th.CommentIDs) == 0 {
					continue
				}
				if th.Resolved || len(th.CommentIDs) > 1 {
					s.resolvedComment[th.CommentIDs[0]] = true
				}
			}
		}
	}
}

func (s *prQuestionAnswerState) isAnswered(ctx context.Context, store *model.Store, review model.Task,
	finding storedReviewFinding) bool {

	// 1. Direct human reply on the review task itself.
	if store != nil {
		if rComments, err := store.Comments(ctx, review.ID); err == nil {
			for _, c := range rComments {
				if c.Author.Actor.Kind == model.PrincipalHuman && strings.TrimSpace(c.Body) != "" {
					return true
				}
			}
		}
	}
	// 2. Relayed review threads or owner answer on the reviewed task.
	if s.reviewed != nil {
		for _, rt := range model.RelayedReviewThreadsOf(*s.reviewed) {
			if rt.ReviewTaskID != review.ID {
				continue
			}
			if rt.Decided || s.repliedComments[rt.CommentID] || s.resolvedComment[rt.CommentID] {
				return true
			}
		}
		needle := "(task " + review.ID + ")"
		settledNeedle := "answered the open questions from review task " + review.ID
		var askedID int64
		for _, c := range s.reviewedReplies {
			if strings.Contains(c.Body, settledNeedle) {
				return true
			}
			if c.Author.Actor == reviewer && strings.Contains(c.Body, needle) &&
				strings.Contains(c.Body, "Review needs your decision.") {
				askedID = c.ID
			}
		}
		if askedID > 0 {
			for _, c := range s.reviewedReplies {
				if c.ID > askedID && c.Author.Actor.Kind == model.PrincipalHuman && strings.TrimSpace(c.Body) != "" {
					return true
				}
			}
		}
	}
	// 3. GitHub inline comment replied to or resolved.
	if finding.Located {
		key := fmt.Sprintf("%s:%d:%s", finding.Path, finding.Line, oneLineText(finding.Body))
		if cid, ok := s.ghRootByLocBody[key]; ok {
			if s.repliedComments[cid] || s.resolvedComment[cid] {
				return true
			}
		}
	}
	return false
}

// earlierReviewsSection renders the "## Earlier reviews of this pull request"
// section for a new head's review, or "" when no earlier review exists.
func earlierReviewsSection(ctx context.Context, store *model.Store, client github.Client,
	reviewed *model.Task, ref model.PullRequestRef, headSHA, excludeID string, supersedingIDs ...string) string {

	all, err := reviewsOfPullRequest(ctx, store, reviewed, ref)
	if err != nil || len(all) == 0 {
		return ""
	}
	type earlierEntry struct {
		review   model.Task
		head     string
		verdict  string
		commits  []string
		findings []storedReviewFinding
	}
	var entries []earlierEntry
	for _, r := range all {
		if r.ID == excludeID {
			continue
		}
		obs, _ := store.GetObservation(ctx, r.ID)
		state, _ := store.State(ctx, r.ID)
		if isSameHeadReview(r, obs, state, headSHA, supersedingIDs) {
			continue
		}
		findings := loadReviewFindings(ctx, store, r)
		commits := reviewPushedCommits(client, r)
		beingSuperseded := slices.Contains(supersedingIDs, r.ID) || linkTarget(r, model.LinkSupersededBy) != ""
		completed := obs != nil && obs.CompletedAt != nil
		if !completed && len(findings) == 0 && len(commits) == 0 && (obs == nil || obs.PendingQuestionCommentID == nil) {
			runs, _ := store.Runs(ctx, r.ID)
			if len(runs) == 0 && (beingSuperseded || state == model.StateClosed) {
				continue
			}
		}
		entries = append(entries, earlierEntry{
			review:   r,
			head:     filedReviewHead(r),
			verdict:  earlierReviewVerdict(r, obs, state, findings, commits, beingSuperseded),
			commits:  commits,
			findings: findings,
		})
	}
	if len(entries) == 0 {
		return ""
	}

	var answers prQuestionAnswerState
	totalQuestions := 0
	for _, e := range entries {
		totalQuestions += len(e.findings)
	}
	if totalQuestions > 0 {
		answers.ensureLoaded(ctx, store, client, reviewed, ref)
	}

	var b strings.Builder
	b.WriteString(earlierReviewsHeader)
	questionBytes := 0
	renderedQuestions := 0
	cappedOut := false

	for _, e := range entries {
		kind := reviewKindLabel(e.review)
		if e.head != "" {
			fmt.Fprintf(&b, "- Task %s (`%s` at `%s`): verdict `%s`; commits pushed: %s",
				e.review.ID, kind, shortCommit(e.head), e.verdict, formatReviewCommits(e.commits))
		} else {
			fmt.Fprintf(&b, "- Task %s (`%s`): verdict `%s`; commits pushed: %s",
				e.review.ID, kind, e.verdict, formatReviewCommits(e.commits))
		}
		if len(e.findings) == 0 {
			b.WriteString(".\n")
			continue
		}
		if cappedOut {
			fmt.Fprintf(&b, "; %s omitted (cap reached; read full threads with `grain pr open`).\n",
				countOf(len(e.findings), "question"))
			continue
		}
		b.WriteString("; questions:\n")
		for _, f := range e.findings {
			remaining := totalQuestions - renderedQuestions
			omittedFooter := fmt.Sprintf("  - ... (%s omitted; read full threads with `grain pr open`)\n",
				countOf(remaining, "more question"))
			reserve := 0
			if remaining > 1 {
				reserve = len(omittedFooter)
			}
			line := formatReviewQuestionLine(f, answers.isAnswered(ctx, store, e.review, f))
			budget := earlierReviewQuestionsCap - questionBytes - reserve
			if len(line) > budget {
				if budget >= 64 {
					line = truncateUTF8Line(line, budget)
					b.WriteString(line)
					questionBytes += len(line)
					renderedQuestions++
				}
				left := totalQuestions - renderedQuestions
				if left > 0 {
					fmt.Fprintf(&b, "  - ... (%s omitted; read full threads with `grain pr open`)\n",
						countOf(left, "more question"))
				}
				cappedOut = true
				break
			}
			b.WriteString(line)
			questionBytes += len(line)
			renderedQuestions++
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func earlierReviewVerdict(r model.Task, obs *model.Observation, state model.State,
	findings []storedReviewFinding, commits []string, beingSuperseded bool) string {

	if raw := linkTarget(r, linkReviewVerdictCompat); raw != "" {
		var rec struct {
			Verdict     string `json:"verdict"`
			FixesPushed bool   `json:"fixesPushed"`
		}
		if err := json.Unmarshal([]byte(raw), &rec); err == nil && rec.Verdict != "" {
			if rec.Verdict == "clean" && (rec.FixesPushed || len(commits) > 0) {
				return "clean (pushed fixes)"
			}
			return rec.Verdict
		}
	}
	if len(findings) > 0 {
		return "findings"
	}
	if obs != nil && obs.PendingQuestionCommentID != nil {
		return "questions"
	}
	pushed, landed, ok := reviewOutcome(r, obs)
	switch {
	case ok && pushed && !landed:
		return "fixes closed unmerged"
	case ok && (pushed || len(commits) > 0):
		return "clean (pushed fixes)"
	case ok:
		return "clean"
	case !ok && pushed && !beingSuperseded:
		return "fixes open"
	case beingSuperseded:
		return "superseded"
	case state == model.StateRunning:
		return "running"
	case state == model.StateFailed:
		return "failed"
	case obs != nil && obs.ClosedAt != nil:
		return "closed without verdict"
	default:
		return "pending"
	}
}

func reviewPushedCommits(client github.Client, r model.Task) []string {
	if _, inPlace := reviewsInPlace(r); inPlace {
		reviewerHead := linkTarget(r, model.LinkReviewerHead)
		if reviewerHead == "" {
			return nil
		}
		from := filedReviewHead(r)
		if from != "" && strings.EqualFold(from, reviewerHead) {
			return nil
		}
		if client != nil && r.Target != nil && from != "" {
			if cmp, err := client.CompareRefs(r.Target.StoredOwner(), r.Target.Name, from, reviewerHead); err == nil && len(cmp.Commits) > 0 {
				return formatGitHubCommits(cmp.Commits)
			}
		}
		if from != "" {
			return []string{fmt.Sprintf("`%s..%s`", shortCommit(from), shortCommit(reviewerHead))}
		}
		return []string{fmt.Sprintf("`%s`", shortCommit(reviewerHead))}
	}
	if !hasPullRequest(r) {
		return nil
	}
	rRef := model.TaskPullRequest(r)
	if rRef == nil {
		return nil
	}
	if client != nil {
		if commits, err := client.ListPullRequestCommits(rRef.Repo.StoredOwner(), rRef.Repo.Name, rRef.Number); err == nil && len(commits) > 0 {
			return formatGitHubCommits(commits)
		}
	}
	return []string{rRef.String()}
}

func formatGitHubCommits(commits []github.Commit) []string {
	out := make([]string, 0, len(commits))
	for _, c := range commits {
		sha := shortCommit(c.SHA)
		subj := commitSubject(c.Message)
		if subj != "" {
			out = append(out, fmt.Sprintf("`%s` (%q)", sha, subj))
		} else {
			out = append(out, fmt.Sprintf("`%s`", sha))
		}
	}
	return out
}

func commitSubject(msg string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	line = oneLineText(line)
	return truncateUTF8(line, 80)
}

func formatReviewCommits(commits []string) string {
	if len(commits) == 0 {
		return "none"
	}
	if len(commits) <= maxListedReviewCommits {
		return strings.Join(commits, ", ")
	}
	return fmt.Sprintf("%s (+%d more)",
		strings.Join(commits[:maxListedReviewCommits], ", "),
		len(commits)-maxListedReviewCommits)
}

func formatReviewQuestionLine(f storedReviewFinding, answered bool) string {
	status := "open"
	if answered {
		status = "answered"
	}
	text := truncateUTF8(oneLineText(f.Body), maxPerQuestionBodyBytes)
	cleanPath := oneLineText(strings.ReplaceAll(f.Path, "`", ""))
	if f.Located && cleanPath != "" && f.Line > 0 {
		return fmt.Sprintf("  - [%s] `%s:%d`: %s\n", status, cleanPath, f.Line, text)
	}
	return fmt.Sprintf("  - [%s] %s\n", status, text)
}

func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 3 {
		return s[:maxBytes]
	}
	limit := maxBytes - 3
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return strings.TrimRight(s[:limit], " ") + "..."
}

func truncateUTF8Line(line string, maxBytes int) string {
	line = strings.TrimSuffix(line, "\n")
	if maxBytes <= 1 {
		return "\n"
	}
	return truncateUTF8(line, maxBytes-1) + "\n"
}

// parallelReviewsFor returns active reviews of ref at headSHA other than excludeID.
func parallelReviewsFor(ctx context.Context, store *model.Store, reviewed *model.Task,
	ref model.PullRequestRef, headSHA, excludeID string, supersedingIDs ...string) []model.Task {

	all, err := reviewsOfPullRequest(ctx, store, reviewed, ref)
	if err != nil || len(all) == 0 {
		return nil
	}
	var peers []model.Task
	for _, r := range all {
		if r.ID == excludeID {
			continue
		}
		obs, _ := store.GetObservation(ctx, r.ID)
		state, _ := store.State(ctx, r.ID)
		if !isSameHeadReview(r, obs, state, headSHA, supersedingIDs) {
			continue
		}
		if isActiveParallelReview(r, obs, state, supersedingIDs) {
			peers = append(peers, r)
		}
	}
	return peers
}

func parallelReviewsSection(peers []model.Task) string {
	if len(peers) == 0 {
		return ""
	}
	names := make([]string, len(peers))
	for i, p := range peers {
		names[i] = fmt.Sprintf("task %s (`%s`)", p.ID, reviewKindLabel(p))
	}
	if len(peers) == 1 {
		return fmt.Sprintf("%s%s is also reviewing this pull request at this commit. "+
			"Do not re-raise a question or finding that task %s has already posted -- "+
			"check the pull request's review threads with `grain pr open`.\n",
			parallelReviewsHeader, capitalizeFirst(names[0]), peers[0].ID)
	}
	return fmt.Sprintf("%sAlso reviewing this pull request at this commit: %s. "+
		"Do not re-raise a question or finding another review has already posted -- "+
		"check the pull request's review threads with `grain pr open`.\n",
		parallelReviewsHeader, strings.Join(names, ", "))
}

func capitalizeFirst(s string) string {
	if s == "" {
		return ""
	}
	r, width := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r)) + s[width:]
}

func setParallelReviewsSection(body, section string) string {
	if idx := generatedParallelSectionAt(body); idx >= 0 {
		body = body[:idx]
	}
	return body + section
}

// generatedParallelSectionAt returns where the "## Parallel reviews of this
// head" section parallelReviewsSection appended to body starts, or -1. Only
// the last occurrence of the header, followed by exactly the one line
// parallelReviewsSection writes, is grain's own: an earlier occurrence --
// one quoted from a pull request's description, say -- is left alone, so
// refreshing the section can never cut away the instructions that follow it.
func generatedParallelSectionAt(body string) int {
	idx := strings.LastIndex(body, parallelReviewsHeader)
	if idx < 0 {
		return -1
	}
	rest := body[idx+len(parallelReviewsHeader):]
	line, ok := strings.CutSuffix(rest, "\n")
	if !ok || strings.Contains(line, "\n") || !strings.HasSuffix(line, "`grain pr open`.") {
		return -1
	}
	return idx
}

// isGeneratedEarlierSection reports whether content -- what follows an
// earlierReviewsHeader up to the next section -- is a list
// earlierReviewsSection wrote: nothing but "- Task" entries and their
// indented question lines.
func isGeneratedEarlierSection(content string) bool {
	content, ok := strings.CutSuffix(content, "\n")
	if !ok || content == "" {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "- Task ") && !strings.HasPrefix(line, "  - ") {
			return false
		}
	}
	return true
}

// neutralizeReviewSectionHeaders rewrites, in text written outside this
// deployment (a pull request's title, description or a reviewer's request),
// every occurrence of the headers grain later finds its own appended review
// sections by, so none of it can be taken for one of them.
func neutralizeReviewSectionHeaders(text string) string {
	for _, h := range []struct{ header, replacement string }{
		{earlierReviewsHeader, "\n\n### Earlier reviews of this pull request\n\n"},
		{parallelReviewsHeader, "\n\n### Parallel reviews of this head\n\n"},
		{incrementalNoteHeader, "\n**This is an incremental review**."},
	} {
		for strings.Contains(text, h.header) {
			text = strings.ReplaceAll(text, h.header, h.replacement)
		}
	}
	return text
}

// syncParallelReviewBodies refreshes the "## Parallel reviews of this head"
// appendix on every open review of ref at headSHA so reviews filed one after
// another for the same commit each name their sibling(s).
func syncParallelReviewBodies(ctx context.Context, store *model.Store, reviewed *model.Task,
	ref model.PullRequestRef, headSHA string) {

	if store == nil || ref == (model.PullRequestRef{}) {
		return
	}
	all, err := reviewsOfPullRequest(ctx, store, reviewed, ref)
	if err != nil || len(all) == 0 {
		return
	}
	type activeReview struct {
		task  model.Task
		state model.State
	}
	var sameHead []activeReview
	for _, r := range all {
		obs, _ := store.GetObservation(ctx, r.ID)
		state, _ := store.State(ctx, r.ID)
		if !isSameHeadReview(r, obs, state, headSHA, nil) {
			continue
		}
		if isActiveParallelReview(r, obs, state, nil) {
			sameHead = append(sameHead, activeReview{task: r, state: state})
		}
	}
	if len(sameHead) < 2 {
		return
	}
	for _, entry := range sameHead {
		if entry.state == model.StateClosed {
			continue
		}
		var peers []model.Task
		for _, other := range sameHead {
			if other.task.ID != entry.task.ID {
				peers = append(peers, other.task)
			}
		}
		section := parallelReviewsSection(peers)
		if setParallelReviewsSection(entry.task.Body, section) == entry.task.Body {
			continue
		}
		if err := store.UpdateTask(ctx, entry.task.ID, func(t *model.Task) error {
			t.Body = setParallelReviewsSection(t.Body, section)
			return nil
		}); err != nil {
			log.Printf("orchestrator: updating parallel reviews note on %s: %v", entry.task.ID, err)
		}
	}
}

// knownTaskHead returns the head SHA most recently recorded on task by either
// path-trigger judgment or trust-boundary classification, for callers such as
// SyncReviews that file a review without a github.Client in hand.
func knownTaskHead(task model.Task) string {
	if sha := linkTarget(task, model.LinkPathTriggersRead); sha != "" {
		return sha
	}
	return classifiedHead(task)
}

// securityIncrementalBase returns the commit SHA an earlier completed security
// review on (task, ref) already covered, or "" when the new head must be
// reviewed in full.
func securityIncrementalBase(ctx context.Context, store *model.Store, task model.Task,
	ref model.PullRequestRef, headSHA string, rules model.RepoRules) string {

	if store == nil || headSHA == "" || rules.Broken != "" {
		return ""
	}
	prevID := linkTarget(task, model.LinkSecurityReviewTask)
	if prevID != "" {
		if prevTask, err := store.GetTask(ctx, prevID); err == nil && prevTask != nil &&
			reviewIsAboutPullRequest(*prevTask, ref) {
			if prevObs, err := store.GetObservation(ctx, prevID); err == nil {
				if pushed, landed, ok := reviewOutcome(*prevTask, prevObs); ok && (!pushed || landed) {
					version := strings.TrimSuffix(clearedTarget("", rules), "@")
					if recVer, recSHA, hasCleared := strings.Cut(linkTarget(task, model.LinkTrustBoundaryCleared), "@"); hasCleared && recSHA != "" {
						if recVer != version {
							return ""
						}
						if !strings.EqualFold(recSHA, headSHA) {
							return recSHA
						}
					} else {
						ruleStr := ""
						if !rules.Empty() {
							ruleStr = rules.String()
						}
						if prevRules := linkTarget(task, model.LinkRepoRules); prevRules != "" && prevRules != ruleStr {
							return ""
						}
						for _, rf := range model.ReviewFindingsForGate(task, model.GateSecurityReview) {
							if rf.HeadSHA != "" && !strings.EqualFold(rf.HeadSHA, headSHA) {
								return rf.HeadSHA
							}
						}
					}
					if fh := filedReviewHead(*prevTask); fh != "" && !strings.EqualFold(fh, headSHA) {
						return fh
					}
				}
			}
		}
	}

	// If prevID never completed (queued or cancelled in flight), fall back to
	// the newest earlier trust-boundary security review on ref that did
	// complete cleanly.
	all, err := reviewsOfPullRequest(ctx, store, &task, ref)
	if err != nil {
		return ""
	}
	for i := len(all) - 1; i >= 0; i-- {
		r := all[i]
		if r.ID == prevID || !isTrustBoundarySecurityReview(r, task.ID, ref) {
			continue
		}
		obs, err := store.GetObservation(ctx, r.ID)
		if err != nil {
			continue
		}
		if pushed, landed, ok := reviewOutcome(r, obs); !ok || (pushed && !landed) {
			continue
		}
		if fh := filedReviewHead(r); fh != "" && !strings.EqualFold(fh, headSHA) {
			return fh
		}
	}
	return ""
}

func isTrustBoundarySecurityReview(r model.Task, reviewedID string, ref model.PullRequestRef) bool {
	return r.Parent() == reviewedID &&
		r.Origin.Attribution.Actor == reviewer &&
		!hasTag(r, model.PathTriggerMarkerTag) &&
		linkTarget(r, model.LinkExternalPR) == "" &&
		(r.SourceTemplateID == model.SecurityReviewTemplateID || strings.HasPrefix(r.Title, "Security review:")) &&
		reviewIsAboutPullRequest(r, ref)
}

// securityReviewDelta computes the changed files between since and headSHA for
// an incremental security-hold re-review, falling back to ("", nil) for a full
// review under the same safety conditions as pathtriggerreview.go's reviewDelta.
func securityReviewDelta(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, baseRef, since, headSHA string, files []string) ([]string, string) {

	if client == nil || task.Target == nil || since == "" || headSHA == "" || strings.EqualFold(since, headSHA) {
		return nil, ""
	}
	cmp, err := client.CompareRefs(ref.Repo.StoredOwner(), ref.Repo.Name, since, headSHA)
	if err != nil {
		log.Printf("orchestrator: comparing %s %s..%s for an incremental security review: %v -- reviewing it in full",
			ref, shortCommit(since), shortCommit(headSHA), err)
		return nil, ""
	}
	if cmp.Status != "ahead" || cmp.FilesTruncated {
		return nil, ""
	}
	hasMerge := false
	for _, c := range cmp.Commits {
		if c.Merge || len(c.Parents) > 1 {
			hasMerge = true
			break
		}
	}
	ofChange := map[string]bool{}
	for _, f := range files {
		ofChange[f] = true
	}
	if hasMerge {
		if baseRef == "" {
			if detail, err := client.GetPullRequest(ref.Repo.StoredOwner(), ref.Repo.Name, ref.Number); err == nil {
				baseRef = detail.BaseRef
			}
		}
		if baseRef == "" {
			return nil, ""
		}
		prevPR, err := client.CompareRefs(ref.Repo.StoredOwner(), ref.Repo.Name, baseRef, since)
		if err != nil || prevPR.FilesTruncated {
			return nil, ""
		}
		for _, f := range prevPR.Files {
			ofChange[f.Filename] = true
			if f.PreviousFilename != "" {
				ofChange[f.PreviousFilename] = true
			}
		}
	}
	var delta []string
	for _, f := range cmp.Files {
		names := []string{f.Filename}
		if f.PreviousFilename != "" && f.PreviousFilename != f.Filename {
			names = append(names, f.PreviousFilename)
		}
		for _, name := range names {
			if !hasMerge || ofChange[name] {
				if !slices.Contains(delta, name) {
					delta = append(delta, name)
				}
				continue
			}
			if len(trustboundary.Classify([]string{name})) > 0 {
				return nil, ""
			}
			if _, guarded := repoGuard(ctx, store, client, *task.Target, []string{name}, true); len(guarded) > 0 {
				return nil, ""
			}
			if _, ok := guardTemplatePush(client, *task.Target, headSHA, []string{name}); ok {
				return nil, ""
			}
		}
	}
	if len(delta) > incrementalReviewMaxFiles {
		return nil, ""
	}
	slices.Sort(delta)
	return delta, since
}

func setEarlierReviewsSection(body, section string) string {
	// grain's own sections sit at the end of the body, in the order
	// earlier reviews, incremental note, parallel reviews: find them from
	// the end, so a header quoted earlier in the body is never taken for one.
	tail := len(body)
	if p := generatedParallelSectionAt(body); p >= 0 {
		tail = p
	}
	if i := strings.LastIndex(body[:tail], incrementalNoteHeader); i >= 0 {
		tail = i
	}
	if e := strings.LastIndex(body[:tail], earlierReviewsHeader); e >= 0 &&
		isGeneratedEarlierSection(body[e+len(earlierReviewsHeader):tail]) {
		body = body[:e] + body[tail:]
		tail = e
	}
	if section == "" {
		return body
	}
	return body[:tail] + section + body[tail:]
}

// refreshQueuedReviewsAfterFinish updates the "## Earlier reviews of this pull
// request" section on any open, undispatched review of the same pull request at
// a newer head when an in-flight earlier review finishes.
func refreshQueuedReviewsAfterFinish(ctx context.Context, store *model.Store, client github.Client, finished model.Task) {
	if store == nil || !isReviewTask(finished) {
		return
	}
	reviewed, ref, ok, err := reviewedPullRequest(ctx, store, finished)
	if err != nil || !ok {
		return
	}
	all, err := reviewsOfPullRequest(ctx, store, reviewed, ref)
	if err != nil || len(all) == 0 {
		return
	}
	finishedHead := filedReviewHead(finished)
	for _, r := range all {
		if r.ID == finished.ID {
			continue
		}
		rHead := filedReviewHead(r)
		if rHead == "" || (finishedHead != "" && strings.EqualFold(rHead, finishedHead)) {
			continue
		}
		state, err := store.State(ctx, r.ID)
		if err != nil || state == model.StateClosed {
			continue
		}
		if runs, err := store.Runs(ctx, r.ID); err != nil || len(runs) > 0 {
			continue
		}
		section := earlierReviewsSection(ctx, store, client, reviewed, ref, rHead, r.ID)
		if setEarlierReviewsSection(r.Body, section) == r.Body {
			continue
		}
		if err := store.UpdateTask(ctx, r.ID, func(t *model.Task) error {
			t.Body = setEarlierReviewsSection(t.Body, section)
			return nil
		}); err != nil {
			log.Printf("orchestrator: refreshing earlier reviews note on %s: %v", r.ID, err)
		}
	}
}
