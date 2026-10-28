package ui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// ReleaseNote is one week's release notes as the API reports them: what
// this deployment shipped that week, who wrote it up and when
// (model.ReleaseNote, and model/release_note.go for what they are and
// why they are a row rather than a file somebody pushes).
//
// Text is empty in a listing and carries the whole note when one week is
// asked for, exactly as MemoryDocument's is: a year of weeks is a
// megabyte of prose, and the pane wants the list first and the week the
// reader clicked second.
type ReleaseNote struct {
	Period        string     `json:"period"`
	Title         string     `json:"title,omitempty"`
	Text          string     `json:"text,omitempty"`
	Length        int        `json:"length"`
	CreatedAt     *time.Time `json:"createdAt,omitempty"`
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy     string     `json:"updatedBy,omitempty"`
	UpdatedByKind string     `json:"updatedByKind,omitempty"`
}

// ClusterStats is one week's numbers as the API reports them
// (model.ClusterStats): grain's own count of what it did, which the
// weekly job writes its notes around and the pane shows above them.
//
// The pane shows them for the same reason the notes quote them: a
// reader who has just been told the week was busy can see the figure
// that was meant, and a week whose notes were never written still has
// something to say for itself.
// PullRequestsMeasured is how many of those merges the lines are
// actually summed over (model.ClusterStats.PullRequestsMeasured): fewer
// than pullRequestsMerged means grain has no size on file for some of
// the week's merges, and the pane says the figure is partial rather
// than showing a total that is quietly too small.
type ClusterStats struct {
	Period               string     `json:"period"`
	Since                time.Time  `json:"since"`
	Until                time.Time  `json:"until"`
	PullRequestsMerged   int        `json:"pullRequestsMerged"`
	PullRequestsMeasured int        `json:"pullRequestsMeasured"`
	LinesAdded           int        `json:"linesAdded"`
	LinesDeleted         int        `json:"linesDeleted"`
	TasksCompleted       int        `json:"tasksCompleted"`
	TasksFiled           int        `json:"tasksFiled"`
	Runs                 int        `json:"runs"`
	RunsSucceeded        int        `json:"runsSucceeded"`
	AgentHours           float64    `json:"agentHours"`
	Repos                []RepoWork `json:"repos"`
}

// RepoWork is one repository's share of that week, most merged pull
// requests first.
type RepoWork struct {
	Repo                 string `json:"repo"`
	PullRequestsMerged   int    `json:"pullRequestsMerged"`
	PullRequestsMeasured int    `json:"pullRequestsMeasured"`
	LinesAdded           int    `json:"linesAdded"`
	LinesDeleted         int    `json:"linesDeleted"`
	TasksCompleted       int    `json:"tasksCompleted"`
	TasksFiled           int    `json:"tasksFiled"`
	Runs                 int    `json:"runs"`
}

// releaseNoteRequest is PUT /api/release-notes' body: which week, the
// headline, the notes themselves and who is saying so.
//
// Period empty means the week that just ended -- resolved here rather
// than by the caller, so that the weekly job, a person in the pane and
// the tool an agent writes through all agree about which week "this
// one" is (model.LastCompleteReleaseNotePeriod).
//
// Text replaces rather than appends, and an empty text withdraws the
// week outright, for memoryRequest's reasons. Author/AuthorKind are
// memoryRequest's own pair, meaning the same thing and defaulted the
// same way: cmd/grain's daemonReleaseNotes names the run whose
// write_release_notes call this is, and a body that names nobody is
// recorded as this deployment's own actor.
type releaseNoteRequest struct {
	Period     string `json:"period,omitempty"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	Author     string `json:"author,omitempty"`
	AuthorKind string `json:"authorKind,omitempty"`
}

// errReleaseNoteLong and errReleaseNoteTitleLong are how notes that have
// outgrown what they are get refused -- ValidationErrors, so a 400 with
// a sentence the caller (in practice a model, through
// write_release_notes) can act on in the same turn.
var (
	errReleaseNoteLong = validationErrorf(
		"a week's release notes must be at most %d characters: past that they have stopped being "+
			"notes and become the task list they were written from, which grain already has",
		model.MaxReleaseNoteLength)
	errReleaseNoteTitleLong = validationErrorf(
		"a week's headline must be at most %d characters: it is the one line shown in a list of "+
			"weeks, so it is a headline rather than a summary",
		model.MaxReleaseNoteTitleLength)
	errReleaseNoteTitleMissing = validationErrorf(
		"a week's release notes need a title: the pane lists weeks by their headline, and a week " +
			"nobody can tell the subject of is one nobody opens")
)

// ListReleaseNotes is every week that has been written up, newest first
// and without their text -- what the Release notes pane lists and what a
// caller checking whether Monday's run published anything reads.
func (c *Client) ListReleaseNotes(ctx context.Context) ([]ReleaseNote, error) {
	stored, err := c.Store.ListReleaseNotes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ReleaseNote, 0, len(stored))
	for _, n := range stored {
		out = append(out, releaseNoteFrom(n))
	}
	return out, nil
}

// GetReleaseNote is one week whole, text included. An empty period is
// the week that just ended, for releaseNoteRequest's reason.
//
// A week nothing has been written about answers with its period and no
// text rather than a 404: "this week has no notes" is an answer the pane
// renders, and a deployment that has only just installed this build has
// nothing but weeks like that.
func (c *Client) GetReleaseNote(ctx context.Context, period string) (ReleaseNote, error) {
	period, err := c.releaseNotePeriod(ctx, period)
	if err != nil {
		return ReleaseNote{}, err
	}
	stored, err := c.Store.GetReleaseNote(ctx, period)
	if err != nil {
		return ReleaseNote{}, err
	}
	if stored == nil {
		return ReleaseNote{Period: period}, nil
	}
	return releaseNoteFrom(*stored), nil
}

// PutReleaseNote publishes one week, replacing whatever was published
// for it before, and withdraws the week when the text is empty.
//
// by is who to record as having written it, and the zero Principal is
// this deployment's own actor -- the same defaulting PutMemory does, and
// for the same reason: a browser says nothing about who it is, and a run
// does (cmd/grain's daemonReleaseNotes).
func (c *Client) PutReleaseNote(ctx context.Context, period, title, text string,
	by model.Principal) (ReleaseNote, error) {

	period, err := c.releaseNotePeriod(ctx, period)
	if err != nil {
		return ReleaseNote{}, err
	}
	title, text = strings.TrimSpace(title), strings.TrimSpace(text)
	switch {
	case len(text) > model.MaxReleaseNoteLength:
		return ReleaseNote{}, errReleaseNoteLong
	case len(title) > model.MaxReleaseNoteTitleLength:
		return ReleaseNote{}, errReleaseNoteTitleLong
	case text != "" && title == "":
		return ReleaseNote{}, errReleaseNoteTitleMissing
	}
	note := model.ReleaseNote{
		Period: period, Title: title, Text: text,
		UpdatedAt: c.now(), UpdatedBy: c.memoryAuthorOrActor(ctx, by),
	}
	if err := c.Store.PutReleaseNote(ctx, note); err != nil {
		return ReleaseNote{}, err
	}
	return c.GetReleaseNote(ctx, period)
}

// ClusterStats counts one week of this deployment's work. An empty
// period is the week that just ended.
//
// The window comes from the period rather than from "the last seven
// days", so that the numbers and the notes are demonstrably about the
// same week however long after it either is read
// (model.ReleaseNotePeriodRange).
func (c *Client) ClusterStats(ctx context.Context, period string) (ClusterStats, error) {
	period, err := c.releaseNotePeriod(ctx, period)
	if err != nil {
		return ClusterStats{}, err
	}
	loc, err := c.scheduleLocation(ctx)
	if err != nil {
		return ClusterStats{}, err
	}
	since, until, err := model.ReleaseNotePeriodRange(period, loc)
	if err != nil {
		return ClusterStats{}, validationErrorf("%s", err)
	}
	stats, err := c.Store.ClusterStats(ctx, since, until)
	if err != nil {
		return ClusterStats{}, err
	}
	out := ClusterStats{
		Period: period, Since: stats.Since, Until: stats.Until,
		PullRequestsMerged:   stats.PullRequestsMerged,
		PullRequestsMeasured: stats.PullRequestsMeasured,
		LinesAdded:           stats.LinesAdded,
		LinesDeleted:         stats.LinesDeleted,
		TasksCompleted:       stats.TasksCompleted,
		TasksFiled:           stats.TasksFiled,
		Runs:                 stats.Runs,
		RunsSucceeded:        stats.RunsSucceeded,
		AgentHours:           stats.AgentTime.Hours(),
		Repos:                make([]RepoWork, 0, len(stats.Repos)),
	}
	for _, w := range stats.Repos {
		out.Repos = append(out.Repos, RepoWork{
			Repo: w.Repo.String(), PullRequestsMerged: w.PullRequestsMerged,
			PullRequestsMeasured: w.PullRequestsMeasured,
			LinesAdded:           w.LinesAdded,
			LinesDeleted:         w.LinesDeleted,
			TasksCompleted:       w.TasksCompleted, TasksFiled: w.TasksFiled, Runs: w.Runs,
		})
	}
	return out, nil
}

// releaseNotePeriod resolves the period a caller named, or the week that
// just ended for one that named none -- on the deployment's own wall
// clock, since which week it is depends on where the deployment thinks
// it is.
//
// One place rather than three, because a period is a key: the pane, the
// tool and the weekly job all have to mean the same week by "this one",
// and a caller that worked it out for itself would be a second opinion
// about the calendar.
func (c *Client) releaseNotePeriod(ctx context.Context, period string) (string, error) {
	period = strings.TrimSpace(period)
	if period == "" {
		loc, err := c.scheduleLocation(ctx)
		if err != nil {
			return "", err
		}
		return model.LastCompleteReleaseNotePeriod(c.now(), loc), nil
	}
	if err := model.ValidateReleaseNotePeriod(period); err != nil {
		return "", validationErrorf("%s", err)
	}
	return period, nil
}

func releaseNoteFrom(n model.ReleaseNote) ReleaseNote {
	out := ReleaseNote{
		Period: n.Period, Title: n.Title, Text: n.Text, Length: n.Length,
		UpdatedBy: n.UpdatedBy.ID, UpdatedByKind: string(n.UpdatedBy.Kind),
	}
	if out.Length == 0 {
		out.Length = len(n.Text)
	}
	if !n.CreatedAt.IsZero() {
		at := n.CreatedAt
		out.CreatedAt = &at
	}
	if !n.UpdatedAt.IsZero() {
		at := n.UpdatedAt
		out.UpdatedAt = &at
	}
	return out
}

// handleListReleaseNotes answers GET /api/release-notes.
func (s *Server) handleListReleaseNotes(w http.ResponseWriter, r *http.Request) {
	notes, err := s.tasks.ListReleaseNotes(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notes)
}

// handlePutReleaseNote answers PUT /api/release-notes, with the week in
// the body rather than in the path: a write that names no week means the
// one that just ended, and a route that required one in the path would
// make every caller work out which week that is.
func (s *Server) handlePutReleaseNote(w http.ResponseWriter, r *http.Request) {
	var req releaseNoteRequest
	if !readJSON(w, r, &req) {
		return
	}
	by, err := memoryAuthor(req.Author, req.AuthorKind)
	if err != nil {
		writeClientError(w, err)
		return
	}
	note, err := s.tasks.PutReleaseNote(r.Context(), req.Period, req.Title, req.Text, by)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, note)
}

// handleGetReleaseNote answers GET /api/release-notes/{period}, text
// included -- what opening a week in the pane costs.
func (s *Server) handleGetReleaseNote(w http.ResponseWriter, r *http.Request) {
	note, err := s.tasks.GetReleaseNote(r.Context(), r.PathValue("period"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, note)
}

// handleClusterStats answers GET /api/release-notes/stats, for the week
// in ?period= or the week that just ended.
//
// A route under the notes rather than beside them because that is what
// the numbers are for: they are the week's own figures, read by the pane
// that shows the week and by the job that writes it up.
func (s *Server) handleClusterStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.tasks.ClusterStats(r.Context(), r.URL.Query().Get("period"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
