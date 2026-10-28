package ui

// The merge queue's speculative batches, as a person watching them sees
// them: GET /api/merge-batches, one object per batch in flight.
//
// It exists because of what batching looks like from outside without it.
// A batch's members each get the "batching" state on their own row and a
// sentence that names the throwaway ref, the commit and how many others
// are in it (orchestrator.setBatchState), which is enough to know that
// *this* change is being tested with others and nothing else. Three rows
// all saying "Testing with the rest of the batch" do not say that they
// are the same batch, which of them is first (and so which way a failure
// is bisected), or where the one run that decides all three of them is.
// That is a thing -- model.MergeBatch, one row per base branch -- and it
// had no way out of the database.
//
// The whole of it is a read. Nothing here cancels a batch, drops a member
// or forms one: the batch is the reconciler's own state machine
// (orchestrator.reconcileMergeBatches), it ends on its own within
// model.MaxMergeBatchAge whatever happens, and a button that reached into
// the middle of it would be a second thing deciding what the queue is
// testing. What a person needs from the UI is to be able to see the bet,
// which is what this answers.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// MergeBatch is one speculative merge in flight (model.MergeBatch) as the
// API reports it. Deliberately its own type rather than the model's:
// the members carry the task titles and pull request URLs a card has to
// draw, which are joins this package makes and the row does not hold.
type MergeBatch struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// Ref is the throwaway branch the batch was built on
	// (model.MergeBatchRef) and Commit its tip once every member had been
	// merged in -- the tree grain CI is actually judging. BaseCommit is
	// where Branch stood when the batch formed; a batch whose base has
	// moved since is abandoned rather than believed, so the two together
	// are what say whether this answer is still about anything.
	Ref        string `json:"ref"`
	BaseCommit string `json:"baseCommit"`
	Commit     string `json:"commit"`
	// RunID is grain CI's run against Commit, which the UI turns into a
	// link to that run's own page. The one run that decides every member
	// is the single most useful thing on the card: without it, following
	// a batch means guessing which of a repository's runs is the batch's
	// from its ref.
	RunID string `json:"runId,omitempty"`
	// StartedAt is when the run was asked for and FinishedAt when a
	// conclusion was first recorded. MaxAgeSeconds beside them (on the
	// envelope, since it is the same for every batch) is what makes the
	// elapsed time mean something.
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Conclusion is pkg/ci's own word for how the run ended and is empty
	// while it is still going -- which is also what Running says, spelled
	// out so a caller need not know that only "failure" is a failure and
	// that empty is the live state. Detail is that verdict in words.
	Conclusion string `json:"conclusion,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Running    bool   `json:"running"`
	// Splits is how many times this lineage has been halved looking for
	// the change that broke it: 0 for a batch formed fresh from the
	// queue, and the reason a batch of one is worth showing at all.
	Splits int `json:"splits,omitempty"`
	// Members are the changes in the batch, in the queue's own order --
	// first is first, which is the half a failure is bisected towards.
	Members []MergeBatchMember `json:"members"`
}

// MergeBatchMember is one change in a batch: what the row holds, plus
// enough about the task for a card to name it rather than print an id.
type MergeBatchMember struct {
	TaskID string `json:"task"`
	// Title is the task as the rest of the UI names it, empty where the
	// task has since been deleted -- a batch is transient and its members
	// are ordinary tasks, so this is a join that can miss rather than a
	// guarantee. Nothing here reports a member's own state: every member
	// of a batch is a finished task waiting on the queue, and the one
	// thing that differs between them -- where each stands in this batch
	// -- is the order they are reported in.
	Title string `json:"title,omitempty"`
	// Number is the pull request the merge will be made against. The
	// repository is the batch's own, so this and MergeBatch.Repo are the
	// "owner/name#123" the rest of the UI already knows how to turn into
	// a link (state.js's pullRequestUrl) -- reported as the number it is
	// rather than as a URL this package would have to invent, since which
	// host a pull request lives on is not pkg/ui's fact to state.
	Number int `json:"number,omitempty"`
	// HeadRef is the branch merged into the throwaway ref and HeadSHA its
	// tip as it stood at that moment. The merge is pinned to HeadSHA, so
	// a push that lands while the batch runs takes this member out rather
	// than merging a commit the batch never contained -- which is why the
	// commit is reported and not just the branch name.
	HeadRef string `json:"headRef,omitempty"`
	HeadSHA string `json:"head,omitempty"`
}

// MergeBatches is GET /api/merge-batches' whole answer.
type MergeBatches struct {
	// Batches is every batch grain has in flight, in
	// model.Store.ListMergeBatches' stable order, narrowed to one
	// repository where the caller asked for one. Never null, so a caller
	// can iterate it without checking.
	Batches []MergeBatch `json:"batches"`
	// MaxAgeSeconds is model.MaxMergeBatchAge: how long a batch may hold
	// its members before the queue gives up on it and goes back to
	// merging one at a time. It is the denominator for the elapsed time
	// on a running batch -- "22m" says nothing on its own -- and it is
	// reported rather than hard-coded in the browser so that changing the
	// bound changes what the card says.
	MaxAgeSeconds int `json:"maxAgeSeconds"`
}

// MergeBatchesFor reports the batches in flight, for one repository when
// repo names one and for the whole deployment when it is empty.
//
// A deployment with batching turned off (Settings.MergeBatchSize of 1)
// answers with no batches, the same as a deployment that simply has
// nothing queued -- there is no third state to report, since a batch row
// only ever exists because the queue made one.
func (c *Client) MergeBatchesFor(ctx context.Context, repo string) (MergeBatches, error) {
	out := MergeBatches{
		Batches:       []MergeBatch{},
		MaxAgeSeconds: int(model.MaxMergeBatchAge / time.Second),
	}
	var filter model.RepoRef
	narrowed := strings.TrimSpace(repo) != ""
	if narrowed {
		parsed, err := c.parseRepo(repo)
		if err != nil {
			return MergeBatches{}, err
		}
		filter = parsed
	}
	rows, err := c.Store.ListMergeBatches(ctx)
	if err != nil {
		return MergeBatches{}, err
	}
	for _, row := range rows {
		if narrowed && !filter.Same(row.Repo) {
			continue
		}
		view, err := c.mergeBatchView(ctx, row)
		if err != nil {
			return MergeBatches{}, err
		}
		out.Batches = append(out.Batches, view)
	}
	return out, nil
}

// mergeBatchView is one row with its members' titles read alongside it.
//
// One GetTask per member, which is at most model.MaxMergeBatchSize reads
// per batch and a handful of batches on the busiest deployment -- the
// same order as the per-task reads the task list itself already makes,
// and this is asked for by a pane that polls rather than by every view.
func (c *Client) mergeBatchView(ctx context.Context, row model.MergeBatch) (MergeBatch, error) {
	out := MergeBatch{
		Repo:       row.Repo.String(),
		Branch:     row.Branch,
		Ref:        row.Ref,
		BaseCommit: row.BaseCommit,
		Commit:     row.Commit,
		RunID:      row.RunID,
		StartedAt:  row.StartedAt,
		FinishedAt: row.FinishedAt,
		Conclusion: row.Conclusion,
		Detail:     row.Detail,
		Running:    row.Running(),
		Splits:     row.Splits,
		Members:    []MergeBatchMember{},
	}
	for _, m := range row.Members {
		member := MergeBatchMember{
			TaskID:  m.TaskID,
			Number:  m.Number,
			HeadRef: m.HeadRef,
			HeadSHA: m.HeadSHA,
		}
		task, err := c.Store.GetTask(ctx, m.TaskID)
		if err != nil {
			return MergeBatch{}, err
		}
		if task != nil {
			member.Title = task.Title
		}
		out.Members = append(out.Members, member)
	}
	return out, nil
}

// handleListMergeBatches answers GET /api/merge-batches, optionally
// narrowed with ?repo=owner/name the way GET /api/ci/runs is.
func (s *Server) handleListMergeBatches(w http.ResponseWriter, r *http.Request) {
	batches, err := s.tasks.MergeBatchesFor(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, batches)
}
