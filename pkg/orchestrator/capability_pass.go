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
	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This file is admission minting for capability references
// (design/spec/capabilities/capabilities.spec.md §4.2.2-§4.2.4, §4.4.1): a task holding a
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

// TaskCapabilityServers is CapabilityServers whose run: host servers are
// started on the grain hostd each task's run is routed to: ForTask is
// what taskID's run mints through, and the name of that hostd, recorded
// on the run's capability audit (model.CapabilityAuditRecord.Hostd).
// CapabilityServers that do not implement it start every run's on
// Deps.CapabilityHostd.
type TaskCapabilityServers interface {
	ForTask(ctx context.Context, taskID string) (CapabilityServers, string, error)
}

// CapabilityHostdOf is what a run's CapabilityServers implement when its
// servers are placed on more than one grain hostd: the hostd
// capabilityID was reached on for the run, and whether it was one of
// those placed, so each capability audit record names its own server's
// hostd rather than the run's default.
type CapabilityHostdOf interface {
	HostdOf(capabilityID string) (string, bool)
}

// capabilityServersFor is the servers taskID's run mints through, and
// the hostd their run: host servers started on.
func (d Deps) capabilityServersFor(ctx context.Context, taskID string) (CapabilityServers, string, error) {
	if routed, ok := d.CapabilityServers.(TaskCapabilityServers); ok {
		return routed.ForTask(ctx, taskID)
	}
	return d.CapabilityServers, d.CapabilityHostd, nil
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
	retryMu sync.Mutex
	passes  map[string]*heldPass
	backoff map[string]*passBackoff
	// unreleased are the grants of ended runs whose release could not
	// reach their server -- needs-config or failed -- by run id, kept to
	// be released once it attaches again (RetryReleases) rather than
	// left to the server's reaper.
	unreleased map[string]*heldPass
	// now is the clock unreleased grants' deadlines are read against;
	// nil is time.Now.
	now func() time.Time
}

type heldPass struct {
	taskID   string
	pass     *protocol.Pass
	deadline time.Time
	// servers are what the pass was minted through, and hostd the grain
	// hostd their run: host servers started on.
	servers        CapabilityServers
	hostd          string
	revokeApproval func()
}

type passBackoff struct {
	notBefore time.Time
	grant     string
	transient int
	reason    string
}

// NewCapabilityPasses returns an empty holder.
func NewCapabilityPasses() *CapabilityPasses {
	return &CapabilityPasses{passes: map[string]*heldPass{}, backoff: map[string]*passBackoff{}, unreleased: map[string]*heldPass{}}
}

// defaultCapabilityPasses serves a Deps that names none.
var defaultCapabilityPasses = NewCapabilityPasses()

func (d Deps) capabilityPasses() *CapabilityPasses {
	if d.CapabilityPasses != nil {
		return d.CapabilityPasses
	}
	return defaultCapabilityPasses
}

func (h *CapabilityPasses) hold(taskID, runID string, p *protocol.Pass, deadline time.Time, servers CapabilityServers, hostd string, revokeApproval func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.passes[runID] = &heldPass{taskID: taskID, pass: p, deadline: deadline, servers: servers, hostd: hostd, revokeApproval: revokeApproval}
}

// grantHostd returns the grain hostd capabilityID ran on for this pass,
// and whether it was a server placed on a hostd of its own
// (CapabilityHostdOf) rather than the deployment's registry.
func grantHostd(servers CapabilityServers, defaultHostd, capabilityID string) (string, bool) {
	if placed, ok := servers.(CapabilityHostdOf); ok {
		if name, ok := placed.HostdOf(capabilityID); ok {
			return name, true
		}
	}
	return defaultHostd, false
}

// setAuditRecordHostds stamps each audit record with the hostd its
// grant's server ran on: its own placement's (CapabilityHostdOf), or
// defaultHostd.
func setAuditRecordHostds(records []model.CapabilityAuditRecord, defaultHostd string, servers CapabilityServers) {
	for i := range records {
		id, _, _ := strings.Cut(records[i].Grant, "#")
		records[i].Hostd, _ = grantHostd(servers, defaultHostd, id)
	}
}

// holding is every live run whose pass holds a grant capabilityID
// minted (on hostd, when hostd is non-empty; on the deployment's own
// registry rather than a placed server's hostd, when hostd is empty):
// run id to its task id and those grants.
func (h *CapabilityPasses) holding(capabilityID, hostd string) map[string]heldGrants {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]heldGrants{}
	for runID, held := range h.passes {
		gh, placed := grantHostd(held.servers, held.hostd, capabilityID)
		if hostd != "" {
			if gh != hostd {
				continue
			}
		} else if placed {
			continue
		}
		for _, g := range held.pass.Grants {
			if g.Ref.CapabilityID != capabilityID {
				continue
			}
			hg := out[runID]
			hg.taskID = held.taskID
			hg.grants = append(hg.grants, g.Ref.String())
			out[runID] = hg
		}
	}
	return out
}

type heldGrants struct {
	taskID string
	grants []string
}

// NoteCapabilityServerDropped tells every live run holding a grant from
// capabilityID that the server has fallen out of use -- either down in state
// or refusing a configuration change, for reason -- after minting its grant:
// what the 2026-11-03 run had to guess at from permissions that never arrived.
// It reaches the run the way any capability notice does
// (model.CapabilityChangePrincipal). The daemon calls it from the registry's
// servers.Options.OnDrop.
func NoteCapabilityServerDropped(ctx context.Context, store *model.Store, capabilityID, state, reason string, configRefused bool) {
	noteCapabilityServerDropped(ctx, store, defaultCapabilityPasses, capabilityID, "", state, reason, configRefused, time.Now())
}

// NoteCapabilityServerDroppedOnHostd is NoteCapabilityServerDropped for a
// run: host server on one named grain hostd, telling only the live runs
// whose pass was minted there.
func NoteCapabilityServerDroppedOnHostd(ctx context.Context, store *model.Store, capabilityID, hostd, state, reason string, configRefused bool) {
	noteCapabilityServerDropped(ctx, store, defaultCapabilityPasses, capabilityID, hostd, state, reason, configRefused, time.Now())
}

func noteCapabilityServerDropped(ctx context.Context, store *model.Store, passes *CapabilityPasses, capabilityID, hostd, state, reason string, configRefused bool, now time.Time) {
	if store == nil {
		return
	}
	for runID, hg := range passes.holding(capabilityID, hostd) {
		if hg.taskID == "" {
			continue
		}
		body := capabilityServerDroppedNotice(capabilityID, hg.grants, state, reason, configRefused)
		if err := postCapabilityNotice(ctx, store, hg.taskID, body, now); err != nil {
			log.Printf("orchestrator: telling run %s its capability server %s dropped: %v", runID, capabilityID, err)
		}
	}
}

// capabilityServerDroppedNotice words the mid-run notice for a capability
// server that fell out of use after minting grants for a live run.
//
// A refused configuration change and a server drop need separate advice: when
// the server refused a new configuration, the run's existing grant was minted
// under the earlier configuration and a newly expected permission never
// reached it; when the server is down, nothing it minted can refresh or
// release until it re-attaches.
//
// reason is the server's own words (its configure or refresh error), so it is
// quoted as untrusted text rather than said as grain's (§5.5.4), the way
// refuseCapabilityRun quotes a refusal.
func capabilityServerDroppedNotice(capabilityID string, grants []string, state, reason string, configRefused bool) string {
	grantNoun := "grant"
	if len(grants) > 1 {
		grantNoun = "grants"
	}
	quoted := strings.ReplaceAll(reason, "```", "'''")
	if configRefused {
		return fmt.Sprintf("Capability server %s, which minted this run's %s %s, refused the configuration it was changed to.\n\n"+
			"What it said, verbatim -- untrusted text from the capability server, not grain's own words:\n\n```\n%s\n```\n\n"+
			"The grant this run holds was minted under the server's earlier configuration and works as it did then, "+
			"but a change made to that configuration since -- a role added to a permission set, say -- has not reached it "+
			"and will not reach this run. If a permission you were told to expect is missing, this is why: "+
			"say so in what you report rather than waiting for it to appear.",
			capabilityID, grantNoun, strings.Join(grants, ", "), quoted)
	}
	what := "is down"
	if state != "" {
		what = fmt.Sprintf("is down (%s)", state)
	}
	return fmt.Sprintf("Capability server %s, which minted this run's %s %s, %s.\n\n"+
		"What it said, verbatim -- untrusted text from the capability server, not grain's own words:\n\n```\n%s\n```\n\n"+
		"The server is down: grants it minted cannot refresh or release until it re-attaches, "+
		"and any token refresh or data-plane call to it will fail meanwhile. "+
		"Static material already placed in this sandbox stays where it is, so carry on if your work does not need the server live; "+
		"if a credential stops refreshing or a call through the grant fails, this is why: "+
		"say so in what you report rather than retrying until your deadline.",
		capabilityID, grantNoun, strings.Join(grants, ", "), what, quoted)
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
	audits, _, _ := h.release(ctx, runID)
	return audits
}

// release is Release, with the hostd the pass's run: host servers
// started on.
func (h *CapabilityPasses) release(ctx context.Context, runID string) ([]protocol.GrantAudit, string, CapabilityServers) {
	held := h.take(runID)
	if held == nil {
		return nil, "", nil
	}
	if held.revokeApproval != nil {
		held.revokeApproval()
	}
	audits, rest, err := held.pass.ReleaseReachable(ctx)
	if err != nil {
		log.Printf("orchestrator: run %s: releasing its capability grants: %v", runID, err)
	}
	h.keepUnreleased(runID, held, rest, h.clock())
	return audits, held.hostd, held.servers
}

func (h *CapabilityPasses) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// keepUnreleased keeps rest, the grants of runID's pass that release
// could not reach, for RetryReleases -- or, once the pass's deadline has
// passed and the server's own reaper has them, says so and drops them.
func (h *CapabilityPasses) keepUnreleased(runID string, held *heldPass, rest *protocol.Pass, now time.Time) {
	if rest == nil {
		return
	}
	grants := make([]string, 0, len(rest.Grants))
	for _, g := range rest.Grants {
		grants = append(grants, fmt.Sprintf("%s (%s)", g.Ref, g.AttemptID))
	}
	if !held.deadline.IsZero() && !now.Before(held.deadline) {
		log.Printf("orchestrator: run %s: grants %s were never released: their server is not attached and their lease has run out, so they wait on its reaper",
			runID, strings.Join(grants, ", "))
		return
	}
	log.Printf("orchestrator: run %s: grants %s could not be released while their server is not attached: kept, to be released when it attaches again (else by its reaper after %s)",
		runID, strings.Join(grants, ", "), held.deadline.UTC().Format(time.RFC3339))
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unreleased[runID] = &heldPass{taskID: held.taskID, pass: rest, deadline: held.deadline, servers: held.servers, hostd: held.hostd}
}

// RetryReleases releases again every pass kept because its server was
// not attached when its run ended (keepUnreleased) -- called when a
// capability server attaches -- and returns, by run id, the audits the
// releases that now went through returned. Grants still unreachable are
// kept for the next call, until their deadline hands them to the reaper.
func (h *CapabilityPasses) RetryReleases(ctx context.Context) map[string]RetriedRelease {
	h.retryMu.Lock()
	defer h.retryMu.Unlock()
	h.mu.Lock()
	pending := h.unreleased
	h.unreleased = map[string]*heldPass{}
	h.mu.Unlock()
	out := map[string]RetriedRelease{}
	for runID, held := range pending {
		audits, rest, err := held.pass.ReleaseReachable(ctx)
		if err != nil {
			log.Printf("orchestrator: run %s: releasing its kept capability grants again: %v", runID, err)
		}
		released := len(held.pass.Grants)
		if rest != nil {
			released -= len(rest.Grants)
		}
		if released > 0 {
			log.Printf("orchestrator: run %s: released %d kept capability grant(s) now that their server is attached", runID, released)
		}
		h.keepUnreleased(runID, held, rest, h.clock())
		if len(audits) > 0 {
			out[runID] = RetriedRelease{TaskID: held.taskID, Audits: audits, Hostd: held.hostd, Servers: held.servers}
		}
	}
	return out
}

// RetriedRelease is what one run's kept grants returned when
// RetryReleases released them.
type RetriedRelease struct {
	TaskID  string
	Audits  []protocol.GrantAudit
	Hostd   string
	Servers CapabilityServers
}

// RetryCapabilityReleases is RetryReleases on the passes of a daemon
// whose Deps name no holder of their own, recording each run's audit the
// way releaseRunCapabilities does. The daemon calls it whenever a
// capability server attaches.
func RetryCapabilityReleases(ctx context.Context, store *model.Store) {
	for runID, r := range defaultCapabilityPasses.RetryReleases(ctx) {
		records := capabilityAuditRecords(r.Audits, time.Now())
		if len(records) == 0 || store == nil {
			continue
		}
		setAuditRecordHostds(records, r.Hostd, r.Servers)
		if err := store.AppendRunCapabilityAudit(ctx, runID, records); err != nil {
			log.Printf("orchestrator: run %s: recording its capability audit: %v", runID, err)
		}
		if err := recordTaskChangelists(ctx, store, r.TaskID, records); err != nil {
			log.Printf("orchestrator: run %s: linking task %s to its CLs: %v", runID, r.TaskID, err)
		}
	}
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
	audits, hostd, servers := deps.capabilityPasses().release(ctx, runID)
	records := capabilityAuditRecords(audits, deps.Config.now())
	if len(records) == 0 || deps.Store == nil {
		return
	}
	setAuditRecordHostds(records, hostd, servers)
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

// heldDeps is deps with the servers runID's pass was minted through, so
// its ports and endpoints are those of the servers that granted them.
func (h *CapabilityPasses) heldDeps(runID string, deps Deps) Deps {
	h.mu.Lock()
	defer h.mu.Unlock()
	if held := h.passes[runID]; held != nil {
		deps.CapabilityServers = held.servers
	}
	return deps
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
	return capabilityAdmitterFor(deps, admit, nil)
}

// capabilityAdmitterFor is capabilityAdmitter told, by connectionOf, the
// connection each admitted run's own agent spends (the framework admit
// reserved for it, as RunDispatch reads CredentialConnection). The pass is
// minted with it in its context (model.WithRunConnection), so an
// in-process method spending a model credential on the run's behalf --
// model-proxy -- forwards with the run's own team connection exactly as
// the retired in-process <vendor>-proxy providers did. A nil connectionOf
// is a run on no connection.
func capabilityAdmitterFor(deps Deps, admit dispatch.Admitter, connectionOf func(runID string) string) dispatch.Admitter {
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
		// The servers this task's run mints through: run: host ones on
		// the hostd its run is routed to. One that cannot be reached now
		// leaves the candidate queued, as its framework's route does.
		servers, hostd, err := deps.capabilityServersFor(ctx, task.ID)
		if err != nil {
			return decline("capability pass", err.Error())
		}
		mintDeps := deps
		mintDeps.CapabilityServers = servers
		deadline := capabilityDeadline(at, deps.Config.maxRunRuntime())
		mintCtx := model.WithMintRunID(ctx, candidate.RunID)
		if deps.Config.Credentials != nil {
			mintCtx = model.WithMintCredentials(mintCtx, deps.Config.Credentials)
		}
		if connectionOf != nil {
			mintCtx = model.WithRunConnection(mintCtx, connectionOf(candidate.RunID))
		}
		var revokeApproval func()
		if deps.ApprovalCallback != nil {
			cb, revoke, err := deps.ApprovalCallback(task.ID, deadline.Add(protocol.DefaultGrace))
			if err != nil {
				return decline("capability pass", err.Error())
			}
			revokeApproval = revoke
			mintCtx = protocol.WithApprovalCallback(mintCtx, cb)
		}
		p, err := mintCapabilityPass(mintCtx, mintDeps, *task, candidate, at, deadline)
		if err != nil {
			if revokeApproval != nil {
				revokeApproval()
			}
			var hold *admissionHoldError
			if errors.As(err, &hold) {
				passes.clearBackoff(task.ID)
				return decline(hold.gate, hold.reason)
			}
			if serr := admissionMintFailed(ctx, deps, passes, *task, err, at); serr != nil {
				log.Printf("orchestrator: task %s: recording its failed capability admission: %v", task.ID, serr)
			}
			return decline("capability pass", err.Error())
		}
		passes.clearBackoff(task.ID)
		passes.hold(candidate.TaskID, candidate.RunID, p, deadline, servers, hostd, revokeApproval)
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
// reference targeting an external/subprocess capability server rather than an
// in-process server or the stateless embedded bootstrap playbooks (neither of
// which opens a control- or data-plane listener or holds host secrets).
func hasExternalCapabilityRef(grants []model.Grant) bool {
	for _, g := range grants {
		if model.IsCapabilityRef(g.Capability) && !grainserver.IsInProcessGrant(g.Capability) && !model.IsEmbeddedBootstrapOnlyGrant(g.Capability) {
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

	// Repository access (repo#read, repo#write {repo}) is a CI job's,
	// and a task is not an event its gate could admit
	// (model.IsRepoAccessGrant): refused, as ResolveGrants refuses it,
	// before anything is minted.
	for _, g := range task.Grants {
		if model.IsRepoAccessGrant(g.Capability) {
			return nil, &grainRefusalError{Reason: model.CIOnlyRefusal(g.Capability)}
		}
		if rule, ok := model.CIGrantRuleFor(g.Capability); ok && rule.IsCIOnly() {
			return nil, &grainRefusalError{Reason: model.CIGrantRuleCIOnlyRefusal(g.Capability, rule)}
		}
	}
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
	// So can a container left unfenced on this host's own bridge
	// (ContainerConfig.Fence): refused the same way, saying why it is
	// unfenced.
	unfenced := openNetworkUnfenced(ctx, deps.Sandboxes)
	if external {
		if unfenced != "" {
			return nil, &grainRefusalError{Reason: unfenced + ", so it runs no attempt holding a capability grant"}
		}
	}
	var baseLookup protocol.Lookup
	// On an unisolated host sandbox or an unfenced container, never dial an
	// attached capability server even for an embedded-bootstrap-only grant:
	// grainserver.Lookup(nil) serves in-process grants and this binary's
	// embedded bootstrap playbooks directly, with no external control-plane
	// listener or host skill directory (§3.2.4).
	if deps.CapabilityServers != nil && !isUnisolated(deps.Sandboxes) && unfenced == "" {
		baseLookup = deps.CapabilityServers.Lookup
	}
	lookup := grainserver.Lookup(baseLookup)
	recorded, err := model.NormalizeCapabilityGrants(task.Grants, lookup)
	if err != nil {
		return nil, &protocol.TransientError{Grant: "capabilities", Err: err}
	}
	rewroteGrants := !sameGrantNames(recorded, task.Grants)
	if rewroteGrants {
		if err := deps.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
			t.Grants = recorded
			return nil
		}); err != nil {
			return nil, &protocol.TransientError{Grant: "capabilities", Err: err}
		}
		task.Grants = recorded
		if deps.Policy != nil || isUnisolated(deps.Sandboxes) {
			if err := checkPolicy(ctx, deps, task, deploymentNetworkAccess(ctx, deps.Store)); err != nil {
				if serr := recordPolicyRefusal(ctx, deps, task, policyStageAdmission, err, at); serr != nil {
					log.Printf("orchestrator: recording why local policy refuses %s after grant normalization: %v", task.ID, serr)
				}
				return nil, &admissionHoldError{gate: admissionGatePolicy, reason: err.Error()}
			}
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
	if rewroteGrants && (policy.Catalogue != nil || model.CatalogueKindOf(task) == model.CatalogueKindDirector) {
		gc := model.TaskGrantContext(task)
		if gc.TemplateID != "" {
			if tmpl, err := deps.Store.GetTemplate(ctx, gc.TemplateID); err == nil && tmpl != nil {
				gc.TemplateName = tmpl.Name
			}
		}
		if _, err := policy.Catalogue.ValidateGrants(gc); err != nil {
			if serr := deps.Store.SetGrantWait(ctx, model.GrantWait{
				TaskID: task.ID, Capability: model.CatalogueWaitCapability, Resource: err.Error(), WaitingAt: at,
			}); serr != nil {
				log.Printf("orchestrator: recording why the capability catalogue holds %s after grant normalization: %v", task.ID, serr)
			}
			return nil, &admissionHoldError{gate: "capability catalogue", reason: err.Error()}
		}
	}
	configured, _ = intree.EnsureBuiltins(configured)
	subject := model.CapabilitySubject{Repo: task.Target, Kind: model.CapabilityRunTask}
	for _, g := range grants {
		if err := policy.Permits(model.RecordedCapabilityGrant(g), subject, configured); err != nil {
			return nil, &grainRefusalError{Grant: g.Ref.String(), Reason: err.Error()}
		}
	}
	// What a local-scripts declaration became on host-script stays withheld
	// from scheduled runs, whose prompt can come from an untrusted source.
	if task.Origin.Reason == model.ReasonSchedule {
		const why = "a scheduled run cannot verify the prompt it runs on"
		kept := grants[:0]
		for _, g := range grants {
			if from, ok := localscripts.MigratedFrom(model.RecordedCapabilityGrant(g)); ok {
				log.Printf("orchestrator: task %s: not granting %s (from %s): %s", task.ID, model.RecordedCapabilityGrant(g), from, why)
				continue
			}
			kept = append(kept, g)
		}
		grants = kept
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
	pass, err := protocol.MintPass(ctx, grants, minters, protocol.PassOptions{
		Deployment: deployment,
		Task:       task.ID,
		Attempt:    candidate.Attempt,
		Deadline:   deadline,
		// A port grain's own guest transport uses can never be a
		// grant's (§5.3.1).
		ReservedSandboxPorts: reservedGuestPorts(deps.Sandboxes),
		Now:                  func() time.Time { return at },
	})
	if err != nil {
		return nil, err
	}
	if (isUnisolated(deps.Sandboxes) || unfenced != "") && !onlyInProcessOrEmbeddedBootstrap(pass) {
		_, _ = pass.Release(ctx)
		return nil, &grainRefusalError{Reason: "this sandbox backend cannot fence capability control planes, so it runs no attempt whose grant carries anything beyond in-process grants and the embedded bootstrap playbooks"}
	}
	return pass, nil
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

// admissionHoldError indicates that grant normalization revealed a local-policy
// or capability-catalogue refusal, which holds the task on its wait record
// rather than parking it on a server refusal comment.
type admissionHoldError struct {
	gate   string
	reason string
}

func (e *admissionHoldError) Error() string { return e.reason }

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
