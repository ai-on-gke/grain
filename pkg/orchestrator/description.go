package orchestrator

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// A pull request grain opens has to say what the change does, and the
// only account of that grain holds is the one the agent already wrote:
// the messages of the commits on the branch. Everything in this file
// exists to turn those into a title and a description and to keep both
// true as the branch moves.
//
// It is a restoration, not an invention. v1 seeded a freshly opened pull
// request's body with the pushed branch's tip commit message
// (github.BranchHead's own doc comment here still says so, unused until
// now), and the port to this repository dropped it: EnsurePullRequest
// wrote one metadata line and nothing else, which is why grain's own
// pull requests went back to reading as description-free.
//
// Two things about how runs work now shaped what came back:
//
//   - A run pushes several times. BuildPrompt sends every run round a
//     push/check/repair loop, so the tip commit is routinely "Fix the vet
//     warning" rather than the commit that explains the change. So the
//     description is built from every commit the branch carries over its
//     base (github.Client.CompareCommits), not from the tip alone.
//   - A run can open its own pull request before it is done
//     (OpenPullRequestForTask). Whatever it pushed by then is all the
//     description could have been built from, and the commits that
//     followed are the ones a reviewer needs. So the description is
//     rewritten, not just written once -- see refreshPullRequest.

// describeCommits is the description itself: what the agent wrote about
// its own change, arranged for a reviewer.
//
// The lead commit -- whose message becomes the description proper -- is
// the first one that has a body paragraph, not simply the first or the
// last. A message with a body is one written to explain a change rather
// than to name it, and on a branch of "Add the parser" / "Fix lint" the
// difference between picking that one and picking either end is the
// whole point of doing this at all. Every other commit is listed by its
// summary line underneath, so nothing on the branch goes unmentioned.
//
// Merge commits are dropped outright: "Merge branch 'main' into
// grain/task-12" is a fact about the branch's shape, not about the
// change, and a run told to merge its base back in when it conflicts
// (BuildPrompt again) produces them routinely.
//
// A `Port: no` footer on any commit is carried into the description
// under the lead commit's message, so a reviewer reading the description
// sees the change will not be ported. A commit listed by its summary line
// alone would hide it. The merge queue itself reads the opt-out from the
// pull request's own commits (decidePort), not from here.
//
// Empty means there was nothing to say -- no commits, or none that were
// not merges. Callers treat that as "leave the description alone" rather
// than writing an empty one.
func describeCommits(commits []github.Commit) string {
	written := writtenCommits(commits)
	if len(written) == 0 {
		return ""
	}
	lead := leadCommit(written)
	var b strings.Builder
	b.WriteString(strings.TrimSpace(written[lead].Message))
	if !portOptOut.MatchString(written[lead].Message) &&
		slices.ContainsFunc(written, func(c github.Commit) bool { return portOptOut.MatchString(c.Message) }) {
		b.WriteString("\n\nPort: no")
	}
	if len(written) > 1 {
		b.WriteString("\n\nAlso on this branch:\n")
		for i, c := range written {
			if i == lead {
				continue
			}
			fmt.Fprintf(&b, "- %s\n", commitSummary(c.Message))
		}
	}
	return capDescription(strings.TrimSpace(b.String()))
}

// writtenCommits is the commits on a branch that say something about the
// change: everything describeCommits and titleFromCommits are allowed to
// read, and the same list for both, so a pull request's title and its
// description are never drawn from different commits.
//
// Merges and empty messages are what it drops, for describeCommits' own
// reasons -- see its doc comment.
func writtenCommits(commits []github.Commit) []github.Commit {
	var written []github.Commit
	for _, c := range commits {
		if !c.Merge && strings.TrimSpace(c.Message) != "" {
			written = append(written, c)
		}
	}
	return written
}

// leadCommit is the index of the commit that speaks for the branch: the
// first one with a body paragraph, and the first commit outright when no
// commit on the branch has one. describeCommits' own doc comment has why
// a body is what makes a commit the lead; this is here so the title and
// the description agree on which commit that is.
//
// written must be writtenCommits' output and non-empty.
func leadCommit(written []github.Commit) int {
	for i, c := range written {
		if commitBody(c.Message) != "" {
			return i
		}
	}
	return 0
}

// titleFromCommits is what the pull request is called: the summary line
// of the same commit describeCommits leads with, so the name on the pull
// request and the first line of its description are two halves of one
// thing the agent wrote.
//
// It exists because a task's title is a request and a pull request's
// title is an answer, and the two are routinely not the same sentence. A
// task saying "check the parser for bugs" earns a branch that says "Fix
// the off-by-one in the header parser"; naming the pull request after the
// task -- all EnsurePullRequest had to go on before this -- put the
// question on the change, so a reviewer scanning a list of pull requests
// read what somebody had asked for rather than what had been done, and a
// task worded as an investigation named a pull request that was nothing
// of the kind.
//
// This is also the whole of how an agent names its own pull request.
// There is no tool for it and deliberately so: the agent already writes
// the answer, once per branch, in the summary line of the commit that
// explains its work -- run.go's prompt asks every run for exactly that
// line -- and a second place to say the same thing is a second place for
// it to go stale. Rewriting the commit message renames the pull request,
// because the name is read back from the branch every time
// (refreshPullRequest).
//
// Empty means the branch says nothing -- no commits, or none that were
// not merges -- and callers fall back to the task's own title rather than
// opening a pull request called nothing at all.
func titleFromCommits(commits []github.Commit) string {
	written := writtenCommits(commits)
	if len(written) == 0 {
		return ""
	}
	return capTitle(commitSummary(written[leadCommit(written)].Message))
}

// maxTitleBytes leaves room under GitHub's own 256-character limit on a
// pull request title. A summary line anywhere near it is already a
// mistake, but the failure if one went through unchecked is a 422 on the
// call that opens the pull request -- stranding a finished run's work on
// a branch nobody was told about, which is maxDescriptionBytes' own
// argument below.
const maxTitleBytes = 200

func capTitle(title string) string {
	if len(title) <= maxTitleBytes {
		return title
	}
	// ToValidUTF8 for capDescription's reason: a cut at a byte offset can
	// land inside a rune, and half a rune renders as a replacement
	// character.
	return strings.TrimSpace(strings.ToValidUTF8(title[:maxTitleBytes], "")) + "..."
}

// maxDescriptionBytes leaves room under GitHub's own 65536-byte limit on
// a pull request body for the footer and a little slack. Nothing grain
// writes should come close -- a branch would need a book's worth of
// commit messages -- but the failure if one did is a 422 on the call
// that opens the pull request, which would strand a finished run's work
// on a branch nobody was told about. A truncated description is a far
// better outcome than that.
const maxDescriptionBytes = 60000

func capDescription(description string) string {
	if len(description) <= maxDescriptionBytes {
		return description
	}
	// ToValidUTF8 rather than a bare slice: cutting at a byte offset can
	// land inside a rune, and half a rune is what GitHub renders as a
	// replacement character.
	return strings.ToValidUTF8(description[:maxDescriptionBytes], "") +
		"\n\n(Truncated -- the rest is in this branch's own commit messages.)"
}

// commitSummary is a commit message's first line, and commitBody is
// whatever follows it -- git's own convention, which is also what makes
// "this message explains something" a question that can be asked at all.
func commitSummary(message string) string {
	summary, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(summary)
}

func commitBody(message string) string {
	_, body, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(body)
}

// descriptionFooter is the line grain signs a description with, and the
// whole of the description when there is nothing else to say -- the same
// sentence the body used to consist of, so a pull request grain opened
// before any of this existed is still recognised as its own work.
//
// The task id, not an issue reference: a task has no issue to point a
// reader at any more, and its id is what `grain get` takes.
func descriptionFooter(taskID string) string {
	return fmt.Sprintf("Automated change for grain task %s.", taskID)
}

// issueDirectiveRe matches standard GitHub closing keywords referencing an issue number
// (e.g. Fixes #123, Closes owner/repo#123, resolve: #123).
var issueDirectiveRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?\s+(?:[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+)?#(\d+)\b`)

func hasIssueClosingDirective(text string, issue int) bool {
	if issue <= 0 {
		return false
	}
	matches := issueDirectiveRe.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if len(m) > 1 {
			if n, err := strconv.Atoi(m[1]); err == nil && n == issue {
				return true
			}
		}
	}
	return false
}

// pullRequestBody is a description plus grain's own footer, which is
// both an attribution and the marker grainAuthored reads back. When issue
// is non-zero and not already closed by the description, it injects "Fixes #<issue>"
// so GitHub automatically links and closes the issue upon pull request merge.
func pullRequestBody(description, taskID string, issue int) string {
	footer := descriptionFooter(taskID)
	var closing string
	if issue > 0 && !hasIssueClosingDirective(description, issue) {
		closing = fmt.Sprintf("Fixes #%d", issue)
	}

	if description == "" {
		if closing != "" {
			return closing + "\n\n---\n" + footer
		}
		return footer
	}

	if closing != "" {
		return description + "\n\n" + closing + "\n\n---\n" + footer
	}
	return description + "\n\n---\n" + footer
}

// grainAuthored reports whether body is one grain wrote -- its last
// non-empty line being the footer for this very task.
//
// It is what stands between refreshPullRequest and a human's own
// writing. A reviewer who rewrites a pull request's description, adds a
// test plan to it, or pastes in a decision made in review has said
// something grain cannot reconstruct from commit messages, and a refresh
// that overwrote it would destroy the more valuable of the two. An empty
// body counts as grain's: nothing is lost by filling one in, and a pull
// request grain opened before a run had committed anything worth
// describing is exactly the case that leaves one.
func grainAuthored(body, taskID string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return true
	}
	lines := strings.Split(trimmed, "\n")
	return strings.TrimSpace(lines[len(lines)-1]) == descriptionFooter(taskID)
}

// branchCommits is the commits branch carries over base -- the ones a
// pull request from one into the other would contain -- and whether they
// are known at all.
//
// known is false when there was no base to compare against or the read
// failed, and it is not the same as an empty list. An empty list is a
// real answer, and a consequential one: it means the branch adds nothing
// to its base, which is precisely what GitHub refuses to open a pull
// request for (EnsurePullRequest, and github.IsNoCommitsBetween). A
// failed read that returned the same empty slice would have the finish
// path end a task over an API hiccup, so the two are kept apart here
// rather than at each caller.
func branchCommits(client github.Client, repo model.RepoRef, base, branch string) ([]github.Commit, bool) {
	if base == "" {
		return nil, false
	}
	commits, err := client.CompareCommits(repo.Owner, repo.Name, base, branch)
	if err != nil {
		log.Printf("orchestrator: reading %s's commits over %s: %v -- "+
			"the pull request gets grain's plain one-line body instead", branch, base, err)
		return nil, false
	}
	return commits, true
}

// refreshPullRequest brings an already-open pull request's title and
// description up to date with the commits its branch now carries.
//
// This is the half of the restoration that v1 never needed. A run that
// calls open_pull_request opens its pull request mid-flight, deliberately
// (OpenPullRequestForTask's own doc comment: the point is seeing CI while
// there are still turns left to fix it), and everything it pushes
// afterwards -- including the commit that fixed what CI said -- lands on
// a pull request whose description was written before any of it existed.
// The finish path calls EnsurePullRequest again, so this runs there too,
// which is what makes the description a reviewer eventually reads the
// description of the finished change.
//
// The title moves for the same reason and on the same schedule. A run
// that opens its pull request after one exploratory commit and then works
// out what the change actually is would otherwise be stuck with the name
// that first commit gave it, which is the same staleness in one line
// instead of several -- and, since the title is how an agent names its
// own pull request at all (titleFromCommits), the one an agent would most
// want to correct.
//
// It writes nothing at all in three cases: a body a human has touched
// (grainAuthored), a branch whose commits say nothing (a failed read must
// never downgrade a good description to the bare footer, or a real title
// to nothing), and a title or description that already matches. The body
// is what is consulted about the human, for both: somebody who rewrote a
// description has taken this pull request's prose over, and there is no
// comparable marker on a title to read -- so a reviewer who renames a
// pull request and wants that rename to stick should say something in the
// body too. Errors are logged rather than returned, for the reason
// branchCommits' own are: neither line is worth failing a finish over,
// and the pull request is open either way.
func refreshPullRequest(ctx context.Context, store *model.Store, client github.Client, task model.Task, number int) {
	repo := *task.Target
	detail, err := client.GetPullRequest(repo.Owner, repo.Name, number)
	if err != nil {
		log.Printf("orchestrator: reading %s/%s#%d to refresh its description: %v",
			repo.Owner, repo.Name, number, err)
		return
	}
	if updated, retargeted, err := retargetTaskBase(ctx, store, client, task, detail.BaseRef); err == nil && retargeted {
		task = updated
	}
	if !grainAuthored(detail.Body, task.ID) {
		return
	}
	// The pull request's own base, not the task's: those differ on a task
	// whose base branch had vanished by the time it was opened, and
	// pullRequestBase retargeted it (finish.go). The commits worth
	// describing are the ones this pull request actually proposes.
	base := detail.BaseRef
	if base == "" {
		base, _, err = pullRequestBase(ctx, store, client, task)
		if err != nil {
			log.Printf("orchestrator: resolving %s's base to describe its pull request: %v", task.ID, err)
			return
		}
	}
	// One read of the branch for both lines, rather than one each: the
	// title and the description are the same commits arranged twice, and
	// two compares could disagree with each other about a branch a run is
	// still pushing to.
	commits, _ := branchCommits(client, repo, base, task.BranchName())
	if title := titleFromCommits(commits); title != "" && title != detail.Title {
		if err := client.UpdatePullRequestTitle(repo.Owner, repo.Name, number, title); err != nil {
			log.Printf("orchestrator: renaming %s/%s#%d: %v -- "+
				"it is still called what the branch's first commit called it",
				repo.Owner, repo.Name, number, err)
		}
	}
	description := describeCommits(commits)
	if description == "" {
		return
	}
	body := withLineage(pullRequestBody(description, task.ID, task.Issue), taskLineage(ctx, store, task))
	// Commit messages cannot say what images a run published, so the
	// section publishImages wrote is carried over rather than rebuilt.
	body = withImages(body, imagesSection(detail.Body))
	if body == detail.Body {
		return
	}
	if err := client.UpdatePullRequestBody(repo.Owner, repo.Name, number, body); err != nil {
		log.Printf("orchestrator: rewriting %s/%s#%d's description: %v -- "+
			"it still says what the branch looked like when it was opened",
			repo.Owner, repo.Name, number, err)
	}
}
