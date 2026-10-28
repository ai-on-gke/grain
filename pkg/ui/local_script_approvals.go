package ui

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

// LocalScriptApproval is one local-script call (pkg/localexec, a script
// declared approve: true) waiting for a person, or answered by one. The
// run's forked mcpserver posts it (POST
// /api/tasks/{id}/local-script-approvals), waits on it, and runs the
// script only once it is approved; the task pane, the inbox and the
// chat show it with Approve and Deny (docs/local-scripts.md).
type LocalScriptApproval struct {
	ID     string `json:"id"`
	TaskID string `json:"taskId"`
	// Call is the run's own id for the call (localexec.Request.ID).
	Call       string   `json:"call"`
	Tool       string   `json:"tool"`
	Script     string   `json:"script"`
	Args       []string `json:"args"`
	StdinBytes int      `json:"stdinBytes"`
	// StdinPreview is the start of the call's stdin (at most
	// localScriptStdinPreviewMax bytes), StdinTruncated whether it is
	// less than all of it, and StdinSHA256 the hash of the whole: what
	// a person approves a script that reads its instructions from stdin
	// on.
	StdinPreview   string    `json:"stdinPreview,omitempty"`
	StdinTruncated bool      `json:"stdinTruncated,omitempty"`
	StdinSHA256    string    `json:"stdinSha256,omitempty"`
	Deadline       time.Time `json:"deadline"`
	Requested      time.Time `json:"requested"`
	// Status is pending, approved, denied, or expired: the call stopped
	// waiting (its deadline, or the run ended) before anybody answered.
	Status     string     `json:"status"`
	AnsweredBy string     `json:"answeredBy,omitempty"`
	AnsweredAt *time.Time `json:"answeredAt,omitempty"`
}

// The approval statuses.
const (
	LocalScriptPending  = "pending"
	LocalScriptApproved = "approved"
	LocalScriptDenied   = "denied"
	LocalScriptExpired  = "expired"
)

// localScriptKeep is how long an answered approval stays listed after
// the call it answered stopped waiting, so the pane can show what became
// of it; the record that outlives it is the comment Answer posts.
const localScriptKeep = 10 * time.Minute

// LocalScriptApprovals holds the approvals of every live run on this
// daemon. In memory: a waiting call lives exactly as long as its run's
// mcpserver, which is a child of this process, so an approval that
// survived a restart would be one nothing is waiting on.
type LocalScriptApprovals struct {
	mu      sync.Mutex
	seq     int64
	ver     int64
	byID    map[string]*LocalScriptApproval
	changed map[string]chan struct{}
}

func (a *LocalScriptApprovals) init() {
	if a.byID == nil {
		a.byID = map[string]*LocalScriptApproval{}
		a.changed = map[string]chan struct{}{}
	}
}

// Request records a new pending approval.
func (a *LocalScriptApprovals) Request(r LocalScriptApproval, now time.Time) LocalScriptApproval {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	a.prune(now)
	a.seq++
	a.ver++
	r.ID = "lsa-" + strconv.FormatInt(a.seq, 10)
	r.Status = LocalScriptPending
	r.Requested = now
	r.AnsweredBy, r.AnsweredAt = "", nil
	if r.Args == nil {
		r.Args = []string{}
	}
	a.byID[r.ID] = &r
	a.changed[r.ID] = make(chan struct{})
	return r
}

func (a *LocalScriptApprovals) prune(now time.Time) {
	for id, r := range a.byID {
		if r.Status == LocalScriptPending && now.After(r.Deadline) {
			a.settle(r, LocalScriptExpired, "", now)
		}
		if r.Status != LocalScriptPending && now.Sub(r.Deadline) > localScriptKeep {
			delete(a.byID, id)
			delete(a.changed, id)
			a.ver++
		}
	}
}

func (a *LocalScriptApprovals) settle(r *LocalScriptApproval, status, by string, now time.Time) {
	r.Status, r.AnsweredBy = status, by
	at := now
	r.AnsweredAt = &at
	a.ver++
	if ch := a.changed[r.ID]; ch != nil {
		close(ch)
		a.changed[r.ID] = nil
	}
}

// version is a monotonic counter bumped whenever an approval is requested,
// settled, or pruned. taskDetailCache folds it into the store's ChangeToken so
// a cached GET /api/tasks/{id} rebuilds when an in-memory local-script approval
// arrives or expires without a database write.
func (a *LocalScriptApprovals) version(now time.Time) int64 {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.byID) > 0 {
		a.prune(now)
	}
	return a.ver
}

// Get returns approval id of task.
func (a *LocalScriptApprovals) Get(task, id string) (LocalScriptApproval, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.byID[id]
	if !ok || r.TaskID != task {
		return LocalScriptApproval{}, false
	}
	return *r, true
}

// ForTask lists task's approvals, oldest first.
func (a *LocalScriptApprovals) ForTask(task string, now time.Time) []LocalScriptApproval {
	return a.list(now, func(r *LocalScriptApproval) bool { return r.TaskID == task })
}

// Pending lists every pending approval, oldest first.
func (a *LocalScriptApprovals) Pending(now time.Time) []LocalScriptApproval {
	return a.list(now, func(r *LocalScriptApproval) bool { return r.Status == LocalScriptPending })
}

func (a *LocalScriptApprovals) list(now time.Time, keep func(*LocalScriptApproval) bool) []LocalScriptApproval {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	a.prune(now)
	var out []LocalScriptApproval
	for _, r := range a.byID {
		if keep(r) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Requested.Equal(out[j].Requested) {
			return out[i].Requested.Before(out[j].Requested)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

var errLocalScriptAnswered = validationErrorf("this local-script call is no longer waiting for an answer")

// Answer settles a pending approval, or says it is not pending.
func (a *LocalScriptApprovals) Answer(task, id string, approve bool, by string, now time.Time) (LocalScriptApproval, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	a.prune(now)
	r, ok := a.byID[id]
	if !ok || r.TaskID != task {
		return LocalScriptApproval{}, &NotFoundError{ID: id}
	}
	if r.Status != LocalScriptPending {
		return *r, errLocalScriptAnswered
	}
	status := LocalScriptDenied
	if approve {
		status = LocalScriptApproved
	}
	a.settle(r, status, by, now)
	return *r, nil
}

// Expire settles a pending approval as expired: its call stopped waiting.
func (a *LocalScriptApprovals) Expire(task, id string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r, ok := a.byID[id]; ok && r.TaskID == task && r.Status == LocalScriptPending {
		a.settle(r, LocalScriptExpired, "", now)
	}
}

// Wait blocks until approval id is answered, ctx ends, or max passes,
// and returns it as it then stands.
func (a *LocalScriptApprovals) Wait(ctx context.Context, task, id string, max time.Duration) (LocalScriptApproval, bool) {
	a.mu.Lock()
	r, ok := a.byID[id]
	if !ok || r.TaskID != task {
		a.mu.Unlock()
		return LocalScriptApproval{}, false
	}
	ch := a.changed[id]
	a.mu.Unlock()
	if ch != nil {
		timer := time.NewTimer(max)
		defer timer.Stop()
		select {
		case <-ch:
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	return a.Get(task, id)
}

// localScriptApprovals is the server's registry, made on first use.
func (c *Client) localScriptApprovals() *LocalScriptApprovals {
	c.localScriptsOnce.Do(func() {
		if c.Config.LocalScriptApprovals != nil {
			c.localScripts = c.Config.LocalScriptApprovals
		} else {
			c.localScripts = &LocalScriptApprovals{}
		}
	})
	return c.localScripts
}

// localScriptApprovalRequest is the body of POST
// /api/tasks/{id}/local-script-approvals: localexec.Request as the
// run's mcpserver sends it.
type localScriptApprovalRequest struct {
	ID             string    `json:"id"`
	Tool           string    `json:"tool"`
	Script         string    `json:"script"`
	Args           []string  `json:"args"`
	Stdin          int       `json:"stdin_bytes"`
	StdinPreview   string    `json:"stdin_preview"`
	StdinTruncated bool      `json:"stdin_truncated"`
	StdinSHA256    string    `json:"stdin_sha256"`
	Deadline       time.Time `json:"deadline"`
}

// localScriptStdinPreviewMax is localexec.StdinPreviewMax, restated for
// the same reason as localScriptsNamePattern.
const localScriptStdinPreviewMax = 4096

// localScriptMaxWait bounds a request's deadline: a script's timeout is
// operator-declared, but nothing should sit in the inbox for a day.
const localScriptMaxWait = 24 * time.Hour

// errLocalScriptByPerson is what a signed-in person who tries to post a
// request hears: requests are the run's to make.
var errLocalScriptByPerson = forbiddenErrorf("a local-script approval is requested by the task's own run, not by a person")

// errLocalScriptByRun is what a run that tries to answer hears.
var errLocalScriptByRun = forbiddenErrorf("a local-script call is approved or denied by a person who may act on the task, never by a run")

func (s *Server) handleRequestLocalScriptApproval(w http.ResponseWriter, r *http.Request) {
	if requestUser(r) != "" {
		writeClientError(w, errLocalScriptByPerson)
		return
	}
	var req localScriptApprovalRequest
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	task, err := s.tasks.Store.GetTask(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if task == nil {
		writeClientError(w, &NotFoundError{ID: id})
		return
	}
	if p, ok := PrincipalFromContext(r.Context()); ok && p.Kind == model.PrincipalAgent && p.ID != id {
		writeClientError(w, forbiddenErrorf("task %s's run may not ask for approvals on task %s", p.ID, id))
		return
	}
	if !localScriptsGranted(*task) {
		writeClientError(w, validationErrorf("task %s holds no local-scripts capability", id))
		return
	}
	if strings.TrimSpace(req.Tool) == "" || strings.TrimSpace(req.Script) == "" {
		writeClientError(w, validationErrorf("tool and script are required"))
		return
	}
	// The run's mcpserver already bounds the preview; this bounds what
	// the inbox holds whatever posts it.
	if len(req.StdinPreview) > localScriptStdinPreviewMax {
		cut := localScriptStdinPreviewMax
		for cut > 0 && !utf8.RuneStart(req.StdinPreview[cut]) {
			cut--
		}
		req.StdinPreview, req.StdinTruncated = req.StdinPreview[:cut], true
	}
	now := s.tasks.now()
	if req.Deadline.IsZero() || req.Deadline.After(now.Add(localScriptMaxWait)) {
		req.Deadline = now.Add(localScriptMaxWait)
	}
	approval := s.tasks.localScriptApprovals().Request(LocalScriptApproval{
		TaskID: id, Call: req.ID, Tool: req.Tool, Script: req.Script, Args: req.Args,
		StdinBytes: req.Stdin, StdinPreview: req.StdinPreview, StdinTruncated: req.StdinTruncated,
		StdinSHA256: req.StdinSHA256, Deadline: req.Deadline,
	}, now)
	writeJSON(w, http.StatusOK, approval)
}

func localScriptsGranted(t model.Task) bool {
	for _, g := range t.Grants {
		if _, ok := localScriptsName(g.Capability); ok {
			return true
		}
	}
	return false
}

// localScriptsNamePattern is localscripts.Name's, restated because
// pkg/capability/localscripts serves MCP tools and this package is what
// the MCP server talks to.
var localScriptsNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// localScriptsName is the declaration a local-scripts:<name> capability
// names.
func localScriptsName(id string) (string, bool) {
	name, ok := strings.CutPrefix(id, "local-scripts:")
	if !ok || !localScriptsNamePattern.MatchString(name) {
		return "", false
	}
	return name, true
}

// handleWaitLocalScriptApproval answers once the approval is answered,
// or after at most ?wait= seconds (default and cap 25), whichever is
// first -- a long poll the mcpserver repeats until the call's deadline.
func (s *Server) handleWaitLocalScriptApproval(w http.ResponseWriter, r *http.Request) {
	max := 25 * time.Second
	if v, err := strconv.Atoi(r.URL.Query().Get("wait")); err == nil && v >= 0 && time.Duration(v)*time.Second < max {
		max = time.Duration(v) * time.Second
	}
	approval, ok := s.tasks.localScriptApprovals().Wait(r.Context(), r.PathValue("id"), r.PathValue("approval"), max)
	if !ok {
		writeClientError(w, &NotFoundError{ID: r.PathValue("approval")})
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

// handleExpireLocalScriptApproval is the mcpserver saying its call
// stopped waiting.
func (s *Server) handleExpireLocalScriptApproval(w http.ResponseWriter, r *http.Request) {
	if requestUser(r) != "" {
		writeClientError(w, errLocalScriptByPerson)
		return
	}
	reg := s.tasks.localScriptApprovals()
	reg.Expire(r.PathValue("id"), r.PathValue("approval"), s.tasks.now())
	approval, ok := reg.Get(r.PathValue("id"), r.PathValue("approval"))
	if !ok {
		writeClientError(w, &NotFoundError{ID: r.PathValue("approval")})
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

type localScriptAnswerRequest struct {
	Approve bool `json:"approve"`
}

// handleAnswerLocalScriptApproval approves or denies a waiting call. The
// route's own task-write check (Server.route) is who may act on the
// task; this refuses a run on top of it, since the run is the one
// asking. The answer is recorded on the task as a comment by whoever
// gave it.
func (s *Server) handleAnswerLocalScriptApproval(w http.ResponseWriter, r *http.Request) {
	if p, ok := PrincipalFromContext(r.Context()); ok && p.Kind == model.PrincipalAgent {
		writeClientError(w, errLocalScriptByRun)
		return
	}
	var req localScriptAnswerRequest
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	by := requestUser(r)
	approval, err := s.tasks.localScriptApprovals().Answer(id, r.PathValue("approval"), req.Approve, by, s.tasks.now())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if err := s.tasks.AddCommentWithAuthor(r.Context(), id, localScriptAnswerComment(approval), nil, by); err != nil {
		writeClientError(w, fmt.Errorf("the call was %s, but recording that on the task failed: %w", approval.Status, err))
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

func localScriptAnswerComment(a LocalScriptApproval) string {
	var b strings.Builder
	b.WriteString(a.Script)
	for _, arg := range a.Args {
		b.WriteByte(' ')
		b.WriteString(strconv.Quote(arg))
	}
	if a.StdinBytes > 0 {
		fmt.Fprintf(&b, " (and %d bytes on stdin)", a.StdinBytes)
	}
	verb := "Denied"
	if a.Status == LocalScriptApproved {
		verb = "Approved"
	}
	out := fmt.Sprintf("%s local script call %s (`%s`):\n\n```\n%s\n```", verb, a.Call, a.Tool, b.String())
	if a.StdinBytes > 0 {
		out += "\n\n" + localScriptStdinText(a)
	}
	return out
}

// localScriptStdinText is a call's stdin as its approval showed it: the
// hash of the whole, then the preview in a fence no backtick run inside
// it can close.
func localScriptStdinText(a LocalScriptApproval) string {
	var b strings.Builder
	fmt.Fprintf(&b, "stdin (%d bytes, sha256 `%s`)", a.StdinBytes, a.StdinSHA256)
	if a.StdinTruncated {
		fmt.Fprintf(&b, ", truncated: first %d of %d bytes shown", len(a.StdinPreview), a.StdinBytes)
	}
	fence := "```"
	for strings.Contains(a.StdinPreview, fence) {
		fence += "`"
	}
	fmt.Fprintf(&b, ":\n\n%s\n%s\n%s", fence, a.StdinPreview, fence)
	return b.String()
}

// handleListLocalScriptApprovals is every pending approval on a task the
// caller may act on, for the inbox.
func (s *Server) handleListLocalScriptApprovals(w http.ResponseWriter, r *http.Request) {
	if p, ok := PrincipalFromContext(r.Context()); ok && p.Kind == model.PrincipalAgent {
		writeClientError(w, errLocalScriptByRun)
		return
	}
	out := []LocalScriptApproval{}
	for _, a := range s.tasks.localScriptApprovals().Pending(s.tasks.now()) {
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}
