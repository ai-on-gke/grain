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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This file is admission minting for capability references
// (docs/capabilities-spec.md §4.2.2-§4.2.4, §4.4.1): a task holding a
// reference (model.IsCapabilityRef) has every one of them resolved,
// checked against the deployment's capability policy and minted, as one
// pass, once the rest of admission has admitted it and before its
// sandbox is built -- so a run that cannot be granted never boots. The
// pass is held here, in memory only (§3.2.5), under the run's id, and
// released when the run ends, however it ends.

// CapabilityServers is what admission mints through: the daemon's
// attached capability servers (servers.Registry).
type CapabilityServers interface {
	Lookup(capabilityID string) (*protocol.Server, error)
}

const (
	// passTransientLimit is how many consecutive transient failures of
	// one grant refuse the run (§4.2.4).
	passTransientLimit = 3
	// passBackoffBase is the first transient retry's wait, doubled for
	// each further one; passExhaustedBackoff is how long a grant at
	// capacity waits when its server named no retry_after.
	passBackoffBase      = 30 * time.Second
	passExhaustedBackoff = time.Minute
)

// CapabilityPasses holds the minted passes of running attempts, by run
// id, and the backoff of the tasks whose mint has failed, by task id.
// Nothing here is persisted: a pass carries leased secrets.
type CapabilityPasses struct {
	mu      sync.Mutex
	passes  map[string]*heldPass
	backoff map[string]*passBackoff
}

type heldPass struct {
	pass     *protocol.Pass
	deadline time.Time
}

type passBackoff struct {
	notBefore time.Time
	grant     string
	transient int
	reason    string
}

// NewCapabilityPasses returns an empty holder.
func NewCapabilityPasses() *CapabilityPasses {
	return &CapabilityPasses{passes: map[string]*heldPass{}, backoff: map[string]*passBackoff{}}
}

// defaultCapabilityPasses serves a Deps that names none.
var defaultCapabilityPasses = NewCapabilityPasses()

func (d Deps) capabilityPasses() *CapabilityPasses {
	if d.CapabilityPasses != nil {
		return d.CapabilityPasses
	}
	return defaultCapabilityPasses
}

func (h *CapabilityPasses) hold(runID string, p *protocol.Pass, deadline time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.passes[runID] = &heldPass{pass: p, deadline: deadline}
}

func (h *CapabilityPasses) take(runID string) *heldPass {
	h.mu.Lock()
	defer h.mu.Unlock()
	held := h.passes[runID]
	delete(h.passes, runID)
	return held
}

// Release releases runID's pass, if it holds one (§4.4.1), and returns
// the audit its grants' releases returned (§4.4.4).
func (h *CapabilityPasses) Release(ctx context.Context, runID string) []protocol.GrantAudit {
	held := h.take(runID)
	if held == nil {
		return nil
	}
	audits, err := held.pass.Release(ctx)
	if err != nil {
		log.Printf("orchestrator: run %s: releasing its capability grants: %v", runID, err)
	}
	return audits
}

// releaseRunCapabilities releases the pass runID ran with and records
// on the run what its grants' servers said they did for it (§4.4.4).
// It runs once the run has ended, however it ended: grants outlive a
// recreate_sandbox (§4.4.3), so one run's audit covers every sandbox it
// had. A failure to record is logged, never the run's: the servers'
// own logs still hold every record. The CLs the records say are the
// task's own are linked to taskID (recordTaskChangelists).
func releaseRunCapabilities(ctx context.Context, deps Deps, taskID, runID string) {
	publishRunReplies(ctx, deps, taskID, runID)
	audits := deps.capabilityPasses().Release(ctx, runID)
	records := capabilityAuditRecords(audits, deps.Config.now())
	if len(records) == 0 || deps.Store == nil {
		return
	}
	if err := deps.Store.AppendRunCapabilityAudit(ctx, runID, records); err != nil {
		log.Printf("orchestrator: run %s: recording its capability audit: %v", runID, err)
	}
	if err := recordTaskChangelists(ctx, deps.Store, taskID, records); err != nil {
		log.Printf("orchestrator: run %s: linking task %s to its CLs: %v", runID, taskID, err)
	}
}

// publishRunReplies publishes the draft replies runID left on its task's
// CLs (changelists.md §5): a run's answers to its reviewers are drafts
// until it ends, so a reviewer reads one run's replies together. What
// stays unpublished -- a reviewer policy that publishes only CLs grain
// mailed, say -- is left for a person's Publish replies.
func publishRunReplies(ctx context.Context, deps Deps, taskID, runID string) {
	if deps.Store == nil {
		return
	}
	task, err := deps.Store.GetTask(ctx, taskID)
	if err != nil || task == nil || change.Primary(*task) == "" {
		return
	}
	res, err := change.PublishRun(ctx, *task, runID)
	if err != nil {
		log.Printf("orchestrator: run %s: publishing its CL replies: %v", runID, err)
		return
	}
	if n, reason := res.Waiting(); n > 0 {
		log.Printf("orchestrator: run %s: %d CL repl(ies) left as drafts for Publish replies: %s", runID, n, reason)
	}
}

// changelistPattern is a CL as an audit record may name it.
var changelistPattern = regexp.MustCompile(`^cl/[0-9]{1,12}$`)

// recordTaskChangelists links taskID to every CL records name as the
// task's own (protocol.AuditTaskChangelistField) that it is not linked to
// already, as model.LinkChangelist.
func recordTaskChangelists(ctx context.Context, store *model.Store, taskID string, records []model.CapabilityAuditRecord) error {
	var cls []string
	for _, r := range records {
		var rec map[string]any
		if r.Record == "" || json.Unmarshal([]byte(r.Record), &rec) != nil {
			continue
		}
		cl, _ := rec[protocol.AuditTaskChangelistField].(string)
		if changelistPattern.MatchString(cl) && !slices.Contains(cls, cl) {
			cls = append(cls, cl)
		}
	}
	if len(cls) == 0 || taskID == "" {
		return nil
	}
	return store.UpdateTask(ctx, taskID, func(t *model.Task) error {
		for _, cl := range cls {
			if !slices.Contains(t.Links, model.Link{Kind: model.LinkChangelist, Target: cl}) {
				t.Links = append(t.Links, model.Link{Kind: model.LinkChangelist, Target: cl})
			}
		}
		return nil
	})
}

// capabilityAuditRecords is audits as rows: each record at the time it
// names, then grain's own note on the grant when the server left records
// out or grain refused what it sent.
func capabilityAuditRecords(audits []protocol.GrantAudit, now time.Time) []model.CapabilityAuditRecord {
	var out []model.CapabilityAuditRecord
	for _, a := range audits {
		for _, raw := range a.Records {
			at := now
			var head struct {
				Time time.Time `json:"time"`
			}
			if json.Unmarshal(raw, &head) == nil && !head.Time.IsZero() {
				at = head.Time
			}
			out = append(out, model.CapabilityAuditRecord{Grant: a.Grant, AttemptID: a.AttemptID, At: at, Record: string(raw)})
		}
		note := func(text string) {
			out = append(out, model.CapabilityAuditRecord{Grant: a.Grant, AttemptID: a.AttemptID, At: now, Note: text})
		}
		if a.Omitted > 0 {
			note(fmt.Sprintf("the capability server left out %d further record(s)", a.Omitted))
		}
		if a.Refused != "" {
			note("grain refused this grant's audit: " + a.Refused)
		}
	}
	return out
}

// Pass is the pass runID holds, or nil.
func (h *CapabilityPasses) Pass(runID string) *protocol.Pass {
	h.mu.Lock()
	defer h.mu.Unlock()
	if held := h.passes[runID]; held != nil {
		return held.pass
	}
	return nil
}

// held is the pass runID holds and the deadline it is minted to, or nil.
func (h *CapabilityPasses) held(runID string) (*protocol.Pass, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if held := h.passes[runID]; held != nil {
		return held.pass, held.deadline
	}
	return nil, time.Time{}
}

// Extend renews runID's pass when deadline is later than the one it was
// minted for (§4.3.5): an attempt whose budget was extended past what
// admission minted for.
func (h *CapabilityPasses) Extend(ctx context.Context, runID string, deadline time.Time) error {
	h.mu.Lock()
	held := h.passes[runID]
	h.mu.Unlock()
	if held == nil || !deadline.After(held.deadline) {
		return nil
	}
	if err := held.pass.Renew(ctx, deadline); err != nil {
		return err
	}
	h.mu.Lock()
	held.deadline = deadline
	h.mu.Unlock()
	return nil
}

func (h *CapabilityPasses) waiting(taskID string, now time.Time) (*passBackoff, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.backoff[taskID]
	return b, b != nil && now.Before(b.notBefore)
}

func (h *CapabilityPasses) clearBackoff(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.backoff, taskID)
}

// transient records a transient failure of grant for taskID and returns
// how many consecutive ones that grant has now had.
func (h *CapabilityPasses) transient(taskID, grant, reason string, now time.Time) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.backoff[taskID]
	if b == nil || b.grant != grant {
		b = &passBackoff{grant: grant}
		h.backoff[taskID] = b
	}
	b.transient++
	b.reason = reason
	b.notBefore = now.Add(passBackoffBase << (b.transient - 1))
	return b.transient
}

func (h *CapabilityPasses) exhausted(taskID, grant string, retryAt, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if retryAt.IsZero() || !retryAt.After(now) {
		retryAt = now.Add(passExhaustedBackoff)
	}
	if max := now.Add(time.Hour); retryAt.After(max) {
		retryAt = max
	}
	// Not a transient failure: the streak §4.2.4 counts is broken.
	h.backoff[taskID] = &passBackoff{grant: grant, notBefore: retryAt, reason: "at capacity"}
}

// capabilityAdmitter wraps admit so that a candidate it admits whose task
// holds capability references has them minted before it is handed on to
// be started. A pass that cannot be minted gives back whatever admit
// reserved and leaves the candidate queued, backing off, or refused.
func capabilityAdmitter(deps Deps, admit dispatch.Admitter) dispatch.Admitter {
	passes := deps.capabilityPasses()
	return func(ctx context.Context, candidate dispatch.Candidate) (dispatch.Admission, error) {
		// Read before admit rather than after it, so that nothing admit
		// reserves is left outstanding by a read failing in between.
		task, err := deps.Store.GetTask(ctx, candidate.TaskID)
		if err != nil || task == nil || !model.HasCapabilityRef(task.Grants) {
			return admit(ctx, candidate)
		}
		adm, err := admit(ctx, candidate)
		if err != nil || !adm.Admitted {
			return adm, err
		}
		decline := func(gate, reason string) (dispatch.Admission, error) {
			if adm.Release != nil {
				adm.Release()
			}
			return dispatch.Admission{Gate: gate, Reason: reason}, nil
		}
		at := deps.Config.now()
		if b, wait := passes.waiting(task.ID, at); wait {
			_ = deps.Store.SetGrantWait(ctx, model.GrantWait{TaskID: task.ID, Capability: b.grant,
				Resource: b.reason, WaitingAt: at})
			return decline("capability pass "+b.grant, "backing off: "+b.reason)
		}
		deadline := capabilityDeadline(at, deps.Config.maxRunRuntime())
		p, err := mintCapabilityPass(ctx, deps, *task, candidate, at, deadline)
		if err != nil {
			if serr := admissionMintFailed(ctx, deps, passes, *task, err, at); serr != nil {
				log.Printf("orchestrator: task %s: recording its failed capability admission: %v", task.ID, serr)
			}
			return decline("capability pass", err.Error())
		}
		passes.clearBackoff(task.ID)
		passes.hold(candidate.RunID, p, deadline)
		release := adm.Release
		adm.Release = func() {
			passes.Release(context.Background(), candidate.RunID)
			if release != nil {
				release()
			}
		}
		return adm, nil
	}
}

// hasExternalCapabilityRef reports whether any grant in grants is a capability
// reference targeting an external/subprocess capability server rather than the
// in-process grain server (which opens no control- or data-plane listener).
func hasExternalCapabilityRef(grants []model.Grant) bool {
	for _, g := range grants {
		if model.IsCapabilityRef(g.Capability) && !grainserver.IsInProcessGrant(g.Capability) {
			return true
		}
	}
	return false
}

// mintCapabilityPass resolves task's references against their servers,
// records the normalized args on the task (§2.3.1), checks each against
// the capability policy (§4.2.2) and mints the pass (§4.2.3).
func mintCapabilityPass(ctx context.Context, deps Deps, task model.Task, candidate dispatch.Candidate,
	at, deadline time.Time) (*protocol.Pass, error) {

	external := hasExternalCapabilityRef(task.Grants)
	if external && deps.CapabilityServers == nil {
		return nil, &grainRefusalError{Reason: "this deployment attaches no capability servers, so it cannot grant a capability reference"}
	}
	// A sandbox that is a directory on the daemon's own host shares its
	// network namespace and can reach every capability server's control
	// plane and data-plane listener directly (§3.2.4).
	if external && isUnisolated(deps.Sandboxes) {
		return nil, &grainRefusalError{Reason: "this deployment's sandboxes are directories on grain's own host, which cannot keep a capability server's control plane out of a sandbox's reach, so it runs no attempt holding a capability grant"}
	}
	var baseLookup protocol.Lookup
	if deps.CapabilityServers != nil {
		baseLookup = deps.CapabilityServers.Lookup
	}
	lookup := grainserver.Lookup(baseLookup)
	recorded, err := model.NormalizeCapabilityGrants(task.Grants, lookup)
	if err != nil {
		return nil, &protocol.TransientError{Grant: "capabilities", Err: err}
	}
	if !sameGrantNames(recorded, task.Grants) {
		if err := deps.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
			t.Grants = recorded
			return nil
		}); err != nil {
			return nil, &protocol.TransientError{Grant: "capabilities", Err: err}
		}
	}
	grants, err := model.CapabilityGrants(recorded, lookup)
	if err != nil {
		return nil, &protocol.TransientError{Grant: "capabilities", Err: err}
	}
	cfg, err := deps.Store.GetConfig(ctx)
	if err != nil {
		return nil, &protocol.TransientError{Grant: "capabilities", Err: err}
	}
	var policy model.CapabilityPolicy
	var configured map[string]protocol.ServerConfig
	deployment := "grain"
	if cfg != nil {
		policy = cfg.CapabilityPolicy
		configured = cfg.MCPServers
		if id := strings.TrimSpace(cfg.DeploymentID); id != "" {
			deployment = id
		}
	}
	configured, _ = intree.EnsureBuiltins(configured)
	subject := model.CapabilitySubject{Repo: task.Target, Kind: model.CapabilityRunTask}
	for _, g := range grants {
		if err := policy.Permits(model.RecordedCapabilityGrant(g), subject, configured); err != nil {
			return nil, &grainRefusalError{Grant: g.Ref.String(), Reason: err.Error()}
		}
	}
	if len(task.Reviewers) > 0 {
		for i := range grants {
			if grants[i].Ref.Method == "workspace" {
				if grants[i].Args == nil {
					grants[i].Args = map[string]any{}
				}
				grants[i].Args["reviewers"] = slices.Clone(task.Reviewers)
			}
		}
	}
	minters := func(id string) (protocol.Minter, error) {
		srv, err := lookup(id)
		if err != nil {
			return nil, err
		}
		return srv, nil
	}
	return protocol.MintPass(ctx, grants, minters, protocol.PassOptions{
		Deployment: deployment,
		Task:       task.ID,
		Attempt:    candidate.Attempt,
		Deadline:   deadline,
		// A port grain's own guest transport uses can never be a
		// grant's (§5.3.1).
		ReservedSandboxPorts: reservedGuestPorts(deps.Sandboxes),
		Now:                  func() time.Time { return at },
	})
}

// guestPortReserver is a Sandboxes backend whose guests have loopback
// ports grain's own transport or guest services use.
type guestPortReserver interface {
	ReservedGuestPorts() []int
}

// reservedGuestPorts is the guest ports sandboxes reserves, as a set.
func reservedGuestPorts(sandboxes Sandboxes) map[int]bool {
	r, ok := sandboxes.(guestPortReserver)
	if !ok {
		return nil
	}
	out := map[int]bool{}
	for _, p := range r.ReservedGuestPorts() {
		out[p] = true
	}
	return out
}

// sameGrantNames compares two grant sets by capability, ignoring order:
// the store keeps a task's grants in an order of its own.
func sameGrantNames(a, b []model.Grant) bool {
	if len(a) != len(b) {
		return false
	}
	have := make(map[string]int, len(a))
	for _, g := range a {
		have[g.Capability]++
	}
	for _, g := range b {
		if have[g.Capability] == 0 {
			return false
		}
		have[g.Capability]--
	}
	return true
}

// grainRefusalError is a grant grain itself refused before asking any
// capability server: the capability policy (and its catalogue) denying
// it, or this deployment having no way to serve one. Reason is grain's
// own words, so refuseCapabilityRun says it as grain rather than
// quoting it as a server's untrusted text the way it does a
// protocol.RefusedError.
type grainRefusalError struct {
	Grant  string
	Reason string
}

func (e *grainRefusalError) Error() string {
	if e.Grant == "" {
		return "capability grants refused: " + e.Reason
	}
	return fmt.Sprintf("capability %s refused: %s", e.Grant, e.Reason)
}

// admissionMintFailed records why task's pass was not minted: at
// capacity keeps it queued until its server's retry_after (§4.2.3 item
// 3), a transient failure backs it off exponentially and refuses it on
// the third in a row of one grant (§4.2.4), and a refusal parks it with
// the reason posted on it (§4.3.4) -- grain's own, or the server's.
func admissionMintFailed(ctx context.Context, deps Deps, passes *CapabilityPasses, task model.Task, err error, at time.Time) error {
	var exhausted *protocol.ExhaustedError
	var transient *protocol.TransientError
	var refused *protocol.RefusedError
	var byGrain *grainRefusalError
	switch {
	case errors.As(err, &byGrain):
		passes.clearBackoff(task.ID)
		return refuseCapabilityRunByGrain(ctx, deps, task, byGrain.Grant, byGrain.Reason, at)
	case errors.As(err, &exhausted):
		passes.exhausted(task.ID, exhausted.Grant, exhausted.RetryAt, at)
		return deps.Store.SetGrantWait(ctx, model.GrantWait{TaskID: task.ID, Capability: exhausted.Grant,
			Resource: "at capacity", WaitingAt: at})
	case errors.As(err, &transient):
		n := passes.transient(task.ID, transient.Grant, transient.Err.Error(), at)
		if n < passTransientLimit {
			log.Printf("orchestrator: task %s: capability admission failed (%d of %d): %v", task.ID, n, passTransientLimit, err)
			return deps.Store.SetGrantWait(ctx, model.GrantWait{TaskID: task.ID, Capability: transient.Grant,
				Resource: "retrying: " + transient.Err.Error(), WaitingAt: at})
		}
		passes.clearBackoff(task.ID)
		return refuseCapabilityRun(ctx, deps, task, transient.Grant, transient.Err.Error(), at)
	case errors.As(err, &refused):
		passes.clearBackoff(task.ID)
		return refuseCapabilityRun(ctx, deps, task, refused.Grant, refused.Reason, at)
	default:
		passes.clearBackoff(task.ID)
		return refuseCapabilityRun(ctx, deps, task, "", err.Error(), at)
	}
}

// refuseCapabilityRun parks task on a comment saying its capabilities
// were refused. reason may be a capability server's own words, so it is
// attributed to that server and quoted as untrusted text rather than
// written as grain's.
func refuseCapabilityRun(ctx context.Context, deps Deps, task model.Task, grant, reason string, at time.Time) error {
	what := "its capability grants"
	source := "grain"
	if grant != "" && grant != "capabilities" {
		what = "the capability `" + grant + "`"
		source = "capability:" + model.CapabilityGrantRef(grant)
	}
	body := fmt.Sprintf("This task could not be dispatched: %s was refused, so no sandbox was built for it.\n\n"+
		"What was said, verbatim -- untrusted text from %s, not grain's own words:\n\n```\n%s\n```\n\n"+
		"Change the task's capabilities, or close whatever gap the refusal names, and reply here to have it dispatched again.",
		what, source, strings.ReplaceAll(reason, "```", "'''"))
	return parkOnCapabilityRefusal(ctx, deps, task, model.Attribution{
		Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
		OnBehalfOf: &model.Principal{Kind: model.PrincipalAutomation, ID: source},
	}, body, at)
}

// refuseCapabilityRunByGrain parks task the way refuseCapabilityRun
// does, for a refusal that is grain's own (grainRefusalError): reason is
// said as grain's, with nobody else's name on it, since no capability
// server was asked.
func refuseCapabilityRunByGrain(ctx context.Context, deps Deps, task model.Task, grant, reason string, at time.Time) error {
	what := "its capability grants"
	if grant != "" {
		what = "the capability `" + grant + "`"
	}
	body := fmt.Sprintf("This task could not be dispatched: grain refused %s, so no sandbox was built for it: %s.\n\n"+
		"Change the task's capabilities, or close whatever gap the refusal names, and reply here to have it dispatched again.",
		what, strings.TrimSuffix(reason, "."))
	return parkOnCapabilityRefusal(ctx, deps, task, model.Attribution{
		Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
	}, body, at)
}

// parkOnCapabilityRefusal posts body on task as author and parks the
// task on it.
func parkOnCapabilityRefusal(ctx context.Context, deps Deps, task model.Task, author model.Attribution, body string, at time.Time) error {
	commentID, err := deps.Store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    author,
		Body:      body,
		CreatedAt: at,
	})
	if err != nil {
		return err
	}
	if err := observeField(ctx, deps.Store, task.ID, at, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	}); err != nil {
		return err
	}
	return clearAdmissionWait(ctx, deps.Store, task.ID)
}
