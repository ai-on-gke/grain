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

package ui

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/admincaps"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/cifault"
	"github.com/ai-on-gke/grain/pkg/forkhead"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Client is the model code the server wraps: reading and writing tasks in
// a model.Store (list, create, modify, approve, attach/detach a
// capability, comment, close/reopen). Server is a thin JSON/HTTP shim
// over this and nothing more, so a caller that has no reason to speak
// HTTP -- cmd/grain's CLI, most notably -- gets the exact same behaviour
// with no server to run.
//
// Every method takes a context because every one of them is a database
// call now. That is the visible half of the change: this used to be a
// GitHub client, and a task's identity used to be an issue number.
type Client struct {
	Config Config
	Store  *model.Store
	// localScripts is the local-script approvals of this daemon's live
	// runs (localScriptApprovals): Config.LocalScriptApprovals, or one of
	// its own.
	localScripts     *LocalScriptApprovals
	localScriptsOnce sync.Once
	// Now is the clock, injectable so tests get deterministic timestamps.
	// nil means time.Now().UTC().
	Now func() time.Time
	// Lifecycles is this deployment's lifecycle registry, if it has one.
	// nil answers `none` for every repository, which is what every
	// repository gets today.
	Lifecycles       Lifecycles
	githubBranchesMu sync.Mutex
	// modelDiscoveryMu keeps AdvertiseDiscoveredModels to one pass at a
	// time, so a save's kick and the periodic pass never both write.
	modelDiscoveryMu sync.Mutex
	// ModelDiscoveryListTimeout bounds each lister a discovery pass asks;
	// zero means DefaultModelDiscoveryListTimeout.
	ModelDiscoveryListTimeout time.Duration
	githubBranches            map[string]*githubBranchCacheEntry
	// sandboxHealthLast is the last completed sweep of
	// Config.Sandboxes.Health, which every caller of GET /api/sandboxes
	// shares rather than probing every live guest again for itself --
	// see sandboxHealth, and sandboxHealthTTL for why that is safe.
	// sandboxHealthSweep is the one currently running, if any, which is
	// both what a caller arriving mid-sweep waits on and what keeps a
	// second sweep off the same guests. The last sweep outlives its
	// freshness window on purpose: it is what a caller is answered from
	// while a sick guest holds the next sweep open (sandboxHealthWait,
	// sandboxHealthMaxAge).
	// They live on the Client, beside the branch cache above, so that the
	// coalescing covers every path into it and not just one server's.
	sandboxHealthMu    sync.Mutex
	sandboxHealthLast  *sandboxHealthEntry
	sandboxHealthSweep *sandboxHealthEntry
	// ciFaults is the last computation of what CI is failing on several
	// branches at once (cifault.go), shared by every poll of the CI pane
	// for ciFaultTTL -- it costs a read of every recent failing run's
	// output and the pane asks every five seconds. prFiles is the other
	// half of it: what each candidate pull request touches, which is a
	// GitHub call per change, remembered for pkg/cifault's own FileTTL
	// and built on first use (Client.pullRequestFiles).
	ciFaultsMu  sync.Mutex
	ciFaults    *ciFaultCache
	ciFaultRuns map[string]ci.Run
	// ciStorage is the last measurement of what the stored CI history
	// occupies (cistorage.go), shared by every poll of the Storage
	// section for ciHistoryStorageTTL -- it reads the length of every
	// stored step's output, and that pane asks every five seconds.
	ciStorage ciHistoryStorageCache
	// ciDisk is the same for the two directories CI keeps on the volume
	// itself -- the job cache and the collected artifacts (cistorage.go,
	// grain/task-276). Measuring either is a readdir and a stat per file
	// over as many as ci.DefaultCacheMaxEntries of them, on the host
	// whose disk pressure is why anybody has this pane open, so it is
	// cached for the same ciHistoryStorageTTL and for the same reason.
	ciDisk    ciDiskUsageCache
	prFilesMu sync.Mutex
	prFiles   *cifault.FileCache
}

// NewClient builds a Client over a store.
func NewClient(cfg Config, store *model.Store) *Client {
	return &Client{Config: cfg, Store: store}
}

// teamTargetRepos is the deployment's target repos (model.Config), nil
// for all of them. teamID is not read: the default team's were the
// deployment's, and schema 24 moved them to grain_config.
func (c *Client) teamTargetRepos(ctx context.Context, _ string) []string {
	if c.Store != nil {
		if repos, err := c.Store.TargetRepos(ctx); err == nil {
			return repos
		}
	}
	return nil
}

// parseRepo is model.ParseRepo plus the one thing that package cannot
// do for itself: spelling the repo the way this deployment already
// spells it. Every repo name this client is about to store goes through
// here rather than through model.ParseRepo directly -- a task's target
// and its reads, a schedule's, a template's binding, a suite run's, and
// the repo a per-repo config row is keyed by.
func (c *Client) parseRepo(text string) (model.RepoRef, error) {
	parsed, err := model.ParseRepo(text)
	if err != nil {
		return model.RepoRef{}, &ValidationError{err: err}
	}
	return c.canonicalRepo(parsed), nil
}

// canonicalRepo is parseRepo's second half on a repo that is already
// parsed: repo as this deployment spells it, or repo unchanged when it
// spells it nowhere.
func (c *Client) canonicalRepo(repo model.RepoRef) model.RepoRef {
	if c.Store != nil {
		if targets, err := c.Store.TargetRepos(context.Background()); err == nil {
			for _, entry := range targets {
				if allowed, err := model.ParseRepo(entry); err == nil && allowed.Same(repo) {
					return allowed
				}
			}
		}
	}
	if d := c.Config.DefaultTarget; d != nil && d.Same(repo) {
		return *d
	}
	return repo
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

// ValidationError marks a request Client rejected before touching the
// store -- a blank title, an unparseable repo, an unknown capability ID.
// Server maps this to a 400; a CLI caller can print Error() on its own
// without a stack of database detail behind it.
type ValidationError struct {
	err error
	// fields keys the refusal by the request field it is about, where
	// one is known: a capability grant's argument path
	// (grantFieldErrors, capability_grants.go).
	fields map[string]string
}

func (e *ValidationError) Error() string { return e.err.Error() }
func (e *ValidationError) Unwrap() error { return e.err }

func validationErrorf(format string, a ...any) error {
	return &ValidationError{err: fmt.Errorf(format, a...)}
}

// validateBase refuses a base branch no run could ever check out, so a
// task, schedule or template naming one is turned away when it is
// written rather than filed and left to fail once a worker picks it up
// (orchestrator's own "is not a usable base branch"). field names the
// request field in the message. Empty is always fine: it means the
// repo's default branch. The rule is model.ValidBranchName, the stricter
// form of the orchestrator's gitSafe, so anything accepted here is also
// accepted at dispatch.
func validateBase(field, base string) error {
	if base == "" || model.ValidBranchName(base) {
		return nil
	}
	return validationErrorf(
		"%s %q is not a usable branch name: use letters, digits, '.', '_', '-' and '/' only, "+
			"starting with a letter or digit, with no '..', '//' or trailing '/'", field, base)
}

// ForbiddenError marks a request rejected because the caller lacks the required permissions.
// Server maps this to a 403 Forbidden.
type ForbiddenError struct{ err error }

func (e *ForbiddenError) Error() string { return e.err.Error() }
func (e *ForbiddenError) Unwrap() error { return e.err }

func forbiddenErrorf(format string, a ...any) error {
	return &ForbiddenError{err: fmt.Errorf(format, a...)}
}

// NotFoundError marks a task ID with no task behind it. Server maps this
// to a 404 -- the one case where the caller's mistake and the store's own
// trouble need telling apart, which github.Error's status used to answer.
//
// message overrides the default "no task "+ID rendering when set --
// HTTPClient's own httpError (httpclient.go) only ever has the server's
// already-formatted message text to reconstruct this from, not a bare
// ID, and formatting that message through Error() a second time doubled
// it into "no task no task <id>" for every 404 an HTTPClient caller (the
// CLI, and the browser frontend, which displays the JSON body's "error"
// field verbatim) ever saw.
type NotFoundError struct {
	ID      string
	message string
}

func (e *NotFoundError) Error() string {
	if e.message != "" {
		return e.message
	}
	return "no task " + e.ID
}

// UnavailableError is a 503 as HTTPClient reads it back: the server is
// there but cannot answer yet -- a daemon whose CI service is still
// starting (ci.ErrNotUp) -- and asking again shortly is the remedy.
// ci.IsNotUp recognises it by Unavailable.
type UnavailableError struct {
	message string
}

func (e *UnavailableError) Error() string { return e.message }

// Unavailable marks this as a refusal worth retrying.
func (e *UnavailableError) Unavailable() bool { return true }

// AttachmentUpload is one file carried by a CreateTaskRequest or an
// AddComment call (bwsalmon/agents#522) -- Content is base64, matching
// how the frontend already reads a chosen File (FileReader.
// readAsDataURL, with the leading "data:...;base64," stripped) rather
// than widening this JSON API to multipart/form-data for just this one
// field.
type AttachmentUpload struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

const (
	// MaxAttachmentSize bounds one file's decoded size. Generous enough
	// for a screenshot or a small repro zip, small enough that one
	// request's worth of attachments never turns a single sqlite write
	// into one carrying tens of megabytes -- the size a real limit would
	// need to be to matter is a product decision nobody has made yet; this
	// one exists so a request has *some* bound rather than none.
	MaxAttachmentSize = 10 * 1024 * 1024
	// MaxAttachmentsPerRequest bounds how many files one task or one
	// comment can carry -- plenty for "a screenshot and a repro zip",
	// while keeping one request from turning into an unbounded number of
	// store writes.
	MaxAttachmentsPerRequest = 10
)

// errPriorityNotPositive is the refusal for a task priority below 1, on
// create and on edit alike. 0 is refused rather than read as "the
// default": leaving the field out is how a caller asks for that, and
// the Edit form, the host CLI and the sandbox CLI all say the same.
const errPriorityNotPositive = "priority must be a positive integer (1 or more; leave it out for the default, 5)"

// decodeAttachments validates and base64-decodes uploads into the
// model.Attachment shape Store.AddAttachment writes -- TaskID and
// CommentID are left for the caller to fill in once it knows them
// (CreateTask has no task id yet when it validates; AddComment has no
// comment id), so this only ever fails on the upload itself, before
// either write happens.
func decodeAttachments(uploads []AttachmentUpload, now time.Time) ([]model.Attachment, error) {
	if len(uploads) > MaxAttachmentsPerRequest {
		return nil, validationErrorf("at most %d attachments per request, got %d", MaxAttachmentsPerRequest, len(uploads))
	}
	out := make([]model.Attachment, 0, len(uploads))
	for _, u := range uploads {
		name := sanitizeAttachmentFilename(u.Filename)
		content, err := base64.StdEncoding.DecodeString(u.Content)
		if err != nil {
			return nil, validationErrorf("attachment %q is not valid base64: %v", u.Filename, err)
		}
		if len(content) > MaxAttachmentSize {
			return nil, validationErrorf("attachment %q is %d bytes, over the %d byte limit", u.Filename, len(content), MaxAttachmentSize)
		}
		contentType := strings.TrimSpace(u.ContentType)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		out = append(out, model.Attachment{
			Filename: name, ContentType: contentType,
			Size: int64(len(content)), Content: content, CreatedAt: now,
		})
	}
	return out, nil
}

// sanitizeAttachmentFilename strips any directory component a client
// sent -- orchestrator.placeAttachments joins this straight onto a
// sandbox path, and a filename is display text a human (or a browser's
// file picker) chose, never something entitled to say where it lands.
// Both separators are stripped regardless of host OS, since a name here
// came from a browser or a CLI flag, not a filesystem walk.
func sanitizeAttachmentFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	if len(name) > 200 {
		name = name[len(name)-200:]
	}
	return name
}

// capabilityLookup is the attached capability servers a capability
// reference is normalized against (docs/capabilities-spec.md §4.2.1),
// or nil in a process that attaches none -- where only a reference's
// syntax can be checked, and admission normalizes it again anyway.
func (c *Client) capabilityLookup() protocol.Lookup {
	if l, ok := c.Config.CapabilityServers.(interface {
		Lookup(string) (*protocol.Server, error)
	}); ok && l != nil {
		return grainserver.Lookup(l.Lookup)
	}
	return nil
}

// normalizeCapabilityID is id as a task records it: an in-process name
// as it is -- or, for a retired one, as the api grant it became
// (model.CanonicalLegacyGrant) -- and a capability reference resolved and
// validated against its server and in recorded form
// (model.NormalizeCapabilityNames).
func (c *Client) normalizeCapabilityID(id string) (string, error) {
	id = model.CanonicalLegacyGrant(id)
	if !model.IsCapabilityRef(id) {
		return id, nil
	}
	lookup := c.capabilityLookup()
	if lookup == nil && grainserver.IsInProcessGrant(id) {
		lookup = grainserver.Lookup(nil)
	}
	names, err := model.NormalizeCapabilityNames([]string{id}, lookup)
	if err != nil {
		return "", validationErrorf("%v", err)
	}
	return names[0], nil
}

// normalizeCapabilityIDs expands 1-to-many retired legacy grants (such as
// deployment-health -> [api:deployment:read, grain#health={}]) and normalizes
// each resulting capability ID.
func (c *Client) normalizeCapabilityIDs(id string) ([]string, error) {
	expanded := model.CanonicalLegacyGrants(id)
	out := make([]string, 0, len(expanded))
	for _, part := range expanded {
		normalized, err := c.normalizeCapabilityID(part)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

func (c *Client) capabilityByID(id string) (Capability, bool) {
	if model.IsCapabilityRef(id) {
		if model.CheckCapabilityGrantSyntax(id) != nil {
			return Capability{}, false
		}
		ref := model.CapabilityGrantRef(id)
		return Capability{ID: id, Name: ref, Description: "Granted by the capability server " + ref}, true
	}
	for _, capability := range c.Config.Capabilities {
		if capability.ID == id {
			return capability, true
		}
	}
	if name, ok := model.GCPKeyName(id); ok && name != "" {
		return Capability{
			ID:          id,
			Name:        "GCP key (" + name + ")",
			Description: "Mint a short-lived GCP service-account key (" + name + ") for this task and place it in its sandbox",
		}, true
	}
	// An agent API grant (agent_api.go), a repo-limited one included:
	// the scopes and presets are offered rows of their own, but
	// api:<scope>@<repo> is spelled per repository and so only here.
	if row, ok := apiCapability(id); ok {
		return row, true
	}
	if name, ok := localScriptsName(id); ok {
		return Capability{
			ID:   id,
			Name: "Local scripts (" + name + ")",
			Description: "Serve the scripts of the " + name + " local-scripts declaration as tools that run on the node host " +
				"(docs/local-scripts.md); never granted to a scheduled run",
		}, true
	}
	return Capability{}, false
}

// closedTime returns when a task was closed, or its ending/creation time if unknown.
func closedTime(t Task) time.Time {
	if t.ClosedAt != nil {
		return *t.ClosedAt
	}
	if t.EndedAt != nil {
		return *t.EndedAt
	}
	if t.CreatedAt != nil {
		return *t.CreatedAt
	}
	return time.Time{}
}

type redBaseLookup struct {
	broken     map[string]bool
	byRepairID map[string]string
}

func (c *Client) redBaseBranches(ctx context.Context) redBaseLookup {
	out := redBaseLookup{
		broken:     map[string]bool{},
		byRepairID: map[string]string{},
	}
	if c == nil || c.Store == nil {
		return out
	}
	if ciRows, err := c.Store.ListBaseBranchCI(ctx); err == nil {
		for _, r := range ciRows {
			if r.Broken() || (r.Confirmation != model.BaseCICleared &&
				(r.Conclusion == model.BaseBranchCIFailure || r.Conclusion == "errored")) {
				out.broken[strings.ToLower(r.Repo.String())+":"+r.Branch] = true
				if r.RepairTaskID != "" {
					out.byRepairID[r.RepairTaskID] = r.Branch
				}
			}
		}
	}
	if relRows, err := c.Store.ListBaseBranchRelease(ctx); err == nil {
		for _, r := range relRows {
			if r.Failing() || (r.Conclusion != "" && r.Conclusion != "success" && r.Conclusion != "skipped") {
				out.broken[strings.ToLower(r.Repo.String())+":"+r.Branch] = true
			}
		}
	}
	return out
}

func (r redBaseLookup) forTask(t model.Task) string {
	if b, ok := r.byRepairID[t.ID]; ok && b != "" {
		return b
	}
	if t.Target == nil {
		return ""
	}
	isRepair := model.FixesBaseBranch(t) || model.GrainBaseRepair(t)
	title := strings.TrimSpace(t.Title)
	if strings.HasPrefix(title, "Fix grain CI on ") || strings.HasPrefix(title, "Fix base branch release") {
		isRepair = true
	}
	tagBranch := ""
	for _, tag := range t.Tags {
		for _, prefix := range []string{"base-ci-repair:", "base-release-triage:"} {
			if rest, ok := strings.CutPrefix(tag, prefix); ok {
				isRepair = true
				if idx := strings.LastIndex(rest, ":"); idx >= 0 && idx+1 < len(rest) {
					tagBranch = rest[idx+1:]
				}
			}
		}
	}
	if !isRepair {
		return ""
	}
	branch := strings.TrimSpace(t.Base)
	if branch == "" {
		branch = tagBranch
	}
	if branch == "" {
		branch = "main"
	}
	if r.broken[strings.ToLower(t.Target.String())+":"+branch] {
		return branch
	}
	return ""
}

// ListTasks returns every task: open tasks in backlog order --
// Store.ListTasks' own order untouched, ascending OrderKey, which is
// top-to-bottom the order Store.Ready dispatches in -- and closed tasks
// sorted by time closed, newest first.
//
// Whatever runs next is at the top, the pull
// requests the merge queue is about to land at the very top of that
// (Store.MoveToFrontOfBacklog), and the work furthest out is at the
// bottom.
//
// There is no display flip left to apply (grain/task-201). This used to
// hand back the reverse of the store's order -- newest first -- unless
// model.Config.NewestFirst was set, so a list read in the opposite
// direction to the one grain works through it: the task at the top was
// the last one that would run, and "what is grain about to do" meant
// scrolling to the end. Reading the list downwards is reading the future
// forwards, which is what makes the merge queue's own order at the head
// of it (README's "what is grain about to finish, and in what order")
// legible at all.
//
// NewestFirst survives as what it always really was underneath: where
// new work joins the backlog, not how the backlog is drawn. Off, the
// default, files a new task at the end of the list, behind everything
// already queued; on files it at the front so it runs next
// (Store.OrderKeyForNewTask, Client.CreateTask). Either way the list is
// this one order.
func (c *Client) ListTasks(ctx context.Context) ([]Task, error) {
	tasks, err := c.Store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	states, err := c.ReadStates(ctx)
	if err != nil {
		return nil, err
	}
	// One States call already answers "is X closed?" for every task in
	// the store, so a list of many tasks costs nothing extra to also
	// carry each one's blocked signal -- unlike Task below, which has no
	// reason to fetch every task's state just to render one.
	closed := make(map[string]bool, len(states))
	for id, st := range states {
		closed[id] = st == model.StateClosed
	}
	// Same trade for the merge queue's own "gave up on this one" signal:
	// one query over every task_observation row that has it set, rather
	// than a per-task read.
	mergeQueueBlocked, err := c.Store.MergeQueueBlocked(ctx)
	if err != nil {
		return nil, err
	}
	// And once more for "the merge queue is having this one repaired",
	// which is the other thing a row has to say about a task the queue is
	// driving rather than merely watching.
	repairing, err := c.Store.MergeQueueRepairing(ctx)
	if err != nil {
		return nil, err
	}
	mergeQueueStates, err := c.Store.MergeQueueStates(ctx)
	if err != nil {
		return nil, err
	}
	// And for the Run now requests still outstanding, so a row under a
	// pause can say a task has been asked for rather than that it waits
	// for somebody to ask.
	runNow, err := c.Store.RunNowRequested(ctx)
	if err != nil {
		return nil, err
	}
	// And once more for what each running task says it is doing: one
	// query over the live runs that have a synopsis, rather than a read
	// per task, on the same reasoning as the three above.
	activity, err := c.Store.TaskActivity(ctx)
	if err != nil {
		return nil, err
	}
	grantWaits, err := c.Store.GrantWaits(ctx)
	if err != nil {
		return nil, err
	}
	oomRetries, err := c.Store.OOMRetrying(ctx)
	if err != nil {
		return nil, err
	}
	// And once more for the review each task has attached: one read of
	// the template list, which is a handful of rows a deployment writes
	// by hand, rather than a GetTemplate per task naming one.
	reviewNames, err := c.templateNames(ctx)
	if err != nil {
		return nil, err
	}
	// And once more for when each task started and ended, which is what a
	// timeline over the backlog is drawn from (model.TaskSpan): a task
	// that has neither is simply absent from the map and reads back with
	// no span, the way it already does with no activity.
	spans, err := c.Store.TaskSpans(ctx)
	if err != nil {
		return nil, err
	}
	// And once more for when each task entered the state it is in now,
	// which is what a stall note is measured from (model.StateTimes) --
	// two queries for the whole store rather than a read of every task's
	// runs and observation, on the same reasoning as the rest.
	stateTimes, err := c.Store.StateTimes(ctx)
	if err != nil {
		return nil, err
	}
	// One clock for the whole list: two rows drawn from the same read
	// should not disagree about whether the patience between them has
	// been crossed.
	now := c.now()
	redBases := c.redBaseBranches(ctx)
	// And once more for operator pauses: one read of task_pause.
	pauses, err := c.Store.PausedSet(ctx)
	if err != nil {
		return nil, err
	}
	// And once more for the asked tasks whose question is a stopped turn.
	turnsStopped, err := c.Store.TurnsStopped(ctx)
	if err != nil {
		return nil, err
	}
	// And once more for the one line each asked task's question is shown
	// by.
	questionSummaries, err := c.Store.PendingQuestionSummaries(ctx)
	if err != nil {
		return nil, err
	}
	// And once for goals, which name what a resting goal task waits for.
	goalKinds, err := c.Store.TaskGoalKinds(ctx)
	if err != nil {
		return nil, err
	}
	var open, closedTasks []Task
	for _, t := range tasks {
		t.GoalKind = goalKinds[t.ID]
		var blockedAt *time.Time
		if at, ok := mergeQueueBlocked[t.ID]; ok {
			blockedAt = &at
		}
		var doing *model.RunActivity
		if a, ok := activity[t.ID]; ok {
			doing = &a
		} else if wait, ok := grantWaits[t.ID]; ok && states[t.ID] == model.StateQueued {
			doing = grantWaitActivity(wait)
		} else if at, ok := oomRetries[t.ID]; ok && states[t.ID] == model.StateQueued {
			doing = oomRetryActivity(at)
		}
		task := taskFrom(t, states[t.ID], closed, blockedAt, repairing[t.ID], mergeQueueStates[t.ID], doing,
			reviewNames[t.ReviewTemplateID], spans[t.ID], c.stallNote(states[t.ID], stateTimes[t.ID], now))
		if at, ok := runNow[t.ID]; ok {
			task.RunNowAt = &at
		}
		task.TurnStopped = task.State == model.StateAwaitingReply && turnsStopped[t.ID]
		if task.State == model.StateAwaitingReply {
			task.QuestionSummary = questionSummaries[t.ID]
		}
		var wait *model.GrantWait
		if w, ok := grantWaits[t.ID]; ok {
			wait = &w
		}
		task = c.withReasons(task, t, listObservation(mergeQueueStates[t.ID], blockedAt), wait)
		if p, ok := pauses[t.ID]; ok {
			task.withPause(p)
		}
		if task.State != model.StateClosed {
			task.RedBaseBranch = redBases.forTask(t)
		}
		if task.State == model.StateClosed {
			closedTasks = append(closedTasks, task)
		} else {
			open = append(open, task)
		}
	}
	sort.SliceStable(closedTasks, func(i, j int) bool {
		timeI := closedTime(closedTasks[i])
		timeJ := closedTime(closedTasks[j])
		if !timeI.Equal(timeJ) {
			return timeI.After(timeJ)
		}
		return closedTasks[i].ID < closedTasks[j].ID
	})
	return append(open, closedTasks...), nil
}

// ReadStates reads all task states from the store, streaming them in chunks
// via StatesPage to prevent high memory spikes on large backlogs.
func (c *Client) ReadStates(ctx context.Context) (map[string]model.State, error) {
	out := make(map[string]model.State)
	err := c.Store.EachState(ctx, 2000, func(id string, state model.State) error {
		out[id] = state
		return nil
	})
	return out, err
}

// StatesPage retrieves a paginated window of task states from the store.
func (c *Client) StatesPage(ctx context.Context, limit int, cursor string) (map[string]model.State, string, error) {
	return c.Store.StatesPage(ctx, limit, cursor)
}

// EachState streams task states page by page to fn with bounded memory allocation.
func (c *Client) EachState(ctx context.Context, pageSize int, fn func(id string, state model.State) error) error {
	return c.Store.EachState(ctx, pageSize, fn)
}

// stallNote is Task.Stall for one task: what its state has to say for
// itself, given when the task entered it.
//
// Three ways to say nothing, and all three are the ordinary case rather
// than a failure. This deployment's UI may not have been handed the
// deadlines at all (Config.Stall, nil under `grain demo`); the state may
// be one whose beginning the record cannot place, which
// model.StateTimes.Since answers nil for -- every state a person owns
// (a deferral counts only once it has a wake time), and a queued task approved before Task.ApprovedAt existed; and the
// state may simply be inside its patience, which is what the note itself
// answers "" for.
func (c *Client) stallNote(state model.State, times model.StateTimes, now time.Time) string {
	if c.Config.Stall == nil {
		return ""
	}
	since := times.Since(state)
	if since == nil {
		return ""
	}
	return c.Config.Stall(state, *since, now)
}

// withReasons fills in out's two sub-cases (Task.Reason,
// Task.MergeQueueReason) off what the caller has already read about t:
// its observation (nil for none), and the grant wait dispatch recorded
// for it (nil for none). The open dependencies are out's own BlockedBy,
// which taskFrom has just resolved.
func (c *Client) withReasons(out Task, t model.Task, obs *model.Observation, wait *model.GrantWait) Task {
	if obs != nil && obs.MergeQueueState != "" && obs.MergeQueueReason != "" {
		reason := Reason{Reason: obs.MergeQueueReason}
		if c.Config.Reasons != nil {
			if known := c.Config.Reasons.MergeReason(obs.MergeQueueReason); known.Reason != "" {
				reason = known
			}
		}
		out.MergeQueueReason = &reason
	}
	if c.Config.Reasons == nil {
		return out
	}
	if reason := c.Config.Reasons.TaskReason(t, out.State, obs, out.BlockedBy, wait); reason.Reason != "" {
		out.Reason = &reason
	}
	return out
}

// listObservation is the part of a task's observation a list read has to
// hand (MergeQueueStates, MergeQueueBlocked), as the observation
// withReasons reads -- nil where it has neither.
func listObservation(mergeQueue model.MergeQueueActivity, blockedAt *time.Time) *model.Observation {
	if mergeQueue.State == "" && blockedAt == nil {
		return nil
	}
	return &model.Observation{MergeQueueState: mergeQueue.State, MergeQueueReason: mergeQueue.Reason,
		MergeQueueBlockedAt: blockedAt}
}

// templateNames is every template's ID mapped to its Name, for a caller
// rendering many tasks at once -- ListTasks' own "one query for
// everyone" trade, applied to the review a task can name
// (model.Task.ReviewTemplateID). A task naming none, or naming one that
// has since been deleted, simply misses the map and reads back with no
// name, which Task.ReviewTemplateName documents as the two cases it
// cannot tell apart from the id alone.
func (c *Client) templateNames(ctx context.Context) (map[string]string, error) {
	list, err := c.Store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(list))
	for _, t := range list {
		names[t.ID] = t.Name
	}
	return names, nil
}

// reviewTemplateName is templateNames for a single task: the Name of the
// template it names as its review, empty when it names none or when that
// template is gone. One read rather than the whole list, matching
// closedTargets' own "a few queries for one task" trade.
func (c *Client) reviewTemplateName(ctx context.Context, t model.Task) (string, error) {
	if t.ReviewTemplateID == "" {
		return "", nil
	}
	tmpl, err := c.Store.GetTemplate(ctx, t.ReviewTemplateID)
	if err != nil {
		return "", err
	}
	if tmpl == nil {
		return "", nil
	}
	return tmpl.Name, nil
}

// newestFirst reads model.Config.NewestFirst fresh from the store on
// every call: a setting a UI toggles is expected to be honoured by the
// very next task creation, not by the next restart, and this package is
// the one that consumes it, so this is where re-reading it belongs. A
// fresh deployment with no grain_config row yet (nil) reads as false --
// model.Config's own zero value, and the end of the backlog grain has
// always filed new work at.
//
// Every other setting earns the same "no restart" treatment wherever its
// own consumer is: orchestrator.RunCycle re-reads MaxWorkers/MaxMergers and
// MaxAgentTurns every cycle, cmd/grain's dispatchConfig re-reads the
// agent framework and its model per dispatch, and its liveConfig
// re-applies the rest once per reconcile tick (that type's own doc
// comment, including the two settings that genuinely cannot be applied
// live and are reported to this pane as needing a restart).
//
// The Settings pane is no longer the only thing that writes it: a task
// filed with CreateTaskRequest.AtFront set remembers that choice here
// (Store.SetNewestFirst), so what this reads is usually where the last
// task added went rather than a toggle anybody had to go looking for.
func (c *Client) newestFirst(ctx context.Context) (bool, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return false, err
	}
	return cfg != nil && cfg.NewestFirst, nil
}

// autoSubmitLimits is this deployment's bounds on how far a tree of
// self-approved proposals may grow (model.AutoSubmitLimits), read fresh
// per call for newestFirst's own reason above. A deployment with no
// stored row yet, or one whose row predates those two columns, is
// bounded by grain's own defaults rather than by nothing
// (model.AutoSubmitLimitsOf).
func (c *Client) autoSubmitLimits(ctx context.Context) (model.AutoSubmitLimits, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return model.AutoSubmitLimits{}, err
	}
	return model.AutoSubmitLimitsOf(cfg), nil
}

// consoleSessionQueuesProposals is whether proposer is a standing
// console session this deployment has said may queue its own work
// (model.ConsoleSessionQueuesProposals), read fresh per call for
// newestFirst's reason above: it is a settings row, and a deployment that
// unticked it a second ago means it now.
//
// The settings read is here and the rule is in pkg/model because
// orchestrator.consoleSessionApprovesProposals asks the same question of
// the same row for a proposal replayed at the end of a run, and
// model.DirectorSessionBody tells the session the answer. A gate one
// filing path applies and the other does not is not a gate, and a prompt
// written from a third reading of the field is a prompt that can assert
// what neither gate grants -- which is what grain/task-311 found.
func (c *Client) consoleSessionQueuesProposals(ctx context.Context, proposer model.Task) (bool, error) {
	if _, ok := model.ConsoleSessionIDOf(proposer); !ok {
		return false, nil
	}
	stored, err := c.Store.GetConfig(ctx)
	if err != nil {
		return false, err
	}
	cfg := model.DefaultConfig()
	if stored != nil {
		cfg = *stored
	}
	return model.ConsoleSessionQueuesProposals(model.ConsoleSessionsOr(cfg.ConsoleSessions), proposer), nil
}

// Task returns one task's list-shaped view.
func observationMergeQueueState(obs *model.Observation) model.MergeQueueActivity {
	if obs == nil {
		return model.MergeQueueActivity{}
	}
	return model.MergeQueueActivity{
		State:     obs.MergeQueueState,
		Note:      obs.MergeQueueNote,
		At:        obs.MergeQueueStateAt,
		StalledAt: obs.MergeQueueStalledAt,
		Reason:    obs.MergeQueueReason,
	}
}

func (c *Client) Task(ctx context.Context, id string) (Task, error) {
	t, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if t == nil {
		return Task{}, &NotFoundError{ID: id}
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return Task{}, err
	}
	closed, err := c.closedTargets(ctx, t.Links)
	if err != nil {
		return Task{}, err
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return Task{}, err
	}
	var blockedAt *time.Time
	if obs != nil {
		blockedAt = obs.MergeQueueBlockedAt
	}
	activity, err := c.Store.TaskActivityOf(ctx, id)
	if err != nil {
		return Task{}, err
	}
	var wait *model.GrantWait
	if state == model.StateQueued {
		wait, err = c.Store.GrantWaitOf(ctx, id)
		if err != nil {
			return Task{}, err
		}
		if activity == nil && wait != nil {
			activity = grantWaitActivity(*wait)
		}
	}
	if activity == nil && state == model.StateQueued {
		at, err := c.Store.OOMRetryingOf(ctx, id)
		if err != nil {
			return Task{}, err
		}
		if at != nil {
			activity = oomRetryActivity(*at)
		}
	}
	reviewName, err := c.reviewTemplateName(ctx, *t)
	if err != nil {
		return Task{}, err
	}
	span, err := c.Store.TaskSpanOf(ctx, id)
	if err != nil {
		return Task{}, err
	}
	runs, err := c.Store.Runs(ctx, id)
	if err != nil {
		return Task{}, err
	}
	goal, err := c.Store.TaskGoal(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if goal != nil {
		t.GoalKind = goal.Kind
	}
	task := c.withReasons(taskFrom(*t, state, closed, blockedAt, obs.RepairInFlight(), observationMergeQueueState(obs), activity, reviewName, span,
		c.stallNote(state, model.StateTimesFrom(*t, obs, runs), c.now())), *t, obs, wait)
	task.Goal = goal
	if obs != nil {
		task.RunNowAt = obs.RunNowAt
	}
	if state == model.StateAwaitingReply {
		comments, err := c.Store.Comments(ctx, id)
		if err != nil {
			return Task{}, err
		}
		task.TurnStopped = turnStopped(state, obs, comments)
		task.QuestionSummary = questionSummary(state, obs, comments)
	}
	if err := c.attachPause(ctx, &task); err != nil {
		return Task{}, err
	}
	if task.State != model.StateClosed {
		task.RedBaseBranch = c.redBaseBranches(ctx).forTask(*t)
	}
	return task, nil
}

// closedTargets resolves whether each of a task's own blocking-link
// targets reads closed -- a handful of extra queries for one task rather
// than States' whole-store read, matching ListTasks' own "a few queries
// per task" trade-off (store.go's hydrate).
func (c *Client) closedTargets(ctx context.Context, links []model.Link) (map[string]bool, error) {
	closed := map[string]bool{}
	for _, l := range links {
		if !l.Kind.Blocks() {
			continue
		}
		if _, ok := closed[l.Target]; ok {
			continue
		}
		state, err := c.Store.State(ctx, l.Target)
		if err != nil {
			return nil, err
		}
		closed[l.Target] = state == model.StateClosed
	}
	return closed, nil
}

// GetTask returns one task plus its conversation.
func (c *Client) GetTask(ctx context.Context, id string) (TaskDetail, error) {
	t, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	if t == nil {
		return TaskDetail{}, &NotFoundError{ID: id}
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	closed, err := c.closedTargets(ctx, t.Links)
	if err != nil {
		return TaskDetail{}, err
	}

	comments, err := c.Store.Comments(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	attachments, err := c.Store.AttachmentMetas(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	// byComment groups attachments under the comment they were posted
	// alongside; the rest (CommentID nil) belong to the task's own body
	// and land on TaskDetail.Attachments directly below.
	byComment := map[int64][]Attachment{}
	var taskAttachments []Attachment
	for _, a := range attachments {
		if a.CommentID == nil {
			taskAttachments = append(taskAttachments, attachmentFrom(a))
			continue
		}
		byComment[*a.CommentID] = append(byComment[*a.CommentID], attachmentFrom(a))
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	var blockedAt *time.Time
	if obs != nil {
		blockedAt = obs.MergeQueueBlockedAt
	}
	activity, err := c.Store.TaskActivityOf(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	var wait *model.GrantWait
	if state == model.StateQueued {
		wait, err = c.Store.GrantWaitOf(ctx, id)
		if err != nil {
			return TaskDetail{}, err
		}
		if activity == nil && wait != nil {
			activity = grantWaitActivity(*wait)
		}
	}
	if activity == nil && state == model.StateQueued {
		at, err := c.Store.OOMRetryingOf(ctx, id)
		if err != nil {
			return TaskDetail{}, err
		}
		if at != nil {
			activity = oomRetryActivity(*at)
		}
	}
	reviewName, err := c.reviewTemplateName(ctx, *t)
	if err != nil {
		return TaskDetail{}, err
	}
	// The span comes from the store rather than from the runs this
	// function goes on to read: which ending counts as a task's own is a
	// rule with one home (model.TaskSpan), and a detail that answered it
	// differently from the list would put two different bars on screen for
	// one task.
	span, err := c.Store.TaskSpanOf(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	// Read here rather than further down where the attempt list is built,
	// because the stall note below is measured from this task's own runs
	// -- the live one's start, the last finished one's ending
	// (model.StateTimesFrom). One read serves both.
	runs, err := c.Store.Runs(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	goal, err := c.Store.TaskGoal(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	if goal != nil {
		t.GoalKind = goal.Kind
	}
	detail := TaskDetail{
		Task: c.withReasons(taskFrom(*t, state, closed, blockedAt, obs.RepairInFlight(), observationMergeQueueState(obs), activity, reviewName, span,
			c.stallNote(state, model.StateTimesFrom(*t, obs, runs), c.now())), *t, obs, wait),
		Comments:    make([]Comment, 0, len(comments)),
		Attachments: taskAttachments,
	}
	detail.Task.TurnStopped = turnStopped(state, obs, comments)
	detail.Task.QuestionSummary = questionSummary(state, obs, comments)
	detail.Goal = goal
	if err := c.attachPause(ctx, &detail.Task); err != nil {
		return TaskDetail{}, err
	}
	if detail.Task.State != model.StateClosed {
		detail.Task.RedBaseBranch = c.redBaseBranches(ctx).forTask(*t)
	}
	for _, cm := range comments {
		detail.Comments = append(detail.Comments, commentFrom(cm, byComment[cm.ID]))
	}
	if t.TemplateID != "" {
		decl, err := c.Store.GetRepoDeclaration(ctx, t.TemplateID)
		if err != nil {
			return TaskDetail{}, err
		}
		detail.DeclaredPush = declaredPushOf(decl)
	}
	streak, err := c.Store.FailureStreak(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	// Guarded the same way model.StateOf and model.Transitions both guard
	// a failure streak against a task that has since completed or closed:
	// orchestrator.salvagePushedBranch turns a pushed branch into a pull
	// request (and the task into StateCompleted) even for a run whose own
	// outcome stays "failed" forever -- see model.Transitions' own doc
	// comment on why (bwsalmon/agents#502) -- which leaves task_streak
	// sitting at 1 or more permanently, since only a "succeeded" outcome
	// ever resets it. Left unguarded, GetTask kept reporting "1
	// consecutive failed attempt" on a task that plainly succeeded
	// (bwsalmon/agents#514), the same bogus signal #502 already fixed for
	// the task's state and its timeline, just surfacing here instead.
	completedOrClosed := obs != nil && (obs.CompletedAt != nil || obs.ClosedAt != nil)
	detail.Parked = detail.Blocked && streak != nil && streak.LastOutcome == model.ParkedOutcome
	if streak != nil && streak.Count > 0 && !completedOrClosed {
		detail.FailedAttempts = streak.Count
		lastFailureAt := streak.LastFailureAt
		detail.LastFailureAt = &lastFailureAt
		detail.LastFailureReason = streak.LastFailureDetail
	}
	// One query for every attempt's prompt size rather than one per
	// attempt, and sizes rather than the prompts themselves: a task with
	// ten attempts holds ten prompts a detail view wants no word of
	// (Store.RunPromptSizes). A failure to measure is not a failure to
	// show the task -- an instrument that can 500 a task page is worse
	// than one that is sometimes silent -- so the map stays nil and every
	// attempt reads as unmeasured.
	promptBytes, err := c.Store.RunPromptSizes(ctx, id)
	if err != nil {
		log.Printf("ui: task %s: measuring its attempts' prompts: %v", id, err)
		promptBytes = nil
	}
	// Counted the same way and for the same reason: one query, and a
	// failure leaves every attempt reading as having none.
	auditCounts, err := c.Store.RunCapabilityAuditCounts(ctx, id)
	if err != nil {
		log.Printf("ui: task %s: counting its attempts' capability audit records: %v", id, err)
		auditCounts = nil
	}
	detail.Attempts = make([]Attempt, 0, len(runs))
	for _, r := range runs {
		detail.Attempts = append(detail.Attempts, attemptFrom(r, promptBytes, auditCounts))
	}

	transitions := model.Transitions(*t, obs, runs, streak, askedAt(obs, comments))
	// Who closed or reopened it, which the record above cannot say. A
	// failure to read them leaves the timeline as the record alone has it.
	if events, err := c.Store.TaskEvents(ctx, id); err != nil {
		log.Printf("ui: task %s: reading its closes and reopens: %v", id, err)
	} else {
		transitions = model.MergeTaskEvents(transitions, events)
	}
	detail.Transitions = make([]Transition, 0, len(transitions))
	for _, tr := range transitions {
		detail.Transitions = append(detail.Transitions, transitionFrom(tr))
	}
	detail.PullRequestEvents = pullRequestEventsFrom(obs)
	if obs != nil {
		detail.RunNowAt = obs.RunNowAt
	}
	detail.PendingSecret = c.pendingSecretFor(obs)
	if obs != nil {
		detail.PendingConfigChange = obs.PendingConfigChange
	}
	detail.LocalScriptApprovals = c.localScriptApprovals().ForTask(id, c.now())
	detail.CI = ciStatusFrom(obs)
	if ownerID, inPlace := t.CIOwner(); inPlace {
		// A review working on the reviewed change's own branch has no CI
		// of its own: its branch's verdict is the reviewed task's.
		ownerObs, err := c.Store.GetObservation(ctx, ownerID)
		if err != nil {
			return TaskDetail{}, err
		}
		detail.CI = ciStatusFrom(ownerObs)
	}
	branch := t.BranchName()
	repo := ""
	if t.Target != nil {
		repo = t.Target.String()
	}
	runID := ""
	if detail.CI != nil {
		runID = detail.CI.RunID
	}
	if runID != "" || (branch != "" && repo != "") {
		// Off the same computation the CI pane draws from, which is
		// shared for ciFaultTTL -- opening a task pane does not start a
		// second analysis of the same runs.
		for _, fault := range c.CISharedFaults(ctx) {
			for _, r := range fault.Runs {
				matchedRun := runID != "" && r.RunID == runID && fault.Source != ci.FaultSourceGitHub
				matchedGitHubBranch := fault.Source == ci.FaultSourceGitHub && repo != "" && fault.Repo == repo && branch != "" && r.Ref == branch
				if matchedRun || matchedGitHubBranch {
					detail.CIFaults = append(detail.CIFaults, fault)
					if detail.CI != nil {
						detail.CI.Faults = append(detail.CI.Faults, fault)
					}
					break
				}
			}
		}
	}
	detail.SuspendedSession = suspendedSessionFrom(obs)
	if t.Origin.Reason == model.ReasonSuite {
		origin, err := c.Store.SuiteRunOfTask(ctx, id)
		if err != nil {
			return TaskDetail{}, err
		}
		if origin != nil {
			detail.FiledBySuiteRun = &TaskSuiteRunOrigin{
				RunID: origin.RunID, SuiteName: origin.SuiteName,
				Pass: origin.PassNumber, TemplateName: origin.TemplateName,
			}
		}
	}
	if detail.DeploymentWide, err = c.deploymentWideGrants(ctx, *t); err != nil {
		return TaskDetail{}, err
	}
	return detail, nil
}

// deploymentWideGrants is TaskDetail.DeploymentWide for t: each
// team-scoped capability t holds (admincaps.TeamScoped), every one of
// which is deployment-wide since the single-tenant reset (stage 4).
func (c *Client) deploymentWideGrants(ctx context.Context, t model.Task) ([]string, error) {
	var held []string
	for _, g := range t.Grants {
		if admincaps.IsTeamScoped(g.Capability) && !slices.Contains(held, g.Capability) {
			held = append(held, g.Capability)
		}
	}
	return held, nil
}

// pullRequestEventsFrom projects Observation's own PrOpenedAt/PrMergedAt/
// PrClosedAt into TaskDetail's wire shape, oldest first. obs is nil for a
// task orchestrator.SyncPullRequests has never observed anything about,
// same as every other obs field this package reads.
func pullRequestEventsFrom(obs *model.Observation) []PullRequestEvent {
	if obs == nil {
		return nil
	}
	var out []PullRequestEvent
	if obs.PrOpenedAt != nil {
		out = append(out, PullRequestEvent{Kind: "opened", At: *obs.PrOpenedAt})
	}
	if obs.PrMergedAt != nil {
		out = append(out, PullRequestEvent{Kind: "merged", At: *obs.PrMergedAt})
	}
	if obs.PrClosedAt != nil {
		out = append(out, PullRequestEvent{Kind: "closed", At: *obs.PrClosedAt})
	}
	return out
}

// AttemptTranscript returns one attempt's own agent transcript -- the
// full narrative record GetTask's own Attempts list never carries
// (attemptFrom's own doc comment: TaskDetail stays cheap to fetch), for a
// caller that wants to read one attempt's whole story rather than just
// its outcome (bwsalmon/agents#446). For an attempt still running,
// Config.LiveTranscripts (when set) is tried first -- a still-running
// attempt's own transcript-in-progress, straight from whatever file its
// framework has been mirroring it into (bwsalmon/agents#467) -- falling
// back to Store.RunTranscript, which "" (with no error) until the
// attempt finishes, or forever for a framework that never populates one
// (agent.Result.Transcript's own doc comment) or a deployment with no
// LiveTranscripts configured at all.
func (c *Client) AttemptTranscript(ctx context.Context, taskID string, number int) (string, error) {
	runs, err := c.Store.Runs(ctx, taskID)
	if err != nil {
		return "", err
	}
	var run *model.Run
	for i := range runs {
		if runs[i].Attempt == number {
			run = &runs[i]
			break
		}
	}
	if run == nil {
		return "", &NotFoundError{message: fmt.Sprintf("no attempt %d for task %s", number, taskID)}
	}

	if run.FinishedAt == nil && c.Config.LiveTranscripts != nil {
		if text, ok, err := c.Config.LiveTranscripts.Tail(run.ID); err == nil && ok {
			return text, nil
		}
	}

	transcript, ok, err := c.Store.RunTranscript(ctx, taskID, number)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &NotFoundError{message: fmt.Sprintf("no attempt %d for task %s", number, taskID)}
	}
	return transcript, nil
}

// AttemptCapabilityAudit returns what one attempt's capability grants did
// for it, as their servers reported it when the attempt released them
// (docs/capabilities-spec.md §4.4.4): host-script's record of every call
// -- script, argv verbatim, exit code, duration -- above all. Empty until
// the attempt has ended, since grants are released only then. A stored
// record that is somehow not JSON is shown as a note rather than breaking
// the response it would be embedded in.
func (c *Client) AttemptCapabilityAudit(ctx context.Context, taskID string, number int) ([]CapabilityAuditEntry, error) {
	records, ok, err := c.Store.RunCapabilityAudit(ctx, taskID, number)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &NotFoundError{message: fmt.Sprintf("no attempt %d for task %s", number, taskID)}
	}
	out := make([]CapabilityAuditEntry, 0, len(records))
	for _, r := range records {
		e := CapabilityAuditEntry{Grant: r.Grant, AttemptID: r.AttemptID, At: r.At, Note: r.Note}
		if r.Record != "" {
			if json.Valid([]byte(r.Record)) {
				e.Record = json.RawMessage(r.Record)
			} else {
				e.Note = "a record that is not JSON was stored for this grant"
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// TaskPrompt is a task's own prompt, as one attempt was actually given
// it: the whole text orchestrator.RunDispatch assembled and handed the
// agent, plus which attempt it belongs to so a reader knows how old the
// answer is (a redispatched task's prompt grows with its conversation,
// so attempt 3's is not attempt 1's).
//
// Attempt is 0 and Prompt "" for a task nothing has recorded a prompt
// for -- one never dispatched, one whose every attempt died in setup
// before its agent got a turn, and one that ran before the column
// existed all land here. They are one case on the wire on purpose: none
// of them has a prompt to show, and the frontend says exactly that
// rather than inventing one by re-deriving what a dispatch *would*
// build, which would differ from the real thing in the details (the
// checkout, the capability sections) that make it worth reading.
//
// Bytes, Tokens and ContextTokens are that prompt measured
// (model.PromptSize): how big it actually came out, what that is
// estimated to be in tokens, and the window the estimate is judged
// against. A reader looking at a week-old task's prompt is looking at
// the one thing in a dispatch that grows without bound, and "this is
// 412 KB, about a fifth of the window" is the fact that makes the pane
// worth opening a second time.
type TaskPrompt struct {
	Prompt        string `json:"prompt"`
	Attempt       int    `json:"attempt,omitempty"`
	Bytes         int    `json:"bytes,omitempty"`
	Tokens        int    `json:"tokens,omitempty"`
	ContextTokens int    `json:"contextTokens,omitempty"`
}

// TaskPrompt returns the prompt taskID's most recent attempt was given
// -- the read behind GET /api/tasks/{id}/prompt, and the answer to "what
// was the agent actually told?" that a task's own title and description
// only partly are.
//
// Most recent rather than every attempt: the question is what this task
// looks like to an agent now, and the newest prompt is the one that
// includes everything said on the task since. An attempt that never
// reached its agent recorded no prompt, so this walks back from the
// newest until it finds one rather than reporting "no prompt" for a task
// whose latest attempt merely failed to clone.
func (c *Client) TaskPrompt(ctx context.Context, taskID string) (TaskPrompt, error) {
	t, err := c.Store.GetTask(ctx, taskID)
	if err != nil {
		return TaskPrompt{}, err
	}
	if t == nil {
		return TaskPrompt{}, &NotFoundError{ID: taskID}
	}
	runs, err := c.Store.Runs(ctx, taskID)
	if err != nil {
		return TaskPrompt{}, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		prompt, found, err := c.Store.RunPrompt(ctx, taskID, runs[i].Attempt)
		if err != nil {
			return TaskPrompt{}, err
		}
		if found && prompt != "" {
			size := model.MeasurePrompt(prompt)
			return TaskPrompt{
				Prompt:        prompt,
				Attempt:       runs[i].Attempt,
				Bytes:         size.Bytes,
				Tokens:        size.EstimatedTokens,
				ContextTokens: size.ContextTokens,
			}, nil
		}
	}
	return TaskPrompt{}, nil
}

// Attachment returns one attachment's full content, scoped to taskID so
// one task's attachment id can never be used to read another's file --
// the read behind GET /api/tasks/{id}/attachments/{attachmentId}
// (bwsalmon/agents#522).
func (c *Client) Attachment(ctx context.Context, taskID string, id int64) (model.Attachment, error) {
	a, err := c.Store.GetAttachment(ctx, taskID, id)
	if err != nil {
		return model.Attachment{}, err
	}
	if a == nil {
		return model.Attachment{}, &NotFoundError{message: fmt.Sprintf("no attachment %d on task %s", id, taskID)}
	}
	return *a, nil
}

// askedAt is the currently pending question's own CreatedAt, or nil once
// there is none -- model.Transitions' own askedAt parameter, resolved
// against the same comment list GetTask already fetched rather than a
// second query.
func askedAt(obs *model.Observation, comments []model.Comment) *time.Time {
	if obs == nil || obs.PendingQuestionCommentID == nil {
		return nil
	}
	for _, c := range comments {
		if c.ID == *obs.PendingQuestionCommentID {
			at := c.CreatedAt
			return &at
		}
	}
	return nil
}

// turnStopped reports whether an awaiting_reply task is parked on the
// comment Stop posts rather than on a question (Task.TurnStopped),
// resolved against a comment list the caller already has.
func turnStopped(state model.State, obs *model.Observation, comments []model.Comment) bool {
	if state != model.StateAwaitingReply || obs == nil || obs.PendingQuestionCommentID == nil {
		return false
	}
	for _, c := range comments {
		if c.ID == *obs.PendingQuestionCommentID {
			return c.Body == model.TurnStoppedComment
		}
	}
	return false
}

// questionSummary is the one line an awaiting_reply task's pending
// question is shown by (Task.QuestionSummary): the summary its asker gave,
// or one derived from its body for a question stored without one. "" for
// a task not parked on a question, and for a stopped turn, which the UI
// says in its own words.
func questionSummary(state model.State, obs *model.Observation, comments []model.Comment) string {
	if state != model.StateAwaitingReply || obs == nil || obs.PendingQuestionCommentID == nil {
		return ""
	}
	for _, c := range comments {
		if c.ID != *obs.PendingQuestionCommentID || c.Body == model.TurnStoppedComment {
			continue
		}
		if c.Summary != "" {
			return c.Summary
		}
		return model.DeriveQuestionSummary(c.Body)
	}
	return ""
}

// CreateTaskRequest is a new task's fields. Repo, Base and AutoMerge were
// directive lines in an issue body before this package spoke to the
// store; they are columns on the task now, which is what
// docs/data-model.md's "a form knows all of that before the task exists"
// asked for in the first place.
type CreateTaskRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	// TemplateID files this task from a template (grain/task-58): its
	// title, body, reads, writes, capabilities, declared-field values,
	// auto-merge, network access and model, with the template's input fields
	// filled in as Fields says. "" -- the ordinary case -- is a task
	// written out field by field at the form.
	//
	// The template fills the blanks and nothing else: a request that
	// states a field of its own keeps it, so somebody instantiating a
	// template can still say "this one against that branch" or tick a
	// capability off, exactly as they would for a hand-written task. The
	// one field that is not theirs to state is the repo of a *bound*
	// template, which wins here as it does at every other firing point
	// (model.Template.Target).
	TemplateID string `json:"templateId"`
	// BoardID is the board the New Task form was opened from, which
	// picks settings/new-task-form.yml's boards layer (§6.3) when the
	// server applies the defaults of the fields that form hides.
	BoardID string `json:"boardId,omitempty"`
	// Fields is what the template's own input fields are filled in with,
	// keyed by field name (model.TemplateField.Name) -- "the environment
	// to deploy to", not one of this deployment's declared columns,
	// which are CustomFields below. A field left out takes its declared
	// default; a name the template does not declare, or a value outside
	// a selector's options, is refused rather than silently substituting
	// nothing (model.ValidateTemplateFieldValues).
	Fields map[string]string `json:"fields"`
	Repo   string            `json:"repo"`
	Base   string            `json:"base"`
	// NewBranch asks grain to create Base on the task's repo before the
	// task runs, cut from NewBranchFrom ("" for the repo's default
	// branch) -- grain/task-2317's "create a new branch, based on the
	// selected (main by default)". It records the same request the repo
	// page's own "add a branch" does (model.Store.RequestBranchFrom), so
	// the branches reconciler cuts it, or adopts the ref if the name is
	// already on GitHub; dispatch holds the task until then
	// (orchestrator.heldForPendingBranch). Base must be named, and the
	// task must have a repo.
	NewBranch            bool   `json:"newBranch,omitempty"`
	NewBranchFrom        string `json:"newBranchFrom,omitempty"`
	Issue                int    `json:"issue,omitempty"`
	AutoMerge            bool   `json:"autoMerge"`
	AutoApproveProposals bool   `json:"autoApproveProposals"`
	// TimeZone is the IANA zone of whoever is filing the task, which its
	// runs are told the time in (model.Task.TimeZone). POST /api/tasks
	// fills it from the browser's TimeZoneHeader when the body names
	// none; "" -- or a zone this host cannot load -- leaves the task on
	// the deployment's own clock.
	TimeZone string `json:"timeZone,omitempty"`
	// Interactive files this task as a live chat rather than a change
	// handed off to run unattended (bwsalmon/agents#539) -- model.Task's
	// own field of the same name. It also, regardless of Approved, files
	// the task already approved and dispatches it ahead of the ordinary
	// backlog (see CreateTask): a chat nobody has opened yet, sitting as
	// an unapproved proposal or waiting behind everything already
	// queued, has nothing to show for itself. A proposal (ProposedBy) is
	// the exception to the approval: it lands the way any proposal does.
	//
	// A task filed from an interactive template (TemplateID,
	// model.Template.Interactive) is interactive whatever this says.
	Interactive bool `json:"interactive"`
	// Priority is the task's numeric dispatch priority (model.Task.Priority,
	// default model.DefaultTaskPriority = 5; higher numbers mean higher priority).
	// When omitted, defaults to model.DefaultTaskPriority. 0 and below are
	// refused (errPriorityNotPositive), as on every other surface that
	// takes a task priority: there is no number that means "the default".
	Priority *int `json:"priority,omitempty"`
	// SandboxCPUs, SandboxMemoryMB and SandboxDiskGB
	// (bwsalmon/agents#534, grain/task-41) set model.Task's own fields of
	// the same name -- a per-task override of the deployment's default
	// sandbox shape. 0 (the default for all three) means no override: the
	// task dispatches at whatever shape the deployment otherwise
	// configures.
	//
	// Refused outright, rather than stored, on a deployment whose
	// sandboxes have no shape of their own (Config.SandboxShapeIgnored)
	// -- refuseIgnoredSandboxShape has why.
	SandboxCPUs     int `json:"sandboxCpus"`
	SandboxMemoryMB int `json:"sandboxMemoryMb"`
	SandboxDiskGB   int `json:"sandboxDiskGb"`
	// NetworkAccess sets model.Task's own field of the same name
	// (grain/task-269) -- a per-task override of how much of the
	// internet this task's sandbox may reach. "" (the default) means no
	// override: the task gets whatever the deployment is set to when it
	// dispatches. Anything else must be one of model.NetworkAccesses().
	//
	// "none" is refused on a deployment whose sandboxes are directories
	// on this host (Config.SandboxShapeIgnored, which is the same
	// backend question): there is no network there to withhold, and a
	// task filed with it would be queued, dispatched and then fail at
	// sandbox setup -- the same reasoning, and the same moment, as
	// refuseIgnoredSandboxShape.
	NetworkAccess string `json:"networkAccess"`
	// NothingNeededAction sets model.Task's own field of the same name:
	// "close" (or "", the default) closes the task when its run ends on
	// nothing_needed, "ack" leaves it in awaiting_ack for somebody to
	// acknowledge.
	NothingNeededAction string `json:"nothingNeededAction,omitempty"`
	// KeepRunning sets model.Task.KeepRunning: when true, the orchestrator
	// immediately restarts the task when its run exits rather than completing
	// or parking it.
	KeepRunning      bool  `json:"keepRunning,omitempty"`
	KeepRunningSnake *bool `json:"keep_running,omitempty"`
	// Goal declares the task's goal (model.TaskGoal,
	// docs/design/durable-tasks.md#DT-4) as it is filed; nil for none, or
	// for the goal of the template it is filed from.
	Goal *model.GoalDecl `json:"goal,omitempty"`
	// NoPort sets model.Task.NoPort: this task's change stays on the
	// branch it lands on, and is not ported to the repository's other
	// paired branches.
	NoPort bool `json:"noPort,omitempty"`
	// FixesBaseBranch marks this task as fixing its own base branch while
	// that branch is red (model.FixesBaseBranch, grain/task-3451): the
	// "This fixes a red <base>" checkbox. It is never made to wait on the
	// branch's repair, because it is one: it counts as the branch's open
	// repair once approved, and is declared with a base-green goal, so a
	// green branch closes it (docs/design/durable-tasks.md#DT-4). Refused
	// for a task with no repo.
	FixesBaseBranch bool `json:"fixesBaseBranch,omitempty"`
	// Origin is the GitHub pull request or issue this task's work comes
	// from (model.ParseWorkOrigin's forms), for a task filed from a chat
	// that names one. A proposal whose parent already has a GitHub origin
	// keeps that one instead (model.ExplicitOrigin); empty inherits.
	Origin string `json:"origin,omitempty"`
	// AgentFramework sets model.Task's own field of the same name -- a
	// per-task override of the deployment's default agent framework
	// (Settings' own "Agent framework"). "" (the default) means no
	// override: the task is driven by whichever framework the deployment
	// is set to when it dispatches. Anything but "" must be one of
	// model.AgentFrameworks() (the legacy "gemini" spelling is accepted
	// and normalized to "antigravity"), validated the same way
	// UpdateSettings validates the deployment-wide setting.
	AgentFramework string `json:"agentFramework"`
	// AgentBackendID sets model.Task.AgentBackendID -- this task runs on
	// one named model out of Settings' own Models list, rather than on
	// whichever one the rotation reaches (grain/task-110). "" (the
	// default) leaves it to the rotation. Anything else must be the id of
	// a model this deployment has configured, including one that has been
	// taken out of the rotation: naming a disabled model by hand is
	// exactly what disabling one leaves possible.
	//
	// Naming a model also settles AgentFramework above, which is filled
	// in from that model's own framework -- so a pinned task is counted
	// against the right framework's pause without a second lookup. Naming
	// both is only an error when they disagree.
	Model          string `json:"model,omitempty"`
	AgentBackendID string `json:"agentBackendId"`
	// PromptExtension sets model.Task's own field of the same name
	// (grain/task-114) -- this task's own standing instructions, used
	// *instead of* the deployment's and its repo's for this task's runs.
	// "" (the default) means no override, so the task gets whatever those
	// two say when it dispatches. Trimmed on the way in, the same as the
	// deployment-wide and per-repo layers, so a box someone typed a
	// newline into files as no override rather than as an override that
	// silences both.
	PromptExtension string `json:"promptExtension"`
	// ReviewTemplateID attaches a review to this task
	// (model.Task.ReviewTemplateID, grain/task-284): the ID of the
	// template a second task is filed from once this one's own work is
	// done, to read the code it proposed and fix what is wrong with it
	// before it merges. "" (the default) attaches none. Anything else
	// must name a template that exists, checked here rather than left to
	// fail at the one moment the review was supposed to be filed.
	ReviewTemplateID string `json:"reviewTemplateId"`
	Creator          string `json:"creator,omitempty"`
	// Author, when set, is who the task records as its requester
	// (model.Task.Creator) in place of Creator: `grain create -author`,
	// the requester of work filed from a workstation's own CLI. It is
	// metadata only -- every permission check above still goes by
	// Creator, the caller -- and the HTTP handler accepts it only from a
	// caller with no principal of its own (the access code: this
	// deployment's operator); a signed-in person files as
	// themselves.
	Author string `json:"author,omitempty"`
	// Capabilities is the exact set of capability ids this task is filed
	// holding -- but only when the caller names one. nil (the field left
	// out, or JSON null) means the caller expressed no opinion, and the
	// task is filed with this deployment's own default set instead
	// (model.Config.DefaultCapabilities, recorded as model.GrantByDefault
	// grants). An empty-but-present list is an opinion: exactly no
	// capabilities, defaults included.
	//
	// A pointer for the same reason UpdateSettingsRequest's fields are
	// one: "leave this to whatever the deployment says" and "I want none
	// of them" are different requests, and a bare []string cannot tell an
	// omitted field from an empty one for a caller writing Go rather than
	// JSON. NewTaskOverlay.jsx always sends a list, seeded from GET
	// /api/config's own defaultCapabilities, so what a human sees ticked
	// on the form is what the task is filed with, unticking included.
	//
	// On a request that names ProposedBy this is added to the defaults
	// rather than replacing them, and anything named here that the
	// proposing task does not hold keeps the proposal from being queued
	// without a person approving it (creationGrants has the reasoning).
	Capabilities *[]string `json:"capabilities"`
	// DependsOn is a set of task IDs this task cannot dispatch ahead of --
	// model.LinkDependsOn links, filed at creation the same way
	// Capabilities is. SetDependency is the picker's attach/detach
	// counterpart for a task that already exists.
	DependsOn []string `json:"dependsOn"`
	// Reads is a set of owner/name repos this task's run may read but
	// never push to -- model.Task.Reads, docs/data-model.md's "one write
	// target, many read targets". Unlike Repo, a Reads entry grants
	// nothing: it never touches Grants, so naming a capability-offering
	// repo here inherits none of its offers.
	Reads []string `json:"reads"`
	// Writes is every *other* repo this task's work may land in, beside
	// Repo -- model.Task.Writes, one entry per repo with the base branch
	// for that repo. Empty (the default) is the single-repo task grain
	// has always filed.
	//
	// Like Reads, an entry grants nothing: no capability comes with it,
	// and this task's own runs still push only to Repo. What it does is
	// let a run reach that repo the one way a run can -- by proposing a
	// task against it, which grain then files on the branch named here
	// and lets inherit this task's auto-merge (model.LandsWhereGranted).
	Writes []WriteTarget `json:"writes"`
	// Approved files the task already approved, so it is dispatchable at
	// once. False files it proposed, waiting for Approve -- unless
	// ProposedBy names a task whose own permission already covers this
	// one (model.AutoSubmits) and this proposal is inside the bounds on
	// how far that permission propagates (model.AutoSubmitLimits), which
	// together approve it without anybody saying so here.
	Approved bool `json:"approved"`
	// AtFront is which end of the backlog this task joins: true files it
	// at the front, ahead of everything already queued, so it runs next;
	// false files it at the end, behind everything already queued, the
	// FIFO backlog grain has always defaulted to
	// (Store.OrderKeyForNewTask).
	//
	// A pointer, for the reason Capabilities above is one: nil -- the
	// field left out, or JSON null -- is a caller with no opinion, and
	// the task joins whichever end this deployment currently remembers
	// (model.Config.NewestFirst). A caller that does state one is
	// remembered: CreateTask writes that choice back as the deployment's
	// own default (Store.SetNewestFirst), so the next task filed with no
	// opinion, and the next new-task form that opens, start from what the
	// last task added chose rather than from a setting somebody has to go
	// and find in Settings.
	//
	// An interactive task ignores all of this and files at the front
	// regardless (see CreateTask): somebody is waiting on the chat right
	// now. Stating AtFront on one still remembers it, since that is the
	// filing choice being made either way.
	AtFront *bool `json:"atFront"`
	// Attachments is files to carry alongside the task's own body --
	// bwsalmon/agents#522: a screenshot, a repro zip, anything the agent
	// needs that isn't already code in a repo it can clone. Stored under
	// the new task's own id with no CommentID, and materialized into
	// every dispatched run's sandbox the same way a comment's own
	// attachments are (orchestrator.placeAttachments).
	Attachments []AttachmentUpload `json:"attachments"`
	// CustomFields is this task's values for the fields this deployment
	// declares (model.CustomField), keyed by field name. Left out -- the
	// ordinary case, and every case for a caller that is not a form --
	// the task carries none: a declared Default seeds the form, never the
	// filing, so a task filed by an agent or by a schedule says nothing
	// about a field rather than something nobody chose.
	//
	// Every value is checked against its own declaration on the way in
	// (model.NormalizeCustomFieldValues): a name nothing declares, or a
	// value the declared type does not accept, is a validation error
	// rather than a row written and shown nowhere. Empty values are
	// dropped, so a form that renders every field and submits the whole
	// set files only what was actually filled in.
	//
	// One field's value is a string or an array of strings, for a field
	// this deployment declared as holding a set (custom_fields.go). A
	// single-valued field handed several is a validation error rather
	// than a value picked from among them.
	CustomFields map[string]CustomFieldValue `json:"customFields,omitempty"`
	// NoRepo files a task with no repo at all, deliberately -- a
	// standalone task nothing will clone, check out, or push a branch
	// to. Distinct from simply leaving Repo blank, which instead falls
	// back to Config.DefaultTarget (or fails validation if there is
	// none): a blank Repo says nothing either way, while NoRepo says
	// outright that no target exists. orchestrator.BuildPrompt tells the
	// dispatched agent as much, rather than leaving it to guess from an
	// empty checkout directory.
	NoRepo bool `json:"noRepo"`
	// ChangeTarget files a task whose change is not a git branch -- a
	// changelist (CL) in a workspace of a registered change target kind
	// (model.ChangeTarget). Like NoRepo it means no
	// repo: Repo, Base, Writes, AutoMerge, FixesBaseBranch, merge-with
	// links and a base-green goal are refused alongside it.
	ChangeTarget *model.ChangeTarget `json:"changeTarget,omitempty"`
	// Reviewers is the person-set list of reviewer usernames for this task's
	// change (model.Task.Reviewers, docs/design/changelists.md §5). Refused to
	// agents by agentFilingEscalation / externalAgentFilingEscalation.
	Reviewers []string `json:"reviewers,omitempty"`
	// ProposalKey and ProposedBy are daemon-internal fields used by an
	// interactive propose_task call. ProposalKey makes retries and the
	// end-of-run replay idempotent; ProposedBy records provenance -- and,
	// because provenance is what a proposal's authority is read off,
	// decides how far AutoMerge and Approved above reach (CreateTask).
	ProposalKey string `json:"proposalKey,omitempty"`
	ProposedBy  string `json:"proposedBy,omitempty"`
	// HoldForApproval is propose_task's own `hold_for_approval`: file
	// this proposal for a person to approve even if ProposedBy's
	// permission would have queued it (model.HeldForApprovalTag). It can
	// only lower what the proposer may do, never raise it, and means
	// nothing on a request that names no ProposedBy.
	HoldForApproval bool `json:"holdForApproval,omitempty"`
	// Supersedes is the id of an existing task this one replaces -- a
	// propose_task call's own `supersedes` argument (mcp.ProposedTask),
	// arriving over the API while the run that made it is still going.
	// Filing closes that task as superseded by this one and links the
	// two (model.Store.Supersede).
	//
	// Bounded by model.SupersedeRefusal and by nothing here: only a task
	// that has never been dispatched, and only one in the repository
	// this task is filed against. A task outside those bounds is left
	// open, with a note appended to this task's body saying which and
	// why -- the creation itself never fails over it, for the reason
	// model.SupersedeRefusedNote gives.
	Supersedes string `json:"supersedes,omitempty"`
	// Reverts is the ID of an existing task this one was filed to revert --
	// recorded as model.LinkReverts on this task and model.LinkRevertedBy on
	// the reverted task.
	Reverts string `json:"reverts,omitempty"`
	// GoogleChatSpace is the Google Chat space linked to this task
	// (LinkGoogleChat), or "" when unlinked.
	GoogleChatSpace string `json:"googleChatSpace,omitempty"`
	// DeferredUntil files the task already deferred, waking at this
	// instant (model.Task.DeferredUntil) -- "schedule this for later",
	// decided at the moment of filing rather than by a second Defer call
	// afterwards. It is a propose_task call's own `not_before` arriving
	// over the API, and the same thing for anybody filing through
	// POST /api/tasks. nil, or an instant already past, files the task
	// exactly as it would otherwise be filed: a wake time that has come
	// round is nothing to wait for.
	//
	// Approval is unaffected, for the reason SetDeferred keeps it: a
	// task filed approved and deferred wakes onto the queue, and one
	// filed for a human wakes back into proposed.
	DeferredUntil *time.Time `json:"deferredUntil,omitempty"`

	// modelFromTemplate is the model fromTemplate filled Model in with,
	// "" when it filled in none. Never read off the wire: CreateTask
	// compares it with the model it settles on to record
	// model.Task.ModelSource, so only a model that really came from the
	// template is credited to it.
	modelFromTemplate string

	// grantsFromTemplate are the grants fromTemplate filled into the request.
	// Preserves original GrantSource (such as .grain/templates/... provenance)
	// so team authorization boundary enforcement can distinguish declared grants
	// from explicit user requests.
	grantsFromTemplate []model.Grant
}

func proposalTags(key string, held bool) []string {
	var tags []string
	if key != "" {
		tags = append(tags, key)
	}
	if held {
		tags = append(tags, model.HeldForApprovalTag)
	}
	return tags
}

// defaultCapabilities is the capability ids a task filed on this
// deployment, against target, starts out holding: two layers, unioned,
// deployment-wide first.
//
//   - model.Config.DefaultCapabilities, the deployment's own set
//     (task-14), which every task gets wherever it points.
//   - model.RepoConfig.DefaultCapabilities for target, the repo's own
//     additions (task-24). A nil target -- a task filed with NoRepo --
//     has no repo to key on, so it gets the deployment layer alone.
//
// Both are read from the store on every CreateTask rather than cached on
// Client.Config, so an operator who changes either set changes what the
// next task is filed with rather than what the next process is.
//
// Union is the whole composition rule: a repo adds, and never subtracts.
// model.RepoConfig.DefaultCapabilities has why "everything except gcp-key
// here" is deferred rather than spelled some other way.
//
// An id this build no longer offers is dropped rather than failing the
// creation: UpdateSettings and SetRepoDefaultCapabilities each validate
// their own set against OfferedCapabilities when it is saved, so a stored
// id with no row can only be a capability grain has retired since, and a
// settings row left behind by an upgrade must not become a deployment
// where no task can be filed at all. That is the one place this differs
// from a caller naming its own ids, which are still rejected as unknown
// (grantsFor) -- a human who asks for a capability by name should hear
// that it does not exist.
//
// Only tasks filed through CreateTask are seeded. A schedule, a template
// and a suite each carry a grant set that was authored once, in a form
// of its own, and the tasks they file are filed with it -- defaulting
// them here as well would quietly widen a set somebody already wrote
// down.
func (c *Client) defaultCapabilities(ctx context.Context, teamID string, target *model.RepoRef) ([]string, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	var stored []string
	if cfg != nil {
		stored = cfg.DefaultCapabilities
	}
	if target != nil {
		repoCfg, err := c.Store.GetRepoConfig(ctx, *target)
		if err != nil {
			return nil, err
		}
		if repoCfg != nil {
			stored = append(append([]string{}, stored...), repoCfg.DefaultCapabilities...)
		}
	}
	ids := make([]string, 0, len(stored))
	seen := make(map[string]bool, len(stored))
	for _, id := range stored {
		if seen[id] {
			continue
		}
		if _, ok := c.capabilityByID(id); !ok {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// CreateTask files a task straight into the store.
//
// No GitHub issue is created, and none is needed: Store.NewTaskID
// allocates identity, so a task exists the moment this returns. That is
// the whole inversion in one method -- this used to open an issue and let
// a poll notice it, which meant a task could not be created without
// GitHub reachable, and could not be dispatched until the next tick.
//
// Capabilities are resolved once, for every caller: a
// request that names its own set is filed with exactly that set, and one
// that names none at all is filed with the default set resolved for the
// repo it targets -- this deployment's, plus that repo's own
// (CreateTaskRequest.Capabilities, model.Config.DefaultCapabilities,
// model.RepoConfig.DefaultCapabilities). That resolution happens after
// the target repo is decided just above, so a task filed with no repo at
// all gets Config.DefaultTarget's own repo defaults rather than none.
// Either way what comes out is a plain model.Grant on the task itself --
// there is no second, deployment-level grant set read again at dispatch,
// so every capability a run holds is one that can be seen and detached
// on the task that holds it.
//
// req.AtFront (grain/task-202) is resolved the same "the request has the
// last word, and is remembered" way: it says which end of the backlog
// this task joins, and stating it also stores that end as the
// deployment's own default (model.Config.NewestFirst) for the next task
// filed without one. That is what makes the choice sticky from the form
// rather than from Settings -- whoever files a task at the front is
// usually about to file the next one there too, and the new-task form
// seeds itself from the same stored value (GET /api/config's
// newestFirst).
//
// A per-task sandbox shape is refused outright on a deployment whose
// sandboxes have no shape of their own, rather than filed and then
// discovered at sandbox setup -- refuseIgnoredSandboxShape has why, and
// UpdateTask applies the same rule to an edit.
func (c *Client) CreateTask(ctx context.Context, req CreateTaskRequest) (Task, error) {
	if req.ProposalKey != "" {
		if existing, err := c.Store.TaskWithTag(ctx, req.ProposalKey); err != nil {
			return Task{}, err
		} else if existing != nil {
			return c.Task(ctx, existing.ID)
		}
	}
	req, err := c.fromTemplate(ctx, req)
	if err != nil {
		return Task{}, err
	}
	if strings.TrimSpace(req.Title) == "" {
		return Task{}, validationErrorf("title is required")
	}
	priority := model.DefaultTaskPriority
	if req.Priority != nil {
		if *req.Priority < 1 {
			return Task{}, validationErrorf(errPriorityNotPositive)
		}
		priority = *req.Priority
	}
	if req.NewBranch {
		req.Base = strings.TrimSpace(req.Base)
		req.NewBranchFrom = strings.TrimSpace(req.NewBranchFrom)
	}
	// A GitHub fork head label ("andrzejfudro:chore/bump-agy-1.4.9", what
	// a pull request's page shows as its head) is not a branch name, but
	// it is what people copy: it is resolved to the open pull request it
	// heads once the task's repo is known (forkhead.go), and refused with
	// what is wrong with it when it is not a well-formed label.
	forkHead, isForkHead, err := splitForkHead("base", req.Base)
	if err != nil {
		return Task{}, err
	}
	if isForkHead && req.NewBranch {
		return Task{}, validationErrorf("new branch %q cannot be created: it is a GitHub fork head (<owner>:<branch>), "+
			"not a branch name; leave new branch off to work on the pull request it heads", req.Base)
	}
	if !isForkHead {
		if err := validateBase("base", req.Base); err != nil {
			return Task{}, err
		}
	}
	if req.NewBranch {
		if req.Base == "" {
			return Task{}, validationErrorf("a new branch needs a name")
		}
		if _, fromFork, err := splitForkHead("newBranchFrom", req.NewBranchFrom); err != nil {
			return Task{}, err
		} else if fromFork {
			return Task{}, validationErrorf("newBranchFrom %q is a GitHub fork head (<owner>:<branch>): grain cannot cut "+
				"a branch from a fork; file the task with %q as its branch, and no new branch, to work on the pull "+
				"request it heads", req.NewBranchFrom, req.NewBranchFrom)
		}
		if err := validateBase("newBranchFrom", req.NewBranchFrom); err != nil {
			return Task{}, err
		}
		if req.Base == req.NewBranchFrom {
			return Task{}, validationErrorf("new branch %q cannot be cut from itself", req.Base)
		}
	} else if req.NewBranchFrom != "" {
		return Task{}, validationErrorf("newBranchFrom is only for a task that creates its branch (newBranch)")
	}

	var target *model.RepoRef
	switch {
	case req.ChangeTarget != nil:
		if strings.TrimSpace(req.Repo) != "" || req.Base != "" || req.NewBranch {
			return Task{}, validationErrorf("a %s task has no git repository or base branch: repo and branch cannot be set", req.ChangeTarget.Kind)
		}
		if req.FixesBaseBranch {
			return Task{}, validationErrorf("a %s task has no git base branch to repair: fixes_base_branch cannot be set", req.ChangeTarget.Kind)
		}
		if req.Goal != nil && req.Goal.Kind == model.GoalBaseGreen {
			return Task{}, validationErrorf("a %s task has no git base branch: goal base-green cannot be set", req.ChangeTarget.Kind)
		}
	case req.NoRepo:
		if strings.TrimSpace(req.Repo) != "" {
			return Task{}, validationErrorf("repo and noRepo are mutually exclusive")
		}
	case strings.TrimSpace(req.Repo) != "":
		parsed, err := c.parseRepo(req.Repo)
		if err != nil {
			return Task{}, err
		}
		target = &parsed
	default:
		target = c.Config.DefaultTarget
		if target == nil {
			return Task{}, validationErrorf(
				"no repo given, and this deployment has no default target repo configured")
		}
	}

	var forkPR *forkhead.PR
	if isForkHead {
		if forkPR, err = c.resolveForkHead("base", target, forkHead); err != nil {
			return Task{}, err
		}
		req.Base = forkPR.Base
	}

	if err := validateSandboxShape(req.SandboxCPUs, req.SandboxMemoryMB, req.SandboxDiskGB); err != nil {
		return Task{}, err
	}
	if err := c.refuseIgnoredSandboxShape(req.SandboxCPUs, req.SandboxMemoryMB, req.SandboxDiskGB); err != nil {
		return Task{}, err
	}
	if err := validateAgentFramework(req.AgentFramework); err != nil {
		return Task{}, err
	}
	// The model and the connection this task asks for, kept apart
	// (model.Task.Model, model.Task.AgentBackendID), and the framework
	// they imply.
	chosenModel, backendID, err := c.splitModelChoice(ctx, req.Model, req.AgentBackendID)
	if err != nil {
		return Task{}, err
	}
	req.AgentBackendID = backendID
	// The framework this task is filed with, which a pinned connection or
	// a chosen model decides: either runs on its own framework, and a task
	// that names neither keeps whatever the request said.
	agentFramework, err := c.agentBackendFramework(ctx, firstNonEmpty(backendID, chosenModel), req.AgentFramework)
	if err != nil {
		return Task{}, err
	}
	if err := validateNetworkAccess(req.NetworkAccess); err != nil {
		return Task{}, err
	}
	if err := c.refuseIgnoredNetworkAccess(req.NetworkAccess); err != nil {
		return Task{}, err
	}
	nothingNeeded, err := validateNothingNeededAction(req.NothingNeededAction)
	if err != nil {
		return Task{}, err
	}
	if req.Goal != nil {
		if err := req.Goal.Normalize(); err != nil {
			return Task{}, validationErrorf("%v", err)
		}
	}
	reviewTemplateID, err := c.validReviewTemplate(ctx, req.ReviewTemplateID)
	if err != nil {
		return Task{}, err
	}
	if reviewTemplateID == "" && target != nil {
		defaultID, err := c.defaultReviewTemplateID(ctx, *target)
		if err != nil {
			return Task{}, err
		}
		reviewTemplateID = defaultID
	}
	// A proposal's depends_on is filtered before it is resolved: an entry
	// naming a task that never completes is dropped with a note rather
	// than failing the filing, which is dependsOnLinks' own answer for
	// the person who typed one. Nothing else takes this branch -- a task
	// a human is filing has a human to tell.
	dependsOn := req.DependsOn
	neverEndingNote := ""
	if req.ProposedBy != "" {
		kept, dropped, err := c.neverEndingDependencies(ctx, dependsOn)
		if err != nil {
			return Task{}, err
		}
		if len(dropped) > 0 {
			neverEndingNote = model.NeverEndingDependencyNote(dropped)
		}
		dependsOn = kept
	}
	links, err := c.dependsOnLinks(ctx, dependsOn, "")
	if err != nil {
		return Task{}, err
	}
	forkNote := ""
	if forkPR != nil {
		links = append(links, forkPR.Link())
		forkNote = forkPR.Note()
	}
	var proposedBy *model.Task
	if req.ProposedBy != "" && req.Capabilities != nil {
		// A review grant is grain's to give, on a review it files itself
		// (orchestrator.SyncCLReviews), never an agent's to ask for.
		if err := model.RefuseReviewOfGrants(*req.Capabilities); err != nil {
			return Task{}, validationErrorf("%v", err)
		}
	}
	if req.ProposedBy != "" {
		parent, err := c.Store.GetTask(ctx, req.ProposedBy)
		if err != nil {
			return Task{}, err
		}
		if parent == nil {
			return Task{}, validationErrorf("no such proposing task %s", req.ProposedBy)
		}
		proposedBy = parent
		links = append(links, model.Link{Kind: model.LinkProposedBy, Target: req.ProposedBy})
	}
	// A proposal's review template is held to its proposer's own grants
	// (model.ReviewTemplateWithinTask): the review is filed approved and
	// auto-merge on the branch the proposal's run writes, so a template
	// carrying more than the proposer holds is refused for the
	// repository's default, with a note saying so -- relayProposedTasks'
	// answer for the same call replayed at the end of a run.
	reviewTemplateNote := ""
	if proposedBy != nil && reviewTemplateID != "" {
		tmpl, err := c.Store.GetTemplate(ctx, reviewTemplateID)
		if err != nil {
			return Task{}, err
		}
		if tmpl != nil {
			if why := model.ReviewTemplateWithinTask(*proposedBy, *tmpl, target); why != nil {
				fallback := ""
				if target != nil {
					if fallback, err = c.defaultReviewTemplateID(ctx, *target); err != nil {
						return Task{}, err
					}
				}
				reviewTemplateNote = model.ReviewTemplateRefusedNote(proposedBy.ID, reviewTemplateID, fallback, why)
				reviewTemplateID = fallback
			}
		}
	}
	explicitOrigin, err := model.ExplicitOrigin(req.Origin, target, proposedBy)
	if err != nil {
		return Task{}, validationErrorf("%v", err)
	}
	// A task names no team. One filed against a team's config repo is
	// judged as that team's, and every other task as the default team's.
	teamID := model.DefaultTeamID
	reads, err := c.parseReads(req.Reads)
	if err != nil {
		return Task{}, err
	}
	writes, err := c.parseWrites(ctx, teamID, req.Writes)
	if err != nil {
		return Task{}, err
	}
	if err := refuseTargetAmongWrites(writes, target, req.Base); err != nil {
		return Task{}, err
	}
	// A proposal naming its own repo among its reads is an agent
	// repeating what its run already has, not a choice between two
	// meanings anybody needs to be asked about: it is dropped rather than
	// failing the propose_task call over it (grain/task-1983).
	if req.ProposedBy != "" {
		reads = readsBesidesTarget(reads, target)
	}
	if err := refuseTargetAmongReads(reads, target); err != nil {
		return Task{}, err
	}
	// A repository the team's GitHub credential cannot reach is turned
	// away here, where whoever is filing can still do something about it,
	// rather than failing the task's first clone (grain/task-1850). Work
	// filed by automation -- an agent's proposal, a caller acting as
	// grain -- has nobody at the form to tell, and refusing it would lose
	// what it describes, so it is filed and parked on the same sentence.
	reachRefused, err := c.reachRefusal(ctx, teamID, target, reads, writes)
	if err != nil {
		return Task{}, err
	}
	parkUnreached := false
	if reachRefused != "" {
		if req.ProposedBy == "" && c.requestActor(ctx).Actor.Kind == model.PrincipalHuman {
			return Task{}, validationErrorf("%s", reachRefused)
		}
		parkUnreached = true
	}
	if req.NewBranch && target == nil {
		return Task{}, validationErrorf("a new branch needs a repo to be created on")
	}
	if !blockedByLinks(links) {
		// A task creating its branch needs the branch it is cut from to
		// be there, not the one it names, which grain is about to make.
		existingBase := req.Base
		if req.NewBranch {
			existingBase = req.NewBranchFrom
		}
		if err := c.refuseMissingBases(ctx, target, existingBase, writes); err != nil {
			return Task{}, err
		}
	}
	grants, droppedRefusals, err := c.creationGrants(ctx, req, target, teamID)
	if err != nil {
		return Task{}, err
	}
	// The capability catalogue (settings/capabilities.yml) refuses what
	// it does not admit here, and names what only a person may grant:
	// a proposal or an agent's own filing holding one is left for a
	// person to approve, whatever permission it would otherwise inherit.
	grants, catalogueNeedsPerson, err := c.catalogueFilingGrants(ctx, grants, target, req.TemplateID)
	if err != nil {
		return Task{}, err
	}
	// So does the capability policy (§4.2.2), which admission would
	// otherwise only apply once the task is dispatched.
	grants, policyDropped, err := c.policyFilingGrants(ctx, grants, target)
	if err != nil {
		return Task{}, err
	}
	for _, d := range policyDropped {
		droppedRefusals = append(droppedRefusals, d.Note())
	}
	customFields, err := c.normalizeCustomFields(ctx, req.CustomFields)
	if err != nil {
		return Task{}, err
	}
	// Checked before the task is written and acted on after it, the way
	// orchestrator.relayProposedTasks does the identical thing: the note
	// for a supersede grain will not do belongs in the body a human
	// approves, and the link and the close need a task that exists.
	supersedes, supersedeRefusal, err := c.supersedeCandidate(ctx, req.Supersedes, target)
	if err != nil {
		return Task{}, err
	}
	if supersedes != "" {
		links = append(links, model.Link{Kind: model.LinkSupersedes, Target: supersedes})
	}
	revertsID := model.NormalizeTaskID(req.Reverts)
	if revertsID != "" {
		links = append(links, model.Link{Kind: model.LinkReverts, Target: revertsID})
	}
	if strings.TrimSpace(req.GoogleChatSpace) != "" {
		links = append(links, model.Link{Kind: model.LinkGoogleChat, Target: strings.TrimSpace(req.GoogleChatSpace)})
	}
	now := c.now()
	// Decoded before NewTaskID allocates anything: a bad attachment should
	// fail this call with nothing written at all, not leave a task behind
	// with none of the files it was supposed to carry.
	attachments, err := decodeAttachments(req.Attachments, now)
	if err != nil {
		return Task{}, err
	}

	id, err := c.Store.NewTaskID(ctx)
	if err != nil {
		return Task{}, err
	}
	// atFront: which end of the backlog this task joins. A request that
	// states it (CreateTaskRequest.AtFront) is the last word; one that
	// does not gets the end this deployment remembers, which is the end
	// the last task filed with an opinion chose
	// (model.Config.NewestFirst). Either way, true files it ahead of
	// everything already queued -- the top of the list, dispatched next
	// (Store.OrderKeyForNewTask's own doc comment) -- and false at the
	// end instead, behind everything queued, the FIFO backlog grain has
	// always defaulted to. An interactive task asks for the front
	// unconditionally, on top of whatever either of those says, since
	// somebody is waiting on it right now rather than checking back on it
	// later (CreateTaskRequest.Interactive's own doc comment).
	atFront := false
	if req.AtFront != nil {
		atFront = *req.AtFront
	} else if atFront, err = c.newestFirst(ctx); err != nil {
		return Task{}, err
	}
	if proposedBy == nil {
		submittingUser := strings.TrimSpace(req.Creator)
		if submittingUser == "" {
			submittingUser = c.operatorPerson()
			req.Creator = submittingUser
		}
		if submittingUser == "" {
			return Task{}, validationErrorf("creator is required")
		}
	}
	orderKey, err := c.Store.OrderKeyForNewTask(ctx, atFront || (req.Interactive && proposedBy == nil && c.requestActor(ctx).Actor.Kind != model.PrincipalAgent))
	if err != nil {
		return Task{}, err
	}
	// What a task filed by another task's propose_task call inherits,
	// which is the one case where this call's own arguments are not the
	// last word: a proposal cannot carry more authority than the run
	// making it holds (model.AutoSubmits, and the same gate
	// orchestrator.relayProposedTasks applies to a proposal replayed at
	// the end of a run rather than filed while it is still going).
	//
	// autoMerge is capped by it rather than decided by it -- an agent
	// that opted its proposal out of the merge queue stays out -- while
	// the approval is the permission itself: a task whose own commits
	// merge unread is trusted to queue work of the same shape on the
	// same branch, so its proposals are filed queued rather than waiting
	// on a click nobody would withhold.
	// The bound on the approval is the second half of it
	// (model.AutoSubmitLimits): the permission says a proposal of this
	// shape needs no click, and the bound says how far a tree of such
	// proposals may grow below the last person who approved anything --
	// a proposal past it is filed for a human, with a note saying why,
	// exactly as orchestrator.relayProposedTasks files one.
	//
	// It applies to the approval and not to autoMerge, which is capped
	// by the permission alone: whether this proposal's own pull request
	// is read is the question its parent's setting answers, and a human
	// who is now being asked to queue the work can see the answer on the
	// proposal in front of them.
	//
	// sessionQueued is the other permission that reaches the queue
	// without a click, and the one this path used not to know about: a
	// proposal from a standing console session whose deployment has said
	// that session may queue its own work
	// (model.ConsoleSessionQueuesProposals). A session files through
	// here, not through the end-of-run replay -- its runs have a daemon
	// to file through -- so leaving it out meant the director was told
	// its tasks were queued while every one of them was filed proposed
	// (grain/task-311).
	//
	// It is not bounded by AutoSubmitLimits and the inherited permission
	// is, exactly as orchestrator.relayProposedTasks has it: a session's
	// permission is not a run's inherited one but a person sitting in
	// front of the chat, every task it files is one somebody just asked
	// for in a message, and a cap on those would be a cap on how much a
	// human may ask for in a day. What the person cannot see is what that
	// work goes on to queue for itself, so a session-queued task counts
	// as a level of self-approved work like any other
	// (model.SelfApproved) and everything below it is bounded as usual.
	//
	// It says nothing about autoMerge, which stays capped by AutoSubmits
	// alone. Whether a proposal's own pull request is read is a question
	// about the branch it lands on, and a session that may queue work has
	// been given nothing about anybody's branch.
	//
	// Neither permission reaches a proposal that asked for a capability
	// its proposer does not hold (model.EscalatedCapabilities). It is
	// filed holding what it asked for -- a run may know its follow-up
	// needs a grant it did not -- but for a person to approve, with a
	// note naming the capabilities so approving the work is not silently
	// approving them too. AutoSubmits refuses it on its own; the session's
	// permission has to be told.
	//
	// Nor does either reach one that reads or lands somewhere its proposer
	// could not (model.UngrantedReads, model.UngrantedWrites): filed as
	// asked, for a person, with a note naming the repositories --
	// relayProposedTasks' rule for the identical call replayed at the end
	// of a run. Reads gate the queueing only, so autoMerge is decided on
	// the child without them, as model.AutoSubmits documents.
	// A change target (a registered kind) is checked against everything else the
	// request asked for, and attaches the grant it implies -- the task's
	// own CitC workspace -- before the proposal's grants are measured
	// against its proposer's below. needsPersonWhy is set for a target a
	// person has to approve whatever the proposer's permission.
	var changeTarget *model.ChangeTarget
	var needsPersonWhy string
	if req.ChangeTarget != nil {
		probe := model.Task{ChangeTarget: req.ChangeTarget, Writes: writes, AutoMerge: req.AutoMerge, Grants: grants, Links: links}
		if err := model.ValidateTaskChangeTarget(&probe); err != nil {
			return Task{}, validationErrorf("%v", err)
		}
		changeTarget, grants = probe.ChangeTarget, probe.Grants
		if err := c.changeTargetFilingRefusal(ctx, changeTarget); err != nil {
			return Task{}, err
		}
		if needs, why := model.ChangeTargetNeedsPersonApproval(changeTarget); needs {
			needsPersonWhy = why
		}
	}
	catalogueHold := false
	if len(catalogueNeedsPerson) > 0 && (proposedBy != nil || c.requestActor(ctx).Actor.Kind == model.PrincipalAgent) {
		why := model.CatalogueRequiresPersonNote(catalogueNeedsPerson)
		if needsPersonWhy == "" {
			needsPersonWhy = why
		} else {
			needsPersonWhy += "; " + why
		}
		if proposedBy == nil {
			catalogueHold, req.Approved = true, false
		}
	}
	keepRunning := req.KeepRunning || (req.KeepRunningSnake != nil && *req.KeepRunningSnake)
	autoMerge, autoSubmit, sessionQueued := req.AutoMerge, false, false
	var limitNote, ungrantedNote, heldNote, personOnlyNote, refusal string
	var escalated []string
	if proposedBy != nil {
		child := model.Task{Target: target, Base: req.Base, Grants: grants, Writes: writes}
		autoMerge = autoMerge && proposedBy.AutoMerge && model.AutoSubmits(child, *proposedBy)
		child.Reads = reads
		escalated = model.EscalatedCapabilities(child, *proposedBy)
		autoSubmit = model.AutoSubmits(child, *proposedBy)
		ungrantedWrites := model.UngrantedWrites(child, *proposedBy)
		ungrantedReads := model.UngrantedReads(child, *proposedBy)
		if len(ungrantedWrites) > 0 {
			ungrantedNote += model.UngrantedWritesNote(proposedBy.ID, ungrantedWrites)
		}
		if len(ungrantedReads) > 0 {
			ungrantedNote += model.UngrantedReadsNote(proposedBy.ID, ungrantedReads)
		}
		if sessionQueued, err = c.consoleSessionQueuesProposals(ctx, *proposedBy); err != nil {
			return Task{}, err
		}
		if len(escalated) > 0 || len(ungrantedWrites) > 0 || len(ungrantedReads) > 0 {
			sessionQueued = false
		}
		refusal = model.AutoApprovalRefusal(child, *proposedBy, sessionQueued)
		// Held (propose_task's hold_for_approval): whatever the
		// proposer's permission would have done, nothing below approves
		// this. Asked before the bound on self-approved work, which a
		// proposal that is not going to be self-approved neither counts
		// toward nor is measured against. The tag and the note go only on
		// one the permission would actually have queued: one it did not
		// cover was waiting for a person anyway.
		if req.HoldForApproval {
			if autoSubmit || sessionQueued {
				heldNote = model.HeldForApprovalNote(proposedBy.ID)
			}
			autoSubmit, sessionQueued, req.Approved = false, false, false
		}
		if needsPersonWhy != "" {
			if autoSubmit || sessionQueued || req.Approved {
				heldNote = fmt.Sprintf("\n\n_Filed for a person to approve: %s._", needsPersonWhy)
			}
			autoSubmit, sessionQueued, req.Approved = false, false, false
		}
		// keep_running, or a priority above the proposer's own: settings
		// only a person may give a task (model.PersonOnlyProposalSettings),
		// so neither permission queues a proposal that asks for one. Filed
		// as asked, with a note saying which, and told to the run as what
		// its permission does not cover.
		if why := model.PersonOnlyProposalSettings(model.Task{KeepRunning: keepRunning, Priority: priority}, *proposedBy); why != "" {
			if autoSubmit || sessionQueued || req.Approved {
				personOnlyNote = fmt.Sprintf("\n\n_Filed for a person to approve: %s._", why)
			}
			if refusal == "" {
				refusal = why
			}
			autoSubmit, sessionQueued, req.Approved = false, false, false
		}
		if autoSubmit && !sessionQueued {
			limits, err := c.autoSubmitLimits(ctx)
			if err != nil {
				return Task{}, err
			}
			limit, err := c.Store.ExceededAutoSubmitLimit(ctx, proposedBy.ID, limits)
			if err != nil {
				return Task{}, err
			}
			if limit != model.AutoSubmitWithinLimits {
				autoSubmit = false
				limitNote = model.AutoSubmitLimitNote(proposedBy.ID, limit, limits)
			}
		}
	}
	// Who filed this, which for a proposal is not whoever this client
	// acts as. A request naming ProposedBy is an agent's propose_task
	// call arriving over the API, so it is recorded exactly as the
	// end-of-run replay records the identical call
	// (orchestrator.relayProposedTasks): grain, on behalf of the task
	// that proposed it. Filed live it used to read as the operator's own
	// task -- "Filed by: operator (human)" on work no operator had seen
	// -- which is the one thing a provenance field must never say
	// (grain/task-311).
	origin := model.Origin{
		Attribution: model.Attribution{Actor: c.requestActor(ctx).Actor},
		Reason:      model.ReasonDirect,
	}
	if proposedBy != nil {
		origin.Attribution = model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: proposedBy.ID},
		}
	}
	// The template a person filed this from, which is how the task's runs
	// find that template's declared push set (model.Task.TemplateID). An
	// agent's proposal records none, even one naming a template: a run
	// cannot hand the refs it may push on to work it files.
	var filedFrom string
	if proposedBy == nil && c.requestActor(ctx).Actor.Kind != model.PrincipalAgent {
		filedFrom = strings.TrimSpace(req.TemplateID)
	}
	task := model.Task{
		ID:                   id,
		Intent:               model.IntentImplement,
		Title:                req.Title,
		Body:                 req.Description + forkNote + ungrantedNote + reviewTemplateNote + limitNote + heldNote + personOnlyNote + neverEndingNote + supersedeRefusal + model.OriginKeptNote(req.Origin, target, proposedBy),
		Origin:               origin,
		Target:               target,
		ChangeTarget:         changeTarget,
		Reviewers:            model.NormalizeReviewers(req.Reviewers),
		Binding:              model.BindingDirective,
		Base:                 req.Base,
		Priority:             priority,
		Issue:                req.Issue,
		AutoMerge:            autoMerge,
		AutoApproveProposals: req.AutoApproveProposals,
		Interactive:          req.Interactive,
		SandboxCPUs:          req.SandboxCPUs,
		SandboxMemoryMB:      req.SandboxMemoryMB,
		SandboxDiskGB:        req.SandboxDiskGB,
		NetworkAccess:        model.NetworkAccess(req.NetworkAccess),
		NothingNeededAction:  nothingNeeded,
		KeepRunning:          keepRunning,
		NoPort:               req.NoPort,
		WorkOrigin:           explicitOrigin,
		AgentFramework:       agentFramework,
		AgentBackendID:       strings.TrimSpace(req.AgentBackendID),
		Model:                chosenModel,
		ModelSource:          modelSourceOfRequest(req, chosenModel),
		PromptExtension:      strings.TrimSpace(req.PromptExtension),
		ReviewTemplateID:     reviewTemplateID,
		TemplateID:           filedFrom,
		Creator:              cmp.Or(strings.TrimSpace(req.Author), strings.TrimSpace(req.Creator)),
		TimeZone:             validRequesterTimeZone(req.TimeZone),
		Grants:               grants,
		Links:                links,
		Reads:                reads,
		Writes:               writes,
		CustomFields:         customFields,
		Tags:                 proposalTags(req.ProposalKey, heldNote != ""),
		CreatedAt:            &now,
		OrderKey:             orderKey,
	}
	// A proposal is its proposer's root requester's work, whoever the
	// request names: an agent's propose_task call cannot file as anybody,
	// and filing as nobody would read as grain's own to local policy.
	if proposedBy != nil {
		model.InheritRequester(&task, *proposedBy)
	}
	if req.FixesBaseBranch && !model.SetFixesBaseBranch(&task, true) {
		return Task{}, validationErrorf("a task with no repo has no base branch to fix: " +
			"name its repo, or leave \"fixes a red base branch\" unticked")
	}
	// Who queued this, recorded so "who let this run?" is answerable
	// afterwards: a person filing it here, a standing session this
	// deployment trusts to queue its own work, or the permission its
	// proposer holds to land work nobody reads. The same three, in the
	// same order, as orchestrator.relayProposedTasks' own switch -- a
	// session's permission is not measured against AutoSubmitLimits, so
	// it has to be asked before the bounded one.
	//
	// Interactive approves only a task a person files: a run's proposal
	// can be interactive too, through an interactive template's
	// templateId (fromTemplate), and that must not turn propose_task into
	// a way to approve its own work.
	switch {
	case catalogueHold:
		task.Body += "\n\n_Filed for a person to approve: " + needsPersonWhy + "._"
	case req.Approved || (req.Interactive && proposedBy == nil && c.requestActor(ctx).Actor.Kind != model.PrincipalAgent):
		task.Approval = &model.Attribution{Actor: c.requestActor(ctx).Actor}
		task.ApprovedAt = &now
	case sessionQueued:
		task.Approval = &model.Attribution{Actor: model.ConsoleSessionPrincipal}
		task.ApprovedAt = &now
	case autoSubmit:
		task.Approval = &model.Attribution{Actor: model.AutoSubmitPrincipal}
		task.ApprovedAt = &now
	}
	if task.Approval == nil && proposedBy != nil {
		task.Body += model.ProposalEscalationNote(proposedBy.ID, model.Task{Grants: grants}, escalated)
	}
	if req.DeferredUntil != nil && req.DeferredUntil.After(now) {
		until := req.DeferredUntil.UTC()
		task.DeferredAt = &now
		task.DeferredUntil = &until
	}
	// Requested before the task is written, so no cycle can find the
	// task queued without the request that holds it back from dispatch.
	if req.NewBranch {
		b, _, err := c.Store.RequestBranchFrom(ctx, *target, req.Base, req.NewBranchFrom, now)
		if err != nil {
			if errors.Is(err, model.ErrInvalidBranchName) {
				return Task{}, validationErrorf("invalid branch name")
			}
			return Task{}, err
		}
		// An earlier request for the same name that is still waiting to
		// be cut will be cut from its own base, not this one: say so
		// rather than file a task onto a branch made from something other
		// than what its form asked for (grain/task-2387). One already
		// created or adopted is on GitHub, and is used as it stands.
		if b.Status == model.BranchPending && b.From != req.NewBranchFrom {
			return Task{}, validationErrorf("branch %q is already waiting to be created on %s from %s: "+
				"create it from that too, or name another branch", req.Base, target, branchBaseLabel(b.From))
		}
	}
	if err := c.Store.PutTask(ctx, task); err != nil {
		return Task{}, err
	}
	if req.Goal != nil {
		by := model.PrincipalString(model.Principal{Kind: model.PrincipalHuman, ID: "person"})
		if req.ProposedBy != "" {
			by = model.PrincipalString(model.Principal{Kind: model.PrincipalAgent, ID: req.ProposedBy})
		}
		if err := c.Store.SetTaskGoal(ctx, id, *req.Goal, by, now); err != nil {
			return Task{}, err
		}
	}
	// grain's notes, not the filer's: it was grain that left these off.
	for _, refusal := range droppedRefusals {
		if _, err := c.Store.AddComment(ctx, model.Comment{
			TaskID:    id,
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
			Body:      refusal,
			CreatedAt: now,
		}); err != nil {
			return Task{}, err
		}
	}
	var supersedeResult model.SupersedeResult
	if supersedes != "" {
		superseded, err := c.Store.Supersede(ctx, supersedes, task, req.ProposedBy, now)
		if err != nil {
			return Task{}, err
		}
		supersedeResult = superseded
		if refusal := superseded.Refusal; refusal != "" {
			// It qualified when it was checked above and does not now
			// -- Store.Supersede says how that happens. The link stays,
			// since "this was filed to replace that" is still true, and
			// the change is said on the new task's own thread where
			// whoever reads the pair will find it.
			if _, err := c.Store.AddComment(ctx, model.Comment{
				TaskID:    id,
				Author:    model.Attribution{Actor: c.requestActor(ctx).Actor},
				Body:      model.SupersedeRefusedLine(supersedes, refusal),
				CreatedAt: now,
			}); err != nil {
				return Task{}, err
			}
		}
	}
	// Remembered only once the task it came with is actually filed, and
	// only when the request stated a choice at all: "where the last task
	// added went" is a fact about tasks that exist, and a caller that
	// said nothing has expressed nothing to remember
	// (CreateTaskRequest.AtFront).
	if req.AtFront != nil {
		if err := c.Store.SetNewestFirst(ctx, *req.AtFront); err != nil {
			return Task{}, err
		}
	}
	for i := range attachments {
		attachments[i].TaskID = id
		if _, err := c.Store.AddAttachment(ctx, attachments[i]); err != nil {
			return Task{}, err
		}
	}
	allowed := c.teamTargetRepos(ctx, teamID)
	parkNote := ""
	if target != nil && !targetAllowed(allowed, *target) {
		if err := c.parkOffAllowlist(ctx, id, *target, now); err != nil {
			return Task{}, err
		}
		parkNote = model.OffAllowlistParkComment(*target)
	} else if parkUnreached {
		parkNote = reachParkComment(reachRefused)
		if err := c.park(ctx, id, parkNote, now); err != nil {
			return Task{}, err
		}
	}
	if revertsID != "" {
		if orig, err := c.Store.GetTask(ctx, revertsID); err == nil && orig != nil {
			linked := *orig
			linked.Links = append(append([]model.Link{}, orig.Links...), model.Link{Kind: model.LinkRevertedBy, Target: id})
			_ = c.Store.PutTask(ctx, linked)
		}
	}
	created, err := c.Task(ctx, id)
	if err != nil {
		return Task{}, err
	}
	created.SupersedeMoved = supersedeResult.Moved
	created.ParkNote = parkNote
	created.DroppedDefaults = policyDropped
	if proposedBy != nil && !created.Approved {
		created.ApprovalNote = proposalApprovalNote(req.HoldForApproval, heldNote != "", limitNote != "", refusal)
	}
	for _, k := range supersedeResult.Kept {
		created.SupersedeKept = append(created.SupersedeKept, SupersedeKeptEntry{ID: k.ID, Reason: k.Reason})
	}
	if created.Interactive && created.State == model.StateQueued && c.Config.Wake != nil {
		c.Config.Wake()
	}
	return created, nil
}

// proposalApprovalNote is why a proposal CreateTask just filed is waiting
// for a person, in the words propose_task relays to the run that filed it
// (Task.ApprovalNote): that it asked to be held, that the bound on
// self-approved work was reached, or what its proposer's permission does
// not cover.
func proposalApprovalNote(asked, held, limited bool, refusal string) string {
	switch {
	case held:
		return "you asked to hold it (hold_for_approval); your permission would otherwise have queued it"
	case asked && refusal != "":
		return "you asked to hold it (hold_for_approval), and your permission would not have covered it anyway: " + refusal
	case asked:
		return "you asked to hold it (hold_for_approval)"
	case limited:
		return "your permission covers it, but the limit on how far self-approved work may grow was reached -- its body says which"
	case refusal != "":
		return "your permission does not cover it: " + refusal
	}
	return ""
}

// supersedeCandidate resolves CreateTaskRequest.Supersedes: the id of
// the task the new one replaces and is about to close, and -- when grain
// will not close it -- the note that goes in the new task's body instead
// (model.SupersedeRefusal states every reason and why each one stands).
//
// target is where the new task is being filed, since the bound is that a
// supersede closes a task in the repository its replacement lands in.
//
// A "#" prefix is tolerated, exactly as orchestrator.proposedSupersedes
// tolerates it on the same argument replayed at the end of a run: the
// two paths file the same propose_task call, and an agent's spelling
// must not decide which of them honours it.
func (c *Client) supersedeCandidate(ctx context.Context, named string,
	target *model.RepoRef) (id, note string, err error) {

	named = model.NormalizeTaskID(named)
	if named == "" {
		return "", "", nil
	}
	old, refusal, err := c.Store.SupersedeCandidate(ctx, named, target)
	if err != nil {
		return "", "", err
	}
	if refusal != "" {
		return "", model.SupersedeRefusedNote(named, refusal), nil
	}
	return old.ID, "", nil
}

// validReviewTemplate checks the review a task is being filed or edited
// with, returning the id to store: "" for no review, and otherwise the
// trimmed id, once it has been confirmed to name a template that
// actually exists.
//
// Checked at the point somebody chooses it rather than at the point it
// is used, because those moments are far apart: a review is resolved
// once the task's own work is finished (orchestrator.fileReviewTask),
// which may be hours later, and a typo discovered then costs the review
// entirely -- the task's pull request waits on one that can never be
// filed until the merge queue gives up on it. It is not re-checked at
// use: ui.Client.DeleteTemplate refuses to delete a template an open
// task still names, which is the same guard from the other side.
func (c *Client) validReviewTemplate(ctx context.Context, id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", nil
	}
	tmpl, err := c.Store.GetTemplate(ctx, trimmed)
	if err != nil {
		return "", err
	}
	if tmpl == nil {
		return "", validationErrorf("unknown review template %s", trimmed)
	}
	return trimmed, nil
}

// defaultReviewTemplateID returns the ID of target's declared default review
// template, if one exists in the store.
func (c *Client) defaultReviewTemplateID(ctx context.Context, target model.RepoRef) (string, error) {
	id := model.DefaultReviewTemplateID(target)
	tmpl, err := c.Store.GetTemplate(ctx, id)
	if err != nil {
		return "", err
	}
	if tmpl != nil {
		return id, nil
	}
	return "", nil
}

// validateAgentFramework checks a task's own AgentFramework override.
// "" is the common case and always valid -- it is what "use the
// deployment's own framework" is spelled as (model.Task.AgentFramework's
// own doc comment) -- so unlike UpdateSettings' check of the
// deployment-wide setting, which has no such reading, empty is accepted
// here rather than rejected as unset.
func validateAgentFramework(framework string) error {
	// "" is checked here rather than left to ValidAgentFramework, which
	// deliberately rejects it: for a task, empty means "no override"
	// rather than naming a framework (model.Task.AgentFramework's own
	// doc comment), and only this caller reads it that way.
	if framework == "" || model.ValidAgentFramework(framework) {
		return nil
	}
	return validationErrorf("agentFramework must be %s, or empty for this deployment's default",
		model.AgentFrameworkNames())
}

// modelSourceOfRequest is model.Task.ModelSource for a task CreateTask
// files with chosenModel: the template's, when that is the model
// fromTemplate supplied, and the task's own (empty) otherwise.
func modelSourceOfRequest(req CreateTaskRequest, chosenModel string) model.CompositionLayer {
	if chosenModel == "" || chosenModel != req.modelFromTemplate {
		return ""
	}
	return model.LayerTemplate
}

// agentBackendFramework checks a task's own model pin
// (model.Task.AgentBackendID) against the pool as Settings has it right
// now, and answers with the framework that model runs on.
//
// "" is "no pin, use the rotation" and answers with the framework
// unchanged, exactly as an empty AgentFramework is left alone above.
// Anything else must name a configured model; a model that has been
// taken out of the rotation is deliberately still eligible, since being
// named by hand is the one way a disabled model runs at all
// (model.Task.AgentBackendID's own doc comment).
//
// A task that names a model *and* a framework that model does not run on
// is refused here rather than resolved one way or the other. Both were
// stated, so neither is safe to overrule, and the alternative is a task
// that quietly dispatches onto something nobody asked for -- the reading
// cmd/grain's own pinnedBackend takes of the same pair at dispatch, when
// a pool edit has made a queued task's two halves disagree after the
// fact.
// splitModelChoice separates what a request asked for into the model it
// chose and the connection it pinned (model_selection.go). Before
// model.Task.Model existed a model was asked for through agentBackendId,
// so an agentBackendId naming no connection is read as a model choice.
// A pin together with a model that connection does not advertise is
// refused: dispatch would have nothing it could run.
func (c *Client) splitModelChoice(ctx context.Context, chosenModel, backendID string) (string, string, error) {
	chosenModel, backendID = strings.TrimSpace(chosenModel), strings.TrimSpace(backendID)
	if chosenModel == "" && backendID == "" {
		return "", "", nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return "", "", err
	}
	var backends []model.AgentBackend
	if cfg != nil {
		backends = cfg.AgentBackends
	}
	var pinned *model.AgentBackend
	for i := range backends {
		if backends[i].ID == backendID {
			pinned = &backends[i]
			break
		}
	}
	// A "model" naming a connection is the older spelling of a pin.
	if backendID == "" && chosenModel != "" {
		for i := range backends {
			if backends[i].ID == chosenModel && !backends[i].SupportsModel(chosenModel) {
				pinned, backendID, chosenModel = &backends[i], chosenModel, ""
				break
			}
		}
	}
	if backendID != "" && pinned == nil {
		if chosenModel != "" && chosenModel != backendID {
			return "", "", validationErrorf("agentBackendId %q is not a configured connection", backendID)
		}
		// The legacy spelling of a model choice.
		chosenModel, backendID = backendID, ""
	}
	if pinned != nil && chosenModel != "" && chosenModel != pinned.ID && !pinned.SupportsModel(chosenModel) {
		return "", "", validationErrorf("connection %q does not advertise model %q (it serves %s)",
			pinned.ID, chosenModel, strings.Join(pinned.SupportedModels(), ", "))
	}
	if pinned != nil && chosenModel == pinned.ID {
		chosenModel = ""
	}
	return chosenModel, backendID, nil
}

// declaredModelChoice is the model a template or a schedule chose
// (model.Template.Model, model.Schedule.Model), trimmed. Neither pins a
// connection, so the older spelling splitModelChoice still reads as a pin
// -- a "model" naming a configured connection rather than anything it
// serves -- is refused here instead of being quietly turned into one.
func (c *Client) declaredModelChoice(ctx context.Context, chosen string) (string, error) {
	m, pin, err := c.splitModelChoice(ctx, chosen, "")
	if err != nil {
		return "", err
	}
	if pin != "" {
		return "", validationErrorf("model %q names a connection, not a model it serves", pin)
	}
	return m, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (c *Client) agentBackendFramework(ctx context.Context, backendID, framework string) (string, error) {
	backendID = strings.TrimSpace(backendID)
	if backendID == "" {
		return framework, nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return "", err
	}
	var backends []model.AgentBackend
	if cfg != nil {
		backends = cfg.AgentBackends
	}
	for _, backend := range backends {
		if backend.ID != backendID {
			continue
		}
		runs := model.NormalizeAgentFrameworkName(backend.Framework)
		if want := model.NormalizeAgentFrameworkName(framework); want != "" && want != runs {
			return "", validationErrorf(
				"agentBackendId %q runs on %s, which is not the agentFramework %q this task names",
				backendID, runs, want)
		}
		return runs, nil
	}
	var matchingFrameworks []string
	for _, backend := range backends {
		if backend.SupportsModel(backendID) {
			runs := model.NormalizeAgentFrameworkName(backend.Framework)
			if !slices.Contains(matchingFrameworks, runs) {
				matchingFrameworks = append(matchingFrameworks, runs)
			}
		}
	}
	if len(matchingFrameworks) > 0 {
		want := model.NormalizeAgentFrameworkName(framework)
		if want != "" {
			if slices.Contains(matchingFrameworks, want) {
				return want, nil
			}
			return "", validationErrorf(
				"model %q is not supported on agentFramework %q",
				backendID, want)
		}
		return matchingFrameworks[0], nil
	}
	if len(backends) == 0 {
		want := model.NormalizeAgentFrameworkName(framework)
		if want != "" {
			return want, nil
		}
		if implied := model.ModelFramework(nil, "", backendID); implied != "" {
			return implied, nil
		}
		return model.AgentFrameworkAntigravity, nil
	}
	return "", validationErrorf("agentBackendId %q is not a configured model or connection", backendID)
}

// validateNetworkAccess checks a task's own NetworkAccess override,
// mirroring validateAgentFramework above: "" is accepted here rather
// than rejected, because for a task empty means "no override" rather
// than naming a network (model.Task.NetworkAccess's own doc comment),
// and only this caller reads it that way.
func validateNetworkAccess(access string) error {
	if access == "" || model.ValidNetworkAccess(model.NetworkAccess(access)) {
		return nil
	}
	return validationErrorf("networkAccess must be %s, or empty for this deployment's default",
		model.NetworkAccessNames())
}

// validateNothingNeededAction checks a task's NothingNeededAction, as
// the API spells it, and returns it as the model's type.
func validateNothingNeededAction(action string) (model.NothingNeededAction, error) {
	a, err := model.ParseNothingNeededAction(strings.TrimSpace(action))
	if err != nil {
		return "", validationErrorf("nothingNeededAction must be %q or %q, or empty for close",
			model.NothingNeededClose, model.NothingNeededAck)
	}
	return a, nil
}

// refuseIgnoredNetworkAccess refuses a per-task model.NetworkNone on a
// deployment whose sandboxes are directories on this host
// (Config.SandboxShapeIgnored -- the same flag, because it is the same
// question about the same backend: whether a sandbox here is a VM with a
// shape and a network of its own, or a path on the machine the daemon
// runs on).
//
// It is refuseIgnoredSandboxShape's own reasoning, one setting over, and
// the same trade: orchestrator.HostSandboxes.Acquire already refuses
// this, correctly, but by then the task exists, has been queued and
// dispatched, and the answer arrives in a run's log rather than to the
// person filing it. Refused rather than accepted-and-cleared for the
// reason that one gives -- and more so here, since silently clearing
// this one would run a job on the internet after somebody said it must
// not be.
//
// Only "none" is refused. "open" is what a host-directory sandbox has,
// so a task that asks for it is asking for what it will get.
func (c *Client) refuseIgnoredNetworkAccess(access string) error {
	if !c.Config.SandboxShapeIgnored || model.NetworkAccess(access) != model.NetworkNone {
		return nil
	}
	return validationErrorf(
		"this deployment's sandboxes are directories on the host grain itself runs on, which have no "+
			"network of their own to withhold, so a task filed with networkAccess %q is queued and "+
			"dispatched and then fails at sandbox setup. File it with networkAccess unset, or run it on a "+
			"deployment with VM sandboxes.", model.NetworkNone)
}

// validateSandboxShape checks a task's own SandboxCPUs/SandboxMemoryMB/
// SandboxDiskGB override, mirroring bwsalmon/granule's own
// staticpod.VMSpec.Validate bounds ("cpus must be at least 1",
// "memory-mb must be at least 128") the same way UpdateSettings'
// identical check does for the deployment-wide default -- 0 is the one
// value each rejects that Validate would not, since 0 means "no
// override" here rather than a literal request for a zero-vCPU or
// zero-memory VM.
//
// Disk has no Validate bound to mirror (granule checks a disk size
// against the guest image it reads through to rather than against a
// floor of its own), so the only value rejected there is a negative one.
func validateSandboxShape(cpus, memoryMB, diskGB int) error {
	if cpus != 0 && cpus < 1 {
		return validationErrorf("sandboxCpus must be 0 (no override) or at least 1")
	}
	if memoryMB != 0 && memoryMB < 128 {
		return validationErrorf("sandboxMemoryMb must be 0 (no override) or at least 128")
	}
	if diskGB < 0 {
		return validationErrorf("sandboxDiskGb must be 0 (no override) or at least 1")
	}
	return nil
}

// refuseIgnoredSandboxShape refuses a per-task sandbox shape on a
// deployment whose sandbox backend has none of its own
// (Config.SandboxShapeIgnored, asked of the backend this daemon actually
// built -- pkg/cli/daemon.go's sandboxShapeIgnored).
//
// This is the same answer orchestrator.HostSandboxes.Acquire already
// gives ("a host-directory sandbox has no shape of its own"), moved to
// the moment somebody states the shape. Acquire's refusal is in the
// right place for correctness and the wrong place for the person filing
// the task: by then the task exists, has been queued and dispatched, and
// the answer arrives in a run's log rather than as a failed create. The
// new-task form already disables the three fields where this is set
// (grain/task-26), so what is left to catch here is every caller that is
// not that form: an HTTPClient caller, a script, anything posting JSON
// at POST /api/tasks. (`grain task create` has no flags for the three,
// so the CLI never reached this on its own.)
//
// Refused rather than accepted-and-cleared, which is the other way this
// could have gone: clearing never fails a filing, but it silently
// discards a stated intent, and a caller that asked for 8 vCPUs and got
// a run on an unshaped directory was told nothing. Acquire was
// deliberately written not to do that, and this agrees with it
// (grain/task-33).
//
// Note that the deployment-wide default (Settings.SandboxCPUs and
// friends) is *not* refused the same way: it is stored and annotated as
// not in effect, because a value worth setting before switching a
// deployment over is worth keeping. Only a per-task shape reaches
// Acquire, and only a per-task shape kills a run.
func (c *Client) refuseIgnoredSandboxShape(cpus, memoryMB, diskGB int) error {
	if !c.Config.SandboxShapeIgnored || (cpus == 0 && memoryMB == 0 && diskGB == 0) {
		return nil
	}
	return validationErrorf(
		"this deployment's sandboxes have no shape of their own, so a per-task "+
			"%d vCPU/%d MiB/%d GiB cannot be applied: a task filed with one is "+
			"queued and dispatched and then fails at sandbox setup. File it with "+
			"sandboxCpus, sandboxMemoryMb and sandboxDiskGb unset.",
		cpus, memoryMB, diskGB)
}

// targetAllowed reports whether target may be dispatched into --
// model.RepoAllowed, which is where this rule now lives and which
// documents it. It moved there when a second package started asking the
// same question of the same list: a propose_task call may now name a
// repository other than the one its run worked in, and
// orchestrator.proposedTarget files it only against a repository this
// deployment works in (model.Store.WorksInRepo).
//
// parseRepo has already rewritten a target to the allowlist's own
// spelling by the time this is asked, so the case-folding that rule does
// matters for the tasks already stored in some other case rather than
// for new ones.
func targetAllowed(allowed []string, target model.RepoRef) bool {
	return model.RepoAllowed(allowed, target)
}

// refuseOffAllowlist is what every path that writes a repo grain would
// dispatch work into answers with when that repo is off
// Config.TargetRepos, except the one filing that parks instead
// (parkOffAllowlist): nothing is written, and the caller is told, with
// the repo and the list as it currently stands both named.
//
// Filing a task parks and everything else refuses, and the difference
// between them is whether there is anything to park. CreateTask files
// the task exactly as asked because that repo is the whole of what the
// task is for, and re-entering the task once an operator widens the
// list would be busywork. Every other writer of a target is editing
// something that already exists and already names a repo this
// deployment may work in: refusing costs nothing to redo, leaves that
// task, schedule or template exactly as it was, and does not take work
// nobody objected to out of the queue over a keystroke somebody can
// simply undo. That is parseWrites' own reasoning about an extra write
// target, which refuses for the same reason and now says so through
// here.
//
// Asked afresh on every write rather than taken on trust from the
// filing, because the answer moves underneath what is already stored:
// Config.TargetRepos is edited under Settings while tasks and schedules
// sit there (UpdateSettings), and the git proxy authorizes a sandbox
// against the live task's own Target rather than against the list
// (pkg/gitproxy/authorize.go's own doc comment), so any repo that
// reaches a Target is one grain's runs really will clone and push to.
// This and the park are where that boundary is decided on the way in
// (grain/task-336) -- and neither of them can speak for work that was
// already stored when the list narrowed, which is why the last thing
// between a task and a sandbox asks the same question a third time
// (orchestrator.reconcileTargetRepos, grain/task-404).
//
// consequence completes "`repo` is not on this deployment's target repo
// list, so ..." -- what this caller was about to do and now will not, in
// the terms of the thing being written. The sentence itself is
// model.OffAllowlistRefusal, beside the rule it is the refusal of.
func (c *Client) refuseOffAllowlist(target model.RepoRef, consequence string) error {
	return c.refuseOffAllowlistForTeam(context.Background(), model.DefaultTeamID, target, consequence)
}

func (c *Client) refuseOffAllowlistForTeam(ctx context.Context, teamID string, target model.RepoRef, consequence string) error {
	allowed := c.teamTargetRepos(ctx, teamID)
	if targetAllowed(allowed, target) {
		return nil
	}
	return validationErrorf("%s", model.OffAllowlistRefusal(target, allowed, consequence))
}

// refuseOffRepoAllowlist reports whether target is allowed by the
// deployment's target repos.
func (c *Client) refuseOffRepoAllowlist(ctx context.Context, target model.RepoRef, consequence string) error {
	allowed := c.teamTargetRepos(ctx, model.DefaultTeamID)
	if targetAllowed(allowed, target) {
		return nil
	}
	return validationErrorf("%s", model.OffAllowlistRefusal(target, allowed, consequence))
}

// parkOffAllowlist is CreateTask's counterpart to a run's own
// ask_question parking (finish.go's relayComment/PendingQuestionCommentID):
// a task naming a repo outside the team's TargetRepos is filed exactly as
// asked -- Target still names it, so fixing this needs no re-entering
// anything -- but StateOf reads PendingQuestionCommentID before Approval,
// so it never reaches task_ready, and so is never dispatched, until an
// operator widens TargetRepos.
func (c *Client) parkOffAllowlist(ctx context.Context, taskID string, target model.RepoRef, now time.Time) error {
	return c.park(ctx, taskID, model.OffAllowlistParkComment(target), now)
}

// park is what parkOffAllowlist above, and AddComment's re-park of a
// reply whose target is still off the list, do once they have their
// wording: a
// comment from grain itself, and the task pointed at it
// (Observation.PendingQuestionCommentID), which is what StateOf reads as
// awaiting_reply and task_ready reads as not dispatchable.
//
// PendingSecret is cleared with it, for AddComment's own reason in
// reverse: a task parked on a question of grain's must not still be
// offering a box that writes a credential some earlier run asked for,
// since nothing is waiting on that value any more. It is always already
// empty on a task being filed, so only AddComment's re-park ever clears
// anything here.
func (c *Client) park(ctx context.Context, taskID, reason string, now time.Time) error {
	commentID, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      reason,
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("ui: parking %s: %w", taskID, err)
	}
	return c.Store.ObserveField(ctx, taskID, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
		o.PendingSecret = ""
	})
}

// The wording of all three of the above -- the refusal, the park and the
// re-park -- is model.OffAllowlistRefusal/OffAllowlistParkComment/
// OffAllowlistAgainComment, beside RepoAllowed itself, along with the
// quoting of the list they each show back. It moved there when the
// orchestrator gained a fourth of them (grain/task-404): the rule had
// one home already, and two packages writing their own sentences about
// one boundary is how an operator ends up told two different things
// about the same list.

// grantsFor turns capability ids into model.Grants, rejecting any this
// deployment does not offer. via is what each one records as its source
// -- model.GrantByLabel for ids a caller named, model.GrantByDefault for
// ids this deployment attaches to every new task by itself.
func (c *Client) grantsFor(ids []string, via model.GrantSource) ([]model.Grant, error) {
	grants := make([]model.Grant, 0, len(ids))
	for _, id := range ids {
		// grain-ci is the target repository's CI Mode to grant, never
		// a caller's (pkg/capability/grainci's package doc), and
		// creationGrants adds or strips it from that mode after this.
		// A caller naming it is refused with the words that say so. A
		// default set still holding it -- written before task 2015
		// refused it there -- is not: dropping it changes nothing the
		// mode does not already decide, and refusing it would fail
		// every task filed against that repository.
		if id == grainci.CapabilityName {
			if via == model.GrantByDefault {
				continue
			}
			return nil, validationErrorf("%s", grainci.Refusal())
		}
		normalizedIDs, err := c.normalizeCapabilityIDs(id)
		if err != nil {
			return nil, grantValidationError(err)
		}
		for _, nid := range normalizedIDs {
			if _, ok := c.capabilityByID(nid); !ok {
				return nil, validationErrorf("%s", capabilityRefusal(nid))
			}
			grants = append(grants, model.Grant{Capability: nid, Via: via})
		}
	}
	return model.NormalizeCapabilityGrants(grants, nil)
}

// creationGrants is the grant set a new task is filed with: whatever the
// request named, or -- when it named nothing at all -- the default
// capabilities resolved for target (this deployment's, plus target's own
// repo row).
//
// With one exception, and it is the rule docs/data-model.md's "sub-tasks
// are tasks" section calls the load-bearing one: a task filed by another
// task's propose_task call (req.ProposedBy) is filed with the defaults,
// plus whatever it named. Capabilities do not inherit. An agent granted
// self-repair once, whose proposals were filed holding it, would have
// turned a single human grant into an unbounded number of uses of that
// grant -- and since grain/task-112 files an auto-merge task's proposals
// approved, into uses nobody was ever shown. Every proposal asks for its
// capabilities on its own task, where a human can see the request and
// decide: a named capability its proposer does not hold
// (model.EscalatedCapabilities) keeps it from being queued by any
// permission but a person's, and CreateTask says so in its body
// (model.CapabilityEscalationNote). What a proposal names is never
// intersected away, since a run may know its follow-up needs a grant it
// did not; it is only never granted without somebody seeing it asked.
//
// This is enforced here rather than left to each caller because
// ProposedBy is reachable over the API: the daemon's own propose_task
// filer (cmd/grain's daemonProposalFiler) used to send its parent's
// capability list, which arrived as a set of model.GrantByLabel grants
// on a task nobody had applied a label to, so the same proposal was
// filed with different authority depending on whether its run had a
// daemon to file it through or was replayed at the end
// (orchestrator.relayProposedTasks, whose defaultTaskGrants is the same
// answer). The subset check in model.AutoSubmits passed trivially on
// such a proposal, since the child's grants were exactly the parent's --
// the rule working as written on grants it should never have been handed.
//
// What a proposal does still inherit is the two things a branch and a
// merge queue are about: its parent's target repo, and, within
// model.AutoSubmits, its parent's permission to land work unreviewed
// where that parent was allowed to land it. Neither is a capability.
//
// target is the repo CreateTask has already resolved, defaults and all,
// rather than req.Repo as written: a request that names no repo is filed
// against Config.DefaultTarget, and that repo's own defaults are the ones
// such a task should get. nil is a NoRepo task, which has no repo layer
// to resolve at all.
//
// The two are told apart by CreateTaskRequest.Capabilities being nil
// rather than empty, and they record different sources: a caller's ids
// are model.GrantByLabel ("a human applied it"), a deployment's are
// model.GrantByDefault. Nothing reads Via to decide what a grant does --
// it is provenance, so that a task carrying gcp-key nobody remembers
// asking for can be traced back to Settings or to the repo page rather
// than to whoever filed it. One source for both layers, deliberately:
// which of the two attached it is a question the two panes that own them
// answer, and a task holding a grant it can drop either way has nothing
// to do differently with the answer.
func (c *Client) creationGrants(ctx context.Context, req CreateTaskRequest, target *model.RepoRef, teamID string) ([]model.Grant, []string, error) {
	var grants []model.Grant
	if req.ProposedBy != "" {
		ids, err := c.defaultCapabilities(ctx, teamID, target)
		if err != nil {
			return nil, nil, err
		}
		defaults, err := c.grantsFor(ids, model.GrantByDefault)
		if err != nil {
			return nil, nil, err
		}
		grants = defaults
		if req.Capabilities != nil {
			for _, id := range *req.Capabilities {
				id = strings.TrimSpace(id)
				if id == "" {
					continue
				}
				// An agent's proposal may name grain-ci because its own
				// task holds it. That is passed over rather than refused:
				// the target repository's CI Mode decides it below, as it
				// does for every task, and a propose_task call failing
				// on a grant grain itself handed the agent would be the
				// wrong lesson.
				if id == grainci.CapabilityName {
					continue
				}
				normalizedIDs, err := c.normalizeCapabilityIDs(id)
				if err != nil {
					return nil, nil, err
				}
				for _, nid := range normalizedIDs {
					if _, ok := c.capabilityByID(nid); !ok {
						return nil, nil, validationErrorf("%s", capabilityRefusal(nid))
					}
					found := false
					for _, g := range grants {
						if g.Capability == nid {
							found = true
							break
						}
					}
					if !found {
						grants = append(grants, model.Grant{
							Capability: nid,
							Via:        model.GrantByLabel,
						})
					}
				}
			}
		}
	} else if req.Capabilities != nil {
		named, err := c.grantsFor(*req.Capabilities, model.GrantByLabel)
		if err != nil {
			return nil, nil, err
		}
		if len(req.grantsFromTemplate) > 0 {
			tmplGrants := make(map[string]model.GrantSource, len(req.grantsFromTemplate))
			for _, g := range req.grantsFromTemplate {
				tmplGrants[g.Capability] = g.Via
			}
			for i, g := range named {
				if via, ok := tmplGrants[g.Capability]; ok {
					named[i].Via = via
				}
			}
		}
		grants = named
	} else {
		ids, err := c.defaultCapabilities(ctx, teamID, target)
		if err != nil {
			return nil, nil, err
		}
		defaults, err := c.grantsFor(ids, model.GrantByDefault)
		if err != nil {
			return nil, nil, err
		}
		grants = defaults
	}
	if target != nil {
		usesGrainCI, err := c.teamRepoUsesGrainCI(ctx, teamID, *target)
		if err != nil {
			return nil, nil, err
		}
		if usesGrainCI {
			found := false
			for _, grant := range grants {
				if grant.Capability == grainci.CapabilityName {
					found = true
					break
				}
			}
			if !found {
				grants = append(grants, model.Grant{
					Capability: grainci.CapabilityName,
					Via:        model.GrantByDefault,
				})
			}
		} else {
			filtered := make([]model.Grant, 0, len(grants))
			for _, grant := range grants {
				if grant.Capability != grainci.CapabilityName {
					filtered = append(filtered, grant)
				}
			}
			grants = filtered
		}
	}
	// A GCP key the deployment has not configured is refused when somebody
	// asked for it, and dropped when it was only inherited: a deployment or
	// repo default naming a key nobody has set up yet is not a reason every
	// task on that repo fails to file. A declaration in .grain naming one is
	// dropped and explained on the task thread.
	allowed := grants[:0:0]
	var droppedRefusals []string
	for _, g := range grants {
		if g.Capability != grainci.CapabilityName {
			if keyName, ok := model.GCPKeyName(g.Capability); ok && keyName != "" && c.Store != nil {
				k, err := c.Store.GetGCPKey(ctx, keyName)
				if err != nil {
					return nil, nil, err
				}
				if k == nil {
					if g.Via == model.GrantByDefault {
						continue
					}
					refusal := model.MissingGCPKeyRefusal(g.Capability)
					if g.Via.IsDeclaration() {
						droppedRefusals = append(droppedRefusals, refusal)
						continue
					}
					return nil, nil, validationErrorf("%s", refusal)
				}
			}
		}
		allowed = append(allowed, g)
	}
	return allowed, droppedRefusals, nil
}

func (c *Client) teamRepoUsesGrainCI(ctx context.Context, teamID string, repo model.RepoRef) (bool, error) {
	if grainci.RequiredFor(repo) {
		return true, nil
	}
	if c.Store == nil {
		return false, nil
	}
	return c.Store.RepoUsesGrainCI(ctx, repo)
}

// dependsOnLinks turns a set of task IDs into model.LinkDependsOn links,
// rejecting one that names no task, one that names selfID, and one that
// names a task nothing will ever finish -- caught here, before the store
// is touched, rather than left to surface as a task silently blocked on
// nothing (an unknown id) or forever (itself, or a standing chat
// session). Blank entries and repeats are dropped rather than rejected:
// a picker built from free text will produce both.
//
// The third refusal is the other two's argument with a third cause:
// model.Store.DependencyNeverSatisfiable names a task that by design
// never reaches a terminal state, and a blocking link is released only
// by its target closing, so such a link is as final as a self-dependency
// (grain/task-487). A person attaching one in the picker is told so and
// nothing is written; a proposal a run filed while it was still going is
// not failed over it -- CreateTask drops those entries first and says so
// in the body, since losing a whole proposal to an argument nobody will
// ever see is worse than filing it with the ordering written out in
// words.
func (c *Client) dependsOnLinks(ctx context.Context, ids []string, selfID string) ([]model.Link, error) {
	links := make([]model.Link, 0, len(ids))
	seen := map[string]bool{}
	selfID = model.NormalizeTaskID(selfID)
	for _, raw := range ids {
		id := model.NormalizeTaskID(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if id == selfID {
			return nil, validationErrorf("a task cannot depend on itself")
		}
		target, err := c.Store.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		if target == nil {
			return nil, validationErrorf("depends on unknown task %s", id)
		}
		never, err := c.Store.DependencyNeverSatisfiable(ctx, id)
		if err != nil {
			return nil, err
		}
		if never {
			return nil, validationErrorf("task %s never completes -- it is a standing chat "+
				"session, and a task waiting on it would never be dispatched", id)
		}
		if selfID != "" {
			path, err := c.blockingPath(ctx, id, selfID)
			if err != nil {
				return nil, err
			}
			if path != nil {
				return nil, validationErrorf("task %s already waits on task %s (%s), so task %s depending on it "+
					"would be a loop neither could leave", id, selfID, strings.Join(path, " → "), selfID)
			}
		}
		links = append(links, model.Link{Kind: model.LinkDependsOn, Target: id})
	}
	return links, nil
}

// blockingPath is the chain of blocking links (depends-on, child-of) from
// from to to, both ends included, or nil if there is none. A task that is
// closed waits on nothing, so the walk does not go through one. Without
// this a person could attach a dependency that closes a loop and both
// tasks would sit blocked for good.
func (c *Client) blockingPath(ctx context.Context, from, to string) ([]string, error) {
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == to {
			var path []string
			for at := id; at != ""; at = prev[at] {
				path = append([]string{at}, path...)
			}
			return path, nil
		}
		st, err := c.Store.State(ctx, id)
		if err != nil {
			return nil, err
		}
		if st == model.StateClosed {
			continue
		}
		task, err := c.Store.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		if task == nil {
			continue
		}
		for _, l := range task.Links {
			if !l.Kind.Blocks() {
				continue
			}
			next := model.NormalizeTaskID(l.Target)
			if _, seen := prev[next]; seen || next == "" {
				continue
			}
			prev[next] = id
			queue = append(queue, next)
		}
	}
	return nil, nil
}

// neverEndingDependencies splits a proposal's depends_on into the
// entries grain will make links from and the ones it will not, because
// they name a task that never completes
// (model.Store.DependencyNeverSatisfiable).
//
// Only a proposal goes through here. A person naming such a task in the
// picker gets a validation error from dependsOnLinks and can pick
// something else; a run that filed a proposal mid-run is not there to be
// told, and the proposal is worth more than the argument -- see
// model.NeverEndingDependencyNote, which is what the approver reads
// instead of the link.
func (c *Client) neverEndingDependencies(ctx context.Context, ids []string) (kept, dropped []string, err error) {
	for _, raw := range ids {
		id := model.NormalizeTaskID(raw)
		if id == "" {
			continue
		}
		never, err := c.Store.DependencyNeverSatisfiable(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if never {
			dropped = append(dropped, id)
			continue
		}
		kept = append(kept, id)
	}
	return kept, dropped, nil
}

// WriteTarget is one of a task's extra writeable repos as this API
// spells it: the repo, and the branch work for it is based on --
// model.WriteTarget, with the repo written the one way this package
// writes a repo (owner/name).
//
// Base empty means that repo's default branch, exactly as an empty Base
// on the task itself does. It is a separate field rather than an
// "owner/name@branch" string because a caller building one of these in
// JSON should not have to know an escaping rule, and because a UI that
// offers a branch box needs the two apart anyway.
type WriteTarget struct {
	Repo string `json:"repo"`
	Base string `json:"base,omitempty"`
}

// parseWrites turns a request's extra write targets into
// model.WriteTargets, refusing here rather than downstream the two ways
// one can be malformed: a repo that is not owner/name (parseReads' own
// reasoning, and the same parseRepo so a repo is spelled one way
// whichever field it was typed into), and a repo named twice.
//
// A duplicate is refused rather than folded together because the two
// entries disagree about something that matters: the branch. task_write
// is keyed by (task, owner, name), so the second row would silently
// replace the first, and a form that offered "acme/widgets on main" and
// "acme/widgets on deploy" would file whichever survived the write with
// no sign the other was dropped.
//
// The task's own target is refused too, by refuseTargetAmongWrites --
// separately, because an edit does not always state the target and so
// cannot know it until it is holding the stored task.
//
// A repo off this deployment's target repo list is refused here as well,
// and refused rather than parked. A task whose *own* target is off the
// list is filed anyway and parked for an operator (parkOffAllowlist),
// because that repo is the whole of what the task is for and re-entering
// it later would be busywork. An extra write target is not: the task's
// own work is unaffected by it, so parking would stop work an operator
// never objected to over an entry somebody can simply take off again --
// and filing it unparked would be worse still, since the proposal it
// eventually produces is written straight to the store
// (orchestrator.relayProposedTasks) and passes through no allowlist
// check of its own. This is the one place that boundary can be decided
// for it, and it is decided in the words every other refusal of an
// off-list repo uses (refuseOffAllowlist).
func (c *Client) parseWrites(ctx context.Context, teamID string, writes []WriteTarget) ([]model.WriteTarget, error) {
	out := make([]model.WriteTarget, 0, len(writes))
	for _, w := range writes {
		text := strings.TrimSpace(w.Repo)
		if text == "" {
			continue
		}
		repo, err := c.parseRepo(text)
		if err != nil {
			return nil, err
		}
		if err := c.refuseOffAllowlistForTeam(ctx, teamID, repo, "it cannot be listed as a repo to write to"); err != nil {
			return nil, err
		}
		base := strings.TrimSpace(w.Base)
		if err := validateBase(fmt.Sprintf("base for %s", repo), base); err != nil {
			return nil, err
		}
		for _, already := range out {
			if already.Repo.Same(repo) && already.Base == base {
				if base == "" {
					return nil, validationErrorf("%s is listed twice as a repo to write to on its default branch", repo)
				}
				return nil, validationErrorf("%s is listed twice as a repo to write to on branch %s", repo, base)
			}
		}
		out = append(out, model.WriteTarget{Repo: repo, Base: base})
	}
	return out, nil
}

// refuseTargetAmongWrites rejects an extra write target that is the
// task's own repo on the task's own base branch: that is where its runs
// already push, and a second declaration of it on the same branch is redundant.
// Listing the task's own repo on another branch gives the task an additional
// branch to write to.
func refuseTargetAmongWrites(writes []model.WriteTarget, target *model.RepoRef, base string) error {
	if target == nil {
		return nil
	}
	for _, w := range writes {
		if w.Repo.Same(*target) && w.Base == base {
			return validationErrorf(
				"%s is this task's own repo and base branch, which it already writes to",
				w.Repo)
		}
	}
	return nil
}

// parseReads turns a set of owner/name strings into model.RepoRefs,
// rejecting one that does not parse before the store is touched -- the
// same "caught here, not left to surface later" reasoning target's own
// parse in CreateTask follows. Blank entries are dropped rather than
// rejected, matching dependsOnLinks: a picker built from free text will
// produce them.
//
// A read-only repo is spelled the deployment's way like any other
// (parseRepo): a repo grain both works in and reads from is one repo,
// and it should not be two depending on which field it was typed into.
//
// A repo named twice is kept once, rather than refused the way a repo
// written twice among the writes is (parseWrites). The two are not the
// same question: a write target carries a base branch, so two entries
// for one repo can disagree and there is no answer to pick between
// them, while two reads of one repo say the identical thing. And the
// duplicate is easy to arrive at without meaning anything by it --
// RepoPicker offers a comma-separated paste ("owner/a, owner/b,
// owner/a") and keeps only exact spellings out of its own suggestions,
// so "acme/lib" beside "Acme/Lib" gets past it. Both used to reach the
// store, where task_read's (task, owner, name) primary key answered the
// first with a raw "UNIQUE constraint failed" 500 and the second by
// storing one repository twice under two spellings, which the prompt
// then listed twice. Comparing with RepoRef.Same rather than by string
// is what catches the second: it is the comparison the rest of this
// file already uses for "is this the same repository?".
//
// The task's own target is refused, not deduped and not kept, by
// refuseTargetAmongReads -- separately from here for parseWrites' own
// reason: an edit does not always state the target, so this cannot know
// it until it is holding the stored task.
func (c *Client) parseReads(repos []string) ([]model.RepoRef, error) {
	reads := make([]model.RepoRef, 0, len(repos))
	for _, raw := range repos {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		repo, err := c.parseRepo(text)
		if err != nil {
			return nil, err
		}
		seen := false
		for _, already := range reads {
			if already.Same(repo) {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		reads = append(reads, repo)
	}
	return reads, nil
}

// refuseTargetAmongReads rejects a read-only repo that is the task's own
// target: the repo a run is filed against is cloned into its sandbox
// already (orchestrator.prepareCheckout) and is the one place it may
// push, so "may read but never push to" is not a thing that can be true
// of it. The prompt has left it out of that sentence since grain/task-209
// -- it used to tell one run both "push your change to acme/widgets" and
// "you may also read acme/widgets ... but never push to them"
// (orchestrator.readOnlyNames) -- and this is the other half: the field
// that produces the contradiction, refused where somebody states it
// rather than silently emptied of meaning downstream.
//
// Refused rather than deduped, unlike the repeated read parseReads drops.
// A repo named twice among the reads says the identical thing twice and
// there is nothing to tell anybody; a repo named as both the target and
// a read says two different things, and the one the caller meant is not
// ours to guess -- somebody who wants a second repo has one to name, and
// somebody who typed this one wants to know it did nothing.
//
// It covers callers of this package, which is where both halves are
// stated together: a filing (CreateTask) and an edit that states either
// half (UpdateTask, against the target as the edit leaves it -- its own
// call has why it is only those). Reads written straight to the store
// -- a template's or a schedule's, a backport carrying the original's --
// go on being able to carry it, exactly as their Writes can
// (model.WritesBesides' own reasoning: content saved long before anybody
// chose which repo it would be fired at should not fail a firing nobody
// is watching). readOnlyNames is what keeps those harmless.
//
// A proposal (CreateTaskRequest.ProposedBy) is the exception, and has its
// target dropped from its reads instead (readsBesidesTarget): the caller
// there is an agent's propose_task call, which names the repo it is
// filing against and the repos it wants to read in two arguments, and
// listing the first among the second as well is a slip with one obvious
// meaning, not a question -- refusing it lost the proposal outright.
func refuseTargetAmongReads(reads []model.RepoRef, target *model.RepoRef) error {
	if target == nil {
		return nil
	}
	for _, r := range reads {
		if r.Same(*target) {
			return validationErrorf(
				"%s is this task's own repo, which its run already clones and pushes to -- it cannot also be a repo the run may only read",
				r)
		}
	}
	return nil
}

// readsBesidesTarget is reads without target, which a run clones and
// pushes to already.
func readsBesidesTarget(reads []model.RepoRef, target *model.RepoRef) []model.RepoRef {
	if target == nil {
		return reads
	}
	out := reads[:0:0]
	for _, r := range reads {
		if !r.Same(*target) {
			out = append(out, r)
		}
	}
	return out
}

// UpdateTaskRequest is a task's editable fields -- nil means "leave this
// one alone". Repo pointing at an empty string is rejected rather than
// clearing the target: a task with no target cannot be dispatched, and
// the store has a real column for it now rather than an optional
// directive line that could simply be absent.
type UpdateTaskRequest struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	// Repo and Base are the task's target and the branch its runs check
	// out from. Both reach the task's next run and neither reaches one
	// already live: the checkout is made once, when a run starts
	// (orchestrator.prepareCheckout), and the repo that run may push is
	// recorded on the run itself as it is dispatched
	// (model.Store.SetRunSandbox, GitScope) rather than re-read from the
	// task per request. So an edit here is safe to make while a task is
	// running -- which is what makes it worth allowing at all, since a
	// task pointed at a base branch that has since been deleted fails
	// every attempt until somebody corrects it.
	Repo                 *string `json:"repo,omitempty"`
	Base                 *string `json:"base,omitempty"`
	Priority             *int    `json:"priority,omitempty"` // 1 or more; see errPriorityNotPositive
	Issue                *int    `json:"issue,omitempty"`
	AutoMerge            *bool   `json:"autoMerge,omitempty"`
	AutoApproveProposals *bool   `json:"autoApproveProposals,omitempty"`
	// SandboxCPUs, SandboxMemoryMB and SandboxDiskGB
	// (bwsalmon/agents#534, grain/task-41) edit the same per-task
	// override CreateTaskRequest's own fields set. Unlike most
	// pointer fields here, *req.SandboxCPUs == 0 is a meaningful, valid
	// edit (clearing a previously-set override back to "use the
	// deployment default"), not rejected the way Repo's own empty string
	// is -- only the request field itself being nil means "leave alone".
	SandboxCPUs     *int `json:"sandboxCpus,omitempty"`
	SandboxMemoryMB *int `json:"sandboxMemoryMb,omitempty"`
	SandboxDiskGB   *int `json:"sandboxDiskGb,omitempty"`
	// AgentFramework edits the same per-task override
	// CreateTaskRequest.AgentFramework sets, and an empty string is a
	// meaningful edit here for the same reason a 0 is above: it clears
	// the override back to "use the deployment's framework". A task
	// already dispatched keeps whichever framework its live run started
	// with -- Deps.Framework is asked once, when the run begins.
	AgentFramework *string `json:"agentFramework,omitempty"`
	// AgentBackendID edits the same model pin
	// CreateTaskRequest.AgentBackendID sets, with an empty string
	// meaningful for the reason it is on AgentFramework above: it takes
	// the pin off, so the task's next run goes through the rotation
	// again. A task already dispatched keeps the model its live run
	// started on -- Deps.Framework is asked once, when the run begins.
	//
	// Pinning a model here rewrites AgentFramework to that model's own,
	// whatever the task carried before, since the two travel together
	// (CreateTaskRequest.AgentBackendID). Taking the pin off leaves the
	// framework alone: it is a choice the task now makes on its own, and
	// one more edit clears it.
	Model          *string `json:"model,omitempty"`
	AgentBackendID *string `json:"agentBackendId,omitempty"`
	// NetworkAccess edits the same per-task override
	// CreateTaskRequest.NetworkAccess sets, with an empty string
	// meaningful for the reason it is on AgentFramework above: it clears
	// the override back to "use the deployment's own". A task already
	// dispatched keeps the network its live run's sandbox was built
	// with, which is decided once, when that sandbox is created.
	NetworkAccess *string `json:"networkAccess,omitempty"`
	// NothingNeededAction edits CreateTaskRequest.NothingNeededAction's
	// field; "" sets it back to the default, close.
	NothingNeededAction *string `json:"nothingNeededAction,omitempty"`
	// KeepRunning edits CreateTaskRequest.KeepRunning's field.
	KeepRunning      *bool `json:"keepRunning,omitempty"`
	KeepRunningSnake *bool `json:"keep_running,omitempty"`
	// NoPort edits CreateTaskRequest.NoPort's field.
	NoPort *bool `json:"noPort,omitempty"`
	// FixesBaseBranch marks or unmarks this task as fixing its base
	// branch (CreateTaskRequest.FixesBaseBranch). A marked task whose repo or base
	// is edited is re-marked for the new base. grain's own repairs cannot
	// be unmarked.
	FixesBaseBranch *bool `json:"fixesBaseBranch,omitempty"`
	// PromptExtension edits the same per-task override
	// CreateTaskRequest.PromptExtension sets, with an empty string
	// meaningful for the same reason it is on AgentFramework above: it
	// clears the override, so the task's next run is told whatever its
	// deployment and its repo say. Read at dispatch rather than at
	// creation (model.Task.PromptExtension), so an edit here reaches any
	// run this task has not started yet -- but not one already live,
	// whose prompt was built when it began.
	PromptExtension *string `json:"promptExtension,omitempty"`
	// ReviewTemplateID attaches, changes or detaches this task's review
	// (CreateTaskRequest.ReviewTemplateID sets the same field), with an
	// empty string meaningful for the reason it is on the two above: it
	// detaches the review, so the task merges without waiting for one.
	//
	// An edit reaches any review not yet filed, since
	// orchestrator.SyncReviews resolves this the first cycle after the
	// task's own work is done rather than at creation -- so a review can
	// be attached to a task that is already queued, already running, or
	// already finished and waiting to merge. Once the review task has
	// been filed (model.LinkReviewTask), changing this changes nothing:
	// exactly one review is ever filed per task.
	ReviewTemplateID *string `json:"reviewTemplateId,omitempty"`
	// CustomFields, given, replaces this task's values for every
	// *declared* field rather than merging into them -- the shape Reads
	// below has, and for the same reason: the pane that edits them
	// renders every declared field at once, so what it sends is the
	// whole answer and a field it left blank is one somebody cleared. A
	// non-nil empty map clears every declared field.
	//
	// Values are checked against their declarations almost exactly as
	// CreateTaskRequest.CustomFields is, so a name nothing declares is
	// still an error and a value an enum does not offer is still
	// refused. The one difference is what this request may say about a
	// value the task *already* holds: it may restate it unchanged, even
	// where the declaration has moved on since it was written -- an enum
	// option retired, a field that used to hold a set and now holds one
	// value (model.NormalizeCustomFieldValuesFrom has the policy and why
	// it is this one). Without that, a form which renders every field
	// and so restates every field could not edit the title of a task
	// carrying a legacy value at all.
	//
	// What the task already carries for a field that has since been
	// *deleted* from the state repository is left alone whether this
	// request mentions it or not: nothing renders such a field, so an
	// edit that says nothing about it is not an edit that cleared it,
	// and dropping it would mean editing a task's title silently
	// discarded what somebody had recorded about it (model.Task.
	// CustomFields). Naming it with an empty value is still how it is
	// cleared.
	CustomFields *map[string]CustomFieldValue `json:"customFields,omitempty"`
	// Reads, given, replaces the whole set of read-only repos rather than
	// adding to it -- there is no per-entry attach/detach endpoint for
	// Reads the way SetCapability and SetDependency give Grants and
	// Links, since a read-only repo grants nothing and so has no toggle
	// worth exposing on its own. A non-nil empty slice clears the set.
	Reads *[]string `json:"reads,omitempty"`
	// Writes, given, replaces the whole set of extra writeable repos --
	// Reads' own contract above, for the same reason: an entry is a
	// declaration with two halves (repo and branch), so there is nothing
	// to toggle per entry. A non-nil empty slice clears the set, leaving
	// a task that writes only to its own repo.
	//
	// An edit reaches any run this task has not started yet, and reaches
	// a live one only as far as Reads does: an entry here is a repo the
	// git proxy lets a sandbox *fetch* and never push (model.Store.
	// GitScope returns Writes alongside Reads, read per request), so
	// adding one lets a live sandbox clone it and removing one stops it
	// at once -- while the prompt naming these repos was built when the
	// run began, and the run's own push target was fixed then too.
	Writes *[]WriteTarget `json:"writes,omitempty"`
	// GoogleChatSpace links the task to a Google Chat space, or clears
	// the link when set to "".
	GoogleChatSpace *string `json:"googleChatSpace,omitempty"`
	// Reviewers, given, replaces this task's person-set reviewer list
	// (model.Task.Reviewers, docs/design/changelists.md §5). A non-nil empty
	// slice clears it. Refused to agents by agentFilingEscalation /
	// externalAgentFilingEscalation.
	Reviewers *[]string `json:"reviewers,omitempty"`
	// Capabilities, given, replaces the task's capability grants while
	// preserving durable on standing console-session tasks.
	Capabilities *[]string `json:"capabilities,omitempty"`
	// PriorityDispatch attaches or detaches PriorityDispatchCapability on the
	// task (also populated when JSON "priority" is a boolean).
	PriorityDispatch *bool `json:"priorityDispatch,omitempty"`
}

// UnmarshalJSON accepts both camelCase and snake_case spellings for common
// task parameter overrides as well as capabilities/grants lists containing
// strings, {capability, with} objects, or model.Grant objects.
func (r *UpdateTaskRequest) UnmarshalJSON(data []byte) error {
	type Alias UpdateTaskRequest
	var aux struct {
		Alias
		RawCapabilities       json.RawMessage `json:"capabilities,omitempty"`
		RawGrants             json.RawMessage `json:"grants,omitempty"`
		RawPriority           json.RawMessage `json:"priority,omitempty"`
		NetworkAccessSnake    *string         `json:"network_access,omitempty"`
		AutoApproveSnake      *bool           `json:"auto_approve_proposals,omitempty"`
		KeepRunningSnake      *bool           `json:"keep_running,omitempty"`
		PriorityDispatchSnake *bool           `json:"priority_dispatch,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = UpdateTaskRequest(aux.Alias)
	if trimmed := strings.TrimSpace(string(aux.RawPriority)); trimmed != "" && trimmed != "null" {
		var b bool
		if json.Unmarshal(aux.RawPriority, &b) == nil {
			r.PriorityDispatch = &b
		} else {
			var n int
			if err := json.Unmarshal(aux.RawPriority, &n); err != nil {
				return err
			}
			r.Priority = &n
		}
	}
	parseCapRaw := func(raw json.RawMessage) (*[]string, error) {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" || trimmed == "null" {
			return nil, nil
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("capabilities must be a list")
		}
		caps := make([]string, 0, len(items))
		for _, item := range items {
			if entries, err := model.CapabilityEntryNames(item); err == nil {
				caps = append(caps, entries...)
				continue
			} else if spec, gerr := protocol.ParseGrant(item); gerr == nil && len(spec.With) == 0 {
				if c := strings.TrimSpace(spec.Capability); c != "" {
					caps = append(caps, c)
				}
				continue
			} else {
				return nil, err
			}
		}
		return &caps, nil
	}
	if len(aux.RawCapabilities) > 0 {
		caps, err := parseCapRaw(aux.RawCapabilities)
		if err != nil {
			return err
		}
		r.Capabilities = caps
	}
	if r.Capabilities == nil && len(aux.RawGrants) > 0 {
		caps, err := parseCapRaw(aux.RawGrants)
		if err != nil {
			return err
		}
		r.Capabilities = caps
	}
	if r.NetworkAccess == nil && aux.NetworkAccessSnake != nil {
		r.NetworkAccess = aux.NetworkAccessSnake
	}
	if r.AutoApproveProposals == nil && aux.AutoApproveSnake != nil {
		r.AutoApproveProposals = aux.AutoApproveSnake
	}
	if r.KeepRunning == nil && aux.KeepRunningSnake != nil {
		r.KeepRunning = aux.KeepRunningSnake
	}
	if r.PriorityDispatch == nil && aux.PriorityDispatchSnake != nil {
		r.PriorityDispatch = aux.PriorityDispatchSnake
	}
	return nil
}

// UpdateTask edits a task's fields. Unlike the issue-backed version, this
// needs no read-modify-write of a body with directives embedded in it:
// each field is a column, so leaving one alone means not setting it.
func (c *Client) UpdateTask(ctx context.Context, id string, req UpdateTaskRequest) (Task, error) {
	// Validated once, up front. Only the applying half runs inside the
	// store's closure, which may run more than once if another writer
	// wins: validating in there would repeat work whose answer cannot
	// change, and would make the error a caller sees depend on how many
	// times the retry happened to run.
	var target *model.RepoRef
	if req.Repo != nil {
		if strings.TrimSpace(*req.Repo) == "" {
			return Task{}, validationErrorf("repo cannot be empty: a task with no target cannot be dispatched")
		}
		parsed, err := c.parseRepo(*req.Repo)
		if err != nil {
			return Task{}, err
		}
		target = &parsed
	}
	if req.Title != nil && strings.TrimSpace(*req.Title) == "" {
		return Task{}, validationErrorf("title cannot be empty")
	}
	if req.Priority != nil && *req.Priority < 1 {
		return Task{}, validationErrorf(errPriorityNotPositive)
	}
	// A fork head label is resolved to the pull request it heads the way
	// CreateTask resolves one (forkhead.go): the edit stores that pull
	// request's base and links the task to it.
	var forkPR *forkhead.PR
	if req.Base != nil {
		head, isForkHead, err := splitForkHead("base", *req.Base)
		if err != nil {
			return Task{}, err
		}
		if isForkHead {
			repo := target
			if repo == nil {
				existing, err := c.Store.GetTask(ctx, id)
				if err != nil {
					return Task{}, err
				}
				if existing != nil {
					repo = existing.Target
				}
			}
			if forkPR, err = c.resolveForkHead("base", repo, head); err != nil {
				return Task{}, err
			}
			base := forkPR.Base
			req.Base = &base
		} else if err := validateBase("base", *req.Base); err != nil {
			return Task{}, err
		}
	}
	if req.AutoMerge != nil && *req.AutoMerge {
		// Turning auto-merge on is Submit by another name, and a change
		// waiting for named approvers is released only through Submit's
		// check of who is pressing it (approvals.go).
		if existing, err := c.Store.GetTask(ctx, id); err != nil {
			return Task{}, err
		} else if existing != nil && !existing.AutoMerge {
			if pending := model.PendingApprovals(*existing); len(pending) > 0 {
				return Task{}, forbiddenErrorf("%s waits for a named approver's Submit, so auto-merge "+
					"cannot be turned on by editing it:\n%s", id, describePending(pending))
			}
		}
	}
	if req.SandboxCPUs != nil || req.SandboxMemoryMB != nil || req.SandboxDiskGB != nil {
		var cpus, memoryMB, diskGB int
		if req.SandboxCPUs != nil {
			cpus = *req.SandboxCPUs
		}
		if req.SandboxMemoryMB != nil {
			memoryMB = *req.SandboxMemoryMB
		}
		if req.SandboxDiskGB != nil {
			diskGB = *req.SandboxDiskGB
		}
		if err := validateSandboxShape(cpus, memoryMB, diskGB); err != nil {
			return Task{}, err
		}
		// Asked of the three numbers this request *states* -- an unstated
		// field is a 0 here whatever the task already carries -- so that a
		// task filed with a shape before the deployment moved onto a
		// backend that has none can still be edited back to no override,
		// one field at a time, rather than being frozen by its own stored
		// numbers. Nothing this accepts leaves a non-zero shape the store
		// did not already hold.
		if err := c.refuseIgnoredSandboxShape(cpus, memoryMB, diskGB); err != nil {
			return Task{}, err
		}
	}
	if req.AgentFramework != nil {
		if err := validateAgentFramework(*req.AgentFramework); err != nil {
			return Task{}, err
		}
	}
	// A pin stated here decides the framework, so it is checked against
	// the framework this same request states and not against whatever the
	// task happens to carry: moving a task onto a model of another kind
	// is one edit, not two that have to be sent in the right order.
	//
	// The other order -- a framework stated on its own, over a task that
	// is pinned to a model of a different kind -- is read as the pin
	// having been overtaken: "run this on Claude" cannot hold together
	// with "run this on that Codex account", one of the two has to go,
	// and the one just stated is the one somebody means. Refusing the
	// edit instead would make a bulk "move these onto Claude" fail on
	// whichever task in the selection happened to be pinned. This map is
	// what the closure below decides that against, read once here rather
	// than per attempt inside it.
	// The model and the connection are separate fields (model.Task.Model,
	// model.Task.AgentBackendID); splitModelChoice reads a legacy model
	// sent as agentBackendId as the model choice it was.
	var editModel *string
	var isConsoleSessionTask bool
	if req.Model != nil || req.AgentBackendID != nil || req.Capabilities != nil || req.PriorityDispatch != nil || req.KeepRunning != nil {
		if existing, err := c.Store.GetTask(ctx, id); err == nil && existing != nil {
			isConsoleSessionTask = existing.IsConsoleSession()
		}
	}
	if req.Model != nil || req.AgentBackendID != nil {
		if isConsoleSessionTask && req.AgentBackendID == nil && req.Model != nil {
			chosen, err := c.declaredModelChoice(ctx, *req.Model)
			if err != nil {
				return Task{}, err
			}
			editModel = &chosen
		} else {
			var m, pin string
			if req.Model != nil {
				m = *req.Model
			}
			if req.AgentBackendID != nil {
				pin = *req.AgentBackendID
			}
			chosen, pinned, err := c.splitModelChoice(ctx, m, pin)
			if err != nil {
				return Task{}, err
			}
			if req.Model != nil || chosen != "" {
				editModel = &chosen
			}
			if req.AgentBackendID != nil {
				req.AgentBackendID = &pinned
			}
		}
	}
	var backendFrameworks map[string]string
	if req.AgentFramework != nil && req.AgentBackendID == nil {
		cfg, err := c.Store.GetConfig(ctx)
		if err != nil {
			return Task{}, err
		}
		backendFrameworks = map[string]string{}
		if cfg != nil {
			for _, backend := range cfg.AgentBackends {
				fw := model.NormalizeAgentFrameworkName(backend.Framework)
				backendFrameworks[backend.ID] = fw
				for _, m := range backend.SupportedModels() {
					backendFrameworks[m] = fw
				}
			}
		}
	}
	var pinnedFramework string
	var statedPin, statedModel string
	if req.AgentBackendID != nil {
		statedPin = strings.TrimSpace(*req.AgentBackendID)
	}
	if editModel != nil && !isConsoleSessionTask {
		statedModel = *editModel
	}
	if statedPin != "" || statedModel != "" {
		stated := ""
		if req.AgentFramework != nil {
			stated = *req.AgentFramework
		}
		var err error
		if pinnedFramework, err = c.agentBackendFramework(ctx, firstNonEmpty(statedPin, statedModel), stated); err != nil {
			return Task{}, err
		}
	}
	if req.NetworkAccess != nil {
		if err := validateNetworkAccess(*req.NetworkAccess); err != nil {
			return Task{}, err
		}
		// Asked of what this request states, like the sandbox shape
		// above: clearing an override back to empty stays possible on a
		// deployment that has since moved onto a backend which cannot
		// honour it, and nothing this accepts leaves a "none" the store
		// did not already hold.
		if err := c.refuseIgnoredNetworkAccess(*req.NetworkAccess); err != nil {
			return Task{}, err
		}
	}
	var nothingNeeded model.NothingNeededAction
	if req.NothingNeededAction != nil {
		var err error
		if nothingNeeded, err = validateNothingNeededAction(*req.NothingNeededAction); err != nil {
			return Task{}, err
		}
	}
	var reviewTemplateID string
	if req.ReviewTemplateID != nil {
		var err error
		reviewTemplateID, err = c.validReviewTemplate(ctx, *req.ReviewTemplateID)
		if err != nil {
			return Task{}, err
		}
	}
	var reads []model.RepoRef
	if req.Reads != nil {
		var err error
		reads, err = c.parseReads(*req.Reads)
		if err != nil {
			return Task{}, err
		}
	}
	var writes []model.WriteTarget
	if req.Writes != nil {
		var err error
		writes, err = c.parseWrites(ctx, model.DefaultTeamID, *req.Writes)
		if err != nil {
			return Task{}, err
		}
	}
	// A base this edit changes is held to CreateTask's rule: refused
	// when GitHub says it is not there and nothing the task waits on
	// could be what cuts it. A base the task already carries is not
	// asked about again, so an edit to anything else is never refused
	// over a branch that has since been deleted.
	if req.Base != nil || req.Writes != nil {
		existing, err := c.Store.GetTask(ctx, id)
		if err != nil {
			return Task{}, err
		}
		if existing != nil && !blockedByLinks(existing.Links) {
			repo := existing.Target
			if target != nil {
				repo = target
			}
			var checkTarget *model.RepoRef
			base := existing.Base
			if req.Base != nil && *req.Base != existing.Base {
				checkTarget, base = repo, *req.Base
			}
			var newWrites []model.WriteTarget
			for _, w := range writes {
				if !hasWriteTarget(existing.Writes, w) {
					newWrites = append(newWrites, w)
				}
			}
			if err := c.refuseMissingBases(ctx, checkTarget, base, newWrites); err != nil {
				return Task{}, err
			}
		}
	}
	// The declarations are read here, once; the values are checked
	// against them *inside* the closure below, which is the one piece of
	// this request that cannot be decided before the task has been read.
	// What an edit may say about a field depends on what the task
	// already holds for it -- a value stored under a declaration that
	// has changed since may be restated, cleared or replaced but not
	// introduced (model.NormalizeCustomFieldValuesFrom) -- so a retry
	// against a fresh read re-decides it against the values that read
	// found, which is the only answer that is true of the task the write
	// actually lands on.

	// titleChanged/descriptionChanged are recomputed on every call apply
	// makes, including a retry against a fresh read after another writer
	// won the race -- so whichever attempt actually lands is the one
	// whose diff the addendum below describes, never a stale one from an
	// attempt that got overwritten before it ever took effect.
	var titleChanged, descriptionChanged bool
	// A running task's repo and base are the ones its run was dispatched
	// against, and they stay as they are until that run ends
	// (grain/task-2310): the checkout and the push target were fixed when
	// it started, so an edit here could reach nothing but a later run,
	// while reading as though it steered this one. Only an edit that
	// moves either is refused -- a request restating what the task
	// already carries, as a whole-form save does, says nothing new.
	if target != nil || req.Base != nil {
		if err := c.refuseRetargetingARun(ctx, id, target, req.Base); err != nil {
			return Task{}, err
		}
	}
	// Read outside the closure, which may run more than once: the
	// catalogue a retarget is judged against (settings/capabilities.yml).
	catalogue, err := c.capabilityCatalogue(ctx)
	if err != nil {
		return Task{}, err
	}
	var normalizedCaps []string
	if req.Capabilities != nil {
		seen := map[string]bool{}
		normalizedCaps = make([]string, 0, len(*req.Capabilities))
		for _, rawCap := range *req.Capabilities {
			capIDs, err := c.normalizeCapabilityIDs(rawCap)
			if err != nil {
				return Task{}, err
			}
			for _, capID := range capIDs {
				if _, ok := c.capabilityByID(capID); !ok {
					return Task{}, validationErrorf("capabilities: %s", capabilityRefusal(capID))
				}
				if seen[capID] {
					continue
				}
				seen[capID] = true
				normalizedCaps = append(normalizedCaps, capID)
			}
		}
	}
	submitted, submittedAt, submittedHead := false, c.now(), ""
	if req.AutoMerge != nil && *req.AutoMerge {
		// Read outside the closure: it may ask GitHub, and the
		// closure may run more than once.
		if current, err := c.Store.GetTask(ctx, id); err == nil && current != nil && !current.AutoMerge {
			submittedHead = c.submittedHead(ctx, *current)
		}
	}
	if err := c.mutate(ctx, id, func(task *model.Task) error {
		submitted = false
		// Whether this edit moves the task off a repository it had a
		// target in, which takes the pull requests it opened there off
		// it (below).
		retargeted := false
		if req.Title != nil {
			titleChanged = *req.Title != task.Title
			task.Title = *req.Title
		}
		if req.Description != nil {
			descriptionChanged = *req.Description != task.Body
			task.Body = *req.Description
		}
		// A repo off Config.TargetRepos is refused here rather than
		// parked as a filing against one is (refuseOffAllowlist), and
		// only when this edit actually moves the task: a request that
		// restates the repo the task already carries says nothing new
		// about it, and refusing that would stop the task pane -- which
		// saves the whole form, repo field included -- from ever
		// editing the title or description of a task already parked
		// off the list, which is precisely the task somebody is most
		// likely to be editing.
		//
		// Refusing here at all is what closes the way round the park:
		// a task filed against an allowed repo is queued and
		// dispatchable, and moving it onto an off-list repo by editing
		// it left it queued and dispatchable, with the git proxy
		// authorizing its sandbox against exactly the target this line
		// writes (grain/task-336).
		if target != nil {
			if task.Target == nil || !task.Target.Same(*target) {
				allowed := c.teamTargetRepos(ctx, model.DefaultTeamID)
				if !targetAllowed(allowed, *target) {
					return validationErrorf("%s", model.OffAllowlistRefusal(*target, allowed, "this task cannot be moved onto it"))
				}
				if catalogue != nil {
					gc := model.TaskGrantContext(*task)
					gc.Repo = target
					if _, err := catalogue.ValidateGrants(gc); err != nil {
						return validationErrorf("this task cannot be moved onto %s: %v", target, err)
					}
				}
			}
			retargeted = task.Target != nil && !task.Target.Same(*target)
			task.Target = target
		}
		if req.Base != nil {
			task.Base = *req.Base
		}
		if req.Priority != nil {
			if *req.Priority == 0 {
				task.Priority = model.DefaultTaskPriority
			} else {
				task.Priority = *req.Priority
			}
		}
		if forkPR != nil && !hasLinkTo(task.Links, forkPR.Link()) {
			task.Links = append(task.Links, forkPR.Link())
			task.Body += forkPR.Note()
		}
		if req.Issue != nil {
			task.Issue = *req.Issue
		}
		if req.AutoMerge != nil {
			// A change target's change is a CL, with no pull request
			// to merge (model.ValidateTaskChangeTarget): a task turned
			// to auto-merge is one no dispatch can set up.
			if *req.AutoMerge && !task.AutoMerge && task.ChangeTarget != nil {
				return validationErrorf("a %s task has no git pull request: auto_merge cannot be set", task.ChangeTarget.Kind)
			}
			// Turning it on is Submit by another name (see above), and
			// says what Submit does about the holds it was waiting under.
			if *req.AutoMerge && !task.AutoMerge {
				humanSubmitted(task, submittedAt, submittedHead)
				submitted = true
			}
			task.AutoMerge = *req.AutoMerge
		}
		if req.AutoApproveProposals != nil {
			task.AutoApproveProposals = *req.AutoApproveProposals
			if _, ok := model.ConsoleSessionIDOf(*task); ok {
				task.Tags = slices.DeleteFunc(task.Tags, func(tag string) bool {
					return tag == model.ConsoleSessionTasksApprovedTag || tag == model.ConsoleSessionTasksUnapprovedTag
				})
				if *req.AutoApproveProposals {
					task.Tags = append(task.Tags, model.ConsoleSessionTasksApprovedTag)
				} else {
					task.Tags = append(task.Tags, model.ConsoleSessionTasksUnapprovedTag)
				}
			}
		}
		if req.SandboxCPUs != nil {
			task.SandboxCPUs = *req.SandboxCPUs
		}
		if req.SandboxMemoryMB != nil {
			task.SandboxMemoryMB = *req.SandboxMemoryMB
		}
		if req.SandboxDiskGB != nil {
			task.SandboxDiskGB = *req.SandboxDiskGB
		}
		if req.AgentFramework != nil {
			task.AgentFramework = *req.AgentFramework
			// Only a framework that contradicts the pin takes it off --
			// clearing the framework back to the deployment's own
			// (UpdateTaskRequest.AgentFramework's empty string) says
			// nothing about which model, and a task pinned to a model of
			// that same framework is already saying it.
			if want := model.NormalizeAgentFrameworkName(*req.AgentFramework); want != "" &&
				task.AgentBackendID != "" && backendFrameworks[task.AgentBackendID] != want {
				task.AgentBackendID = ""
			}
		}
		if req.AgentBackendID != nil {
			task.AgentBackendID = strings.TrimSpace(*req.AgentBackendID)
			if task.AgentBackendID != "" {
				task.AgentFramework = pinnedFramework
			}
		}
		if editModel != nil {
			// A model somebody edits in is the task's own choice now,
			// whichever declaration chose the one it replaces; restating
			// the same model takes nothing over.
			if *editModel != task.Model {
				task.ModelSource = ""
			}
			task.Model = *editModel
			if task.Model != "" && task.AgentBackendID == "" && pinnedFramework != "" {
				task.AgentFramework = pinnedFramework
			}
		}
		if req.NetworkAccess != nil {
			task.NetworkAccess = model.NetworkAccess(*req.NetworkAccess)
		}
		if req.NothingNeededAction != nil {
			task.NothingNeededAction = nothingNeeded
		}
		if req.KeepRunning != nil {
			task.KeepRunning = *req.KeepRunning
		} else if req.KeepRunningSnake != nil {
			task.KeepRunning = *req.KeepRunningSnake
		}
		if req.NoPort != nil {
			task.NoPort = *req.NoPort
		}
		if req.PromptExtension != nil {
			task.PromptExtension = strings.TrimSpace(*req.PromptExtension)
		}
		if req.ReviewTemplateID != nil {
			task.ReviewTemplateID = reviewTemplateID
		}
		if req.Reads != nil {
			if task.IsConsoleSession() {
				stateRepo := c.stateRepoRef()
				var taskReads []model.RepoRef
				if stateRepo.Owner != "" {
					taskReads = append(taskReads, stateRepo)
				}
				for _, r := range reads {
					if r.Owner != "" && r.Name != "" && !r.Same(stateRepo) {
						taskReads = append(taskReads, r)
					}
				}
				task.Reads = taskReads
			} else {
				task.Reads = reads
			}
		}
		if req.Writes != nil {
			task.Writes = writes
		}
		if req.Reviewers != nil {
			task.Reviewers = model.NormalizeReviewers(*req.Reviewers)
		}
		if req.Capabilities != nil || req.PriorityDispatch != nil {
			existingByCap := make(map[string]model.Grant, len(task.Grants))
			for _, g := range task.Grants {
				existingByCap[g.Capability] = g
			}
			if req.Capabilities != nil {
				policy, configured, err := c.capabilityPolicyFor(ctx)
				if err != nil {
					return err
				}
				for _, capID := range normalizedCaps {
					if _, had := existingByCap[capID]; !had {
						if model.IsCapabilityRef(capID) {
							subject := model.CapabilitySubject{Repo: task.Target, Kind: model.CapabilityRunTask}
							if err := policy.Permits(capID, subject, configured); err != nil {
								return validationErrorf("%v", err)
							}
						}
						if err := c.catalogueEditRefusal(ctx, *task, capID); err != nil {
							return err
						}
					}
				}
				newGrants := make([]model.Grant, 0, len(normalizedCaps)+1)
				seen := map[string]bool{}
				if task.IsConsoleSession() {
					newGrants = append(newGrants, model.Grant{Capability: model.DurableCapability, Via: model.GrantByGrain})
					seen[model.DurableCapability] = true
				}
				for _, capID := range normalizedCaps {
					if capID == "" || seen[capID] {
						continue
					}
					seen[capID] = true
					if prevGrant, had := existingByCap[capID]; had {
						newGrants = append(newGrants, prevGrant)
					} else {
						newGrants = append(newGrants, model.Grant{Capability: capID, Via: model.GrantByLabel})
					}
				}
				task.Grants = newGrants
			}
			if req.PriorityDispatch != nil {
				if *req.PriorityDispatch {
					if _, had := existingByCap[model.PriorityDispatchCapability]; !had {
						if err := c.catalogueEditRefusal(ctx, *task, model.PriorityDispatchCapability); err != nil {
							return err
						}
					}
					if !slices.ContainsFunc(task.Grants, func(g model.Grant) bool { return g.Capability == model.PriorityDispatchCapability }) {
						task.Grants = append(task.Grants, model.Grant{Capability: model.PriorityDispatchCapability, Via: model.GrantByLabel})
					}
				} else {
					task.Grants = slices.DeleteFunc(task.Grants, func(g model.Grant) bool { return g.Capability == model.PriorityDispatchCapability })
				}
			}
		}
		if retargeted {
			// A pull request a run opened in the repository the task is
			// moving off is not its change any more: left linked, the
			// merge queue would go on reconciling it against this task
			// beside whatever the next run opens, and closing it on
			// GitHub would close the task. What was pinned to that pull
			// request's head -- the classification of it, the hold it
			// earned, a person's Submit of it -- goes with it; the next
			// pull request is classified afresh.
			//
			// An external pull request in that repository -- the one a
			// fork head label resolved to (forkhead.go) -- goes too, and
			// so does the guidance appended about it: the next run
			// cannot fetch its head or follow it up from here.
			fixesDropped := false
			for _, l := range task.Links {
				if !model.LinkOffTarget(*task, l) {
					continue
				}
				if l.Kind == model.LinkFixes {
					fixesDropped = true
					continue
				}
				ref, _ := model.ParsePullRequestRef(l.Target)
				if body, stripped := forkhead.StripNote(task.Body, ref); stripped {
					task.Body = body
				} else if strings.Contains(task.Body, ref.String()) {
					task.Body += forkhead.MovedNote(ref, *task.Target)
				}
			}
			if links, dropped := model.WithoutOffTargetPullRequests(*task); dropped {
				task.Links = links
				if fixesDropped {
					task.Links = slices.DeleteFunc(links, func(l model.Link) bool {
						return l.Kind == model.LinkSecurityHold || l.Kind == model.LinkTrustBoundaryCleared ||
							l.Kind == model.LinkHumanSubmitted
					})
				}
			}
		}
		if req.GoogleChatSpace != nil {
			var newLinks []model.Link
			for _, l := range task.Links {
				if l.Kind != model.LinkGoogleChat {
					newLinks = append(newLinks, l)
				}
			}
			if space := strings.TrimSpace(*req.GoogleChatSpace); space != "" {
				newLinks = append(newLinks, model.Link{Kind: model.LinkGoogleChat, Target: space})
			}
			task.Links = newLinks
		}
		if req.CustomFields != nil {
			declaredFields, err := c.Store.ListCustomFields(ctx)
			if err != nil {
				return err
			}
			customFields, err := model.NormalizeCustomFieldValuesFrom(declaredFields,
				customFieldModelValues(*req.CustomFields), task.CustomFields)
			if err != nil {
				return validationErrorf("%s", err)
			}
			task.CustomFields = mergeCustomFields(task.CustomFields, *req.CustomFields, customFields, declaredFields)
		}
		// Against the target as this edit leaves it, not as it arrived:
		// a request that moves the task to one of its own extra write
		// targets, or adds the repo it already writes to, is the same
		// contradiction whichever half of it was stated (see
		// refuseTargetAmongWrites). Its read-only repos are checked the
		// same way and for the same reason (refuseTargetAmongReads) --
		// but only when this request states one half or the other of it.
		//
		// That gate is the one difference between the two, and it is
		// there because the two states are reachable differently. Nothing
		// but this package can leave a task carrying its own target among
		// its Writes: every path that files one from saved content drops
		// it first (model.WritesBesides). Reads have no such sieve -- a
		// template's, a schedule's, and a backport carrying the
		// original's are written to the store as they stand -- so
		// checking unconditionally would let a stale entry nobody typed
		// freeze the rest of the task, and editing its title would fail
		// over a repo list the request never mentioned. Asking only of
		// what the request states is refuseIgnoredSandboxShape's own
		// reasoning above, and it still catches both ways somebody can
		// state this one: adding the read, and moving the task onto a
		// repo it already reads.
		// After the repo and base edits above, since the mark names the
		// base it fixes (model.SetFixesBaseBranch).
		//
		// Not grain's own repair, though: it was filed for the branch and
		// commit its tags name, which are what main's CI row, its waiters
		// and closing it on green all find it by. Re-tagging it for an
		// edited base made it grain's repair of a branch it was never
		// filed for, and left the branch it was filed for with a repair
		// that green never closed and waiters it never released.
		grains := model.GrainBaseRepair(*task)
		if marked := model.FixesBaseBranch(*task); req.FixesBaseBranch != nil ||
			(marked && !grains && (req.Repo != nil || req.Base != nil || req.Writes != nil)) {
			on := marked
			if req.FixesBaseBranch != nil {
				on = *req.FixesBaseBranch
			}
			if !on && grains {
				return validationErrorf("%s is grain's own repair of its base branch, so it cannot be unmarked: close it instead", id)
			}
			if grains {
				// Already marked, as the repair of what it was filed for.
			} else if !model.SetFixesBaseBranch(task, on) {
				return validationErrorf("%s has no repo, so there is no base branch for it to fix", id)
			}
		}
		if err := refuseTargetAmongWrites(task.Writes, task.Target, task.Base); err != nil {
			return err
		}
		if req.Reads != nil || req.Repo != nil {
			if err := refuseTargetAmongReads(task.Reads, task.Target); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return Task{}, err
	}
	if titleChanged || descriptionChanged {
		if err := c.noteEdit(ctx, id, titleChanged, req.Title, descriptionChanged, req.Description); err != nil {
			return Task{}, err
		}
	}
	if submitted {
		if err := c.unblockSubmitted(ctx, id, submittedAt); err != nil {
			return Task{}, err
		}
	}
	return c.Task(ctx, id)
}

// refuseRetargetingARun is UpdateTask's refusal of a repo or base edit
// that would move a running task (see its call).
func (c *Client) refuseRetargetingARun(ctx context.Context, id string, target *model.RepoRef, base *string) error {
	state, err := c.Store.State(ctx, id)
	if err != nil || state != model.StateRunning {
		return err
	}
	existing, err := c.Store.GetTask(ctx, id)
	if err != nil || existing == nil {
		return err
	}
	if target != nil && (existing.Target == nil || !existing.Target.Same(*target)) {
		return validationErrorf("%s is running, so its repo cannot be changed until its run ends", id)
	}
	if base != nil && *base != existing.Base {
		return validationErrorf("%s is running, so its base branch cannot be changed until its run ends", id)
	}
	return nil
}

// normalizeCustomFields checks a filing's custom field values against
// what this deployment declares, and returns what to store: canonical
// values for the fields named, nothing for a request that named none.
//
// The declarations are read from the store on every call rather than
// cached on Client.Config, the same way defaultCapabilities reads its
// two layers: a field an agent got merged is offered by the very next
// filing rather than by the next process.
func (c *Client) normalizeCustomFields(ctx context.Context, values map[string]CustomFieldValue) (map[string][]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	fields, err := c.Store.ListCustomFields(ctx)
	if err != nil {
		return nil, err
	}
	out, err := model.NormalizeCustomFieldValues(fields, customFieldModelValues(values))
	if err != nil {
		return nil, validationErrorf("%s", err)
	}
	return out, nil
}

// mergeCustomFields is what an edit leaves a task carrying: the values
// the request states, plus whatever the task already held for a field
// nothing declares any more.
//
// The second half is the whole of this function. An edit sends every
// field the pane could render, so for declared fields "not sent" means
// "cleared" -- but a value whose declaration has been deleted from the
// state repository can be rendered by nothing and re-stated by nobody,
// and dropping it would mean that editing a task's title threw away
// what somebody had recorded about it. So it stays until an edit names
// it, which the pane's own row for a stale value is what does.
func mergeCustomFields(had map[string][]string, stated map[string]CustomFieldValue,
	normalized map[string][]string, declared []model.CustomField) map[string][]string {
	out := map[string][]string{}
	isDeclared := make(map[string]bool, len(declared))
	for _, f := range declared {
		isDeclared[f.Name] = true
	}
	for name, value := range had {
		if _, named := stated[name]; named {
			continue
		}
		if !isDeclared[name] {
			out[name] = value
		}
	}
	for name, value := range normalized {
		out[name] = value
	}
	// nil rather than an empty map for a task left carrying nothing, so
	// that "cleared the last one" and "never had any" are the same task
	// (model.Task.CustomFields).
	if len(out) == 0 {
		return nil
	}
	return out
}

// noteEdit records an editorial change to title or description as an
// ordinary Comment, attributed the same way AddComment attributes a
// human's own reply -- there is no separate "edit log" here, because a
// second channel for the same fact would also need its own way to reach
// a run in flight, and Comment already has one: orchestrator.
// addendaPoller and commentThreadSection both read every task's
// conversation without caring whether an entry started life as a reply
// or as this. An edit to any other field (repo, base, auto-merge, reads)
// gets no comment: those are dispatch mechanics a running agent has no
// use for mid-run, unlike the title and body BuildPrompt actually hands
// it.
func (c *Client) noteEdit(ctx context.Context, id string, titleChanged bool, title *string, descriptionChanged bool, description *string) error {
	var parts []string
	if titleChanged {
		parts = append(parts, fmt.Sprintf("the title changed to %q", *title))
	}
	body := "This task was just edited -- " + strings.Join(parts, "; and ")
	// The new description is quoted last and verbatim, with nothing
	// after it: a full stop here would land on its last line, turning
	// "done." into "done.." and a closing ``` fence into "```." -- which
	// no longer closes the block -- in the text a live run is handed.
	if descriptionChanged {
		if titleChanged {
			body += "; and "
		}
		body += fmt.Sprintf("the description changed to:\n\n%s", *description)
	} else {
		body += "."
	}
	_, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    id,
		Author:    model.Attribution{Actor: c.requestActor(ctx).Actor},
		Body:      body,
		CreatedAt: c.now(),
	})
	return err
}

// mutate wraps Store.UpdateTask so a missing task reports as this
// package's own NotFoundError -- which the server turns into a 404 --
// rather than as the store's own "no such task" string.
func (c *Client) mutate(ctx context.Context, id string, apply func(*model.Task) error) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	c.ensureConsoleSessionSnapshot(ctx, *task)
	return c.Store.UpdateTask(ctx, id, apply)
}

// SetCapability attaches or detaches one capability grant. Detaching one
// that is not attached is a no-op rather than an error, matching what
// removing an absent label used to do.
//
// Only attaching checks the id against this deployment's listing.
// Detaching an id with no row is allowed, because a task can be holding
// a grant nothing offers any more -- a renamed capability
// (OfferedCapabilities' own "scratch-repo", now github-sandbox), or one
// a deployment stopped listing -- and such a grant is exactly the one an
// operator most needs to remove: it fails the task's every dispatch at
// model.ResolveGrants. Refusing to detach it would leave the only route
// out through the store. Detaching can only ever shrink the grant set,
// so nothing the validation protects is reachable this way.
//
// DetailOverlay.jsx's CapabilityToggles is the other half of that route:
// it gives such a grant a picker row of its own purely so it can be
// unticked, since only rows can be toggled off.
func (c *Client) SetCapability(ctx context.Context, id, capabilityID string, attach bool) error {
	// An empty id names no capability: the body was empty or spelled its
	// fields wrong ({"capability":...}), and answering that with a 200
	// that changed nothing hides the client's bug (grain/task-1613).
	capabilityID = strings.TrimSpace(capabilityID)
	if capabilityID == "" {
		return validationErrorf("capability id is required")
	}
	var targetIDs []string
	if attach {
		normalized, err := c.normalizeCapabilityIDs(capabilityID)
		if err != nil {
			return grantValidationError(err)
		}
		targetIDs = normalized
		for _, nid := range targetIDs {
			if _, ok := c.capabilityByID(nid); !ok {
				return validationErrorf("%s", capabilityRefusal(nid))
			}
			if err := c.catalogueEditRefusalFor(ctx, id, nid); err != nil {
				return err
			}
		}
	} else {
		targetIDs = model.CanonicalLegacyGrants(capabilityID)
		if !slices.Contains(targetIDs, capabilityID) {
			targetIDs = append(targetIDs, capabilityID)
		}
	}
	// Rebuilding the grant set inside the closure is what lets two people
	// attach two different capabilities without one losing: a retry runs
	// again against the set the winner already wrote.
	return c.mutate(ctx, id, func(task *model.Task) error {
		kept := make([]model.Grant, 0, len(task.Grants)+len(targetIDs))
		for _, g := range task.Grants {
			if !attach && g.Capability == model.DurableCapability && task.IsConsoleSession() {
				kept = append(kept, g)
				continue
			}
			if !slices.Contains(targetIDs, g.Capability) {
				kept = append(kept, g)
			}
		}
		if attach {
			for _, nid := range targetIDs {
				kept = append(kept, model.Grant{Capability: nid, Via: model.GrantByLabel})
			}
		}
		task.Grants = kept
		return nil
	})
}

// SetDependency attaches or detaches one depends-on link -- the picker's
// attach/detach counterpart to SetCapability, so a dependency named after
// creation goes through the same mutate-and-retry path every other edit
// here does. Detaching one not attached is a no-op, matching
// SetCapability; attaching one already attached is too, since the link
// table's own primary key (task_id, kind, target) already forbids a
// duplicate.
func (c *Client) SetDependency(ctx context.Context, id, dependsOnID string, attach bool) error {
	id = model.NormalizeTaskID(id)
	normDependsOnID := model.NormalizeTaskID(dependsOnID)
	if normDependsOnID == "" {
		return validationErrorf("dependsOn id is required")
	}
	if !attach {
		return c.mutate(ctx, id, func(task *model.Task) error {
			kept := make([]model.Link, 0, len(task.Links))
			for _, l := range task.Links {
				if !(l.Kind == model.LinkDependsOn && (l.Target == normDependsOnID || l.Target == dependsOnID)) {
					kept = append(kept, l)
				}
			}
			task.Links = kept
			return nil
		})
	}
	links, err := c.dependsOnLinks(ctx, []string{normDependsOnID}, id)
	if err != nil {
		return err
	}
	return c.mutate(ctx, id, func(task *model.Task) error {
		for _, l := range task.Links {
			if l.Kind == model.LinkDependsOn &&
				(l.Target == normDependsOnID || l.Target == strings.TrimSpace(dependsOnID)) {
				return nil
			}
		}
		task.Links = append(task.Links, links...)
		return nil
	})
}

// ReorderRequest is a drag-and-drop move (bwsalmon/agents#476): ids, in
// the order they should keep relative to each other, dropped between
// whatever AfterID and BeforeID currently name. Either may be empty --
// AfterID empty means ids become the new minimum, dropped at the head of
// a list ("just before the following job"), BeforeID empty means they
// become the new maximum. Both resolve against the full backlog, not
// whatever view the drag happened in: TaskList.jsx computes them from the
// nearest neighbours still visible under its current filter, which is
// what lets a filtered drag land the dragged task(s) correctly in the
// unfiltered order even though it never saw most of that order.
type ReorderRequest struct {
	IDs      []string `json:"ids"`
	AfterID  string   `json:"afterId"`
	BeforeID string   `json:"beforeId"`
}

// Reorder applies a ReorderRequest. A blank AfterID/BeforeID means "no
// bound on this side" (Store.Reorder's own nil), not "the task with the
// empty string id" -- task ids are never empty (model.BranchName's own
// doc comment: NewTaskID allocates them, decimal and always non-empty).
func (c *Client) Reorder(ctx context.Context, req ReorderRequest) error {
	if len(req.IDs) == 0 {
		return validationErrorf("ids is required")
	}
	// A drop naming a task nothing exists behind is a stale list in
	// somebody's browser, not a broken server -- so it reports as this
	// package's own NotFoundError, which the server answers 404, rather
	// than falling out of the store as "reordering: no such task 999"
	// and being reported as a 500 (found by hand, task 244). The same
	// translation Client.mutate does for every single-task edit.
	for _, id := range append(append([]string{}, req.IDs...), req.AfterID, req.BeforeID) {
		if id == "" {
			continue
		}
		task, err := c.Store.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if task == nil {
			return &NotFoundError{ID: id}
		}
	}
	var afterID, beforeID *string
	if req.AfterID != "" {
		afterID = &req.AfterID
	}
	if req.BeforeID != "" {
		beforeID = &req.BeforeID
	}
	return c.Store.Reorder(ctx, req.IDs, afterID, beforeID)
}

// Approve records approval on a proposed or deferred task, which is what
// makes it dispatchable -- model.StateOf reads an undeferred task with no
// Approval as 'proposed' and one with it as 'queued', and a deferred task
// keeps its deferral (DeferredAt and DeferredUntil) untouched while waking
// onto the queue instead of back into the proposals. Approving an
// already-approved task is a no-op.
func (c *Client) Approve(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	if task.Approval != nil {
		return nil
	}
	if err := c.catalogueApprovalRefusal(ctx, *task); err != nil {
		return err
	}
	if err := c.Store.Approve(ctx, id, model.Attribution{Actor: c.requestActor(ctx).Actor}, c.now()); err != nil {
		return err
	}
	if task.Interactive && task.DeferredAt == nil && c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// WithdrawApproval takes an approved task back out of the queue: it
// clears the approval Approve recorded, which is all it takes for
// model.StateOf to read the task as 'proposed' again and for task_ready
// to stop offering it to dispatch. It is the way to stop a task that has
// been queued -- reconsidered, filed too early, or simply not wanted
// this week -- without closing it, and re-approving it later is the
// ordinary Approve call, nothing special.
//
// "Withdraw approval" rather than "unapprove" because that is what
// docs/data-model.md already calls it, in the same paragraph that gave
// this operation its reason to exist: approval is a declaration, so
// "cancellation gets a mechanism it did not have ... withdrawing
// approval is [available], and it is [reviewable] like any other
// declaration change". Nothing here writes a state -- the state moves
// because the declaration behind it went away.
//
// Two boundaries, both about not rewriting history:
//
//   - A task with no approval is untouched, mirroring Approve's own
//     no-op on an already-approved task.
//   - A task that is running, or past its run (completed, awaiting
//     submit, awaiting ack, or closed) is refused. Its
//     approval has already been spent on work that happened, so clearing
//     it would erase the record (Task.ApprovedAt, which metrics reads as
//     "queued since") of a queue wait that was real, and it would stop
//     nothing: the way to stop a run in flight is Close, which cancels
//     it. The check races a dispatch that starts a run just after it --
//     harmlessly, since the withdrawal only means that run is the last
//     one, which is what withdrawing asks for anyway.
func (c *Client) WithdrawApproval(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	if task.Approval == nil {
		return nil
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return err
	}
	switch state {
	case model.StateRunning:
		return validationErrorf("task %s is running: close it to cancel the run", id)
	case model.StateCompleted, model.StateAwaitingSubmit, model.StateAwaitingReply, model.StateClosed:
		return validationErrorf("task %s is %s: its approval is a record of work that already happened", id, state)
	}
	return c.Store.WithdrawApproval(ctx, id)
}

// Defer puts a task aside: it stamps model.Task.DeferredAt, which is the
// whole of model.StateDeferred (that constant's own doc comment for what
// the state means, and Task.DeferredAt's for why the field is a
// declaration). A deferred task drops out of task_ready, out of every
// task list that has not asked to see it, and off the board -- it is the
// answer to "not this month, but don't lose it" that closing a task,
// which means "this is over", could never be.
//
// Approval is untouched in both directions, which is what makes Undefer
// the exact inverse of this and nothing more: a task put aside while it
// was queued comes back queued.
//
// Only from the two states a task can be in before it has ever run.
// Everything else is refused rather than quietly done, for the reasons
// WithdrawApproval refuses the same ground:
//
//   - running -- deferring would not stop the run, and Close is the one
//     thing that does. "Put this aside" about work happening right now
//     is a request grain cannot honestly answer with a flag.
//   - awaiting_reply -- the task is parked on a question with a run
//     half-finished behind it. Answering it, or closing it, is what
//     moves it; hiding it would leave the run's own agent waiting on
//     somebody who has been told the task is put away.
//   - failed, completed, awaiting_submit, closed -- each is
//     a task whose work has already happened, and none of them is waiting
//     on a dispatch that deferring could call off. What such a task needs
//     is Retry, Submit or Close.
//
// Deferring an already-deferred task restamps it rather than being a
// no-op: the state is unchanged either way, and the timestamp is the one
// thing a second deferral could sensibly mean. That is also how a wake
// time is changed or dropped -- defer it again, saying the new one or
// saying nothing.
func (c *Client) Defer(ctx context.Context, id string, opts DeferOptions) error {
	state, err := c.taskState(ctx, id)
	if err != nil {
		return err
	}
	switch state {
	case model.StateProposed, model.StateQueued, model.StateDeferred:
	case model.StateRunning:
		return validationErrorf("task %s is running: close it to cancel the run, or wait for it to finish", id)
	default:
		return validationErrorf("task %s is %s: only a task that has not run can be deferred", id, state)
	}
	now := c.now()
	if opts.Until != nil && !opts.Until.After(now) {
		return validationErrorf("wake time %s is not in the future: a deferral until a moment that has passed would be undone by the next cycle",
			opts.Until.UTC().Format(time.RFC3339))
	}
	return c.Store.SetDeferred(ctx, id, &now, opts.Until, now)
}

// DeferOptions is what a caller chose at the moment of deferring, beyond
// putting the task aside itself. The zero value is the deferral grain
// has always had: aside until somebody presses Undefer.
type DeferOptions struct {
	// Until is when the task should come back by itself
	// (model.Task.DeferredUntil) -- nil for a deferral with no date on
	// it. The orchestrator undeferrs it on the first cycle at or after
	// this instant, so it is "not before", not "at": a wake time is a
	// floor on when the task may start, and how soon after it actually
	// starts is the ordinary question of capacity every queued task
	// faces.
	//
	// Refused if it is not in the future, rather than obeyed and undone
	// on the next cycle. A caller asking for a wake time already past is
	// either working from a stale clock or has mistyped the year, and
	// neither is served by a deferral that silently lasts one tick.
	Until *time.Time
}

// Undefer picks a deferred task back up, clearing what Defer stamped --
// the wake time along with it, so a task picked up early stays picked up
// rather than deferring itself again on the date it was going to wake.
// The task returns to whichever of 'proposed' and 'queued' its own
// approval implies -- nothing else is stored about where it came from,
// and nothing needs to be, since the approval never went anywhere.
//
// A task that is not deferred is left alone, mirroring Approve's no-op on
// an already-approved task: this is the undo of a flag, so there is
// nothing to refuse when the flag is already off.
func (c *Client) Undefer(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	if task.DeferredAt == nil {
		return nil
	}
	if err := c.Store.SetDeferred(ctx, id, nil, nil, c.now()); err != nil {
		return err
	}
	if task.Interactive && task.Approval != nil && c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// taskState is "what state is this task in", with a missing task reported
// as the NotFoundError every other method here answers with rather than
// as an empty state -- the pair of reads Defer above needs before it
// decides anything.
func (c *Client) taskState(ctx context.Context, id string) (model.State, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return "", err
	}
	if task == nil {
		return "", &NotFoundError{ID: id}
	}
	return c.Store.State(ctx, id)
}

// Submit is the UI's own "submit" button: once a task has a pull request
// open, this is what puts it on its target repo's merge queue for
// automatic conflict resolution and merging -- see
// orchestrator.isQueueMember's own doc comment, which already names this
// step and reuses AutoMerge as the field that means it rather than adding
// a second one. Submitting an already-submitted task is a no-op.
//
// A change a sensitive path or grain's own guard holds for named
// approvers is released only by them (submitWithApprovals, approvals.go).
//
// Submitting also drops a grain CI verdict that concluded without passing
// -- cancelled, errored, failed -- the way RetryMerge does
// (clearUnansweredCI), so the merge queue starts a fresh required run on
// its next cycle rather than waiting out orchestrator's ciRetryDelay, or
// waiting on a verdict nothing will replace.
func (c *Client) Submit(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	if submitsAsCL(*task) {
		// A CL is submitted by a person through its provider, never
		// queued for a merge (change_submit.go).
		return c.submitChange(ctx, *task)
	}
	if task.ChangeTarget != nil {
		// A change target's change is only ever a CL: with none live
		// there is nothing to submit, and the merge queue below has no
		// pull request to take -- auto-merge on such a task is one
		// every dispatch refuses (model.ValidateTaskChangeTarget).
		return validationErrorf("%s has no %s change to submit yet", id, task.ChangeTarget.Kind)
	}
	// A CL whose provider gates Submit on its required reviews
	// (change.ReviewGate) is submitted only once they have passed on its
	// current snapshot -- asked before the approvals path below, which
	// turns auto-merge on by itself once the last approval is in.
	if ok, reason, err := change.GateSubmit(ctx, *task); err != nil {
		return fmt.Errorf("checking whether %s may be submitted: %w", id, err)
	} else if !ok {
		if reason == "" {
			reason = "its required reviews have not passed on the current snapshot"
		}
		return validationErrorf("%s cannot be submitted yet: %s", id, reason)
	}
	if pending := model.PendingApprovals(*task); len(pending) > 0 && !task.AutoMerge {
		return c.submitWithApprovals(ctx, *task, pending)
	}
	now, head := c.now(), c.submittedHead(ctx, *task)
	if err := c.mutate(ctx, id, func(task *model.Task) error {
		// Only a Submit that turns auto-merge on answers a hold. One
		// over a change already merging itself (pressed to retry its CI)
		// has looked at no new head, and must not pin a clearance to
		// whatever head the next cycle reads -- one a stacked fix or any
		// other push may have moved to without being classified.
		if !task.AutoMerge {
			humanSubmitted(task, now, head)
		}
		task.AutoMerge = true
		return nil
	}); err != nil {
		return err
	}
	if err := c.unblockSubmitted(ctx, id, now); err != nil {
		return err
	}
	return c.clearUnansweredCI(ctx, id)
}

// humanSubmitted is what a human's Submit says about the holds a change
// is waiting under, beside turning its auto-merge on: a security hold, a
// human-only hold, a broken-rules hold and a review's open questions are
// all waiting for exactly this decision, so none of them is left for a
// later cycle to notice and drop. Their absence alone is not enough, though. A
// clearance recorded for an older head (model.LinkTrustBoundaryCleared)
// would read as a head that moved the moment the merge queue merges the
// base branch in, and file another security review of what the human
// has just shipped. So the decision is marked (model.LinkHumanSubmitted)
// with head, the head the human was shown (submittedHead), for the
// orchestrator to pin before it merges anything into the branch -- and
// only if the branch is still there (orchestrator.pinHumanSubmit).
func humanSubmitted(t *model.Task, now time.Time, head string) {
	t.Links = slices.DeleteFunc(t.Links, func(l model.Link) bool {
		switch l.Kind {
		case model.LinkSecurityHold, model.LinkHumanOnlyHold, model.LinkBrokenRulesHold,
			model.LinkReviewQuestions, model.LinkHumanSubmitted:
			return true
		}
		return false
	})
	t.Links = append(t.Links, model.Link{Kind: model.LinkHumanSubmitted, Target: model.HumanSubmittedTarget(now, head)})
}

// submittedHead is the pull request head a human submitting task was
// shown: GitHub's head for it where this client can ask, and otherwise
// the commit grain CI last judged, which is the one the task page names
// (the order forceMergeHead settles an override's head in). "" where
// neither says, which the orchestrator takes to be about the head it
// last classified, and classifies any other head it reads again.
//
// Read here rather than left to the next cycle because a push landing
// between the Submit and that cycle is a commit nobody reviewed: pinned
// to whatever head the cycle reads, it merged on the strength of the
// human's look at its parent (task 979).
//
// It is the head at the moment of the press, not the one the page was
// drawn with: a push landing between the page loading and the click is
// recorded as what the human saw. That window is accepted, being seconds
// against the old one of a whole cycle; closing it means the page
// sending the head it showed with the request (docs/architecture.md,
// "When something is stuck").
func (c *Client) submittedHead(ctx context.Context, task model.Task) string {
	ref := pullRequestRefOf(task)
	if ref == "" {
		return ""
	}
	if head := c.pullRequestHead(ref); head != "" {
		return head
	}
	obs, err := c.Store.GetObservation(ctx, task.ID)
	if err != nil || obs == nil || obs.CI == nil {
		return ""
	}
	return strings.TrimSpace(obs.CI.Commit)
}

// unblockSubmitted puts a change the merge queue had stopped driving back
// on it, the part of RetryMerge a Submit implies: the queue never revisits
// a block of its own accord, so a Submit over one would otherwise leave
// the change auto-merging and still out of the queue.
func (c *Client) unblockSubmitted(ctx context.Context, id string, now time.Time) error {
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil || obs == nil || obs.MergeQueueBlockedAt == nil {
		return err
	}
	return c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.MergeQueueBlockedAt = nil
		o.MergeQueueBlockedBase, o.MergeQueueRearms = "", 0
		// The row's "Merge failed" goes with the block, as RetryMerge's does.
		o.MergeQueueState, o.MergeQueueNote, o.MergeQueueStateAt = "", "", nil
	})
}

// clearUnansweredCI forgets a task's grain CI verdict when it concluded
// without passing, so orchestrator.reconcileTaskCI starts a fresh
// required run for the head on its next cycle (it starts one for any
// task with no status). A run still going and a pass are kept: one is
// about to answer, the other already has.
func (c *Client) clearUnansweredCI(ctx context.Context, id string) error {
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return err
	}
	if obs == nil || !obs.CI.Done() || obs.CI.Green() {
		return nil
	}
	return c.Store.ObserveField(ctx, id, c.now(), func(o *model.Observation) {
		if o.CI.Done() && !o.CI.Green() {
			o.CI = nil
		}
	})
}

// AddComment appends to a task's conversation, optionally carrying files
// alongside it (bwsalmon/agents#522's "attachable to follow-on comments
// in addition to the main task content") -- a comment needs a body, at
// least one attachment, or both; one with neither says nothing at all.
//
// This is also how a human answers a question a run parked on: an
// awaiting_reply task has Observation.PendingQuestionCommentID set, and
// replying clears it so the task queues again. That used to require
// re-applying the trigger label to the issue so the next poll would
// notice -- the reply and the re-trigger were two separate acts, and
// forgetting the second left the task parked forever.
//
// One kind of park a reply cannot lift on its own: a task whose target
// is outside its team's target repos (teamTargetRepos). The reply still
// clears the pending question, and this then re-asks that list about the
// task's target and re-parks it where the answer is still no
// (model.OffAllowlistAgainComment). See
// parkOffAllowlist -- the park is the only place that boundary is
// decided, so the reply has to be the nudge to look again rather than
// the answer itself.
func (c *Client) AddComment(ctx context.Context, id, body string, uploads []AttachmentUpload) error {
	return c.AddCommentWithAuthor(ctx, id, body, uploads, "")
}

// AddCommentWithAuthor appends a comment to the task's conversation with an explicit author.
func (c *Client) AddCommentWithAuthor(ctx context.Context, id, body string, uploads []AttachmentUpload, author string) error {
	return c.commentWithAuthor(ctx, id, body, uploads, author, true)
}

// commentWithAuthor is AddCommentWithAuthor with the one thing a team
// broadcast does differently: reopenAck false leaves a task in
// model.StateAwaitingAck where it is (reopenAcknowledgeable). A
// broadcast lands on every such task of the team at once, and is
// addressed to the team rather than to any one finished report, so it
// is not the "continue" a comment on that task's own page is.
func (c *Client) commentWithAuthor(ctx context.Context, id, body string, uploads []AttachmentUpload, author string, reopenAck bool) error {
	if strings.TrimSpace(body) == "" && len(uploads) == 0 {
		return validationErrorf("a comment needs a body, an attachment, or both")
	}
	now := c.now()
	// Decoded before the comment itself is written, for the same "fail
	// with nothing written" reason CreateTask decodes its own attachments
	// before NewTaskID allocates anything.
	attachments, err := decodeAttachments(uploads, now)
	if err != nil {
		return err
	}
	actor := c.requestActor(ctx).Actor
	if trimmed := strings.TrimSpace(author); trimmed != "" {
		actor = model.Principal{Kind: model.PrincipalHuman, ID: trimmed}
	}
	return c.addAttributedComment(ctx, id, body, attachments, model.Attribution{Actor: actor}, now, reopenAck)
}

// addAttributedComment is AddCommentWithAuthor once the author is
// settled: it writes the comment as by, and lifts a pending question the
// same way any reply does -- and, when reopenAck is set and by is a
// person, sends a task in model.StateAwaitingAck back to the queue
// (reopenAcknowledgeable). BroadcastTeamMessage comes in here directly so
// that a run's broadcast is signed as that run's (grain relaying it on the
// task's behalf) rather than as whoever c.actor resolves the daemon's own
// hop to -- the operator, which is a human.
func (c *Client) addAttributedComment(ctx context.Context, id, body string, attachments []model.Attachment, by model.Attribution, now time.Time, reopenAck bool) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	if _, _, err := c.Store.AddCommentWithAttachments(ctx, model.Comment{
		TaskID:    id,
		Author:    by,
		Body:      body,
		CreatedAt: now,
	}, attachments); err != nil {
		return err
	}

	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return err
	}
	// A run that stopped with resume_later(when: {message: true}) -- or a
	// console session that ParkConsoleSessionTask both parked on a question
	// and deferred on {message: true} -- is waiting for anybody's post but
	// its own. Wake before reopenAcknowledgeable so the first human reply
	// clears DeferredAt as well as PendingQuestionCommentID instead of
	// returning early from reopenAcknowledgeable with the task still deferred.
	wokeOnMessage := false
	ownAgent := model.Principal{Kind: model.PrincipalAgent, ID: id}
	if by.Actor != ownAgent && (by.OnBehalfOf == nil || *by.OnBehalfOf != ownAgent) {
		var err error
		wokeOnMessage, err = c.Store.WakeOnMessage(ctx, id, now)
		if err != nil {
			return err
		}
	}
	if reopenAck && by.Actor.Kind == model.PrincipalHuman {
		reopened, err := c.reopenAcknowledgeable(ctx, id, now)
		if err != nil || reopened {
			return err
		}
	}
	if obs == nil || obs.PendingQuestionCommentID == nil {
		if (wokeOnMessage || (task.Interactive && by.Actor.Kind == model.PrincipalHuman)) && c.Config.Wake != nil {
			c.Config.Wake()
		}
		return nil
	}
	// Whatever the task was parked on, it does not become dispatchable by
	// being replied to if its target is off this deployment's target repo
	// list: that park is lifted by widening the list, and this re-park
	// says so rather than letting the reply through (parkOffAllowlist,
	// grain/task-276). Asked of every reply rather than only of a task
	// parked for this reason in the first place, because the question is
	// about the task as it stands now -- a list narrowed while a run's
	// own question sat unanswered has the same answer, and a task that
	// may not be worked in may not be worked in whatever it was waiting
	// for.
	allowed := c.teamTargetRepos(ctx, model.DefaultTeamID)
	if task.Target != nil && !targetAllowed(allowed, *task.Target) {
		return c.park(ctx, id, model.OffAllowlistAgainComment(*task.Target, allowed), now)
	}
	// PendingSecret goes with it. A run's request_secret call parks the
	// task through the very field above (orchestrator.relayParkingCalls),
	// so a reply un-parks a task that was waiting for a credential just
	// as surely as one that was waiting for an answer -- and a task back
	// in the queue must not still be offering a box that writes a value
	// nothing is waiting for. Answering "not this one, use the staging
	// key" in words is a legitimate answer to a request for a secret.
	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = nil
		o.PendingSecret = ""
	}); err != nil {
		return err
	}
	// The task is dispatchable as of this instant, and somebody is
	// waiting on the cycle that will notice. Asked for here rather than
	// at the top of this method because this is the branch that changed
	// something a cycle acts on: a comment on a task that was not parked
	// leaves the dispatch decision exactly as it was, and waking the loop
	// for it would be a cycle spent on nothing. See Config.Wake.
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// reopenAcknowledgeable is what a person's comment does to a task in
// model.StateAwaitingReply: it is taken as "continue", the same as a reply
// to a question, and the task goes back to the queue for another run.
// reopened reports whether it was such a task; for any other state
// nothing is written.
//
// The writes are RunNow's, less RunNowAt: the completion goes, so the
// task is queued again; RetryRequestedAt is stamped, so one at its
// failure-streak limit still gets the attempt it is being asked for; and
// a stale question or secret request goes with them. Without the run-now
// stamp the task takes its ordinary place in the backlog rather than
// jumping it -- a comment is a reply, not a request to go first. No note
// of grain's own is added: the person's comment is the newest on the
// thread, past Observation.DeliveredCommentID, and the run's prompt
// carries it like any other reply.
//
// Only awaiting_reply. A closed task stays closed (reopening it is its own
// action), and a completed task with a pull request -- awaiting_submit,
// or on the merge queue -- has its own ways forward that a comment must
// not short-circuit; model.AwaitsAck already excludes every task with a
// LinkFixes pull request, so the state alone draws that line. The caller
// asks this only for human comments: what agents and grain itself say
// on a task never sends it anywhere.
//
// The target allowlist is asked here for the same reason a reply asks
// it: a task that may not be worked in is re-parked with grain's
// sentence saying why, rather than queued for a dispatch that would
// refuse it.
func (c *Client) reopenAcknowledgeable(ctx context.Context, id string, now time.Time) (reopened bool, err error) {
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return false, err
	}
	if state != model.StateAwaitingReply {
		return false, nil
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return false, err
	}
	if task == nil {
		return false, &NotFoundError{ID: id}
	}
	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.CompletedAt = nil
		o.RetryRequestedAt = &now
		o.PendingQuestionCommentID = nil
		o.PendingSecret = ""
	}); err != nil {
		return false, err
	}
	allowed := c.teamTargetRepos(ctx, model.DefaultTeamID)
	if task.Target != nil && !targetAllowed(allowed, *task.Target) {
		return true, c.park(ctx, id, model.OffAllowlistAgainComment(*task.Target, allowed), now)
	}
	// Dispatchable as of now, and the person who said "continue" is
	// waiting to see it start (Config.Wake).
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	return true, nil
}

// Stop interrupts an in-flight turn on a task, posting a turn stopped comment
// and transitioning the task to awaiting_reply.
func (c *Client) Stop(ctx context.Context, id string) error {
	now := c.now()
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}

	commentID, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    id,
		Author:    model.Attribution{Actor: c.requestActor(ctx).Actor},
		Body:      model.TurnStoppedComment,
		CreatedAt: now,
	})
	if err != nil {
		return err
	}

	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
		o.PendingSecret = ""
	}); err != nil {
		return err
	}
	if err := c.Store.AdvanceDelivered(ctx, id, commentID, now); err != nil {
		return err
	}

	if err := c.cancelActiveRuns(ctx, id, now); err != nil {
		return err
	}
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// cancelActiveRuns stops the agent run working on id, if there is one:
// through Config.TaskCanceller where this daemon dispatched it, and
// otherwise by finishing whatever run the store still has open, so the
// task stops reading as running either way. Stop's, and ForceMerge's
// when the merge is forced over a live run.
func (c *Client) cancelActiveRuns(ctx context.Context, id string, now time.Time) error {
	if c.Config.TaskCanceller != nil && c.Config.TaskCanceller.CancelTask(ctx, id) {
		return nil
	}
	runs, err := c.Store.Runs(ctx, id)
	if err != nil {
		return err
	}
	for i := range runs {
		if runs[i].FinishedAt != nil {
			continue
		}
		if err := c.Store.FinishRun(ctx, runs[i].ID, now, "cancelled", "cancelled by user"); err != nil {
			return err
		}
	}
	return nil
}

// CloseOptions is what a human chose at the moment of closing, beyond
// closing the task itself.
//
// An argument rather than a setting anywhere, on purpose: its one field
// destroys work on GitHub, and the only defensible basis for that is
// somebody saying so about this task, now. Nothing stored could mean
// that, and a default that meant it would eventually mean it about a
// task nobody was looking at. The zero value is therefore the whole of
// grain's own behaviour -- every caller that is not a human close passes
// it, and the checkbox beside the Close button is the only thing in the
// tree that ever sets the field.
type CloseOptions struct {
	// ClosePullRequest asks grain to close the pull request this close
	// would otherwise orphan, on GitHub, without merging it. The commits
	// and the branch survive it untouched (reopening the pull request
	// restores it whole), which is the only reason it is offered at all.
	//
	// Best-effort, like the note itself: a GitHub that refuses leaves the
	// task closed and the pull request open, and says so in the note. See
	// noteOrphanedPullRequests.
	ClosePullRequest bool
	// Reason is what whoever is closing the task says about why, kept on
	// the close's model.TaskEvent beside who they are. Optional.
	Reason string
}

// Close marks a task closed. Closed is the terminal state
// model.StateClosed already names, and it is what "delete a task" means
// here -- the store has no delete either, deliberately: a task that ran
// is a record of a dispatch that happened.
//
// A task closed with a pull request still open leaves that pull request
// behind: grain will not merge it and will not look at it again (only an
// unclosed task's fixes-link reaches Store.OpenPullRequestLinks). That
// is the intended outcome, and this is where the person who caused it --
// and whoever finds the pull request later -- is told about it.
//
// opts.ClosePullRequest is the other ending, and the only way grain ever
// closes a pull request: whoever is closing the task says, in the same
// breath, that its pull request should be closed with it. Both endings
// are said in both places. See noteOrphanedPullRequests.
func (c *Client) Close(ctx context.Context, id string, opts CloseOptions) error {
	return c.setClosed(ctx, id, true, opts)
}

// Reopen clears a task's closure, returning it to whatever state its
// observations and approval imply.
//
// It does not reopen a pull request a close closed. Reopening one is a
// click on GitHub and grain has no way to tell a pull request it closed
// from one a human closed themselves, so guessing here would be grain
// reopening work somebody deliberately shut -- the note left by the
// close says as much, and points at the one place that can.
//
// What it does do is take back a pull request still open from the
// task's own branch: the close-time note says "reopening task N is what
// puts it back under grain's watch", and a deployment whose
// Config.PullRequests can re-attach one (PullRequestReattacher) makes
// that true here, before any run of the reopened task starts. A
// re-attach that fails is logged rather than failing the reopen -- the
// task is open either way, and its next run looks again as it starts.
func (c *Client) Reopen(ctx context.Context, id string) error {
	return c.ReopenBecause(ctx, id, "")
}

// ReopenBecause is Reopen with the reason whoever is reopening it gave,
// kept on the reopen's model.TaskEvent.
func (c *Client) ReopenBecause(ctx context.Context, id, reason string) error {
	if err := c.setClosed(ctx, id, false, CloseOptions{Reason: reason}); err != nil {
		return err
	}
	if r, ok := c.Config.PullRequests.(PullRequestReattacher); ok {
		if err := r.ReattachForTask(ctx, id); err != nil {
			log.Printf("ui: reopening %s: could not re-attach its pull request: %v", id, err)
		}
	}
	if task, err := c.Store.GetTask(ctx, id); err == nil && task != nil && task.Interactive && c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// Retry clears a task's own failure streak (model.Store.FailureStreak),
// the human "try again" signal bwsalmon/agents#403 asks for: without it,
// a task that reached model.MaxConsecutiveFailures would stay in
// StateFailed forever, since nothing else ever resets that count. Calling
// it on a task that is not currently failed is a harmless no-op --
// RetryRequestedAt only ever narrows how far back task_streak looks, and
// a task with no failing streak to narrow just keeps whatever state it
// already had.
//
// A task parked on a stopped turn (Task.TurnStopped) is the exception:
// what holds it is the stop, not a streak, and stamping RetryRequestedAt
// alone left it awaiting_reply with nothing ever dispatching it. So Retry
// lifts the stop as a reply would (reopenAcknowledgeable) -- the person
// pressing it is the person the stop was waiting on -- and the task is
// queued again.
func (c *Client) Retry(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	// An operator's pause is the one hold Retry does not lift: it is a
	// separate decision with its own way out, and lifting it here would
	// be undoing somebody else's pause behind their back. Said, rather
	// than stamping a retry nothing will ever dispatch.
	if pause, err := c.Store.GetPause(ctx, id); err != nil {
		return err
	} else if pause != nil {
		return validationErrorf("%s is paused by %s: Resume it to put it back in the queue",
			id, principalName(pause.PausedBy))
	}
	now := c.now()
	stopped, err := c.Store.TurnStopped(ctx, id)
	if err != nil {
		return err
	}
	// Only a person lifts a stop a person made, the same rule a reply
	// is held to (addAttributedComment): a run calling retry through the
	// agent API on a task it may write is not the one the stop waits on.
	if stopped && c.requestActor(ctx).Actor.Kind != model.PrincipalHuman {
		return validationErrorf("%s was stopped by an operator: only a person can resume it, by replying or pressing Retry", id)
	}
	if stopped {
		reopened, err := c.reopenAcknowledgeable(ctx, id, now)
		if err != nil || reopened {
			return err
		}
	}
	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.RetryRequestedAt = &now
		// A stopped task in some state other than awaiting_reply (one at
		// its failure-streak limit, say) still has the stop to lift, or
		// clearing its streak would leave it parked all the same.
		if stopped {
			o.PendingQuestionCommentID = nil
			o.PendingSecret = ""
		}
	}); err != nil {
		return err
	}
	if (task.Interactive || stopped) && c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// RetryMerge puts a task the merge queue has given up on back on that
// queue: the human "try landing this again" signal, and the only way one
// is ever given, since the queue itself never revisits a decision to
// stop (Observation.MergeQueueBlockedAt, orchestrator.blockMergeQueue).
//
// The queue's whole memory of having tried is three stamps, and this
// clears all three. MergeQueueBlockedAt is what keeps the task out of
// orchestrator.isQueueMember, so clearing it makes the task a queue
// entry again -- and clearing only that would give the task its
// position back with nothing new to do in it: MergeQueueRepairAt is what
// holds a pull request to one automatic repair for the life of the task,
// and MergeQueueRefreshedAt what holds it to one base-branch merge per
// repair round. A retry that kept them would put the task at the head of
// its repo's queue, find it still broken, and give up again on the same
// cycle. So a retry is a fresh round in full: read the checks again,
// merge the base branch in if the branch has fallen behind, and send it
// back to an agent once more if that is not enough.
//
// It is deliberately not a merge. Nothing here goes to GitHub: the next
// orchestrator.SyncPullRequests cycle reads the pull request as it
// stands now and decides, exactly as it would have for a task that had
// never been blocked -- so a retry cannot land a change whose checks the
// queue has not read, and a pull request that is still broken is still
// broken after one.
//
// The fourth thing it clears is a grain CI verdict that is not a pass,
// and that one is not bookkeeping: it is the only way this deployment's
// own checks are ever asked again. A recorded verdict is kept per commit
// and replaced by the next push (orchestrator.reconcileTaskCI), so a
// branch whose run was cancelled -- or failed, and was then fixed by
// something other than a commit on it -- keeps that answer for as long as
// the head stands, and "read the checks again" would have meant reading
// the same cancelled run again forever. Clearing it makes the next
// reconcile start a fresh full-suite run for the head commit, which is
// what a person pressing this button means by it. A pass is left alone,
// since asking again could only produce the answer already held, and a
// run still in flight is left alone too -- cancelling it would throw away
// the very answer being waited for, and collectCIRun needs the status row
// to collect it.
//
// Refused for a task with no pull request (there is nothing for a queue
// to land) and for a closed one (a closed task is out of
// Store.OpenPullRequestLinks, so no cycle would ever look at it -- reopen
// it first). Retrying a task the queue has *not* given up on is allowed
// and does nothing beyond returning its refresh and its repair: the queue
// is already driving it, and this is the same offer made a moment early.
func (c *Client) RetryMerge(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return err
	}
	if state == model.StateClosed {
		return validationErrorf("%s is closed: reopen it before asking the merge queue to try again", id)
	}
	ref := pullRequestRefOf(*task)
	if ref == "" {
		return validationErrorf("%s has no pull request for the merge queue to land", id)
	}
	now := c.now()
	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.MergeQueueBlockedAt = nil
		o.MergeQueueRepairAt = nil
		o.MergeQueueRefreshedAt = nil
		// And the queue's own re-arms: their bound is on the ones nobody
		// asked for, and this is somebody asking
		// (orchestrator.rearmBlockedOnBaseMove).
		o.MergeQueueBlockedBase, o.MergeQueueRearms, o.MergeQueueInherited = "", 0, ""
		// And the automatic repairs: a person's retry is a fresh round,
		// with both of them available again (orchestrator.maxAutomaticRepairs).
		o.MergeQueueRepairs, o.MergeQueueRepairBase = 0, ""
		o.MergeQueueRepairFailures, o.MergeQueueRepairWhy = "", ""
		// And the stall retry: a fresh round gets its one automatic answer
		// to a CI stall back (orchestrator.retryStalledCI).
		o.MergeQueueStallRetryAt, o.MergeQueueStallRetryHead, o.MergeQueueStallRetryWhy = nil, "", ""
		// The row's own account of having failed goes with them: the next
		// cycle writes what the queue is doing now, and until it runs a
		// "Merge failed" line beside a task somebody has just put back on
		// the queue is a stale answer to a question already settled.
		o.MergeQueueState, o.MergeQueueNote, o.MergeQueueStateAt = "", "", nil
		o.MergeQueueReason = ""
		// Including the flag that said the last round had stopped
		// getting anywhere (Observation.MergeQueueStalledAt): the next
		// cycle starts a fresh wait with a fresh clock, and a row that
		// still read "stalled" would be flagging the round a person has
		// just asked for against the one it replaced.
		o.MergeQueueStalledAt = nil
		if o.CI.Done() && !o.CI.Green() {
			o.CI = nil
		}
	}); err != nil {
		return err
	}
	// Said in the same thread the queue said it was giving up in, and
	// after the write rather than before it, for setClosed's own reason:
	// a note in front of a write that then failed says something untrue.
	// The comment is this action's whole record -- what was cleared is
	// three fields going back to nil, which a person reading the task
	// later has no other way to see happened.
	_, err = c.Store.AddComment(ctx, model.Comment{
		TaskID: id,
		Author: model.Attribution{Actor: c.requestActor(ctx).Actor},
		Body: fmt.Sprintf(
			"Asked the merge queue to try %s again. It takes its place in the queue "+
				"as if it had never been given up on: the checks are read afresh -- and "+
				"grain's own CI asked for again, where its last answer was anything but a "+
				"pass -- the base branch is merged in if the branch has fallen behind, and "+
				"if that is not enough it gets one more repair run of its own.", ref),
		CreatedAt: now,
	})
	return err
}

// ForceMerge is an operator overruling the merge queue: land this change
// anyway, here is why. It is the one sanctioned exception to the merge
// predicate (orchestrator's mergepredicate.go, grain/task-479), and the
// whole of what it does here is record that somebody asked --
// Observation.ForceMergeAt and the three fields beside it. grain performs
// the merge itself, on the next orchestrator.SyncPullRequests cycle.
//
// That division is the point rather than an implementation detail. The
// override already existed, outside grain: a person merging the pull
// request underneath the queue by hand, half a dozen times in a day on
// the deployment this was written for. Every one of those left grain's
// own state stale, because grain did not do the merge and did not know
// it had happened -- the task sat on the queue, the row went on saying
// what it was waiting for, the post-merge dispatch and the base-branch
// run never fired. Merging from here, out of an API handler, would have
// the same fault in a smaller way: the merge and everything grain does
// about a merge belong in the same cycle.
//
// A reason is required, and it is required here rather than being
// encouraged: an override nobody wrote a reason for is indistinguishable
// afterwards from a queue that merged something it should not have. It
// is recorded on the task now, and on the task and the pull request
// again when the merge happens (orchestrator's recordForcedMerge), along
// with what was not green at the time and a count of how often this has
// been done lately -- "it should be rare" is only a rule if somebody can
// check it.
//
// What it does not do is make the change green, and nothing it writes
// says otherwise. A conflicted pull request is refused by the predicate
// however loudly it is asked, because a merge GitHub will not perform is
// not something an override can authorise; such a request simply stays
// outstanding and lands when the branch merges cleanly -- with the same
// head, since a push to resolve it is a commit nobody has reviewed.
//
// It is an operator's action. This method, the route in front of it and
// `grain force-merge` are the only ways to ask: there is no MCP tool, no
// task field, and nothing a running agent can do to force its own change
// in.
//
// A task being worked on right now is not refused, because the usual
// reason to reach for this is a run sitting in wait_for_checks on CI
// that is never going to finish. Forcing the merge ends that run (as
// Stop would), marks the task completed so Store.OpenPullRequestLinks
// hands it to the next cycle, opens the pull request for its pushed
// branch if the run had not got that far, and cancels the task's CI run
// still in flight so its jobs give their sandboxes back. The same is
// done for a task a Stop already parked in awaiting_reply, which would
// otherwise never reach the queue. The pin to head below is what keeps
// this honest: a commit the run pushed after the operator looked is
// not merged on the strength of it.
//
// Refused for a task with no pull request (there is nothing to merge)
// and for a closed one (no cycle reads it -- reopen it first). Not
// refused for a task that has not been submitted: an operator saying
// "merge this" is a stronger statement than the auto-merge flag, and
// asking them to tick a box first would only teach them to reach for
// GitHub instead.
//
// The override is for one commit: head, the pull request's head as the
// operator saw it, which is what they reviewed (bwsalmon/grain#2399).
// The merge queue performs it only while the branch still points there,
// and withdraws it the moment it does not -- so a push between this
// request and the cycle that acts on it is never merged on the strength
// of it. An empty head means "whatever it is now", read off GitHub here
// rather than at the merge; see forceMergeHead for how it is settled.
func (c *Client) ForceMerge(ctx context.Context, id, reason, head string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return validationErrorf("forcing a merge needs a reason: it is the whole record of why " +
			"this landed without the checks the queue asks for")
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return err
	}
	if state == model.StateClosed {
		return validationErrorf("%s is closed: reopen it before forcing its merge", id)
	}
	// A live run, or one Stop has parked: neither is anywhere the merge
	// queue looks, so forcing the merge also finishes the task (below).
	finish := state == model.StateRunning || state == model.StateAwaitingReply
	if finish && pullRequestRefOf(*task) == "" && c.Config.PullRequests != nil {
		// The run pushed its branch but never got as far as asking for
		// its pull request, which its own finish would have done.
		if _, err := c.OpenPullRequest(ctx, id); err != nil {
			return validationErrorf("%s has no pull request to merge, and opening one for its "+
				"branch failed: %v", id, err)
		}
		if task, err = c.Store.GetTask(ctx, id); err != nil {
			return err
		}
		if task == nil {
			return &NotFoundError{ID: id}
		}
	}
	ref := pullRequestRefOf(*task)
	if ref == "" {
		return validationErrorf("%s has no pull request to merge", id)
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return err
	}
	// What is being skipped, captured now rather than at the merge: the
	// queue's own account of why it is not merging this is what the
	// operator is looking at as they press the button, and by the time
	// the merge happens it has been overwritten with "merging".
	skipped := forceMergeSkipped(obs)
	// Lower-cased because git reads an object name in either case and a
	// person pasting one may not have kept it: the comparisons below, and
	// fullSHA, are against git's own lower-case spelling.
	head, err = c.forceMergeHead(ref, strings.ToLower(strings.TrimSpace(head)), obs)
	if err != nil {
		return err
	}
	now := c.now()
	by := c.requestActor(ctx).Actor.ID
	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.ForceMergeAt = &now
		o.ForceMergeReason, o.ForceMergeBy, o.ForceMergeSkipped = reason, by, skipped
		o.ForceMergeHeadSHA = head
		if finish {
			// Completed, so the next SyncPullRequests cycle reads it
			// (Store.OpenPullRequestLinks); a CompletedAt already set
			// is the run's own and is kept.
			o.PendingQuestionCommentID = nil
			if o.CompletedAt == nil {
				o.CompletedAt = &now
			}
		}
	}); err != nil {
		return err
	}
	if state == model.StateRunning {
		if err := c.cancelActiveRuns(ctx, id, now); err != nil {
			return err
		}
	}
	// CI still running on the task is not going to decide anything now,
	// and a hung one is holding sandboxes: grain's own cancel, since it
	// is grain giving up on a verdict it no longer needs.
	if c.Config.CI != nil && obs != nil && obs.CI != nil && !obs.CI.Done() && obs.CI.RunID != "" {
		c.Config.CI.Cancel(obs.CI.RunID, "the merge of "+ref+" was forced", ci.CancelBySystem)
	}
	// After the write, the way every note in this file is: a note in
	// front of a write that then failed says something untrue. This one
	// is what a person reading the task sees between the request and the
	// cycle that acts on it, and it says plainly that nothing has merged
	// yet -- a button whose effect is one cycle away needs to say so, or
	// it reads as a button that did not work.
	body := fmt.Sprintf(
		"Asked grain to force the merge of %s at %s, overriding the merge queue's own decision.\n\n"+
			"**Reason given:** %s", ref, shortHead(head), reason)
	if skipped != "" {
		body += "\n\n**What the queue was saying instead:** " + skipped
	}
	if finish {
		body += "\n\nThe task's own run was stopped and the task marked done, so the merge " +
			"queue takes it up; any grain CI still running on it was cancelled."
	}
	body += "\n\ngrain performs the merge itself on its next merge-queue cycle, so its own " +
		"record of this task stays true: it is merged here as well as on GitHub, the queue row " +
		"closes, and the base branch is tested afterwards as it is after any other merge. A " +
		"conflict is the one thing this cannot override -- a merge GitHub refuses is not " +
		"grain's to authorise -- and such a request waits until the branch merges cleanly. " +
		"The override is for this commit only: if anything is pushed to the branch before the " +
		"merge, grain withdraws it rather than merge what nobody reviewed, and it has to be " +
		"forced again."
	if _, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    id,
		Author:    model.Attribution{Actor: c.requestActor(ctx).Actor},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		return err
	}
	// Somebody is watching for this one, exactly as they are for "Run
	// now": an override that visibly does nothing until the next poll
	// interval is what sends a person back to merging by hand.
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// forceMergeHead settles which commit an override is for: the head the
// operator says they reviewed, checked against the pull request's head
// as GitHub reports it now, and widened to the full sha the merge queue
// compares against (orchestrator's forcedMerge.movedSince compares whole
// shas, never prefixes).
//
// What "now" is comes from GitHub where this client can ask it
// (Config.GitHubPullRequests), and otherwise from the commit grain CI
// last judged -- which is the commit the task page names, and so the
// one the operator was looking at. Either way the queue checks the
// stamp against the head again before it merges; this is where a
// mismatch is caught while the operator is still at the button, rather
// than a cycle later on the task's thread.
//
// Refused, rather than guessed, when the operator names a head and
// GitHub says the branch is somewhere else (it moved while they were
// reading), and when nothing at all says which commit this would be.
func (c *Client) forceMergeHead(ref, asked string, obs *model.Observation) (string, error) {
	live := c.pullRequestHead(ref)
	known := live
	if known == "" && obs != nil && obs.CI != nil {
		known = strings.TrimSpace(obs.CI.Commit)
	}
	switch {
	case asked == "" && known == "":
		return "", validationErrorf("grain cannot tell which commit of %s this would merge: name "+
			"the head commit you reviewed", ref)
	case asked == "":
		return known, nil
	case known != "" && (known == asked || (len(asked) >= 7 && strings.HasPrefix(known, asked))):
		return known, nil
	case live != "":
		return "", validationErrorf("%s has moved since you looked: you reviewed %s and its head "+
			"is now %s -- review that and force again", ref, shortHead(asked), shortHead(live))
	case fullSHA(asked):
		// Only grain CI's last commit to go on, and the operator is
		// looking at a newer one. Taken as said: the queue compares it
		// against the real head before it merges anything.
		return asked, nil
	default:
		return "", validationErrorf("%s is not a commit grain can confirm is the head of %s: "+
			"name it in full", asked, ref)
	}
}

// pullRequestHead is ref's head commit as GitHub reports it, or "" where
// this client has no GitHub to ask or GitHub did not answer -- which
// forceMergeHead then makes do without, since the queue checks again.
func (c *Client) pullRequestHead(ref string) string {
	if c.Config.GitHubPullRequests == nil {
		return ""
	}
	pr, err := model.ParsePullRequestRef(ref)
	if err != nil || model.IsConfigRepo(pr.Repo) {
		// The config repository's pull requests are grain's own and
		// GitHub has none of them: CI's last commit is the answer there.
		return ""
	}
	detail, err := c.Config.GitHubPullRequests.GetPullRequest(pr.Repo.Owner, pr.Repo.Name, pr.Number)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(detail.HeadSHA)
}

// fullSHA is whether s is a whole git object name, SHA-1 or SHA-256.
func fullSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// shortHead is a commit abbreviated the way the task page shows one.
func shortHead(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// forceMergeSkipped is what was not green when an override was asked
// for, in the words the row was already using: the merge queue's own
// note -- which is where the predicate's refusal is written
// (orchestrator.mergeDecision.why) and where a failing check is named --
// and grain CI's own verdict where it has one that is not a pass.
//
// Both, where there are both, because they answer different halves: the
// note says what the queue was waiting for, and the CI row says what the
// suite concluded about this exact commit and when. A task grain has not
// got round to judging yet gets a sentence saying so rather than an
// empty string, since "what did this skip?" answered with a blank reads
// as a record that was lost rather than one there was nothing to make.
func forceMergeSkipped(obs *model.Observation) string {
	if obs == nil {
		return "grain had not recorded anything about this pull request yet"
	}
	var parts []string
	if note := strings.TrimSpace(obs.MergeQueueNote); note != "" {
		parts = append(parts, note)
	}
	if obs.CI.Done() && !obs.CI.Green() {
		ci := fmt.Sprintf("grain CI concluded `%s` for this change's own head", obs.CI.Conclusion)
		if detail := strings.TrimSpace(obs.CI.Detail); detail != "" {
			ci += " (" + detail + ")"
		}
		parts = append(parts, ci)
	}
	if len(parts) == 0 {
		return "grain had not recorded anything about this pull request yet"
	}
	return strings.Join(parts, "; ")
}

// RunNow asks for an agent to be put on this task on the next cycle,
// ahead of everything in the queue and whatever the state machine
// currently makes of it -- the UI's "Run now" button and `grain
// run-now`. It is the one action in this file that overrides the
// ordering and the readiness rules rather than feeding them, and it
// exists for the two things a queue cannot express:
//
//   - the operator knows what they want done next, and would otherwise
//     have to drag a row to the top and wait for whatever is in front of
//     it to clear; and
//   - a task that is past its run -- completed, awaiting submit, on the
//     merge queue -- needs more work done on its branch, and nothing in
//     grain sends one back to an agent except the merge queue's own
//     repair, which is automatic, granted once per pull request and not
//     something a person can ask for.
//
// The request itself is one stamp (Observation.RunNowAt), read by
// dispatch.Cycle before it looks at the ready list at all and spent by
// the run it starts (Store.RunNow, model.startRun). Everything else
// written here is the state machine being told the truth about a task
// that is about to be worked on again, since leaving those as they were
// would have grain acting on a task it believes is finished:
//
//   - CompletedAt cleared is what puts the task back to running rather
//     than leaving it displayed as completed while an agent works on it
//     -- and, for a task with a pull request on the merge queue, what
//     stops that queue merging the branch out from under the run it is
//     about to get. It is exactly what the queue's own repair does
//     (orchestrator.requeueForRepair), for the same reason.
//   - RetryRequestedAt is what lets a task that has exhausted its
//     failure streak have this attempt at all: asking for a run is
//     asking for the attempt (requeueForRepair again).
//   - PendingQuestionCommentID cleared un-parks a task waiting on an
//     answer. Starting a run is an answer of a kind -- "go on without
//     one" -- and a task cannot both be dispatched and be waiting. The
//     PendingSecret that may sit beside it goes with it, for the reason
//     that field's own doc gives: it is the same parking, and an input
//     left offered on a task nobody is waiting to hear from is an offer
//     to write a value nothing asked for.
//
// An unapproved task is approved and a deferred one picked back up,
// rather than run in spite of either: both are declarations a person
// makes, this is the same person making a newer one, and a task that ran
// while still reading 'proposed' would go back to being a proposal the
// moment its run ended.
//
// Refused for a closed task (reopen it first -- its run would be
// cancelled on the spot) and for one already running (there is an agent
// on it; the request would be for a second run of the same task, which
// the store forbids outright). A task that is queued and merely waiting
// its turn is the ordinary case, and so is a task with a blocker still
// open: the blocker is not cleared, and it holds nothing back once
// somebody has said to run this anyway.
func (c *Client) RunNow(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	if by := c.requestActor(ctx).Actor; by.Kind == model.PrincipalAgent {
		if p, err := c.Store.GetPause(ctx, id); err != nil {
			return err
		} else if p != nil {
			return forbiddenErrorf("%s is paused by %s, and agent %q may not start it while it is paused",
				id, principalName(p.PausedBy), by.ID)
		}
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return err
	}
	switch state {
	case model.StateClosed:
		return validationErrorf("%s is closed: reopen it before asking for a run", id)
	case model.StateRunning:
		return validationErrorf("%s is already running: an agent is on it now", id)
	case model.StateAwaitingReply:
		// Un-parking is "go on without an answer", which only the person
		// the question (or request_secret) was put to may decide. An
		// agent's run_task_now would otherwise answer it for them.
		if by := c.requestActor(ctx).Actor; by.Kind == model.PrincipalAgent {
			return forbiddenErrorf("%s is waiting for a person's reply, and agent %q may not start it in "+
				"their place: a person has to answer it or Run now it themselves", id, by.ID)
		}
	}
	now := c.now()
	if task.Approval == nil {
		if err := c.agentMayApprove(ctx, *task); err != nil {
			return err
		}
		if err := c.Store.Approve(ctx, id, model.Attribution{Actor: c.requestActor(ctx).Actor}, now); err != nil {
			return err
		}
	}
	if task.DeferredAt != nil {
		if err := c.Store.SetDeferred(ctx, id, nil, nil, now); err != nil {
			return err
		}
	}
	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.RunNowAt = &now
		o.CompletedAt = nil
		o.RetryRequestedAt = &now
		o.PendingQuestionCommentID = nil
		o.PendingSecret = ""
	}); err != nil {
		return err
	}
	// After the write, the way every note in this file is: a note in
	// front of a write that then failed says something untrue. It is
	// also the only record of what this did -- an approval that was
	// granted, a completion that went back to nil -- and, for a task
	// being sent back over a pull request, it is the first thing the
	// run's own agent reads (orchestrator.BuildPrompt renders the
	// thread), so it names the branch that work belongs on.
	if _, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    id,
		Author:    model.Attribution{Actor: c.requestActor(ctx).Actor},
		Body:      runNowComment(state, pullRequestRefOf(*task)),
		CreatedAt: now,
	}); err != nil {
		return err
	}
	// Somebody is watching for this one to start, so the loop is woken
	// rather than left to its own poll interval -- the same nudge a reply
	// to a parked question gives, and for a stronger reason: "run now"
	// that visibly does nothing for half a minute reads as a button that
	// did not work.
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// agentMayApprove refuses the approval RunNow would grant task when the
// one asking is an agent (dispatch-control's run_task_now) and approving
// would hand task authority that agent's own task was never given: a
// capability, a write target or a read it does not hold itself
// (agentEscalatedCapabilities, model.UngrantedWrites, UngrantedReads), or a
// target repository it cannot read. Those
// are exactly what keeps a proposal waiting for a person rather than
// queued by any permission (creationGrants), and an agent that could
// approve them through Run now could propose itself any grant and then
// start it. A person asking -- the button, `grain run-now` -- is the
// person those proposals wait for, so nothing is checked for them.
func (c *Client) agentMayApprove(ctx context.Context, task model.Task) error {
	by := c.requestActor(ctx).Actor
	if by.Kind != model.PrincipalAgent {
		return nil
	}
	if err := c.catalogueApprovalRefusal(ctx, task); err != nil {
		return err
	}
	requester, err := c.Store.GetTask(ctx, by.ID)
	if err != nil {
		return err
	}
	if requester == nil {
		return forbiddenErrorf("%s is not approved, and agent %q serves no task whose grants could cover approving it",
			task.ID, by.ID)
	}
	var asked []string
	for _, capability := range agentEscalatedCapabilities(task, *requester) {
		asked = append(asked, "capability "+capability)
	}
	for _, w := range model.UngrantedWrites(task, *requester) {
		asked = append(asked, "write target "+w.Repo.String())
	}
	for _, r := range model.UngrantedReads(task, *requester) {
		asked = append(asked, "read of "+r.String())
	}
	// The task's own repository is a read (and a branch to push) too, and
	// one propose_task may name freely: a proposal filed against a
	// repository the requester cannot read would otherwise be the agent
	// reaching it, one approval later. Asked of the repository only, so a
	// follow-up in the requester's own repository on another branch --
	// the ordinary proposal -- is not refused.
	if task.Target != nil {
		for _, r := range model.UngrantedReads(model.Task{Reads: []model.RepoRef{*task.Target}}, *requester) {
			asked = append(asked, "work in "+r.String())
		}
	}
	// Approving an auto-merge task is letting its change land with no
	// person reading it. A proposal only keeps AutoMerge when its proposer
	// is an auto-merge job landing where it may (CreateTask), so the same
	// is asked of an agent approving one: otherwise an agent whose own
	// work waits for review could land unreviewed work by approving a task
	// somebody else filed as auto-merge.
	if task.AutoMerge && !(requester.AutoMerge && model.LandsWhereGranted(task, *requester)) {
		asked = append(asked, "auto-merge")
	}
	// A task filed from a repository-declared template with a push set
	// lets its sandbox push refs beyond its own branch without a pull
	// request (Store.GitDeclaredPush). An agent cannot attach that push
	// set when filing (CreateTask), and approving an unapproved task
	// that carries one is a person's decision too.
	if task.TemplateID != "" {
		decl, err := c.Store.GetRepoDeclaration(ctx, task.TemplateID)
		if err != nil {
			return err
		}
		if push := declaredPushOf(decl); push != nil {
			asked = append(asked, "declared push set from "+push.Path)
		}
	}
	if len(asked) > 0 {
		return forbiddenErrorf("%s is not approved and asks for %s, which task %s does not hold itself: "+
			"a person has to approve it before an agent may run it", task.ID, strings.Join(asked, ", "), by.ID)
	}
	return nil
}

// agentEscalatedCapabilities is model.EscalatedCapabilities for an
// approval rather than a proposal: every capability task holds that
// requester does not, whatever put it there, but a default. Escalated
// counts only what a proposal named (GrantByLabel), which is the whole of
// what a propose_task call can add; the task an agent approves through
// Run now need not be one it proposed, and a grant a directive, a folder,
// a .grain declaration or grain itself put on an unapproved task is
// authority waiting for a person just the same. A default is attached to
// every task filed there whoever files it, so it asks for nothing. A
// grant only a person may give (model.IsPersonOnlyGrant: a dangerous api
// grant, or priority-dispatch) asks for a person even when requester
// holds it, as it does of a proposal: otherwise the pipeline steward,
// which holds priority-dispatch and run_task_now, could propose a task
// naming it and then approve that task itself.
func agentEscalatedCapabilities(task, requester model.Task) []string {
	held := make(map[string]bool, len(requester.Grants))
	for _, g := range requester.Grants {
		held[g.Capability] = true
	}
	var out []string
	for _, g := range task.Grants {
		if model.IsCapabilityRef(g.Capability) || g.Via == model.GrantByDefault ||
			(held[g.Capability] && !model.IsPersonOnlyGrant(g.Capability)) {
			continue
		}
		held[g.Capability] = true
		out = append(out, g.Capability)
	}
	return append(out, model.UncoveredCapabilityRefs(task.Grants, requester.Grants)...)
}

// runNowComment is what a run-now leaves on the task's own thread. It
// says the same thing twice over on purpose: to the person reading the
// conversation later, and to the agent of the run being asked for, which
// is handed this thread and otherwise has no way to know that the work
// it is starting on is a branch somebody already opened a pull request
// from.
func runNowComment(state model.State, ref string) string {
	body := "Asked for this task to be picked up now, ahead of the queue. " +
		"The next dispatch cycle starts a run of it whatever its place in " +
		"the backlog, and whether or not anything else says it is ready."
	switch {
	case ref != "":
		body += fmt.Sprintf(
			" It already has %s open: the work continues on that same branch -- push there "+
				"rather than starting a new one, and the pull request picks the commits up.", ref)
	case state == model.StateAwaitingReply:
		body += " It was waiting on an answer, which it is no longer: the run starts without one."
	}
	return body
}

// pullRequestRefOf is the pull request a task has open for its own work
// (model.LinkFixes), or "" for a task with none -- the only link kind
// that means "this task's own change", which is why both RunNow and
// RetryMerge ask for it and nothing else.
//
// The last such link wins, for the vanishingly rare task carrying two:
// grain files one per task, and a second could only come from a change
// made later, which is the one to name.
func pullRequestRefOf(task model.Task) string {
	var ref string
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes && !model.LinkOffTarget(task, l) {
			ref = l.Target
		}
	}
	return ref
}

// setClosed closes or reopens a task and records who did it
// (model.TaskEvent): the closure itself is one stamp the next reopen
// clears, so the event is the only lasting trace of either. Closing a
// task already closed, or reopening one already open, changes nothing and
// records nothing.
func (c *Client) setClosed(ctx context.Context, id string, closed bool, opts CloseOptions) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	now := c.now()
	by := c.requestActor(ctx).Actor
	eventReason := strings.TrimSpace(opts.Reason)
	if eventReason == "" {
		if call, ok := agentCallFromContext(ctx); ok {
			eventReason = call.reason
		}
	}
	// The live run is read before the close is written: the orchestrator
	// cancels it as soon as it sees the closure, and the event says so.
	var liveRun *model.Run
	if closed {
		runs, err := c.Store.Runs(ctx, id)
		if err != nil {
			log.Printf("ui: closing %s: reading its runs: %v", id, err)
		}
		for i := range runs {
			if runs[i].FinishedAt == nil {
				liveRun = &runs[i]
			}
		}
	}
	var obs *model.Observation
	changed := false
	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		changed = (o.ClosedAt == nil) == closed
		if closed {
			o.ClosedAt = &now
			obs = o
			return
		}
		o.ClosedAt = nil
	}); err != nil {
		return err
	}
	if !closed {
		if changed {
			c.recordTaskEvent(ctx, model.TaskEvent{TaskID: id, Kind: model.TaskEventReopened, At: now,
				By: by, Reason: eventReason})
		}
		return nil
	}
	var cancelledCI []string
	if c.Config.CI != nil && (obs == nil || obs.PrMergedAt == nil) {
		reason := fmt.Sprintf("task %s was closed", id)
		if by.ID != "" {
			reason += " by " + principalLabel(by)
		}
		var cancelledRunID string
		if obs != nil && obs.CI != nil && !obs.CI.Done() && obs.CI.RunID != "" {
			c.Config.CI.Cancel(obs.CI.RunID, reason, ci.CancelByOperator)
			cancelledRunID = obs.CI.RunID
			cancelledCI = append(cancelledCI, cancelledRunID)
		}
		for _, run := range c.Config.CI.List() {
			if run.ID == "" || run.ID == cancelledRunID || run.Conclusion.Done() || run.Interrupted {
				continue
			}
			if run.RequestedBy == id || (task.Target != nil && run.Repo == task.Target.String() && run.Ref == task.BranchName()) {
				c.Config.CI.Cancel(run.ID, reason, ci.CancelByOperator)
				cancelledCI = append(cancelledCI, run.ID)
			}
		}
	}
	if changed {
		c.recordTaskEvent(ctx, model.TaskEvent{TaskID: id, Kind: model.TaskEventClosed, At: now,
			By: by, Reason: eventReason, Detail: closeCancelled(liveRun, cancelledCI)})
	}
	// After the close is written, not before: the note says the task *is*
	// closed, and a note left in front of a close that then failed would
	// say something untrue. Reading the links off the task fetched above
	// is enough -- a link written between that read and this call belongs
	// to a run still finishing, and the finish path re-reads the closure
	// and leaves the same note itself (orchestrator's own
	// salvagePushedBranch).
	return c.noteOrphanedPullRequests(ctx, *task, opts, now)
}

// recordTaskEvent writes a close or reopen's event, and only logs a
// failure: the closure it records has already been written, and failing
// the call now would tell the caller it had not.
func (c *Client) recordTaskEvent(ctx context.Context, e model.TaskEvent) {
	if _, err := c.Store.RecordTaskEvent(ctx, e); err != nil {
		log.Printf("ui: recording the %s of task %s by %s: %v", e.Kind, e.TaskID, principalLabel(e.By), err)
	}
}

// closeCancelled says what a close cancelled, "" for nothing: the live
// run the orchestrator stops once it sees the closure, and the CI runs
// the close cancelled itself.
func closeCancelled(live *model.Run, ciRuns []string) string {
	var parts []string
	if live != nil {
		parts = append(parts, fmt.Sprintf("run %s (attempt %d)", live.ID, live.Attempt))
	}
	switch len(ciRuns) {
	case 0:
	case 1:
		parts = append(parts, "CI run "+ciRuns[0])
	default:
		parts = append(parts, "CI runs "+strings.Join(ciRuns, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "cancelled " + strings.Join(parts, " and ")
}

// principalLabel is a principal as a reader is shown it: "human
// alice@example.com", "agent 1920", or the kind alone for one with no id.
func principalLabel(p model.Principal) string {
	switch {
	case p.Kind == "" && p.ID == "":
		return "unknown"
	case p.ID == "":
		return string(p.Kind)
	case p.Kind == "":
		return p.ID
	}
	return string(p.Kind) + " " + p.ID
}
