package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// Work trees on GitHub (docs/work-trees.md). Every task has an origin
// (model.WorkOrigin) -- the pull request, issue or grain task its tree of
// work came from -- and a parent when another task's run filed it. This
// file is what makes that tree readable from GitHub and git alone:
//
//   - every task's pull request description carries a section grain
//     writes itself (lineageSection): the task that filed it, the origin,
//     and the follow-up tasks it filed, with their state;
//   - that pull request carries an `origin/#<n>` label when the origin is
//     on GitHub (labelOrigin);
//   - the merge commit grain makes for it carries `Origin:` and
//     `Filed-by:` trailers (lineageTrailers);
//   - and the origin itself, when it is a pull request or an issue, has
//     one comment grain keeps up to date listing the whole tree
//     (reconcileWorkTrees).

const (
	lineageStart = "<!-- grain:lineage -->"
	lineageEnd   = "<!-- /grain:lineage -->"
	treeMarker   = "<!-- grain:work-tree -->"
)

// workTreeInterval is how often one origin's progress comment, and the
// descriptions of its tree's open pull requests, are looked at again: a
// rate limit, so a tree of fifty tasks changing state in one minute costs
// one edit rather than fifty.
const workTreeInterval = 2 * time.Minute

// settledTreeInterval is the same for a tree with nothing left to do,
// which can still move (somebody reopens a task) but rarely does.
const settledTreeInterval = time.Hour

// taskStatus is how a tree names where task stands (model.WorkTreeStatus),
// with the link to its pull request after "PR open" and "merged" -- and
// whether it counts toward "n of m done".
func taskStatus(ctx context.Context, store *model.Store, task model.Task) (string, bool, error) {
	status, done, err := store.WorkTreeStatus(ctx, task)
	if err != nil {
		return "", false, err
	}
	return statusWithLink(status, model.TaskPullRequest(task)), done, nil
}

// statusWithLink is a tree status as GitHub markdown: the pull request a
// "PR open" or "merged" task was opened as, linked after the word.
func statusWithLink(status string, pr *model.PullRequestRef) string {
	if status != model.TreeStatusPROpen && status != model.TreeStatusMerged {
		return status
	}
	link := ""
	if pr != nil {
		link = fmt.Sprintf("[%s#%d](https://github.com/%s/pull/%d)", pr.Repo, pr.Number, pr.Repo, pr.Number)
	}
	return status + " " + link
}

// lastPullRequest is the pull request task's work was opened as, nil if
// none.
func lastPullRequest(task model.Task) *model.PullRequestRef {
	return model.TaskPullRequest(task)
}

// originLink is origin as a markdown link: GitHub's own URL for a pull
// request or an issue (its /issues/ path serves both), or the task.
func originLink(origin model.WorkOrigin) string {
	if repo, n, ok := origin.GitHub(); ok {
		return fmt.Sprintf("[%s#%d](https://github.com/%s/issues/%d)", repo, n, repo, n)
	}
	if id := origin.Task(); id != "" {
		return "task " + id
	}
	return "none"
}

// taskRef names a task for a GitHub reader: its id, and its pull request
// when it has one, which is the link a reader can follow.
func taskRef(task model.Task) string {
	if pr := lastPullRequest(task); pr != nil {
		return fmt.Sprintf("task %s ([%s#%d](https://github.com/%s/pull/%d))", task.ID, pr.Repo, pr.Number, pr.Repo, pr.Number)
	}
	return "task " + task.ID
}

// lineageSection is the part of task's pull request description grain
// writes itself: who filed the task, where its tree began, and what it
// has filed for later.
func lineageSection(ctx context.Context, store *model.Store, task model.Task) (string, error) {
	children, err := store.ChildTasks(ctx, task.ID)
	if err != nil {
		return "", err
	}
	// A task nobody filed, with no origin and nothing filed by it, has no
	// tree to show: its description stays what it always was.
	if task.Parent() == "" && task.WorkOrigin == "" && len(children) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString(lineageStart + "\n")
	if parentID := task.Parent(); parentID != "" {
		parent, err := store.GetTask(ctx, parentID)
		if err != nil {
			return "", err
		}
		if parent != nil {
			fmt.Fprintf(&b, "Filed by: %s, %s\n", taskRef(*parent), treeTitle(parent.Title))
		} else {
			fmt.Fprintf(&b, "Filed by: task %s\n", parentID)
		}
	}
	// A task stored with no origin is its tree's root only when nobody
	// filed it: one from before origins existed, filed part way down a
	// tree, began where its filer's tree did (Store.TreeOrigin).
	origin := task.WorkOrigin
	if origin == "" && task.Parent() != "" {
		if origin, err = store.TreeOrigin(ctx, task.ID); err != nil {
			return "", err
		}
	}
	if origin != "" {
		fmt.Fprintf(&b, "Origin: %s\n", originLink(origin))
	} else {
		b.WriteString("Origin: this task\n")
	}
	if len(children) == 0 {
		b.WriteString("\nFollow-up tasks filed: none\n")
	} else {
		b.WriteString("\nFollow-up tasks filed:\n")
		for _, c := range children {
			status, _, err := taskStatus(ctx, store, c)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "- task %s: %s (%s)\n", c.ID, treeTitle(c.Title), status)
		}
	}
	b.WriteString(lineageEnd)
	return b.String(), nil
}

// taskLineage is lineageSection for a description being written, "" (and
// a log line) when it cannot be drawn: a pull request's prose is never
// worth failing a finish over (refreshPullRequest).
func taskLineage(ctx context.Context, store *model.Store, task model.Task) string {
	if store == nil {
		return ""
	}
	section, err := lineageSection(ctx, store, task)
	if err != nil {
		log.Printf("orchestrator: drawing %s's lineage for its pull request: %v", task.ID, err)
		return ""
	}
	return section
}

func treeTitle(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// withLineage puts section into body just above grain's footer, replacing
// a section an earlier write put there. The footer stays the last line,
// which is what grainAuthored reads.
func withLineage(body, section string) string {
	if section == "" {
		return body
	}
	if start := strings.Index(body, lineageStart); start >= 0 {
		if end := strings.Index(body[start:], lineageEnd); end >= 0 {
			return body[:start] + section + body[start+end+len(lineageEnd):]
		}
	}
	if i := strings.LastIndex(body, "\n---\n"); i >= 0 {
		return body[:i] + "\n\n" + section + "\n" + body[i:]
	}
	return section + "\n\n---\n" + body
}

// lineageTrailers are the trailers the merge commit for task's pull
// request carries, so `git log --grep 'Origin: …'` finds every change in
// a tree: `Origin:`, and `Filed-by:` for a task another task filed.
func lineageTrailers(task model.Task) []string {
	var out []string
	if task.WorkOrigin != "" {
		out = append(out, "Origin: "+task.WorkOrigin.Trailer())
	}
	if parent := task.Parent(); parent != "" {
		out = append(out, "Filed-by: task "+parent)
	}
	return out
}

// identityTrailers are the identity claims task carries, as pass-through
// metadata a change can be traced back to the people behind it by:
// `Requested-by:` for its requester (Task.Creator, as the UI or the
// local CLI set it), and `Approved-by:` for the human who approved it. An
// approval by grain itself (a schedule, auto-approval) names nobody and
// adds nothing.
//
// They ride along with lineageTrailers wherever those are written, and
// never on their own on a merge commit: a merge grain writes no message
// for keeps the one the forge would have given it, and a task that names
// a requester is no reason to replace that.
func identityTrailers(task model.Task) []string {
	var out []string
	if requester := strings.TrimSpace(task.Creator); requester != "" {
		out = append(out, "Requested-by: "+requester)
	}
	if a := task.Approval; a != nil && a.Actor.Kind == model.PrincipalHuman && strings.TrimSpace(a.Actor.ID) != "" {
		out = append(out, "Approved-by: "+strings.TrimSpace(a.Actor.ID))
	}
	return out
}

// CommitTrailers are every trailer a task's commits are asked to end
// with: identityTrailers alongside lineageTrailers.
func CommitTrailers(task model.Task) []string {
	return append(lineageTrailers(task), identityTrailers(task)...)
}

// labelOrigin applies task's `origin/#<n>` label to its pull request.
// GitHub creates a label it is asked to apply and does not yet have; a
// repository that refuses (or a token without the permission) costs a
// log line and nothing else.
func labelOrigin(client github.Client, task model.Task, number int) {
	if task.Target == nil {
		return
	}
	label := task.WorkOrigin.Label(*task.Target)
	if label == "" {
		return
	}
	if err := client.AddLabel(task.Target.Owner, task.Target.Name, number, label); err != nil {
		log.Printf("orchestrator: labelling %s#%d %q: %v -- the pull request goes unlabelled",
			task.Target, number, label, err)
	}
}

// renderWorkTree is the progress comment for origin: every task in its
// tree nested under the task that filed it, each with its state, and an
// "n of m done" summary. The tree itself is model.Store.WorkTree, the
// same one grain's task view draws (GET /api/tasks/{id}/tree).
func renderWorkTree(ctx context.Context, store *model.Store, origin model.WorkOrigin) (string, int, int, error) {
	tree, err := store.WorkTree(ctx, origin, nil)
	if err != nil || tree == nil {
		return "", 0, 0, err
	}
	var b strings.Builder
	b.WriteString(treeMarker + "\n")
	fmt.Fprintf(&b, "**Work from %s: %d of %d done**\n\n", originLink(origin), tree.Done, tree.Total)
	var walk func(nodes []*model.WorkTreeNode, depth int)
	walk = func(nodes []*model.WorkTreeNode, depth int) {
		for _, n := range nodes {
			check := " "
			if n.Done {
				check = "x"
			}
			fmt.Fprintf(&b, "%s- [%s] task %s: %s (%s)\n", strings.Repeat("  ", depth), check, n.Task.ID,
				treeTitle(n.Task.Title), statusWithLink(n.Status, n.PullRequest))
			walk(n.Children, depth+1)
		}
	}
	walk(tree.Roots, 0)
	b.WriteString("\n<sub>grain edits this comment as the work moves. `git log --grep 'Origin: " + origin.Trailer() + "'` lists what has landed.</sub>\n")
	return b.String(), tree.Done, tree.Total, nil
}

// commentEditor is the edit a progress comment needs, which
// github.RESTClient has and test fakes may not.
type commentEditor interface {
	UpdateComment(owner, repo string, commentID int64, body string) error
}

// reconcileWorkTrees keeps the progress comment on every GitHub origin,
// and the lineage section of every open pull request in its tree, in
// step with the tasks: created once, then edited in place, and looked at
// no more than once per workTreeInterval per origin.
func reconcileWorkTrees(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Client == nil {
		return nil
	}
	origins, err := deps.Store.GitHubWorkOrigins(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, origin := range origins {
		if err := syncWorkTree(ctx, deps.Store, deps.Client, origin, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// syncWorkTree is reconcileWorkTrees for one origin.
func syncWorkTree(ctx context.Context, store *model.Store, client github.Client, origin model.WorkOrigin, now time.Time) error {
	repo, number, ok := origin.GitHub()
	if !ok {
		return nil
	}
	prev, err := store.GetWorkOriginComment(ctx, origin)
	if err != nil {
		return err
	}
	if prev != nil && prev.CheckedAt != nil {
		interval := workTreeInterval
		if strings.Contains(prev.Body, settledMarker) {
			interval = settledTreeInterval
		}
		if now.Sub(*prev.CheckedAt) < interval {
			return nil
		}
	}
	body, done, total, err := renderWorkTree(ctx, store, origin)
	if err != nil || body == "" {
		return err
	}
	if done == total {
		body += settledMarker + "\n"
	}
	record := model.WorkOriginComment{Origin: origin, Body: body, CheckedAt: &now}
	if prev != nil {
		record.CommentID = prev.CommentID
	}
	switch {
	case prev != nil && prev.Body == body:
		// Nothing to say: just note that it was looked at.
	case record.CommentID == 0:
		id, err := client.CreateComment(repo.Owner, repo.Name, number, body)
		if err != nil {
			return fmt.Errorf("orchestrator: posting the progress comment on %s: %w", origin, err)
		}
		record.CommentID = int64(id)
	default:
		editor, ok := client.(commentEditor)
		if !ok {
			return nil
		}
		if err := editor.UpdateComment(repo.Owner, repo.Name, record.CommentID, body); err != nil {
			return fmt.Errorf("orchestrator: editing the progress comment on %s: %w", origin, err)
		}
	}
	if err := store.PutWorkOriginComment(ctx, record); err != nil {
		return err
	}
	if prev != nil && prev.Body == body {
		return nil
	}
	// Something in the tree moved: bring the lineage sections of its
	// open pull requests up to date too, since one of them lists the
	// task that moved among its follow-ups.
	tasks, err := store.TasksWithOrigin(ctx, origin)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		refreshLineage(ctx, store, client, t)
	}
	return nil
}

// settledMarker marks a progress comment on a tree with nothing left to
// do, which is looked at again less often.
const settledMarker = "<!-- grain:work-tree settled -->"

// refreshLineage rewrites the lineage section of task's open pull request
// when it no longer says what is true -- a follow-up's state moved. Only
// the section is touched, and only on a description grain wrote.
func refreshLineage(ctx context.Context, store *model.Store, client github.Client, task model.Task) {
	pr := lastPullRequest(task)
	if pr == nil {
		return
	}
	if obs, err := store.GetObservation(ctx, task.ID); err != nil || (obs != nil && (obs.PrMergedAt != nil || obs.PrClosedAt != nil)) {
		return
	}
	section, err := lineageSection(ctx, store, task)
	if err != nil {
		log.Printf("orchestrator: drawing %s's lineage: %v", task.ID, err)
		return
	}
	detail, err := client.GetPullRequest(pr.Repo.Owner, pr.Repo.Name, pr.Number)
	if err != nil || detail.State != "open" || !grainAuthored(detail.Body, task.ID) {
		return
	}
	body := withLineage(detail.Body, section)
	if body == detail.Body {
		return
	}
	if err := client.UpdatePullRequestBody(pr.Repo.Owner, pr.Repo.Name, pr.Number, body); err != nil {
		log.Printf("orchestrator: updating %s's lineage: %v", pr, err)
	}
}

// messageMerger is the merge that names its own commit message, which
// github.RESTClient has and test fakes may not.
type messageMerger interface {
	MergePullRequestWithMessage(owner, repo string, number int, headSHA, title, message string) error
}

// mergeTaskPullRequest merges a task's pull request the way
// github.Client.MergePullRequest does, with the task's lineage trailers
// (lineageTrailers) added to the merge commit's message under the pull
// request's title -- GitHub's own default message -- so the history of
// the base branch names every change's origin whether or not the agent
// wrote the trailers on its own commits. A task with none, or a client
// that cannot set the message, merges exactly as before.
func mergeTaskPullRequest(ctx context.Context, store *model.Store, client github.Client,
	repo model.RepoRef, number int, headSHA, taskID, prTitle string) error {

	var trailers []string
	if store != nil && taskID != "" {
		if task, err := store.GetTask(ctx, taskID); err == nil && task != nil {
			if trailers = lineageTrailers(*task); len(trailers) > 0 {
				trailers = append(trailers, identityTrailers(*task)...)
			}
		}
	}
	merger, ok := client.(messageMerger)
	if len(trailers) == 0 || !ok {
		return client.MergePullRequest(repo.Owner, repo.Name, number, headSHA)
	}
	if prTitle == "" {
		if detail, err := client.GetPullRequest(repo.Owner, repo.Name, number); err == nil {
			prTitle = detail.Title
		}
	}
	message := strings.Join(trailers, "\n")
	if prTitle != "" {
		message = prTitle + "\n\n" + message
	}
	return merger.MergePullRequestWithMessage(repo.Owner, repo.Name, number, headSHA, "", message)
}
