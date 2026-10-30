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

// Package model is the grain task model: the entities, their derivations,
// and a store for them.
//
// docs/data-model.md is the reasoning; this is that document's decided
// shape in code. Four of its decisions are load-bearing here, and each is
// why something is absent rather than present:
//
//   - TaskState is not a field. It is derived from approval plus grain's
//     own observations, so Task has no State and no StateSince. StateOf
//     computes it, and schema.go computes the same thing as a SQL view —
//     deliberately twice, with state_test.go holding the two to agreeing.
//   - Approval is an Attribution, not a bool, so "who approved this, and
//     did they do it directly?" is answerable from the task rather than
//     only from an audit log.
//   - Landing state is decided by the creating actor, never the reason —
//     see LandsQueued. A new OriginReason therefore cannot queue itself.
//   - Blocked is not a state. It is derived from links, and a blocked
//     task is still queued.
//
// Nothing in this file imports a database. SQLite is one representation
// of these types and GitHub labels are another; neither belongs in the
// types.
package model

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// --- principals ------------------------------------------------------

// PrincipalKind is who acted. GitHub knows one entity, the user; grain
// has three, and collapsing them is what forces the trust gate to
// authenticate by looking for a signature substring in a comment body.
type PrincipalKind string

const (
	PrincipalAutomation PrincipalKind = "automation" // the controller loop
	PrincipalAgent      PrincipalKind = "agent"      // one dispatched run
	PrincipalHuman      PrincipalKind = "human"      // a person
)

// Valid reports whether k is one of the three kinds above.
func (k PrincipalKind) Valid() bool {
	switch k {
	case PrincipalAutomation, PrincipalAgent, PrincipalHuman:
		return true
	}
	return false
}

// Principal names an actor. ID is a GitHub login for a human, a Run.ID
// for an agent, the deployment name for automation — opaque here on
// purpose, since this model records which principal acted and never
// resolves one to an account.
type Principal struct {
	Kind PrincipalKind
	ID   string
}

// Attribution is who performed an action and whose output they relayed.
//
// OnBehalfOf is what a signature marker gestures at with one bit. Grain
// relaying an agent's question is (automation, on behalf of agent); grain
// restarting a task because a human commented is (automation, on behalf
// of human); a human acting directly is (human, nil).
type Attribution struct {
	Actor      Principal
	OnBehalfOf *Principal
}

// --- origin ----------------------------------------------------------

// OriginReason is why a task was created -- the half of Origin that is
// never consulted to decide whether it lands queued (LandsQueued).
type OriginReason string

const (
	ReasonDirect        OriginReason = "direct"        // somebody just filed it
	ReasonSchedule      OriginReason = "schedule"      // a scheduled job fired
	ReasonFix           OriginReason = "fix"           // filed for a broken PR
	ReasonReview        OriginReason = "review"        // a review of another task's own work
	ReasonProposal      OriginReason = "proposal"      // an agent or parent proposed it
	ReasonQualification OriginReason = "qualification" // a release candidate's qualification plan fired it
	ReasonSuite         OriginReason = "suite"         // a suite run fired it (bwsalmon/agents#642)
	// ReasonUpgradeCheck is grain checking an upgrade of itself: the task
	// orchestrator files, once, after an in-place upgrade has installed a
	// new build and restarted the deployment, to have an agent go and
	// look at whether what came back up actually works
	// (orchestrator.reconcileUpgradeCheck).
	//
	// Its own reason rather than ReasonSchedule's or ReasonDirect's, for
	// the reason this enum exists at all: "grain upgraded itself and
	// wants that checked" is a different thing to have happened than
	// "a cadence came due", and a person reading the task's origin is
	// entitled to be told which.
	ReasonUpgradeCheck OriginReason = "upgrade-check"
	// ReasonConsoleSession is the task holding one of this deployment's
	// standing interactive sessions -- the director, and whatever else
	// Config.ConsoleSessions names (ConsoleSession, grain/task-415).
	//
	// Its own reason for ReasonUpgradeCheck's reason, and with more to
	// tell apart than most: this task was not filed for a piece of work
	// at all. It is a conversation grain keeps open, it is never
	// finished, and the rules a reader would otherwise apply to it --
	// that a completed task is done, that a task nobody approved is
	// waiting for somebody -- are exactly the ones
	// orchestrator.reconcileConsoleSessions exempts it from.
	ReasonConsoleSession OriginReason = "console-session"
	// ReasonImported is an open GitHub issue the periodic issue importer
	// turned into a task (orchestrator.reconcileIssueImports, configured
	// per repository by RepoConfig.IssueImport). The task names the issue
	// it came from in Issue and WorkOrigin, which is also what keeps the
	// importer from filing the same issue twice.
	//
	// Its own reason for ReasonUpgradeCheck's reason: "somebody opened an
	// issue on GitHub and grain picked it up" is not "somebody filed this
	// here", and the person deciding whether to approve it is entitled to
	// be told that the words in it came from outside.
	ReasonImported OriginReason = "imported"
)

// Origin is two orthogonal facts kept together: who created a task, and
// why. An earlier design fused them into one enum, which hid that two of
// its cases were identical in who and differed only in why.
type Origin struct {
	Attribution Attribution
	Reason      OriginReason
}

// LandsQueued reports whether a task with this origin starts approved.
//
// The actor decides; the reason does not. An OriginReason added later
// cannot queue itself, because reasons are never consulted — the
// trust gate as code rather than as a convention each filing path
// remembers.
func LandsQueued(o Origin) bool {
	return o.Attribution.Actor.Kind == PrincipalHuman
}

// --- repos and folders -----------------------------------------------

// RepoRef names one repository, as owner/name, or host/owner/name for a
// repository on a git host other than the deployment's default
// (ParseRepo, docs/git-hosts.md).
type RepoRef struct {
	Owner string
	Name  string
	// Host is the git host the repository is on, normalized
	// (NormalizeGitHost), and empty for the deployment's default host
	// (DefaultRepoHost) -- which is every repository grain knew before
	// hosts existed, so their RepoRefs, and every form they are written
	// down in, are unchanged.
	Host string
}

// String is r's one written form: owner/name, or host/owner/name off the
// default host.
func (r RepoRef) String() string {
	if host := r.OffDefaultHost(); host != "" {
		return host + "/" + r.Owner + "/" + r.Name
	}
	return r.Owner + "/" + r.Name
}

// OffDefaultHost is r's host when it is not the deployment's default, and
// empty when it is -- the one spelling of "which host" that compares
// equal for two RepoRefs on one host however theirs was written.
func (r RepoRef) OffDefaultHost() string {
	host, err := canonicalRepoHost(r.Host)
	if err != nil {
		// Not a host at all: kept as written rather than silently read
		// as the default host, so nothing routes it there.
		return r.Host
	}
	return host
}

// GitHost is the host r is on, resolved: r.Host normalized, or the
// deployment's default host.
func (r RepoRef) GitHost() string {
	if host := r.OffDefaultHost(); host != "" {
		return host
	}
	return DefaultRepoHost()
}

// StoredOwner is what r's owner column holds: Owner, or host/Owner for a
// repository off the default host. An owner can never contain a slash
// (validRepoSegment), so RepoFromStored splits it back unambiguously,
// and every (owner, name) column and key grain already has carries the
// host without a schema change -- while a default-host repository's row
// is byte for byte what it always was.
func (r RepoRef) StoredOwner() string {
	if host := r.OffDefaultHost(); host != "" {
		return host + "/" + r.Owner
	}
	return r.Owner
}

// RepoFromStored is the RepoRef an (owner, name) column pair written by
// StoredOwner holds.
func RepoFromStored(owner, name string) RepoRef {
	if host, o, ok := strings.Cut(owner, "/"); ok {
		return RepoRef{Host: host, Owner: o, Name: name}
	}
	return RepoRef{Owner: owner, Name: name}
}

// Same reports whether r and other name the same repository, comparing
// owner and name case-insensitively -- the way GitHub itself resolves
// them, where owner/name is one repository however whoever typed it
// capitalised it -- and the hosts as hosts (an empty one being the
// default).
//
// This is the comparison; it is not a key. Everything grain *stores* a
// repo in -- repo_config and qualification_config's (owner, name)
// primary keys, the task rows a repos page groups by, the target repo
// allowlist -- matches exactly, so two spellings of one repository
// there are two repositories. Keeping that from happening is a matter of
// spelling a repo the same way every time it is written down
// (ui.(*Client).parseRepo), not of teaching every one of those keys to
// fold case.
func (r RepoRef) Same(other RepoRef) bool {
	return strings.EqualFold(r.Owner, other.Owner) && strings.EqualFold(r.Name, other.Name) &&
		r.OffDefaultHost() == other.OffDefaultHost()
}

// SameRepoText reports whether two written repositories name the same
// one: parsed and compared with Same where both parse (so
// git@host:owner/name, https://host/owner/name.git and host/owner/name
// are one repository however they are cased), and compared as text,
// case-insensitively, where either does not.
func SameRepoText(a, b string) bool {
	ra, errA := ParseRepo(a)
	rb, errB := ParseRepo(b)
	if errA != nil || errB != nil {
		return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	return ra.Same(rb)
}

// ParseRepo reads the written form of a repository: owner/name, or
// host/owner/name for one on a git host other than the default. A first
// segment containing a dot or a colon is a host -- GitHub owners can
// contain neither.
//
// It also accepts the two shapes an operator most often has in the
// clipboard instead -- a browser URL (https://github.com/owner/name,
// with or without a trailing .git or /) and an SSH remote
// (git@github.com:owner/name.git) -- and keeps the host either names,
// unless it is the deployment's default host (DefaultRepoHost), which a
// RepoRef never spells out. The alternative was worse than either
// accepting or rejecting them: a bare strings.Cut on the first "/" took
// "https://github.com/owner/name" as owner "https:", name
// "/github.com/owner/name", stored it, and left the task to fail much
// later at clone time with an error naming neither the paste nor the
// field it went into.
//
// Everything else is refused here rather than downstream. What comes out
// of this function is used to build clone URLs, GitHub API paths and
// branch refs, so a segment carrying a slash, a space, a colon or a
// control character is not a repository grain can act on however it was
// typed.
func ParseRepo(text string) (RepoRef, error) {
	trimmed := strings.TrimSpace(text)
	spec := trimmed
	host := ""
	hostGiven := false
	// scp-style SSH remotes: everything up to the colon is the host.
	if at := strings.Index(spec, "@"); at >= 0 && !strings.Contains(spec, "://") {
		if colon := strings.Index(spec[at:], ":"); colon >= 0 {
			host, hostGiven = spec[at+1:at+colon], true
			spec = spec[at+colon+1:]
		}
	}
	if scheme := strings.Index(spec, "://"); scheme >= 0 {
		spec = spec[scheme+len("://"):]
		// The host (and any userinfo folded into it) is everything up
		// to the first slash.
		authority := spec
		if slash := strings.Index(spec, "/"); slash >= 0 {
			authority, spec = spec[:slash], spec[slash+1:]
		} else {
			spec = ""
		}
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			authority = authority[at+1:]
		}
		host, hostGiven = authority, true
	}
	spec = strings.TrimSuffix(strings.TrimSuffix(strings.Trim(spec, "/"), ".git"), "/")

	if !hostGiven {
		if first, rest, ok := strings.Cut(spec, "/"); ok && strings.ContainsAny(first, ".:") {
			host, hostGiven, spec = first, true, rest
		}
	}
	owner, name, ok := strings.Cut(spec, "/")
	if !ok || !validRepoSegment(owner) || !validRepoSegment(name) {
		return RepoRef{}, fmt.Errorf("repo must be owner/name, got %q (one on another git host is host/owner/name)", trimmed)
	}
	ref := RepoRef{Owner: owner, Name: name}
	if hostGiven {
		h, err := canonicalRepoHost(host)
		if err != nil {
			return RepoRef{}, fmt.Errorf("repo %q names a git host that is not a host name", trimmed)
		}
		ref.Host = h
	}
	return ref, nil
}

// validRepoSegment reports whether one half of an owner/name pair is a
// segment grain can safely put in a URL or a ref. GitHub is stricter
// than this (its own owner names are alphanumerics and hyphens); the
// point here is not to re-implement its rules but to refuse anything
// that would change the *shape* of a URL built from it, plus the two
// path segments that would escape it.
func validRepoSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '?', '#', '@', '[', ']', '%', '~', '^':
			return false
		}
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// WriteTarget is one more repository a task is allowed to land work in,
// beside Task.Target, together with the branch that work is based on
// there -- Task.Writes' own element, and the whole of what "a task with
// several writeable repos" means here.
//
// A run still pushes to exactly one repository. Its sandbox is one
// checkout of Task.Target, its branch is Task.BranchName() in
// that repository, and the git proxy still refuses a push anywhere else
// (gitproxy/authorize.go): one run, one branch, one pull request. What a
// WriteTarget adds is the *permission* to have work done in another
// repository at all, spent by proposing a task for it --
// orchestrator.relayProposedTasks files a propose_task call naming this
// Repo against it, with this Base, and BuildPrompt tells the run that is
// how a change over there gets made. That keeps the second repository's
// change reviewable as its own pull request against its own base,
// instead of one run holding two checkouts and two branches whose pull
// requests would have to land in an order nothing here could express.
//
// Base is per repository for the reason a task has a Base at all: the
// branch a change belongs on is a fact about the repository it is going
// into, and a deployment that lands grain's own work on `main` and its
// state repository's on `deploy` cannot say so with one field. "" means
// that repository's default branch, exactly as Task.Base's own "" does.
type WriteTarget struct {
	Repo RepoRef
	Base string
}

// WritesBesides is writes with any entry naming target on base dropped -- what a
// caller filing a task from reusable content (a Template's Writes, a
// Schedule's) hands to Task.Writes, so the filed task never lists the
// repository and base branch it is filed against as somewhere to propose work for.
//
// ui.refuseTargetAmongWrites refuses that combination outright where
// somebody typed both halves in one form, and can: a task's target and
// its extra write targets are chosen together. Template content is not
// like that. A template is usually unbound, so which repo its firings
// target is decided by whoever fires it -- a schedule's own repo, a
// suite run's, a qualification candidate's -- and "acme/docs is also
// writeable" only collides with it for some of those callers, long
// after the template was saved. Refusing at that point would mean a
// firing nobody is watching failing over a declaration that was
// perfectly sensible when it was made, so the redundant half is dropped
// instead: the target is already the one repo the run pushes to, which
// is strictly more than a WriteTarget grants.
func WritesBesides(writes []WriteTarget, target RepoRef, base string) []WriteTarget {
	out := make([]WriteTarget, 0, len(writes))
	for _, w := range writes {
		if w.Repo.Same(target) && w.Base == base {
			continue
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// WriteTargets is every repository a task may have work landed in, with
// the base branch for each: Target/Base first, when it has one, then
// Writes in whatever order they are in (the store hands them back sorted
// by repo, the same as Reads -- nothing here depends on the order they
// were typed in).
//
// The one place the two kinds are treated alike. Everything else keeps
// them apart on purpose -- the target is where *this* run pushes, and a
// Writes entry is where a proposal it makes may be filed -- but "which
// repositories is this task allowed to change, and on what branch" is a
// question with one answer, and both the prompt and proposal filing ask
// it that way.
func WriteTargets(t Task) []WriteTarget {
	out := make([]WriteTarget, 0, len(t.Writes)+1)
	if t.Target != nil {
		out = append(out, WriteTarget{Repo: *t.Target, Base: t.Base})
	}
	return append(out, t.Writes...)
}

// WriteTargetFor is the WriteTargets entry naming repo, and false for a
// repository this task may not have work landed in at all. Repos compare
// the way RepoRef.Same compares them, so a proposal that spelled the
// owner in another case still finds the entry a human declared.
func WriteTargetFor(t Task, repo RepoRef) (WriteTarget, bool) {
	for _, w := range WriteTargets(t) {
		if w.Repo.Same(repo) {
			return w, true
		}
	}
	return WriteTarget{}, false
}

// RepoBinding is how a task came by its target repository.
type RepoBinding string

const (
	BindingDirective RepoBinding = "directive" // named explicitly; pinned
)

// FolderRef is a node in the containment tree, as a path from the root.
// Around (a group of repos) and inside (a path within one) are the same
// kind of node, which is why one type covers both.
type FolderRef struct {
	Path []string
}

// String is f's path joined with "/".
func (f FolderRef) String() string { return strings.Join(f.Path, "/") }

// ParseFolder reads FolderRef.String back, nil for the empty string.
func ParseFolder(text string) *FolderRef {
	if text == "" {
		return nil
	}
	return &FolderRef{Path: strings.Split(text, "/")}
}

// --- capabilities ----------------------------------------------------

// GrantSource is how a task came by one of its grants (Grant.Via) --
// provenance only, never what the grant lets it do.
type GrantSource string

const (
	GrantByLabel     GrantSource = "label"     // a human applied it
	GrantByDirective GrantSource = "directive" // a trusted author wrote it
	GrantByFolder    GrantSource = "folder"    // a folder's offers granted it
	GrantByGrain     GrantSource = "grain"     // grain applied it to itself
	// GrantByDefault records a grant a task was filed with because it was
	// listed as a default -- by the deployment
	// (Config.DefaultCapabilities) or by the repo the task targets
	// (RepoConfig.DefaultCapabilities), unioned at creation. Nobody
	// ticked it for this task in particular; it is what filing a task
	// here, against that repo, starts out with (ui.CreateTask).
	//
	// One source for both layers, deliberately: which of the two attached
	// it is a question the two panes that own them answer, and nothing
	// about a task changes with the answer.
	//
	// It is provenance only, exactly like the four above: nothing reads
	// Via to decide what a grant lets a task do, and a default-sourced
	// grant is in every other way an ordinary one -- it sits on the task,
	// shows in its capability list, is detached through the same picker,
	// and fails the same way at ResolveGrants/MaterializeGrants if the
	// capability behind it is misconfigured. That is deliberate: a
	// deployment-wide grant applied at dispatch instead would have no
	// task to be visible on and no way to be taken off one.
	GrantByDefault GrantSource = "default"
)

// IsDeclaration reports whether via records that a grant was declared in a
// repository file (.grain/templates/..., .grain/schedules/...).
func (via GrantSource) IsDeclaration() bool {
	return strings.HasPrefix(string(via), DeclarationDir+"/")
}

// Provision is how a capability is provided to a run.
type Provision string

const (
	ProvisionMint   Provision = "mint"   // mints a lease from a credential
	ProvisionSelect Provision = "select" // names which credential to use
	ProvisionGrant  Provision = "grant"  // needs no credential at all
	ProvisionPlace  Provision = "place"  // places operator-owned secret material
)

// CredentialRef is a credential name. Never material: resolving this to a
// token happens on the controller at the moment of use, and the result
// reaches neither this model, the store, a declaration, nor a UI.
type CredentialRef struct{ Name string }

// Grant records how this task came by a capability, which a capability's
// own definition cannot say: the same capability can be label-requested
// on one task and folder-inherited on another.
type Grant struct {
	Capability string
	Via        GrantSource
	Folder     *FolderRef
}

// GrantsSubsetOf reports whether every capability in grants also appears
// in allowed, comparing by Capability name alone -- Via and Folder record
// how a grant was come by, not what it lets a task do, so two grants for
// the same capability compare equal here regardless of either.
//
// This is the guard docs/data-model.md's "sub-tasks are tasks" section
// asks for before relaxing any trust gate for a proposed child: a task
// that asks for nothing beyond what its parent already held cannot turn
// one human grant into a wider one just by being proposed.
//
// A capability reference is covered only by a reference with the same
// canonical capability and deep-equal normalized args
// (UncoveredCapabilityRefs, docs/capabilities-spec.md §2.3.4).
func GrantsSubsetOf(grants, allowed []Grant) bool {
	have := make(map[string]bool, len(allowed))
	for _, g := range allowed {
		have[g.Capability] = true
	}
	for _, g := range grants {
		if IsCapabilityRef(g.Capability) {
			continue
		}
		if !have[g.Capability] {
			return false
		}
	}
	return len(UncoveredCapabilityRefs(grants, allowed)) == 0
}

// SameBranch reports whether two tasks' work lands in the same place:
// the same write target, and the same base branch within it.
//
// It is the second half of the trust gate GrantsSubsetOf opens, for the
// same "sub-tasks are tasks" reason. Auto-merge is a human's standing
// permission for one branch to take commits nobody reviewed, not a
// property of the run that happens to hold it: a task whose pull
// requests merge themselves into a release branch has been told nothing
// at all about the default branch, so a proposal it makes that lands
// somewhere else is work a human still has to look at.
//
// Base compares as written, with "" -- the repo's default branch -- left
// unresolved, because nothing in this package can reach the remote to
// ask which branch that is. So a task that names its default branch
// explicitly reads as a different branch from one that leaves Base
// empty, and a proposal from it does not inherit auto-merge. That errs
// toward withholding a permission that would in fact have been the same
// one, which is the direction a trust gate should fail in.
//
// Owner and name compare case-insensitively, the way GitHub itself
// resolves them. Two tasks with no
// write target at all are on the same branch of the same nowhere:
// neither opens a pull request, so there is no unreviewed merge here to
// guard.
func SameBranch(a, b Task) bool {
	if a.Base != b.Base {
		return false
	}
	if a.Target == nil || b.Target == nil {
		return a.Target == nil && b.Target == nil
	}
	return a.Target.Same(*b.Target)
}

// LandsWhereGranted reports whether child lands somewhere the human who
// set parent up has already said unreviewed commits may go: SameBranch's
// question, widened by exactly one thing -- the extra write targets
// parent declares (Task.Writes).
//
// It is what lets a task with several writeable repos pass its
// auto-merge on to the proposals it makes for them. SameBranch alone
// cannot: a proposal filed against the second repository is by
// definition not on the parent's branch, so a run told "file a task for
// the other repo" would have every such task land as ordinary reviewed
// work however the parent was configured -- which would make the whole
// arrangement no better than filing them by hand.
//
// Widened by *exactly* one thing, though. A Writes entry is a human
// naming a repository and a branch in the same form they named the
// target in, and a child that lands on that repository, on that branch,
// is landing where they said; a child that lands anywhere else is not,
// and gets nothing here. So this is still the trust gate SameBranch is
// -- it is only that the permission it reads now has more than one
// branch in it. The capability half of the gate (GrantsSubsetOf) is
// unaffected and still checked separately by its caller.
//
// Base compares as written, with "" left unresolved, for SameBranch's
// own reason: nothing in this package can ask a remote which branch its
// default is, so a child that names a branch explicitly where the
// declaration left it empty reads as a different branch and inherits
// nothing. That errs toward withholding, which is the direction a trust
// gate should fail in.
//
// Where the child lands is only half of it. A child carries extra write
// targets of its own (Task.Writes), and those are permission it hands
// on to the proposals *it* makes -- so a child declaring a repository
// its parent was never given would be the parent minting itself new
// authority, one propose_task call later (#2378). Every one of the
// child's own Writes entries therefore has to be one of parent's
// WriteTargets, repo and branch alike, or the child lands nowhere
// granted.
//
// And a configuration repository (IsConfigRepo) is never reached
// through the widening at all. It holds this deployment's capability
// grants and secrets configuration, so landing unreviewed work in it is
// a permission ordinary auto-merge does not imply: only a task already
// targeting it, on the same branch -- one a human set up there
// themselves -- passes that permission on.
func LandsWhereGranted(child, parent Task) bool {
	if len(UngrantedWrites(child, parent)) > 0 {
		return false
	}
	if SameBranch(child, parent) {
		return true
	}
	if child.Target == nil || IsConfigRepo(*child.Target) {
		return false
	}
	for _, w := range WriteTargets(parent) {
		if w.Repo.Same(*child.Target) && w.Base == child.Base {
			return true
		}
	}
	return false
}

// UngrantedWrites is the entries of child's own Writes that are not
// already among parent's WriteTargets, repo and branch alike -- the
// write targets a proposal asked for that the task proposing it was
// never given. Empty when there are none.
func UngrantedWrites(child, parent Task) []WriteTarget {
	granted := WriteTargets(parent)
	var out []WriteTarget
	for _, w := range child.Writes {
		found := false
		for _, g := range granted {
			if g.Repo.Same(w.Repo) && g.Base == w.Base {
				found = true
				break
			}
		}
		if !found {
			out = append(out, w)
		}
	}
	return out
}

// UngrantedReads is the entries of child's own Reads the task proposing
// it could not already read -- not parent's target, not one of its
// Reads, not one of its WriteTargets. Empty when there are none.
//
// UngrantedWrites' question for read access. The git proxy lets a
// sandbox fetch any repository in its task's Reads (gitproxy's
// ModelAuthorizer), and nothing between a propose_task call and that
// fetch narrows the list, so a child queued with a read its parent never
// held is the parent reaching a repository nobody gave it, one
// propose_task call later -- the same minting #2378 closed for Writes.
// Only the forbidden set (configuration repos) is withheld downstream
// regardless.
func UngrantedReads(child, parent Task) []RepoRef {
	var out []RepoRef
	for _, r := range child.Reads {
		if !mayRead(parent, r) {
			out = append(out, r)
		}
	}
	return out
}

// mayRead reports whether t's sandbox may already fetch repo: its own
// target, a read-only repository, or a declared write target.
func mayRead(t Task, repo RepoRef) bool {
	for _, r := range t.Reads {
		if r.Same(repo) {
			return true
		}
	}
	for _, w := range WriteTargets(t) {
		if w.Repo.Same(repo) {
			return true
		}
	}
	return false
}

// AutoSubmitPrincipal is who grain approves a task as when nobody looked
// at it -- the task that proposed it already held a human's standing
// permission to land unreviewed work, and AutoSubmits says that
// permission reaches this one.
//
// An automation actor with no OnBehalfOf, which is the shape
// docs/data-model.md reserves for an approval made by policy: a human
// approving a task is (human, nil), grain relaying somebody's approval
// is (automation, on behalf of that human), and this is neither, because
// no person saw this task before it was queued. Distinguishing the three
// on the task itself is what keeps "who let this run?" answerable
// afterwards.
var AutoSubmitPrincipal = Principal{Kind: PrincipalAutomation, ID: "auto-submit"}

// AutoSubmits reports whether child may be filed already approved --
// queued for dispatch with no human in the loop -- on the strength of
// the permission parent holds.
//
// The permission is parent.AutoMerge -- a human saying this task's own
// commits may land without anybody reading them -- or
// parent.AutoApproveProposals, the narrower half of it: a human saying
// the work this task splits out may be queued, whether or not its own
// commits merge themselves. A task holding that is
// trusted with more than a pull request. It is trusted to decide what
// work happens on that branch at all, since anything it could get a
// person to approve it could equally have written itself and merged
// unreviewed -- so making it file the follow-up it split out as a
// proposal somebody has to click is a gate that stops nothing and costs
// a round trip through a human.
//
// Trusted with *that* branch, and with nothing beyond what it was given,
// which is why this is the same gate auto-merge inheritance passes
// through rather than a looser one. GrantsSubsetOf: a child asking for a
// capability its parent never held is asking a human for something new,
// and that is a question only a human can answer. LandsWhereGranted:
// auto-merge is a permission for one branch (or for a declared write
// target's own branch), not a property of the run holding it, so a child
// landing anywhere else is work nobody has approved the shape of.
// UngrantedReads: a child that may fetch a repository its parent could
// not is a widening of read access, which is a human's call for the same
// reason a new capability is. All of them have to hold, and a child that
// fails any is filed proposed like any other.
//
// Reads gate the queueing only, not the child's auto-merge
// (LandsWhereGranted): what a task may read says nothing about where its
// commits land, and a human approving the wider read has approved the
// rest of the proposal as asked.
//
// This says nothing about the child's own AutoMerge. The two travel
// together for the usual piece-of-the-same-task proposal, but they are
// different questions -- whether the work is queued, and whether its
// pull request merges itself -- and a run that opts a proposal out of
// the second (propose_task's auto_merge: false, separate work deserving
// a human's own review) has said nothing about the first.
func AutoSubmits(child, parent Task) bool {
	return (parent.AutoMerge || parent.AutoApproveProposals) &&
		GrantsSubsetOf(child.Grants, parent.Grants) &&
		len(UngrantedReads(child, parent)) == 0 &&
		LandsWhereGranted(child, parent)
}

// GitCredentialCapabilityPrefix marks a capability id as a per-task git
// credential override: the named credential (gitproxy's
// CredentialSet.Get) a sandbox's git proxy requests should use in place
// of the owner/repo ladder, for a scope the ladder's own credentials
// deliberately withhold (docs/design.md, "Scopes to withhold" --
// `workflow`, most notably).
//
// One id per named credential a deployment has configured beyond its
// default one, which is what makes each of those tokens a capability of
// its own -- offered by ui.GitHubTokenCapabilities, provided by
// pkg/capability/githubtoken, and attached to a task like any other.
const GitCredentialCapabilityPrefix = "github-credential:"

// GitCredentialCapability is the capability id standing for "use the
// GitHub token named name instead of this deployment's default one".
func GitCredentialCapability(name string) string {
	return GitCredentialCapabilityPrefix + name
}

// GitCredentialName is the credential a GitCredentialCapability id
// names, and false for any other capability id.
func GitCredentialName(capability string) (name string, ok bool) {
	name, ok = strings.CutPrefix(capability, GitCredentialCapabilityPrefix)
	return name, ok && name != ""
}

// GitPushAnyRefCapability is the admin-only grant that lets a task's run
// push any ref of the repositories it may write, not only its own task
// branch (pkg/capability/gitpushanyref, Store.GitPushAnyRef).
const GitPushAnyRefCapability = "git-push-any-ref"

// ReleaseGateReadCapability is the CI capability a release workflow's job
// reads CI runs, persistent sandboxes and tasks through, and files its
// release-notes task with: an alias, in a CI job's `capabilities:`, for
// the built-in api preset of the same name (api:release-gate-read,
// pkg/capability/ciapi). Like repository access and every api grant, a
// job is granted it by declaring it (ci's declaredOnly): the provider's
// own event gate and repository list are what bound it.
const ReleaseGateReadCapability = "release-gate-read"

// SelfDebugCapability is the grant that lets a task's run read grain's
// own source, tasks and metrics (pkg/capability/selfdebug). The source
// is the deployment's own baked-in copy, placed in the sandbox at
// grain-src/ (orchestrator's placeGrainSource) and read by
// read_grain_source: the grant reaches no repository through the git
// proxy, and implies none (CapabilityRepos).
const SelfDebugCapability = "self-debug"

// GrainSourceRepo is grain's own source repository. Nothing grants a
// read of it implicitly: a self-debug run is given the deployment's
// baked-in copy instead, and a task that wants the repository itself
// names it in its reads or a repo-read: grant like any other.
func GrainSourceRepo() RepoRef { return RepoRef{Owner: "ai-on-gke", Name: "grain"} }

// RepoReadCapabilityPrefix marks a capability id as a cross-repository
// read grant for CI.
const RepoReadCapabilityPrefix = "repo-read:"

// RepoReadCapability is the capability id standing for "grant CI read
// access to repo".
func RepoReadCapability(repo RepoRef) string {
	return RepoReadCapabilityPrefix + repo.String()
}

// RepoReadTarget returns the repository a RepoReadCapability id names,
// and false for any other capability id.
func RepoReadTarget(capability string) (RepoRef, bool) {
	name, ok := strings.CutPrefix(capability, RepoReadCapabilityPrefix)
	if !ok || name == "" {
		return RepoRef{}, false
	}
	repo, err := ParseRepo(name)
	if err != nil {
		return RepoRef{}, false
	}
	return repo, true
}

// CapabilityRepos returns the repositories a grant of capability lets a
// run reach beyond its task's own Target, Writes and Reads: the
// repository a RepoReadCapability or RepoWriteCapability names. Nil for
// any other capability -- SelfDebugCapability included, whose grain-src/
// is the deployment's baked-in copy rather than a clone, so a local
// policy never refuses a self-debug task over bwsalmon/grain. Anything
// that judges a task by the repositories it touches -- a local policy's
// repos -- judges these too.
func CapabilityRepos(capability string) []RepoRef {
	if repo, ok := RepoReadTarget(capability); ok {
		return []RepoRef{repo}
	}
	if repo, ok := RepoWriteTarget(capability); ok {
		return []RepoRef{repo}
	}
	return nil
}

// RepoWriteCapabilityPrefix marks a capability id as a cross-repository
// write grant for CI.
const RepoWriteCapabilityPrefix = "repo-write:"

// RepoWriteCapability is the capability id standing for "grant CI write
// access to repo".
func RepoWriteCapability(repo RepoRef) string {
	return RepoWriteCapabilityPrefix + repo.String()
}

// RepoWriteTarget returns the repository a RepoWriteCapability id names,
// and false for any other capability id.
func RepoWriteTarget(capability string) (RepoRef, bool) {
	name, ok := strings.CutPrefix(capability, RepoWriteCapabilityPrefix)
	if !ok || name == "" {
		return RepoRef{}, false
	}
	repo, err := ParseRepo(name)
	if err != nil {
		return RepoRef{}, false
	}
	return repo, true
}

// GitCredentialGrant is the Grant attaching a GitCredentialCapability to
// a task. Via is GrantByLabel: it is something a human turns on
// per task, the same trust tier applying the trigger label itself relies
// on, so this opens no new gate -- the same reasoning bwsalmon/agents#52
// gave for the `grain-github-<name>` label this started as. Unlike
// grain/proxy's SandboxCredentialOverrides, it needs no storage or
// dispatch/release lifecycle of its own: it lives and dies with the task
// the same as every other Grant.
func GitCredentialGrant(name string) Grant {
	return Grant{Capability: GitCredentialCapability(name), Via: GrantByLabel}
}

// GCPKeyCapabilityPrefix marks a capability id as a named GCP key credential.
const GCPKeyCapabilityPrefix = "gcp-key:"

// GCPKeyCapability is the capability id standing for "mint a GCP key using named configuration name".
// If name is empty, it returns the default "gcp-key" capability id.
func GCPKeyCapability(name string) string {
	if name == "" || name == "default" {
		return "gcp-key"
	}
	return GCPKeyCapabilityPrefix + name
}

// GCPKeyName returns the key name a GCP key capability names, and false for any other capability.
func GCPKeyName(capability string) (name string, ok bool) {
	if capability == "gcp-key" {
		return "", true
	}
	name, ok = strings.CutPrefix(capability, GCPKeyCapabilityPrefix)
	return name, ok && name != ""
}

// GCPKeyGrant is the Grant attaching a named GCP key capability to a task.
func GCPKeyGrant(name string) Grant {
	return Grant{Capability: GCPKeyCapability(name), Via: GrantByLabel}
}

// gitCredentialOverride returns the credential name a GitCredentialGrant
// among grants asks for, if any.
//
// A task holding more than one of these gets the first in grants' own
// order -- which, coming from Store.GitCredentialOverride, is the
// capability id sorted ascending, so the same task resolves to the same
// token on every request rather than to whichever row was read first.
// Picking a winner rather than refusing is deliberate: two named tokens
// on one task is an odd thing to ask for, not a dangerous one (both are
// credentials this deployment already trusts a task with), and failing
// the run over it would be a worse answer than quietly using one of
// them.
func gitCredentialOverride(grants []Grant) (name string, ok bool) {
	for _, g := range grants {
		if n, isOverride := GitCredentialName(g.Capability); isOverride {
			return n, true
		}
	}
	return "", false
}

// Lease is something minted for a task that must be given back.
//
// MintedBy turns revocation and rotation from control flow into data:
// releasing asks the lease what to call, and "which live leases came from
// the credential I am about to rotate?" becomes a query.
type Lease struct {
	Capability string
	Resource   string
	MintedBy   CredentialRef
	IssuedAt   time.Time
	ExpiresAt  *time.Time
}

// Expired reports whether a lease is past its own expiry or past an
// unconditional backstop. The backstop exists because materialisation has
// a window that cannot be closed: a failure between minting and recording
// leaks a credential nothing knows to revoke.
func (l Lease) Expired(now time.Time, maxAge time.Duration) bool {
	if l.ExpiresAt != nil && !now.Before(*l.ExpiresAt) {
		return true
	}
	if maxAge > 0 && now.Sub(l.IssuedAt) >= maxAge {
		return true
	}
	return false
}

// --- links -----------------------------------------------------------

// LinkKind is what a Link says about the task it is on and its target.
type LinkKind string

const (
	LinkDependsOn  LinkKind = "depends-on"  // blocks dispatch
	LinkChildOf    LinkKind = "child-of"    // blocks the parent
	LinkFixes      LinkKind = "fixes"       // -> a pull request
	LinkMergeWith  LinkKind = "merge-with"  // blocks the merge, not the run
	LinkAddresses  LinkKind = "addresses"   // -> a review thread
	LinkProposedBy LinkKind = "proposed-by" // provenance only
	// LinkFixTask recorded the separate task the merge queue used to file
	// to repair this task's own PR (-> that task's ID). Nothing writes it
	// any more: a repair now runs as another attempt of the task itself,
	// on the very branch its pull request is open from, so that the
	// resolution and the pull request share one set of CI runs rather
	// than needing a second branch merged back in (Observation.
	// MergeQueueRepairAt, orchestrator.requeueForRepair).
	//
	// The kind stays defined, and the queue still merges any such task
	// still in flight, because databases written before that change carry
	// these links and a report over them (Store.TaskTimings) should go on
	// counting the repairs they record.
	LinkFixTask LinkKind = "fix-task"
	// LinkReviewTask records the task grain filed to review this one's
	// own work (-> the review task's ID), from the template
	// Task.ReviewTemplateID names. It is LinkFixTask's shape for the
	// other reason a second task is stacked on a first one's branch, and
	// it is read the same three ways: orchestrator.SyncReviews checks it
	// before filing, so exactly one review is ever filed; syncEntry
	// checks it to hold this task's own merge until the review has
	// landed; and the UI reads it as provenance. Like LinkFixTask it
	// blocks nothing -- the review task dispatches the moment it is
	// ready, and what waits is the merge, not a run.
	LinkReviewTask LinkKind = "review-task"
	// LinkSecurityHold records that grain withdrew this task's auto-merge
	// because its pull request touches a trust-boundary path
	// (pkg/trustboundary), and that a clean security review gives it back
	// (-> the trustboundary.Version the diff was classified under).
	// Present only while the hold may still be released by a review: one
	// that leaves findings removes it, so does releasing it, and a change
	// touching a path no review may release (Category.RequiresHuman) never
	// gets one (it gets LinkHumanOnlyHold instead) -- in each case what is
	// left is a task with auto-merge off, which waits for a human's Submit. Written and read by
	// orchestrator's securityhold.go; blocks nothing.
	LinkSecurityHold LinkKind = "security-hold"
	// LinkHumanOnlyHold records that grain withdrew this task's
	// auto-merge because its pull request touches a trust-boundary path
	// no review may release (Category.RequiresHuman), or a file list it
	// could not read in full (-> the trustboundary.Version the diff was
	// classified under). LinkSecurityHold's counterpart for the hold only
	// a human's Submit ends: while it is present nothing grain does on
	// its own -- a repository's gating trigger settling clean, a review's
	// questions being answered, the base's rules parsing again -- hands
	// auto-merge back. Dropped by a Submit (ui's humanSubmitted, and
	// orchestrator's pinHumanSubmit) and by a classification of a later
	// head that no longer needs it. Written and read by orchestrator's
	// securityhold.go.
	LinkHumanOnlyHold LinkKind = "human-only-hold"
	// LinkSecurityReviewTask records the security review grain filed for
	// this task's trust-boundary diff (-> the review task's ID), from
	// SecurityReviewTemplateID. LinkReviewTask's shape, kept apart from
	// it so a task can carry both its declared review and a security one.
	LinkSecurityReviewTask LinkKind = "security-review-task"
	// LinkPushesReviewedBranch marks a review task that works on the
	// reviewed change's own branch rather than one of its own (-> the
	// reviewed task's ID): its Branch is the reviewed task's, the git
	// proxy lets its run push that branch and no other, and it never opens
	// a pull request -- its fixes land in the reviewed change's own. The
	// security review is filed this way (orchestrator.putSecurityReview);
	// a review written before it, or a declared review, is not, and
	// stacks a pull request of its own on the branch instead. Blocks
	// nothing.
	LinkPushesReviewedBranch LinkKind = "pushes-reviewed-branch"
	// LinkReviewerHead records, on a review carrying
	// LinkPushesReviewedBranch, the head of the reviewed branch its run
	// left when it pushed fixes there (-> the commit sha): the commit the
	// reviewer read last, its own fixes included, and so the one a clean
	// review clears the reviewed change at. Absent on a review that pushed
	// nothing. Written by orchestrator's finish, read by
	// settleSecurityHold; blocks nothing.
	LinkReviewerHead LinkKind = "reviewer-head"
	// LinkTrustBoundaryCleared records the commit grain last read this
	// task's pull request file list at in full, and classified
	// (-> "<trustboundary.Version>@<head sha>"): one that touched no
	// trust-boundary path, one whose paths a clean security review
	// releases, or one held for a human -- a head already classified is
	// not filed a second security review (holdForTrustBoundary). It is a
	// clearance only on a change that merges itself: before merging,
	// syncEntry compares it with the head it is about to merge, and
	// commits that reached the branch outside a run (a stacked fix or
	// review merging back, a push nobody dispatched) and a list upgraded
	// since both read as a mismatch, and the change is classified again.
	// A security review's own merged fixes keep it when they account for
	// every trust-boundary change since (reviewFixClearance). Dropped when
	// a review's findings end a releasable hold. Written and read by
	// orchestrator's securityhold.go; blocks nothing.
	LinkTrustBoundaryCleared LinkKind = "trust-boundary-cleared"
	// LinkClearanceInheritedFrom records that this port's
	// LinkTrustBoundaryCleared was carried over from the task it ports
	// (-> that source task's ID) rather than earned by a review of its
	// own: its trust-boundary files carry exactly the patches the
	// source's cleared pull request did. Read only so the pull request is
	// told once, not at every head that keeps it. Written by
	// orchestrator's securityhold_inherit.go; blocks nothing.
	LinkClearanceInheritedFrom LinkKind = "clearance-inherited-from"
	// LinkHumanSubmitted records that a human's Submit turned this task's
	// auto-merge on (ui.Client.Submit, or an edit doing so; a Submit over
	// a change already auto-merging records nothing) and that grain
	// has not yet pinned that decision to a commit (-> when, RFC 3339,
	// and "@" the head the human was shown: HumanSubmittedTarget). The
	// next cycle that reads the branch -- syncEntry, before anything
	// there can merge or refresh it -- turns it into
	// LinkTrustBoundaryCleared for that head if the branch is still
	// there, so a base-branch merge the queue makes afterwards carries
	// the human's clearance forward (carryClearance) instead of filing
	// another review; a branch that has moved since (or a marker naming
	// no head) is classified again instead. Dropped by that cycle and by
	// any classification of a head a run left (holdForTrustBoundary).
	// Written by ui, read by orchestrator's securityhold_submit.go;
	// blocks nothing.
	LinkHumanSubmitted LinkKind = "human-submitted"
	// LinkBrokenRulesHold records that the only owner-release category
	// holding this self-merging change is the base's broken rules
	// (`unparseable-triggers`) -> the head it was held at. Once the
	// base's rules parse again, SyncSecurityHolds classifies that same
	// head again under them: the fix releases what the rules alone held,
	// and holds for what the fixed rules ask. Dropped by a release, by a
	// classification that finds anything else needing an owner, by
	// findings from any review of the change, and by a head that has
	// moved -- each of which leaves the change waiting for an owner's
	// Submit. Written and read by orchestrator's securityhold.go.
	LinkBrokenRulesHold LinkKind = "broken-rules-hold"
	// LinkPathTriggerHold records that grain withdrew this task's
	// auto-merge because its pull request touches a path a repository's
	// gating `.grain/triggers` file watches (-> the strictest `human`
	// setting among those triggers, model.TriggerHuman). The security
	// hold's shape (LinkSecurityHold), for a hold the repository asked
	// for rather than grain's own list. Written and read by
	// orchestrator's pathtriggerhold.go.
	LinkPathTriggerHold LinkKind = "path-trigger-hold"
	// LinkReviewQuestions records that a review of this task's change
	// left findings it could not settle without the owner -- questions,
	// each with the reviewer's recommended answer -- and that the change
	// waits on that owner's decision (-> ReviewQuestions.Target()). A
	// human reply on the task, or on the review, sends the task back to
	// the queue with the questions and the reply in its prompt; Submit
	// ships it as is. Written and read by orchestrator's
	// reviewquestions.go.
	LinkReviewQuestions LinkKind = "review-questions"
	// LinkDraftReview records a draft review grain posted on this task's
	// pull request for a review task's findings (-> GitHub's review ID),
	// so the inline comments it carries are never mistaken for review
	// threads somebody else opened (orchestrator's reviewthreads.go).
	LinkDraftReview LinkKind = "draft-review"
	// LinkReviewThread records one GitHub review thread on this task's
	// pull request that grain has already sent a run to deal with (-> the
	// REST ID of the comment that opened it). One run per new batch of
	// threads, never one per cycle a thread stays open.
	LinkReviewThread LinkKind = "review-thread"
	// LinkPathTriggerTask records one task a pull-request trigger filed
	// for this task's change (-> "<trigger>=<task ID>"). A gated hold
	// is released only once every gating one has finished.
	LinkPathTriggerTask LinkKind = "path-trigger-task"
	// LinkPathTriggersRead records the head commit grain last judged
	// this task's pull request against its repository's pull-request
	// triggers at (-> the sha), so a head that moves is judged again.
	LinkPathTriggersRead LinkKind = "path-triggers-read"
	// LinkApprovalRequired records that a rule this task's change matched
	// lets only named people release it (-> "<rule>=<approver>,..."),
	// and LinkApprovedBy that one of them has (-> "<rule>=<user>"). A
	// sensitive trigger with `human: required` writes the first
	// (pathtriggerhold.go), and so does grain's own guard over a
	// repository's trigger declarations (securityhold.go); ui.Client.Submit
	// refuses anybody else and writes the second. See sensitive_path.go.
	LinkApprovalRequired LinkKind = "approval-required"
	LinkApprovedBy       LinkKind = "approved-by"
	// LinkRepoRules records the identity of the repository's own rules
	// (RepoRules: base commit and hash of its trigger declarations) a
	// security hold was decided under, beside the trustboundary.Version
	// LinkSecurityHold names.
	LinkRepoRules LinkKind = "repo-rules"
	// LinkBaseRepairBypass records the head SHA at which a base-branch
	// repair pull request bypassed human gates (sensitive path triggers or
	// trust-boundary auto-merge withdrawal) under
	// RepoConfig.BaseRepairBypassesHumanGates while the base branch was red.
	LinkBaseRepairBypass LinkKind = "base-repair-bypass"
	// LinkContinues records that this task carries on a conversation
	// that ended on another one (-> that task's ID). One writer: a
	// standing console session filed again after its own task was
	// closed, which is how a chat that never ends gets a fresh thread
	// (orchestrator.fileConsoleSession, docs/implementation-notes.md's
	// "A chat that never ends is not split for you").
	//
	// LinkProposedBy's shape -- provenance, the timeline, and nothing
	// else. It must never be made to block: the replacement session's
	// whole purpose is to be talked to, and a task depending on the
	// closed conversation it replaces would never be dispatched, so the
	// session would be dead the moment somebody split it.
	LinkContinues LinkKind = "continues"
	// LinkSupersedes records that this task was filed to replace another
	// one (-> that task's ID), and LinkSupersededBy is the same fact
	// written on the task that was replaced (-> the replacement's ID).
	// One writer for both: a propose_task call naming `supersedes`
	// (Store.Supersede, grain/task-473).
	//
	// Both directions are stored rather than one being queried back,
	// because both are read the same way and in the same breath: a chain
	// of five near-identical tasks is walked forwards from the first
	// ("what replaced this?") and backwards from the last ("what did
	// this replace?"), and the answer has to be on the task in front of
	// the reader rather than behind a scan of every other task's links.
	// LinkFixTask and LinkReviewTask already pair with the
	// LinkProposedBy their own tasks carry the same way.
	//
	// LinkContinues' shape: provenance, the timeline, and nothing else.
	// Neither blocks -- the replacement is the work now, and the task it
	// replaced is closed, so there is nothing for either to hold up.
	LinkSupersedes   LinkKind = "supersedes"
	LinkSupersededBy LinkKind = "superseded-by"
	// LinkGoogleChat records the Google Chat space linked to this task
	// (-> space resource name like "spaces/AAAAAAAAAAA" or space URL).
	// Provenance/external reference only, never blocks dispatch.
	LinkGoogleChat LinkKind = "google-chat"
	// LinkReverts records that this task was filed to revert another task's change
	// (-> origin task ID), and LinkRevertedBy is the reverse link on the reverted task.
	// Provenance and lineage only, never blocks dispatch.
	LinkReverts    LinkKind = "reverts"
	LinkRevertedBy LinkKind = "reverted-by"
	// LinkPortOf records that this task was filed to port another task's
	// merged change onto a paired branch (-> the source task's ID), and
	// LinkPortedBy is the same fact written on the source (-> the port's
	// ID). Written by the merge queue when the source lands
	// (orchestrator's port.go). Provenance only, never blocks dispatch --
	// but a task carrying LinkPortOf is never itself ported again, which
	// is what stops a change ping-ponging between the two branches.
	LinkPortOf   LinkKind = "port-of"
	LinkPortedBy LinkKind = "ported-by"
	// LinkExternalPR records an external pull request ("owner/repo#number")
	// that this task reviews or acts alongside without having opened it
	// itself (unlike LinkFixes). Provenance and PR-tool scoping only; never
	// blocks dispatch and never places a task in awaiting_submit.
	LinkExternalPR LinkKind = "external-pr"
	// LinkTakeover marks a shepherd task filed by the GitHub bot's
	// "@<slug> take over" command (-> the pull request it took over,
	// "owner/repo#number"). The task's own pull request is its LinkFixes
	// one: the same pull request when grain can push its head branch, or
	// the replacement grain opened when it cannot. While a task carries
	// this, the merge queue holds its pull request until every review
	// grain requires has passed at the head it is about to merge
	// (LinkTakeoverCleared). Written by orchestrator's takeover.go.
	LinkTakeover LinkKind = "takes-over"
	// LinkTakeoverReview records, on a shepherd task, one review grain
	// filed of the pull request it holds (-> "<review task ID>@<head
	// sha>"): the head the review was asked to read. The newest head's
	// set is the one the merge gate reads; older ones are history.
	LinkTakeoverReview LinkKind = "takeover-review"
	// LinkTakeoverReviewOf is LinkTakeoverReview's back link, on the
	// review task (-> the shepherd task's ID).
	LinkTakeoverReviewOf LinkKind = "takeover-review-of"
	// LinkTakeoverFindings records, on a review task carrying
	// LinkTakeoverReviewOf, how many findings its run posted (-> the
	// count, in decimal). Absent until the run has finished.
	LinkTakeoverFindings LinkKind = "takeover-findings"
	// LinkTakeoverCleared records, on a shepherd task, the head every
	// required review has passed at (-> the commit sha). The merge queue
	// refuses the pull request while this is absent; a push moves the
	// head, drops it and files the reviews again.
	LinkTakeoverCleared LinkKind = "takeover-cleared"
	// LinkTakeoverCheck records the grain/review check run grain keeps on
	// a shepherd task's pull request (-> "<check run id>@<head sha>").
	LinkTakeoverCheck LinkKind = "takeover-check"
	// LinkGitHubEvent records an inbound GitHub event ("<kind>:<id>", e.g.
	// "comment:555" or "review:991") already acted on for this task so
	// webhook redeliveries and restarts stay idempotent. Blocks nothing.
	LinkGitHubEvent LinkKind = "github-event"
	// LinkChangelist records a changelist (CL) that is this task's work (->
	// "cl/<number>"), the way a pull request is a git task's: written
	// when a run's capability grants report one in their audit
	// (protocol.AuditTaskChangelistField), or when open_pull_request
	// creates one (pkg/change). It is the task's change as LinkFixes is:
	// a finished task with one is awaiting_submit (AwaitsSubmit). A task
	// may have several; the first is its primary CL. Never blocks
	// dispatch.
	LinkChangelist LinkKind = "changelist"
	// LinkChangelistSubmitted records the number a task's CL was
	// submitted as ("cl/<submitted number>"): a forge that renumbers a
	// pending CL on submit records the submitted number here. A work tree
	// reads it as cl-submitted.
	LinkChangelistSubmitted LinkKind = "changelist-submitted"
	// LinkChangelistAbandoned records a task's CL that was abandoned
	// (-> "cl/<number>"), as a poll found it (changelists.md §7). It is
	// not a change link: a task whose every CL is abandoned no longer
	// awaits a Submit, and reads awaiting_reply once its run is over.
	LinkChangelistAbandoned LinkKind = "changelist-abandoned"
	// LinkCLThread records one review thread on a task's CL that grain
	// has already sent a run (or a running run's addendum) to deal with
	// (-> "cl/<number>:<comment id>"): LinkReviewThread's job for a CL.
	// Recorded before the run is asked for, so one batch of threads sends
	// one run. Written by orchestrator's reviewthreads.go and read by the
	// edition's change provider, which leaves recorded threads out of
	// Threads. Blocks nothing.
	LinkCLThread LinkKind = "cl-thread"
	// LinkCLReviewCleared records that a required review passed on one
	// snapshot of a task's CL (-> "cl/<number>@<snapshot>:<template>"),
	// the CL's LinkTrustBoundaryCleared. A new snapshot clears nothing
	// that an older one did. Read by the edition's review gate
	// (changelists.md §5, G3-5). Blocks nothing itself.
	LinkCLReviewCleared LinkKind = "cl-review-cleared"
	// LinkCLReviewFiled records that grain filed a required review of one
	// snapshot of a task's CL (-> "cl/<number>@<snapshot>:<template>",
	// CLReviewTarget), so each snapshot is reviewed once per template. It
	// sits on the reviewed task beside LinkReviewTask.
	LinkCLReviewFiled LinkKind = "cl-review-filed"
	// LinkCLReviewOf is the same target on the review task itself: grain's
	// record that it filed this task as that review, with the provider's
	// read-only grant (orchestrator.SyncCLReviews). Only grain writes it,
	// and it is what a clean finish clears (LinkCLReviewCleared).
	LinkCLReviewOf LinkKind = "cl-review-of"
)

// CLThreadsPrincipal is who tells a task about new review threads on its
// CL. Unlike grain's other automation notices, a running run is handed
// its comments as addenda (pkg/ui's AddendaSince): a reviewer's thread
// opened mid-run is the run's to deal with before it ends.
var CLThreadsPrincipal = Principal{Kind: PrincipalAutomation, ID: "cl-review-threads"}

// IsChange reports whether a link of kind k is the task's own change --
// a pull request (LinkFixes) or a changelist (LinkChangelist) -- which
// is what parks a finished task on awaiting_submit rather than on a
// reader (AwaitsSubmit, AwaitsAck, and the task_state view's
// changeLinkKindsSQL).
func (k LinkKind) IsChange() bool {
	return k == LinkFixes || k == LinkChangelist
}

// changeLinkKindsSQL is IsChange's kinds as a SQL IN list.
const changeLinkKindsSQL = `'` + string(LinkFixes) + `','` + string(LinkChangelist) + `'`

// TagMechanicalPort marks a port grain made itself, by cherry-picking
// with no agent run behind it (orchestrator.filePorts): what the merges
// metrics tell mechanical ports from agent ones by (TaskTiming.Port).
const TagMechanicalPort = "port-mechanical"

// Blocks reports whether a link holds a task back from dispatch.
//
// LinkMergeWith is deliberately absent: it gates merging, not running,
// which is what lets the members of a coordinated cross-repo change be
// worked in parallel.
func (k LinkKind) Blocks() bool {
	return k == LinkDependsOn || k == LinkChildOf
}

// Link is one typed edge from a task to another task, a pull request,
// a review thread or an external reference.
type Link struct {
	Kind LinkKind
	// A task ID for depends-on/child-of/proposed-by; a pull request or
	// review thread reference for fixes/addresses.
	Target string
}

// --- tasks -----------------------------------------------------------

// Intent is what a task's run is expected to produce.
type Intent string

const (
	IntentImplement Intent = "implement" // fresh branch -> a new PR
	IntentReview    Intent = "review"    // post a review, push nothing
	IntentAnalyze   Intent = "analyze"   // answer in a comment
)

// State is a task's derived state (StateOf, task_state). Never stored.
type State string

const (
	StateProposed      State = "proposed"
	StateQueued        State = "queued"
	StateRunning       State = "running"
	StateAwaitingReply State = "awaiting_reply"
	// StateFailed means MaxConsecutiveFailures runs in a row ended
	// without succeeding, and none of them has succeeded since -- see
	// StateOf and task_streak (schema.go). Unlike the other states, a
	// task does not leave this one on its own: task_ready only ever
	// selects 'queued', so nothing here retries automatically, and a
	// human has to set Observation.RetryRequestedAt (Store.Retry) before
	// it is eligible for another attempt.
	StateFailed State = "failed"
	// StateAwaitingSubmit is a task whose run is over and whose pull
	// request nobody has submitted: it has a LinkFixes pull request and
	// AutoMerge is not set, so the merge queue is not driving it and
	// nothing will ever land it on its own. Like StateAwaitingReply, and
	// unlike every other post-run state, the task does not leave this one
	// by itself -- somebody has to click Submit (ui.Client.Submit, which
	// is what sets AutoMerge) or close it.
	//
	// It was a chip beside the state badge before it was a state
	// (ui/src/state.js's completionPhase, bwsalmon/agents#494), which
	// meant the badge itself said "Queued for merge" about a task that
	// was on no queue at all -- the one reading that is never true of it.
	// A state of its own is what makes the wait countable: it gets its
	// own sidebar entry and its own filter, so "what is sitting here
	// waiting on me?" is a question the task list can answer.
	StateAwaitingSubmit State = "awaiting_submit"
	// StateCompleted is a task whose run is over and which needs nobody:
	// it opened a pull request and that pull request is on the merge
	// queue (AutoMerge set). StateOf holds it here until that pull
	// request merges or the task is closed.
	//
	// A run that produced no pull request at all does not land here: it
	// has nothing to merge and somebody has to read what it did produce,
	// which is StateAwaitingReply above. The one task that is neither is a
	// review grain filed itself, which delivers its findings somewhere
	// else entirely (AwaitsAck) and so waits on nobody at all: it used to
	// sit here, badged "Queued for merge" with nothing to merge and
	// nothing that could ever move it, and orchestrator.SyncReviews now
	// closes it the cycle after its run ends.
	StateCompleted State = "completed"
	// StateDeferred is a task somebody has put aside: it is not closed --
	// it is still work grain is meant to do one day -- but it is not
	// waiting on a dispatch either, and it should not sit in the list of
	// things that are. Task.DeferredAt is the declaration behind it, and
	// undeferring is the only way out, which puts the task back in
	// whichever of 'proposed' or 'queued' its approval implies.
	//
	// Like 'proposed' it is a state a task reaches by a human saying so
	// rather than by anything happening to it, which is why it sits down
	// here beside it: task_ready only ever selects 'queued', so a
	// deferred task dispatches no more than a proposed one does.
	//
	// Hidden by default wherever tasks are listed, the same treatment
	// 'closed' already gets (ui/src/state.js's SET_ASIDE_STATES, and no
	// column of its own on the board): the whole point of deferring is to
	// get something out of the way of what is actually in play, which a
	// state that still filled a row of the backlog would not do.
	StateDeferred State = "deferred"
	StateClosed   State = "closed"
)

// SettledStates are the states a task is done being in play in: it has
// completed, it has been closed, or a human has deliberately put it
// aside. Everything else -- proposed, queued, running, awaiting a reply,
// awaiting a submit or an ack, failed -- is work that can still dispatch
// a run or still wants a person.
//
// Deferred sits with the finished two rather than with the live ones on
// purpose. It is the one state that says so on a human's authority
// rather than grain's, and the deployment that defers a task has already
// decided it is not in play; treating it as live would let a task
// somebody parked indefinitely block a repo from ever being removed.
//
// Named here, once, because two readers ask the same question in two
// languages: Store.UnsettledTasksInRepo in SQL, and State.Settled below
// for code holding a State. ui/src/state.js's SET_ASIDE_STATES is the
// nearest thing on the frontend and is deliberately not the same list --
// that one is about what a task *list* hides, and a completed task is
// still worth showing there.
var SettledStates = []State{StateCompleted, StateClosed, StateDeferred}

// Settled reports whether s is one of SettledStates.
func (s State) Settled() bool { return slices.Contains(SettledStates, s) }

// NeverCompletes reports whether a task is one that, by design, never
// reaches a terminal state -- and so is a task nothing can ever be made
// to wait for.
//
// The property rather than the kind, deliberately. Every caller here is
// really asking "will this task ever finish?", and a caller that instead
// asks "is this a console session?" is one that has to be found and
// changed again the next time grain grows something else that never
// ends. There is one such kind today: a standing console session
// (ConsoleSession, materialized by orchestrator.fileConsoleSession),
// whose whole definition is a task that is never finished, recognized
// off the tag that is the only record of the association
// (ConsoleSessionIDOf). A second kind adds itself here and nowhere else.
//
// This reads the declaration alone, so it says nothing about what has
// since happened to the task: a session's own task, closed and replaced
// by a fresh conversation, is a perfectly good thing to depend on. A
// caller deciding whether a dependency could ever be satisfied wants
// both halves and should ask Store.DependencyNeverSatisfiable.
func NeverCompletes(t Task) bool {
	_, ok := ConsoleSessionIDOf(t)
	return ok
}

// NeverEndingDependencyNote is what a human approving a proposal reads
// in place of a depends_on link grain would not make -- attributed to
// grain, since the rest of the body is the agent's own words, the same
// bargain orchestrator's unresolvedDependencyNote and SupersedeRefusedNote
// strike.
//
// It exists because dropping the link silently is the other half of the
// failure it prevents: the run said something about what this work has
// to follow, and the person approving it is the one who can say what the
// run should have named instead.
func NeverEndingDependencyNote(refs []string) string {
	return fmt.Sprintf("\n\n---\n\ngrain: the run that proposed this said it depends on %s, "+
		"which names a task that by design never completes -- a standing chat session, whose "+
		"conversation is the whole of it. A depends-on link is satisfied by its target closing, "+
		"so that one would have held this task out of the queue forever with nothing able to "+
		"release it. Nothing blocks this task on it. If this work really does have to follow "+
		"something, name the task doing that work.",
		"`"+strings.Join(refs, "`, `")+"`")
}

// Task is the declared half: what a human, or grain proposing, asked for.
// No State field and no StateSince — see StateOf.
type Task struct {
	ID string
	// Branch is the resolved branch this task owns. It is fixed when the
	// task is first written so changing the deployment's naming template
	// cannot strand work already pushed under an earlier name. Empty is a
	// task created before configurable names existed and resolves to the
	// historical grain/task-<id> form.
	Branch string
	Intent Intent
	Origin Origin
	Title  string
	Body   string

	// nil means not approved, which is what makes a task proposed.
	Approval *Attribution
	// ApprovedAt is when Approval was set, display-only -- nothing in
	// StateOf or task_state reads it, the same way CreatedAt decides
	// nothing about a task's state. It exists for Transitions, which needs
	// a "queued since" timestamp Approval's own Attribution has no room
	// for. nil alongside a non-nil Approval means a task approved before
	// this field existed, not that it was never approved.
	ApprovedAt *time.Time
	// DeferredAt is when somebody put this task aside, and nil for a task
	// nobody has -- the whole of what makes StateDeferred (see its own
	// doc comment for what that state means and where such a task goes).
	//
	// A declaration, beside Approval rather than in Observation, because
	// deferring is somebody saying what should happen to this task and
	// not grain reporting what has: it is withdrawing approval's
	// neighbour, not closing's. Under the repo direction
	// (docs/data-model.md) that puts it in the file a human edits and
	// makes a deferral reviewable like any other declaration change --
	// the argument that document already makes for withdrawing approval.
	//
	// Orthogonal to Approval, and deliberately not a clearing of it: a
	// task deferred while queued is approved still, so undeferring puts
	// it straight back on the queue rather than making somebody approve
	// it a second time for a decision they never reversed. StateOf reads
	// this ahead of both, so neither state can dispatch while it is set.
	//
	// A timestamp rather than a bool, for ApprovedAt's own reason: "set
	// aside three months ago" and "set aside this morning" are different
	// facts about the same task, and Transitions has a place on the
	// timeline to put it.
	DeferredAt *time.Time
	// DeferredUntil is when this task should be picked back up by itself,
	// and nil for the deferral nobody put a date on -- "not this month,
	// but don't lose it" with the month named. The orchestrator's
	// reconcileWake undeferrs a task whose wake time has passed, the
	// same way reconcileSchedule fires a schedule that has come due;
	// nothing else reads it.
	//
	// Only meaningful alongside DeferredAt, and cleared with it: the
	// state is still the whole of DeferredAt (StateOf reads that field
	// and not this one), so a wake time is a note on a deferral rather
	// than a second way to be deferred. Undeferring by hand clears both,
	// which is what makes pressing Undefer early exactly as final as it
	// looks -- the task comes back and does not defer itself again.
	//
	// It exists because a date in a task's prose is not a gate: a task
	// asking to be read "not before 2026-09-14" was dispatched a week
	// early twice running (docs/test-runs/
	// 2026-09-08-nightly-week-one-still-early.md), because there was
	// nowhere for the constraint to live. DependsOn cannot say it -- no
	// task's completion means "the date has arrived" -- and a schedule
	// recurs rather than firing once.
	DeferredUntil *time.Time
	// GoalKind is the kind of this task's goal (TaskGoal), "" for none.
	// Never stored with the task -- task_goal is the goal's own record --
	// it is filled by a reader that has read the goal, so that the
	// task's liveness can name what a resting goal task waits for
	// (orchestrator.taskReasonOf).
	GoalKind GoalKind `json:"-"`
	// UndeferredAt is when this task was last picked back up -- the
	// moment DeferredAt was cleared, whether a person cleared it (Undefer,
	// Run now) or its own wake time did (orchestrator.reconcileWake). nil
	// for a task nobody has ever deferred, and for one undeferred before
	// this column existed.
	//
	// It is here because clearing DeferredAt used to leave no trace at
	// all, and a task's "how long has this been queued?" is reconstructed
	// from the timestamps the record happens to keep (StateTimes.Since):
	// a task approved in January, put aside, and picked back up this
	// morning read as queued since January, and was flagged as standing
	// still on the first read after it came back -- the one thing it is
	// not. A stall flag has to err toward saying nothing
	// (orchestrator.runPatience), so the undeferral is stamped rather
	// than inferred.
	//
	// A declaration beside DeferredAt rather than an observation, for
	// DeferredAt's own reason and one more: undeferring *is* the clearing
	// of this declaration, and it is written in the same statement by the
	// same method (Store.SetDeferred), so there is no undeferral that can
	// reach the record without it. Observation would have meant a second
	// table, a row invented for a task grain has never observed, and a
	// write that could be forgotten by the next caller who clears
	// DeferredAt directly. Observation.RetryRequestedAt is not reused for
	// it on stronger grounds still: that field also narrows the window a
	// task's failures are counted over (Store.FailureStreak), so an
	// undeferral written through it would silently forgive a task's
	// earlier failures.
	//
	// It is not cleared by a later deferral. A task deferred again holds
	// the return it made before that, which is a moment that really
	// happened and which Transitions puts on the timeline; nothing reads
	// it while DeferredAt is set, since a deferred task's state is
	// deferred and StateTimes answers for the three states grain itself
	// is working through.
	UndeferredAt *time.Time

	Target       *RepoRef      // the repo this task's own runs push to
	ChangeTarget *ChangeTarget // non-git change target (e.g. a registered changelist workspace)
	// Reviewers is the person-set list of reviewer usernames for this task's
	// change (docs/design/changelists.md §5). Passed as the reviewers argument
	// of a workspace capability grant when the run's capability pass is minted,
	// and used as the default reviewer list when mailing a changelist. Never set
	// by an agent.
	Reviewers []string
	Binding   RepoBinding
	Base      string
	// Writes is every *other* repository this task's work may land in,
	// with the base branch for each -- WriteTarget's own doc comment has
	// what one is and why a run does not push to them itself. Empty, the
	// default, is the single-repo task grain has always had.
	//
	// It grants nothing but that. A Writes entry attaches no capability
	// (the same contract Reads has), and it does not widen what this
	// task's sandbox may push: the proxy still authorizes a push to
	// Target alone, and reads a Writes entry only as one more repository
	// the run may clone to look at. What it does is make a proposal
	// naming that repository land there, on that branch, and inherit this
	// task's auto-merge when it does (LandsWhereGranted).
	Writes []WriteTarget
	Folder *FolderRef
	Reads  []RepoRef // read-only. Grant nothing.

	Grants []Grant
	Links  []Link
	Tags   []string
	// CustomFields is this task's values for the fields this deployment
	// declares (CustomField, custom_field.go), keyed by field name. nil
	// -- the ordinary case -- is a task that says nothing about any of
	// them.
	//
	// grain reads none of it: nothing here dispatches, orders, blocks or
	// filters on a custom field. It travels into the prompt every run is
	// given (orchestrator.BuildPrompt) and is shown and edited in the
	// task pane, which is the whole of what it is for -- what this
	// deployment needs to say about a piece of work that the title and
	// the body would otherwise have to say in prose.
	//
	// Only values for declared fields are ever written (see
	// NormalizeCustomFieldValues), but a value whose field has since
	// been deleted from the state repository stays: it is what somebody
	// recorded about this task, and a declaration going away does not
	// make that untrue.
	// A slice per name because a declaration may say the field holds a
	// set of values rather than one (CustomField.Multiple). A
	// single-valued field -- nearly all of them -- carries a slice of
	// one, and the store refuses it more (NormalizeValues).
	CustomFields map[string][]string

	AutoMerge bool
	// AutoApproveProposals lets the tasks this one proposes be filed
	// approved without AutoMerge's permission over its own pull request.
	// Either is enough for AutoSubmits, which still checks the
	// proposal's grants and landing place against this task's.
	AutoApproveProposals bool
	// NoPort says this task's change belongs on the branch it lands on
	// and nowhere else -- a release fix only `candidate` needs, a feature
	// on `main` that is not ready -- so the merge queue files no port of
	// it onto the repository's other paired branches
	// (RepoConfig.PortBranches). A `Port: no` footer in the change's own
	// commit messages says the same thing from inside the change.
	NoPort bool
	// ResumeOnDeadline makes a run of this task that runs out of wall
	// clock a SuspendDeadline suspension rather than a failure, at most
	// MaxDeadlineResumes times in a row (docs/design/durable-tasks.md#DT-2):
	// the cheap "not done, keep going" for work that is merely large,
	// which is not the durable grant.
	ResumeOnDeadline bool
	// NothingNeededAction is what a run ending on nothing_needed does to
	// this task: close it (NothingNeededClose, which "" also means) or
	// complete it into awaiting_reply like any closing note
	// (NothingNeededAck). See orchestrator.ProcessResult.
	NothingNeededAction NothingNeededAction
	// Interactive marks a task filed for a live back-and-forth with
	// whoever created it, rather than a change handed off to run
	// unattended (bwsalmon/agents#539). It changes nothing about how a
	// dispatched run is driven -- a comment posted while one is live
	// already reaches the agent through the same channel every task's
	// conversation does (agent.RunConfig.Addenda, orchestrator.
	// addendaPoller) -- only how the task is prioritised and presented:
	// ui.Client.CreateTask files it ahead of the ordinary backlog, the
	// same way Config.NewestFirst already can, and the frontend opens
	// its chat view immediately after filing it instead of returning to
	// the task list.
	//
	// That chat view is a real chat window now rather than the task's
	// timeline relabelled (grain/task-414, ui/src/components/TaskChat.
	// jsx): the same conversation, drawn as the back-and-forth it is,
	// with the timeline folded up underneath it. It is still only a
	// presentation of this flag -- what a run does, and how, is
	// unchanged by it.
	Interactive bool
	// SandboxCPUs, SandboxMemoryMB and SandboxDiskGB
	// (bwsalmon/agents#534, grain/task-41) override
	// Config.SandboxCPUs/SandboxMemoryMB/SandboxDiskGB for this task's
	// own dispatch only -- the per-job escape hatch alongside the
	// deployment-wide setting, for a task that is known ahead of time to
	// need more (or less) than the default shape, e.g. a build-heavy repo
	// whose checkout and toolchain do not fit the guest image's own disk,
	// or a task deliberately run on a constrained VM to reproduce a
	// memory-pressure bug. Zero, the default for all three, means "use
	// the deployment default" -- the same "zero means unset" contract
	// Config.SandboxCPUs/SandboxMemoryMB/SandboxDiskGB itself uses,
	// chosen so a task created before these fields existed reads back as
	// unset rather than as an explicit "shrink this VM to nothing."
	//
	// Applied by orchestrator.runOne, once per dispatch, immediately
	// before the sandbox is handed to the run: a slot's sandbox is
	// otherwise sized from orchestrator.KonturConfig's own
	// deployment-wide default, so a task asking for a different shape
	// overrides that default for its own sandbox -- per dimension, at the
	// moment the sandbox is created, which is the only moment its size is
	// decided now that one is built per run (orchestrator.Shape, passed to
	// Sandboxes.Acquire). Only orchestrator.GranuleSandboxes can honour it
	// -- a task with any of the three set,
	// dispatched onto the default orchestrator.HostSandboxes backend
	// (no VM to resize), fails that dispatch outright rather than
	// silently running at whatever shape the host itself happens to be,
	// the same "refuse rather than silently do something else" choice
	// runOne already makes for a task that requests a capability with no
	// local directory to place it in.
	SandboxCPUs     int
	SandboxMemoryMB int
	SandboxDiskGB   int
	// NetworkAccess overrides Config.NetworkAccess for this task's own
	// dispatch only (grain/task-269): NetworkNone for a job with no
	// business reaching anything outside this deployment, NetworkOpen
	// for one that needs the internet on a deployment whose default is
	// to withhold it. Empty, the default, means "use the deployment
	// default" -- the same "zero means unset" contract SandboxCPUs and
	// AgentFramework beside it use, so a task created before this field
	// existed reads back as deferring to the deployment rather than as
	// an explicit request for a network nobody chose for it.
	//
	// Carried to the sandbox as the fourth dimension of
	// orchestrator.Shape and resolved against the deployment default
	// there, per dimension, exactly like the three sizes above: it is a
	// create-time property of a sandbox rather than something applied to
	// a running one. Only orchestrator.GranuleSandboxes can honour
	// NetworkNone -- a task asking for it while this deployment runs the
	// host-directory backend fails that dispatch outright rather than
	// silently running with the daemon's own network, the same "refuse
	// rather than silently do something else" choice a per-task sandbox
	// shape already gets.
	NetworkAccess NetworkAccess
	// AgentFramework overrides Config.AgentFramework for this task's own
	// dispatch only -- the per-task escape hatch alongside the
	// deployment-wide default, for a task better suited to one framework
	// than the others (a change the `claude` CLI's own tooling handles
	// well, say, filed on a deployment that runs agent/antigravity by
	// default). Empty, the default, means "use the deployment default",
	// the same "zero means unset" contract SandboxCPUs/SandboxMemoryMB
	// above already use -- so a task created before this field existed
	// reads back as deferring to the deployment rather than as an
	// explicit choice of a framework that did not exist for it.
	//
	// Its vocabulary is Config.AgentFramework's own
	// (model.AgentFrameworks(), with the legacy
	// "gemini" spelling folded in by NormalizeAgentFrameworkName rather
	// than NormalizeAgentFramework, so that "" keeps meaning "no
	// override" here); ui.CreateTask
	// validates it the same way ui.UpdateSettings validates the
	// deployment-wide one, rather than the schema (schema.go's own doc
	// comment on why an enum is TEXT here). Applied by
	// orchestrator.runOne, once per dispatch, when it asks Deps.Framework
	// for the agent.Framework this run is driven by.
	AgentFramework string
	// AgentBackendID pins this task's dispatch to one configured model
	// -- an AgentBackend.ID out of Config.AgentBackends -- instead of
	// letting the rotation choose (grain/task-110). AgentFramework above
	// says "run this one on Claude" and leaves which Claude account to
	// the pool; this says "run this one on that model", which is what an
	// operator wants for a task worth spending the expensive account on,
	// for a comparison between two models of the same kind, and for the
	// one job a model that is otherwise out of rotation is kept around
	// for.
	//
	// A backend named here is used even when AgentBackend.Disabled is
	// set: disabling removes a model from the rotation, not from the
	// deployment, so naming one by hand is exactly how a disabled model
	// still gets work. That is the whole point of the pair -- an account
	// kept for the occasional job is disabled so nothing lands on it by
	// accident, and pinned when something should.
	//
	// Empty, the default, means "no pin, use the rotation" -- the same
	// "zero means unset" contract AgentFramework above uses, so a task
	// created before this field existed reads back as one nobody pinned.
	// A non-empty value also settles AgentFramework: ui.CreateTask fills
	// that in from the named backend's own framework, so which framework
	// a pinned task counts against (orchestrator's frameworkFor, and the
	// pause it drives) needs no second lookup.
	//
	// Validated at creation against the configured pool rather than by
	// the schema, like AgentFramework; applied by orchestrator.runOne,
	// which passes it to Deps.Framework as part of one AgentChoice.
	AgentBackendID string
	// Model is the model this task runs on -- a model id some connection
	// of its team advertises (model_selection.go). Dispatch round-robins
	// across every enabled connection of the team that serves it, unless
	// AgentBackendID above pins one, in which case that connection must
	// advertise it (ui.CreateTask refuses the pair otherwise). Empty means
	// the team's own default model (Team.DefaultModel).
	//
	// A model implies its framework, so a task that names one has no use
	// for AgentFramework; that field stays readable for tasks filed before
	// this one existed.
	Model string
	// ModelSource is which declaration chose Model, when it was not the
	// task's own choice: LayerTemplate for a model a template carried
	// onto the task it filed, LayerSchedule for one a schedule chose over
	// its template's (CompositionSources.Model, stamped here by
	// Composition.Task and the orchestrator's own template filings).
	// Empty means the task chose Model itself -- at the form, through
	// propose_task, or by an edit, which takes the choice over -- and is
	// what every task filed before this field existed reads back as. It
	// says nothing when Model is empty: the team's default decides then,
	// and that is read at dispatch, not stamped at filing.
	ModelSource CompositionLayer
	// PromptExtension overrides the deployment's and the target repo's
	// standing instructions for this task's own dispatch alone
	// (grain/task-114) -- prompt_extension.go's own doc comment has what
	// those are and why this layer replaces them rather than adding to
	// them. Empty, the default, means "no override", the same "zero means
	// unset" contract AgentFramework and SandboxCPUs above already use,
	// so a task created before this field existed is told exactly what
	// the deployment and its repo say.
	//
	// Read at dispatch (orchestrator.RunDispatch, through
	// model.PromptExtensionFor), not at creation, which is what makes an
	// edit to a queued task's own override reach the run it eventually
	// gets.
	PromptExtension string
	// ReviewTemplateID is the review attached to this task
	// (grain/task-284): the Template.ID whose title, body and grants a
	// second task is filed from once this one's own work is done, to
	// read the code it proposed and fix what is wrong with it before it
	// merges. Empty, the default, means no review -- the same "zero
	// means unset" contract AgentFramework and PromptExtension above
	// use, so a task created before this field existed reads back as one
	// nobody asked for a review of.
	//
	// A template rather than a body written here for the same reason
	// Schedule.TemplateID is one: "review this branch and fix what you
	// find" is the same paragraph on every task it is attached to, and
	// the instructions worth improving once and having every review use.
	// Nothing about the template says it is a review; what makes it one
	// is being named here.
	//
	// Read by orchestrator.SyncReviews, once, the first cycle this task
	// has a pull request open and its run is over -- so attaching a
	// review to a task that has already completed still gets one, and
	// editing this after the review has been filed changes nothing
	// (LinkReviewTask is what records that it was). Never inherited: the
	// review task itself is filed with this empty, which is what stops a
	// review of a review of a review.
	ReviewTemplateID string
	// TemplateID is the template this task was filed from, empty for one
	// that was not (grain/task-2536): set by every firing that composes a
	// task from a template (Composition.Task) and by a person filing from
	// one. It decides nothing about the task's own content, which was
	// copied at filing; what reads it is the git proxy, which asks the
	// template's declaration -- as the declaring repository's default
	// branch has it now -- which refs beyond this task's branch its runs
	// may push (Store.GitDeclaredPush). Never inherited by a proposal: an
	// agent cannot hand its own push set on to work it files.
	TemplateID string
	Creator    string
	TimeZone   string
	// Issue is the GitHub issue number associated with this task, or 0 if unassociated.
	Issue int
	// WorkOrigin is the pull request, issue or grain task this task's
	// tree of work ultimately came from (WorkOrigin's own doc comment).
	// Filled in when the task is first written, from its parent's when a
	// task's run filed it (putTask), and never changed after.
	WorkOrigin WorkOrigin
	CreatedAt  *time.Time

	// OrderKey is this task's position in the backlog -- Store.Ready
	// dispatches ascending, so the task with the smallest OrderKey among
	// everything queued runs next (bwsalmon/agents#476). It is never a
	// timestamp: Store.OrderKeyForNewTask assigns a fresh one a fixed
	// step past whichever extreme is currently in play, and Store.Reorder
	// (a drag-and-drop move) rewrites it directly, so two tasks created
	// or moved in the same instant still compare distinctly.
	//
	// It is also where grain writes down its own ordering, rather than
	// keeping a second one to itself: Store.MoveToFrontOfBacklog puts the
	// tasks waiting on a repo's merge queue at the front of the backlog in
	// the order they will land -- which is also what dispatches a repair
	// promptly, since the task being repaired is the head of that queue
	// (orchestrator.requeueForRepair). Everything that decides what happens
	// next -- Ready, ListTasks, orchestrator.queueOrder -- then reads that
	// one column, which is the one a human can see and drag.
	OrderKey float64
}

// NormalizeReviewers trims, splits on commas, drops blanks and deduplicates a
// list of reviewer usernames, preserving order. It returns nil when none
// remain.
func NormalizeReviewers(in []string) []string {
	var out []string
	for _, entry := range in {
		for _, part := range strings.Split(entry, ",") {
			u := strings.TrimSpace(part)
			if u != "" && !slices.Contains(out, u) {
				out = append(out, u)
			}
		}
	}
	return out
}

// Observation is the half grain writes: what it has seen happen.
//
// Separate from Task because they answer to different records, and
// separate in the schema for the same reason — which is what would let a
// declaration change be branched and reviewed while observations keep
// landing on the trunk.
type Observation struct {
	TaskID      string
	ClosedAt    *time.Time
	CompletedAt *time.Time
	// Baselines, not beliefs: the highest comment id seen, against which
	// a fresh read is compared. Losing one degrades rather than corrupts.
	PendingQuestionCommentID *int64
	BaselineCommentID        *int64
	// DeliveredCommentID is the last comment on this task that has been
	// put in front of a run: folded into a dispatched prompt
	// (orchestrator's agent.RunConfig.AddendaAfter), handed to a run
	// mid-flight as an addendum on a tool result, or returned to it as the
	// reply to a chat call (ui.Client.RecordDelivered). It only moves
	// forward.
	//
	// It is what "has this message been answered?" is decided by when an
	// interactive run ends (orchestrator.undeliveredHumanComment): a
	// person's comment after it is one no run has seen, whenever it was
	// posted, and the task is dispatched again for it rather than parked.
	// Deciding that by when the run *ended* instead dropped a message
	// posted while a run was writing its last answer, since no tool call
	// was left to carry it (grain/task-1774).
	//
	// nil on a task no run has been dispatched for since the column was
	// added, which the readers take as "not recorded" and fall back to
	// their older timing rule for.
	DeliveredCommentID *int64
	// PendingSecret is the credential a parked run asked a human to set
	// -- mcp's request_secret escape hatch, relayed by
	// orchestrator.ProcessResult exactly the way a question is. It holds
	// a *name* in the form secrets.Store.Resolve takes ("stripe-api-key",
	// or "github-app/app-id"), never material: docs/data-model.md's "no
	// secret store in the model" is about values, and this is the same
	// kind of pointer a CredentialRef already is.
	//
	// It is set alongside PendingQuestionCommentID rather than instead of
	// it, because the parking is the same parking: the task sits in
	// awaiting_reply, out of task_ready, until a human acts. What this
	// field adds is *what* to offer them -- a UI holding it renders a
	// write-only box addressed at that name (ui.TaskDetail.PendingSecret)
	// instead of only a reply box, and the value it takes goes straight
	// into the secret store, never through the conversation and never
	// back to the run that asked.
	//
	// Cleared when the secret is set (ui.Client.SetPendingSecret) and
	// when a human replies in words instead (ui.Client.AddComment): both
	// un-park the task, and an input left offered on a task nobody is
	// waiting to hear from would be an offer to write a value nothing
	// asked for.
	PendingSecret string
	// MergeQueueBlockedAt is set once the merge queue has stopped driving
	// this task, for any of the three reasons it ever does: the repair
	// run it asked for finished and the PR is still conflicted or
	// failing, or that repair never finished at all within the time a
	// repair is given (orchestrator.defaultRepairDeadline), or the PR's
	// checks stayed unfinished for longer than the queue is willing to
	// wait on CI that may never report
	// (orchestrator.defaultCheckStallDeadline). The task's own thread
	// says which -- see orchestrator.SyncPullRequests. Either way
	// it no longer counts as any repo's queue head (so a stuck PR cannot
	// block the ones behind it) and gets no automatic repair from here
	// on, but it is still merged the moment it reads clean, in case a
	// human pushes the fix by hand or the checks it was waiting on turn
	// up green after all.
	//
	// Nothing in the queue ever clears it -- giving up is a decision made
	// once per pull request, and a queue that revisited it by itself
	// would be a queue with no way of asking for a person at all. The one
	// thing that does is a person asking for another attempt
	// (ui.Client.RetryMerge, the UI's "Retry merge"), which clears this
	// and the two stamps below together: whatever they were about to look
	// at again, they have already looked at.
	MergeQueueBlockedAt *time.Time
	// MergeQueueRepairAt is set the cycle the merge queue sent this task
	// back to an agent to repair its own pull request branch --
	// orchestrator.requeueForRepair, which clears CompletedAt so the
	// task reads 'queued' again and the next dispatch picks it up.
	//
	// It is the whole record of a repair, and it is read three ways.
	// While CompletedAt is nil it means "a repair is in flight": the task
	// is running (or queued, or waiting on an answer) on the very branch
	// its pull request is open from, so nothing merges that branch
	// underneath it and nothing files a second repair. Once CompletedAt
	// is set again, a repair has been run: if the pull request still
	// reads broken on the next cycle the queue gives up and asks a person
	// (MergeQueueBlockedAt above), because one repair is the whole policy
	// -- see orchestrator.requeueForRepair. And nothing in the queue
	// clearing it is what keeps that policy to one attempt per pull
	// request: only a human's own retry does (MergeQueueBlockedAt above),
	// and that is the whole of the second chance there is.
	//
	// It is also what makes the repair run a *merger*
	// (OriginReason.Merger's own doc comment): a task being repaired
	// draws on the capacity Limits.Mergers keeps back, where before this
	// the repair was a separate ReasonFix task and the reason column
	// alone answered it.
	//
	// Persisted, like MergeQueueRefreshedAt below and for the same
	// reason: losing it would cost a second repair of a branch that has
	// already had the one it gets.
	MergeQueueRepairAt *time.Time
	// MergeQueueState is the live action the automatic merge queue is
	// taking for this task, or the reason it is taking none:
	// "waiting_ci", "waiting_queue", "waiting_review", "merging",
	// "repair_needed", "repair_queued", "repairing", "agent_working",
	// "merge_failed" or "unreadable" -- orchestrator's own merge queue
	// states, which have the whole vocabulary and the cycle they form. It
	// is separate from StateOf because the agent's run is over in all of
	// them, except the repair states and "agent_working": there the task
	// really is running or queued again, on its own pull request's
	// branch, and this says which of the two the merge queue is waiting
	// through.
	//
	// Empty for a task the queue is not driving, which is two things and
	// no others: one that never asked to be merged automatically, and
	// one whose pull request merged or closed on the cycle that wrote
	// this. Every other combination of health, checks, review and queue
	// position has a state (orchestrator's statusOf is total over them),
	// because a row that says nothing is indistinguishable from a task
	// that is fine and quiet.
	MergeQueueState string
	// MergeQueueNote is that state said in words, for a person: which
	// checks are unfinished, which task is ahead of this one in its
	// repository's queue, what GitHub said when it refused the merge. It
	// is empty for a state that needs no elaboration, and empty whenever
	// MergeQueueState is.
	//
	// A sentence rather than a code because nothing reads it but a human:
	// the queue's own decisions are all made from the observations
	// themselves (health, checks, the two stamps above), and this is the
	// account of them that used to exist only in this process's log --
	// which is the wrong place for "what is my task waiting for?" to be
	// answered.
	MergeQueueNote string
	// MergeQueueReason is the sub-case of MergeQueueState the entry is
	// held in, as orchestrator's livenessreasons.go names them
	// ("waiting_ci/grain_running", "base_broken/base_repair_task"):
	// which of the state's several ways of waiting this one is, and so
	// which component is meant to end it. Written with the state, by the
	// cycle that knows the particulars (mergeReasonOf); empty whenever
	// MergeQueueState is, and on a row written before it was recorded.
	MergeQueueReason string
	// MergeQueueStateAt is when the task entered MergeQueueState, so the
	// UI can say how long it has been there. "Merging" said ten seconds
	// ago and the same word left standing for three hours mean opposite
	// things, and only the age tells them apart -- the same argument
	// Run.Activity's own timestamp makes, for the same reason.
	//
	// Stamped when the state changes rather than when the note does: a
	// refusal whose wording changes between attempts is the same wait
	// going on, and restarting its age would hide exactly the standing
	// still this exists to show. nil while there is no state.
	MergeQueueStateAt *time.Time
	// MergeQueueEnteredAt is when the merge queue first took this task on:
	// the first cycle it counted the task a member and placed it
	// (orchestrator.stampQueueEntry), or failing that the first cycle it
	// wrote the task a MergeQueueState (orchestrator.setMergeQueueState).
	// A member it has not stamped yet joins the queue behind every member
	// it has (orchestrator.queueOrder). Set once and never moved
	// -- not by a repair, not by a state change, not by the task leaving
	// the queue for a while -- so that merged minus this is the whole of
	// the time the change spent waiting on the queue
	// (metrics.MergeQueueContention.QueueWaitTime). CompletedAt is not
	// that: a repair clears and re-stamps it. nil for a task the queue
	// never took on, and for every row written before it existed.
	MergeQueueEnteredAt *time.Time
	// MergeQueueStalledAt is the moment this task's wait stopped being
	// one: it has been in MergeQueueState for longer than the component
	// driving that state should plausibly take, so the row should be read
	// as stuck rather than as progress. nil is the ordinary case -- a
	// state that is still inside its patience, and every state that has
	// none.
	//
	// orchestrator's liveness.go owns the judgement (every non-terminal
	// merge-queue state carries a patience, and the same classification
	// writes the sentence on MergeQueueNote); this is that judgement as a
	// fact rather than as prose, so that a reader which is not a person
	// can act on it. The UI is the reason it exists: a flagged entry kept
	// the live mark on its row -- the running dot that means "grain is
	// working on this now" -- because the mark is chosen from the state
	// alone, and only somebody who opened the task read the sentence
	// saying it was stuck (ui/src/state.js's mergeQueueStalled).
	//
	// The stamp is when the patience ran out (MergeQueueStateAt plus the
	// state's own patience), not when a cycle noticed. That makes it a
	// constant for as long as the entry sits there, so the row settles
	// instead of being rewritten every cycle, and it answers "since when
	// has this been stuck" rather than "when did grain last look".
	//
	// Cleared whenever the state changes, since entering a state starts
	// its clock afresh -- including the change into merge_failed, which is
	// the queue giving up rather than a wait that overran
	// (orchestrator.blockMergeQueue).
	MergeQueueStalledAt *time.Time
	// MergeQueueRefreshedAt is set the cycle the merge queue tried to
	// bring this task's pull request branch up to date with its base --
	// whether or not that merge landed, and whether or not it helped. It
	// is what stops a head whose refresh went red from being refreshed
	// again on the next cycle (orchestrator.refreshStaleHead).
	//
	// It is read against MergeQueueRepairAt above rather than on its own,
	// which is what makes the attempt one per *repair round* instead of
	// one per pull request: a stamp older than the repair was spent on the
	// branch as the agent found it, and says nothing about the tree that
	// agent then pushed. A head whose repair has run gets one more, since
	// the queue passes a head over while it is being repaired and so keeps
	// merging changes past it -- the base a resolution is judged against
	// has moved by this queue's own doing, and a merge is what nearly all
	// of those resolutions turn out to need
	// (docs/merge-queue-staleness.md). Two rounds is the whole of it:
	// refresh, repair, refresh, and then a person
	// (orchestrator.refreshSpent, MergeQueueBlockedAt above) -- and that
	// person asking for another attempt clears this alongside the other
	// two, which is what makes their retry a fresh round rather than a
	// task handed straight back to them.
	//
	// Persisted rather than kept in memory, unlike the two CI clocks
	// orchestrator.sync.go runs: losing one of those costs another window
	// of waiting, where losing this one costs a repeated *write to
	// GitHub*, so a process restarting in a loop would merge in a loop.
	MergeQueueRefreshedAt *time.Time
	// MergeQueueInherited names the grain CI jobs, comma-separated, that
	// this pull request's red verdict shares with its base branch's own
	// latest run -- the failures it inherited rather than caused
	// (orchestrator.inheritedFailures). Set while the task depends on the
	// base branch's repair task instead of spending a repair or blocking
	// (orchestrator.waitForBase), and read on the cycle that link clears
	// -- the repair task closing, which a green base does to it -- which
	// is what re-arms the entry. Empty on every other row.
	MergeQueueInherited string
	// MergeQueueBlockedBase is the base branch's tip the first cycle the
	// queue saw this entry blocked (MergeQueueBlockedAt), and
	// MergeQueueRearms how many times the queue has since put a blocked
	// entry back on its own because that base moved and the branch
	// merged it in cleanly. Bounded (orchestrator.maxBaseMovedRearms), and
	// both cleared by a person's "Retry merge": the bound is on re-arms
	// nobody asked for.
	MergeQueueBlockedBase string
	MergeQueueRearms      int
	// MergeQueueRepairs is how many automatic repairs the queue has sent
	// this pull request on (orchestrator.requeueForRepair,
	// sendBatchCulpritBack), bounded by orchestrator.maxAutomaticRepairs.
	// MergeQueueRepairAt above is when the latest one was asked for; this
	// is how many there have been. A row written before the count existed
	// with a repair stamp on it reads as one (RepairsSpent, and the
	// store's migration).
	//
	// MergeQueueRepairBase and MergeQueueRepairFailures are what the
	// latest repair was asked about -- the base branch's tip and the
	// failing jobs or checks, sorted and comma-separated -- so a second
	// repair can be allowed only when one of the two has since changed
	// (orchestrator.secondRepairReason), and MergeQueueRepairWhy is the
	// sentence saying which, for the row and the comment. All four are
	// cleared by a person's "Retry merge".
	MergeQueueRepairs        int
	MergeQueueRepairBase     string
	MergeQueueRepairFailures string
	MergeQueueRepairWhy      string
	// MergeQueueStallRetryAt is when the queue last answered a grain CI
	// stall on this pull request on its own instead of blocking it
	// (orchestrator.retryStalledCI): it either extended the deadline once
	// for a run that was still making progress or started a fresh run.
	// MergeQueueStallRetryHead is the head commit that retry was for. One
	// retry is given per commit, so a second stall at the same head
	// blocks. MergeQueueStallRetryWhy is the sentence saying what the
	// retry did, for the row and the block comment. All three are cleared
	// by a person's "Retry merge", like the repair bookkeeping above.
	MergeQueueStallRetryAt   *time.Time
	MergeQueueStallRetryHead string
	MergeQueueStallRetryWhy  string
	// MergeQueueCancelHandoffAt and MergeQueueCancelHandoffRun recorded
	// when, and for which run, the merge queue handed a grain CI run a
	// person cancelled back to the task's agent. Nothing writes them any
	// more: a cancellation by hand is now replaced like any other
	// (orchestrator.ciReplacementFor). They stay as columns so rows that
	// carry them still load.
	MergeQueueCancelHandoffAt  *time.Time
	MergeQueueCancelHandoffRun string
	// ForceMergeAt is an operator's outstanding "merge this anyway, here
	// is why": the one sanctioned exception to the merge predicate
	// (orchestrator's mergepredicate.go), asked for through
	// ui.Client.ForceMerge and nothing else.
	//
	// It is a request, not a merge. grain performs the merge itself, on
	// the next SyncPullRequests cycle, which is the whole point of the
	// control existing at all: the override was already happening -- a
	// person merging the pull request underneath grain by hand -- and
	// grain's own state went stale every time, because grain did not do
	// the merge and did not know it had happened. Here the task moves to
	// merged, the queue row closes, and post-merge dispatch and the
	// base-branch run fire exactly as they do for any other merge.
	//
	// Spent by the merge it authorises: the cycle that performs it clears
	// all five of these fields and writes a ForceMergeRecord instead, so
	// an override is worth exactly one merge. A request grain cannot
	// perform stays outstanding -- a pull request that does not merge
	// cleanly is refused by the predicate however loudly an operator
	// asks -- but only while the branch's head is the one it was given
	// for (ForceMergeHeadSHA): a push withdraws it.
	//
	// Never set by an agent or by a task. There is no MCP tool for it and
	// no field on a task that asks for one; the API route is an
	// operator's, the way "Retry merge" is.
	ForceMergeAt *time.Time
	// ForceMergeReason is what the operator typed, and it is required:
	// an override nobody wrote a reason for is indistinguishable
	// afterwards from a queue that merged something it should not have.
	// ui.Client.ForceMerge refuses an empty one, and the predicate
	// refuses a forced merge that carries none.
	ForceMergeReason string
	// ForceMergeBy is who asked, off the acting client's own actor.
	ForceMergeBy string
	// ForceMergeSkipped is what was not green when they asked, in words
	// -- the merge queue's own refusal at the time, which names the check
	// and what it concluded. Recorded with the reason so that the record
	// says what the override skipped rather than only that there was one.
	ForceMergeSkipped string
	// ForceMergeHeadSHA is the commit the override is for: the pull
	// request's head as it stood when the operator asked, which is what
	// they reviewed. The override authorises that commit and no other --
	// a push landing between the request and the cycle that acts on it
	// would otherwise merge code nobody looked at under somebody's name
	// (bwsalmon/grain#2399). orchestrator's mayMerge refuses a forced
	// merge whose head is not this one, and the cycle that sees the
	// branch has moved clears the stamp, so the operator re-reviews and
	// forces again. Empty only on a stamp written before this field
	// existed, which authorises nothing for the same reason.
	ForceMergeHeadSHA string
	StreakCount       int
	LastFailedAt      *time.Time
	LastSucceededAt   *time.Time
	ObservedAt        *time.Time
	// RetryRequestedAt is a human's "clear the failure streak and let it
	// try again" signal (Store.Retry) -- the only way a task stuck in
	// StateFailed becomes dispatchable again, since nothing else ever
	// resets task_streak's own count once it reaches MaxConsecutiveFailures.
	// It also bounds task_streak going forward: a failed run only counts
	// toward the streak if it started after the later of this and the
	// task's last succeeded run, so asking for a retry and then failing
	// again does not instantly re-cap the task on the very next attempt.
	RetryRequestedAt *time.Time
	// RunNowAt is a human's "start this one next, whatever the queue
	// thinks" signal (ui.Client.RunNow, the UI's "Run now" button and
	// `grain run-now`): an outstanding request for a run of this task,
	// which dispatch.Cycle starts ahead of the ready list and without
	// consulting task_ready at all.
	//
	// It is a declaration, unlike everything else in this struct, and it
	// lives here for RetryRequestedAt's own reason: it is read in the
	// same breath as the observations that decide whether a task may be
	// dispatched, and writing it through ObserveField is what lets one
	// call clear a completion and ask for a run together.
	//
	// It is consumed by the run it asks for: startRun clears it in the
	// same transaction that records the run, the way it clears a task's
	// grant wait, so a request is worth exactly one run. That is also
	// what stops a task whose run-now run finishes from being started
	// again on the very next cycle -- and what makes a request an
	// ordinary dispatch happens to satisfy first cost nothing extra.
	//
	// A run that ends without having been a run of the task -- it never
	// reached its agent, or the deployment stopped it (RunNowSurvives)
	// -- gives the request back as it records its outcome, with the
	// timestamp it was made at, so the task keeps its place at the front
	// (Store.FinishRun). Only those endings do, which is what keeps the
	// request from restarting a task every cycle.
	RunNowAt *time.Time
	// PrOpenedAt, PrMergedAt and PrClosedAt are the task's own tracked
	// pull request's history (LinkFixes), off github.PullRequestDetail's
	// own CreatedAt/MergedAt -- what lets a task's timeline show "PR
	// opened"/"PR merged"/"PR closed unmerged" (bwsalmon/agents#493), none
	// of which TrackedPullRequest itself can answer since that type is
	// deliberately never stored (its own doc comment). Unlike Health,
	// which is re-read every cycle because it can still change, these are
	// each a fact about one moment in the past that orchestrator.
	// SyncPullRequests writes once, the first cycle it observes each one,
	// and never revisits.
	//
	// PrMergedAt and PrClosedAt are mutually exclusive -- a pull request
	// either merged or it did not -- and distinct from ClosedAt above:
	// ClosedAt is this task's own closure, set on a task with no pull
	// request at all just as often as on one whose PR closed, where
	// PrClosedAt means specifically "the pull request closed without
	// merging."
	PrOpenedAt *time.Time
	PrMergedAt *time.Time
	PrClosedAt *time.Time
	// PrAdditions, PrDeletions and PrChangedFiles are how big that pull
	// request's diff was -- GitHub's own additions/deletions/
	// changed_files, off the same detail response the three moments
	// above come from (github.PullRequestDetail.Additions), written the
	// one cycle orchestrator.SyncPullRequests observes the merge and
	// never revisited.
	//
	// They exist so a week's release notes can report lines changed
	// (Store.ClusterStats), which is the one figure an operator asked
	// for that grain had no record of. Recorded at the merge rather than
	// counted later because it is a fact about a diff that is about to
	// stop being reachable: heads get deleted, branches get rewritten,
	// and the pull-request detail is the only place the number survives.
	//
	// Pointers, and the nil is load-bearing. Every row written before
	// the columns existed carries NULL in all three, and that is "grain
	// never recorded this pull request's size" -- which is not the same
	// statement as a pull request that changed nothing, and must not be
	// summed as if it were. A window containing any such merge reports
	// its lines-changed total as partial (ClusterStats.
	// PullRequestsMeasured). A detail response that carried no size at
	// all is left nil for the same reason.
	PrAdditions    *int
	PrDeletions    *int
	PrChangedFiles *int
	// CI is what grain's own CI last said about the branch this task
	// pushed -- see CIStatus, which has the whole argument for why the
	// verdict is copied onto the task rather than linked to, and why
	// there is one of these rather than a history.
	//
	// nil for the overwhelming majority of tasks: it is written only for
	// a task holding the grain-ci capability whose branch grain has
	// actually put through CI (orchestrator.reconcileCI).
	CI *CIStatus
	// SuspendedSession is the agent conversation this task's last run
	// left parked, for its next run to be resumed into rather than
	// starting the task's thinking over -- see SuspendedSession, which
	// has the whole of what one is and why only some frameworks can
	// leave one.
	//
	// nil is the state "this task has no suspended session": every task
	// before its first run, every task whose last run finished the work
	// or was cancelled with the task itself, and every task on a
	// framework whose CLI keeps no session grain could reopen. It is an
	// observation rather than a declaration for the reason everything
	// else in this struct is one -- nobody decides that a task has a
	// suspended session, grain notices that a run left one.
	SuspendedSession *SuspendedSession
}

// MergeQueueActivity is the four merge-queue fields a list view reads
// together -- Observation.MergeQueueState, MergeQueueNote,
// MergeQueueStateAt and MergeQueueStalledAt -- carried as one value so
// that Store.MergeQueueStates can answer for a whole store in one query
// without its callers having to keep four maps in step.
//
// The zero value is "the merge queue is doing nothing about this task",
// which is the answer for nearly every task there is.
type MergeQueueActivity struct {
	State string
	Note  string
	At    *time.Time
	// StalledAt travels with the other three because it is read in the
	// same breath: a row draws its state, how long it has said it, and
	// whether that is still a wait at all (Observation.MergeQueueStalledAt).
	StalledAt *time.Time
	// Reason is the state's sub-case (Observation.MergeQueueReason).
	Reason string
}

// RepairInFlight reports whether the merge queue has sent this task back
// to an agent to repair its own pull request branch and that repair has
// not finished yet: MergeQueueRepairAt is set and the task has neither
// completed nor closed since.
//
// It is one question with three readers -- orchestrator.
// advanceMergeQueueHead, which waits on it; orchestrator.syncEntry, which
// will not merge a branch an agent is still pushing to; and ui.Client,
// which shows such a task as being repaired rather than as merely running
// -- so it is answered here, off the pair of observations that record it,
// rather than re-derived from the task's state, which says "running" for
// a first attempt just as readily as for a repair. Store.
// MergeQueueRepairing is the same rule as SQL, for a caller listing every
// task at once; the two are edited together, the way StateOf and
// task_state are.
//
// The CompletedAt-before-MergeQueueRepairAt arm is belt and braces:
// orchestrator.requeueForRepair clears CompletedAt in the same write that
// sets MergeQueueRepairAt, so an unfinished repair reads nil here. A
// completion stamped before the repair was asked for could only come from
// a write that raced that one, and it is not evidence the repair has run.
func (o *Observation) RepairInFlight() bool {
	if o == nil || o.MergeQueueRepairAt == nil || o.ClosedAt != nil {
		return false
	}
	return o.CompletedAt == nil || o.CompletedAt.Before(*o.MergeQueueRepairAt)
}

// RepairsSpent is how many automatic repairs this pull request has had:
// MergeQueueRepairs, or one for a row stamped with a repair before the
// count was kept.
func (o *Observation) RepairsSpent() int {
	if o == nil {
		return 0
	}
	if o.MergeQueueRepairs == 0 && o.MergeQueueRepairAt != nil {
		return 1
	}
	return o.MergeQueueRepairs
}

// TaskSpan is a task's own stretch of the calendar: when work on it
// actually began, and when it stopped being work in progress. It is what
// a timeline drawn over many tasks at once needs and cannot derive from
// a task's state -- a state says where a task is now, not when it got
// there (Store.TaskSpans, ui/src/gantt.js).
//
// StartedAt is the first attempt's StartedAt: the moment dispatch picked
// the task up, which is where the queue ends and the work begins. nil for
// a task that has never been dispatched -- a proposal, or anything still
// waiting its turn -- which is a real answer and not a missing one.
//
// EndedAt is the earlier of Observation.CompletedAt and
// Observation.ClosedAt, and nil while neither is set. The earlier of the
// two, because a task that completed and was then closed stopped being
// work in progress when it completed; the closure that followed is
// bookkeeping over a finished thing. nil means the task is still open,
// so whoever is drawing it decides how far to run the bar -- "to now" is
// the honest reading, and it goes on growing.
//
// Neither field is derived from the run *list*: the last attempt's
// FinishedAt is deliberately not an ending, because a task whose attempt
// failed is still open, still somebody's problem, and its bar has not
// stopped.
type TaskSpan struct {
	StartedAt  *time.Time
	EndedAt    *time.Time
	ClosedAt   *time.Time
	PrMergedAt *time.Time
	PrClosedAt *time.Time
}

// Run is one attempt. A live run is a Run with no FinishedAt.
type Run struct {
	ID     string
	TaskID string
	// Sandbox is the sandbox this run was given -- the kontur VM name, or
	// the host directory's own name. It used to sit alongside a Slot, the
	// concurrency unit a long-lived sandbox was reused under, the two
	// being equal in practice and distinct only in principle. A sandbox
	// created and destroyed with the run has no such unit to belong to,
	// so only this half is left, and it names something that exists for
	// exactly as long as the run does.
	//
	// It is written after the row is: dispatch records a run before any
	// sandbox exists for it, and orchestrator fills this in via
	// SetRunSandbox once one has actually been acquired. A live run with
	// an empty Sandbox is one whose sandbox is still being built -- which
	// gitproxy reads, correctly, as a sandbox identity that authorizes
	// nothing (Store.GitScope).
	Sandbox string
	Unit    string
	Attempt int
	// Model and AgentFramework are what this run actually ran on, as
	// dispatch resolved them (Store.SetRunModel) -- not what the task asks
	// for, which may be the deployment's default list or the rotation and
	// so come out differently from one attempt to the next. "" for a run
	// recorded before they were kept, or whose framework named neither.
	Model          string
	AgentFramework string
	StartedAt      time.Time
	FinishedAt     *time.Time
	Outcome        string
	// Detail is a short, human-readable explanation of how Outcome was
	// reached -- the agent framework's own error text ("exceeded max
	// turns (20) without a final answer"), a tool error, or
	// ProcessResult's own "finished without pushing, asking, or
	// commenting" -- recorded so an operator (or the person who filed the
	// task) can see why a run failed from `grain get` without reading
	// graind's own stdout, which per README's security design is not
	// necessarily somewhere they can reach at all.
	//
	// A succeeded run fills it in too, with the tools it called and how
	// often (orchestrator.outcomeOf). "How Outcome was reached" is the
	// honest reading of the field, not "why it went wrong", and success is
	// the ending where nothing else survives: agent.Result is discarded,
	// so without this there was no stored answer to "did this run ever
	// call the tool we built for it" for the runs that worked.
	//
	// Its own transcript -- the agent framework's full narrative record of
	// the run, agent.Result.Transcript -- is not a field here: unlike
	// Detail, nothing needs it alongside a whole Run, only on its own
	// (bwsalmon/agents#446's "show attempt agent logs" pane), so
	// Store.SetRunTranscript/RunTranscript read and write it directly by
	// task ID and attempt number instead.
	Detail string
	// SuspendKind is the SuspensionKind the run was suspended with
	// (task_run.suspend_kind), or "" for a run that was not suspended --
	// still live, or judged a success or a failure.
	SuspendKind SuspensionKind
	// Activity is the one-line synopsis of what this run is doing at the
	// moment -- "waiting for CI on the third push", "reading
	// pkg/orchestrator" -- and ActivityAt is when it was last written.
	//
	// Two hands write it, and which of them is answerable from the row
	// rather than from the words. While task_run.agent_started_at is
	// still unset it is grain's own narration of the run's setup, stamped
	// from the dispatch path (orchestrator.setupNotes: "building a
	// sandbox", "cloning acme/widgets"), because the stretch before the
	// agent's first turn is exactly the one no run can narrate for
	// itself. After it, it is the run's own account of itself, written
	// through the update_status tool and nothing else -- the handover
	// clears grain's last phrase in the same breath as
	// Store.SetRunAgentStarted, so a note standing beside a stamped
	// agent_started_at is always the agent's own (RunActivity.BySetup).
	//
	// Both are empty for a run that never said anything, which is no kind
	// of signal: a framework with no route back to the daemon cannot carry
	// the call at all (see mcp.NewStatusTools), and every run recorded
	// before the columns existed reads the same way.
	//
	// ActivityAt is what makes the phrase readable. "Waiting for CI" said
	// ten seconds ago and the same words left standing for an hour mean
	// opposite things, and only the timestamp tells them apart.
	Activity   string
	ActivityAt *time.Time
	// AgentStartedAt is when this run's agent got its first turn
	// (task_run.agent_started_at), and nil for a run still in setup and
	// for one that never reached its agent at all -- a sandbox that would
	// not build, a checkout that would not clone.
	//
	// It is carried alongside Activity because it is what says whose
	// phrase that is: the row-level form of the distinction
	// RunActivity.BySetup draws for a live run. A note standing on a run
	// with no AgentStartedAt is one grain stamped during setup, and on a
	// *finished* run it is the only record of how far setup got before it
	// broke (ui.Attempt.SetupNote, which is exactly that pairing).
	AgentStartedAt *time.Time
	Leases         []Lease
}

// RunActivity is Run.Activity and Run.ActivityAt on their own -- what a
// live run says it is doing, without the run around them.
//
// It exists because that pair is what a reader of a *task* wants: the
// task list asks "what is each running task up to?" and has no use for
// the attempt number, the sandbox or the outcome that would come with a
// whole Run (Store.TaskActivity, ui.Task.Activity).
//
// At is a pointer for the same reason it is on Run: a row written before
// the columns existed, or by a build that only set one of them, has a
// note and no time, and a zero time.Time rendered as "written in year 1"
// would be worse than showing nothing at all.
type RunActivity struct {
	Note string
	At   *time.Time
	// BySetup says the phrase is grain's rather than the run's: one of
	// orchestrator.setupNotes' own, stamped while the run's sandbox was
	// still being built, its repo cloned or its credentials minted.
	//
	// It is read off the run rather than off the note -- true exactly
	// while task_run.agent_started_at is unset, which is the moment
	// grain hands the run over and clears whatever it last said (see
	// Run.Activity). That is why no prefix or separate column is needed
	// to tell the two apart, and why an agent cannot claim to be grain by
	// writing "grain:" into its own status: the only notes standing
	// before an agent exists are the ones grain wrote.
	//
	// Everything shown on a task row so far is something an agent said,
	// so the distinction is worth carrying to the reader rather than
	// quietly blurring: the UI marks such a phrase as grain's
	// (ui.Task.ActivityBySetup, TaskList.jsx).
	BySetup bool
}

// --- conversation ----------------------------------------------------

// Comment is one entry in a task's conversation: what a human asked for,
// what grain relayed on an agent's behalf, why the merge queue gave up.
//
// This is grain's own record, not a copy of one. v2 used to hold the
// conversation in a GitHub issue's comment thread and read it back as
// input -- which made the issue a second place a task's state lived, and
// made "has a human replied yet?" a question only a poll could answer.
// A task is a row here; so is everything said about it.
//
// The author is an Attribution rather than a bare Principal because the
// distinction is load-bearing exactly here: grain relaying an agent's
// question is (automation, on behalf of agent), and a human answering it
// directly is (human, nil). That is the difference a signature substring
// in a comment body used to gesture at with one bit.
type Comment struct {
	// ID is assigned by the store on write, and ordering by it is the
	// conversation's order. Observation.PendingQuestionCommentID names one
	// of these.
	ID        int64
	TaskID    string
	Author    Attribution
	Body      string
	CreatedAt time.Time
	// Handover marks a comment a run wrote as a standing summary of the
	// older part of this thread -- the decisions, the invariants, the
	// names -- for the next run on the task rather than for a person
	// (mcp's write_handover, relayed by orchestrator.ProcessResult).
	//
	// It is a bit on the comment rather than a table of its own because a
	// handover *is* a comment: a human reads it in the conversation where
	// it was written, in order, like everything else said about the task.
	// What the bit buys is that one reader can find it again --
	// orchestrator.commentThreadSection carries the most recent one
	// outside the head/tail budget it bounds the rest of the thread by,
	// so the one message written to survive the thread being cut is not
	// the message the cut drops.
	//
	// Nothing else in grain treats it specially, and nothing depends on
	// one existing: a handover no run ever wrote leaves the bound doing
	// exactly what it did before (conversation.go's own doc comment on
	// why the bound holds regardless).
	Handover bool
	// Summary is the one line a question is shown by where there is room
	// for no more of it -- the inbox row, the task list's status line, a
	// notification's title -- with Body behind an expander
	// (question_summary.go). Set on the questions a run asks
	// (ask_question, request_secret, chat, POST /api/tasks/{id}/messages);
	// "" on every other comment, and on every question stored before the
	// column existed, which a reader derives one for instead
	// (DeriveQuestionSummary).
	Summary string
}

// Attachment is one file carried alongside a task -- bwsalmon/agents#522's
// "attach files (images, zip files, etc) to a task" and "attachable to
// follow-on comments in addition to the main task content." Content lives
// here, in the store, for the same reason Comment's own doc comment gives
// for the conversation itself: a task is a row, and so is everything
// carried with it, rather than a pointer to somewhere else that has to
// stay reachable to read it back.
//
// CommentID is nil for a file carried by the task's own body, and names a
// Comment.ID for one posted alongside a later comment -- one table for
// both rather than two, since a dispatched run materializing them into
// its sandbox (orchestrator's AttachmentsDir) treats every attachment
// exactly alike regardless of which one it came from.
type Attachment struct {
	ID          int64
	TaskID      string
	CommentID   *int64
	Filename    string
	ContentType string
	Size        int64
	Content     []byte
	CreatedAt   time.Time
}

// BranchName is the historical branch a task's work goes on. It remains
// the fallback for old stored tasks and callers constructing a Task directly;
// persisted tasks use Task.BranchName instead.
func BranchName(taskID string) string { return "grain/task-" + taskID }

// BranchName returns the branch fixed onto this task, falling back to the
// historical name for rows written before the branch column existed.
func (t Task) BranchName() string {
	if t.Branch != "" {
		return t.Branch
	}
	return BranchName(t.ID)
}

// CIOwner is the task whose grain CI verdict t's branch is judged by:
// t's own ID, except for a review working on the reviewed change's own
// branch (LinkPushesReviewedBranch), whose branch -- and so whose CI --
// is the reviewed task's. Such a review never owns a CI status of its
// own: everything that starts, records or reads one for it acts on the
// reviewed task's instead, so one push to the shared branch is one run.
// inPlace reports which of the two this is.
func (t Task) CIOwner() (owner string, inPlace bool) {
	if t.Origin.Reason == ReasonReview {
		for _, l := range t.Links {
			if l.Kind == LinkPushesReviewedBranch && l.Target != "" {
				return l.Target, true
			}
		}
	}
	return t.ID, false
}

// FormatBranchName expands a deployment's task branch template. Empty is
// the compatible default. {id} is the task id, {deployment} is the
// deployment id a system admin can edit (Config.DeploymentID, through
// EffectiveDeploymentID) and {instance} is the durable grain installation
// id; everything else, including a username, is literal.
//
// It is FormatTaskBranchName for a task with no title, so a {slug} in
// the template expands to nothing.
func FormatBranchName(template, instanceID, deploymentID, taskID string) (string, error) {
	return FormatTaskBranchName(template, instanceID, deploymentID, taskID, "")
}

// FormatTaskBranchName is FormatBranchName with the task's title, which
// a {slug} in the template expands to (BranchSlug): a few words of it,
// so a branch -- and the pull request GitHub lists under it -- says what
// it is about and not only which number it is, grain/prod/task-56-fix-
// header-parser rather than grain/prod/task-56. {id} stays required and
// {slug} may only end the template, after {id} and a hyphen
// (checkSlugPlacement), so the name is unique whatever the title says. A
// title with nothing usable in it expands {slug} to nothing, taking the
// hyphen in front of it along so the name does not end in a stray one.
func FormatTaskBranchName(template, instanceID, deploymentID, taskID, title string) (string, error) {
	if template == "" {
		return BranchName(taskID), nil
	}
	if !strings.Contains(template, "{id}") {
		return "", fmt.Errorf("branch template must contain {id}")
	}
	if err := checkSlugPlacement(template); err != nil {
		return "", err
	}
	template = expandSlug(template, BranchSlug(title))
	name := strings.NewReplacer("{instance}", instanceID, "{deployment}", deploymentID, "{id}", taskID).Replace(template)
	if strings.ContainsAny(name, "{}") {
		return "", fmt.Errorf("branch template contains an unknown placeholder")
	}
	if !ValidBranchName(name) {
		return "", fmt.Errorf("branch template produces invalid branch name %q", name)
	}
	return name, nil
}

// MaxBranchSlugLen bounds BranchSlug: long enough for three or four
// words of a title, short enough that the branch name stays readable in
// a pull request list and a terminal prompt.
const MaxBranchSlugLen = 30

// BranchSlug is the short, branch-safe form of a task title that {slug}
// in a task branch template expands to: lowercase ASCII letters and
// digits, every other run of characters one hyphen, cut at a word
// boundary to at most MaxBranchSlugLen characters. "Fix the off-by-one
// in the header parser" is "fix-the-off-by-one-in-the". Empty when the
// title has no ASCII letter or digit in it at all.
func BranchSlug(title string) string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	var slug strings.Builder
	for _, w := range words {
		n := slug.Len()
		if n > 0 {
			n++
		}
		if n+len(w) > MaxBranchSlugLen {
			if slug.Len() == 0 {
				// One word longer than the whole budget: keep what
				// fits of it rather than nothing.
				slug.WriteString(w[:MaxBranchSlugLen])
			}
			break
		}
		if slug.Len() > 0 {
			slug.WriteByte('-')
		}
		slug.WriteString(w)
	}
	return slug.String()
}

// checkSlugPlacement refuses a template whose {slug} would let a task's
// title decide a name that is, or could be, another task's. A slug is
// whatever words the person (or agent) filing the task chose, so it may
// hold digits and may spell out another deployment's id. {slug} is
// therefore accepted in one place only: once, at the very end of the
// template, straight after {id} and a hyphen, in a last path segment
// that holds no {deployment} or {instance} --
// grain/{deployment}/task-{id}-{slug}.
//
// That is what makes the name unique whatever the title says:
//
//   - Touching {id} it would run into it: task-{id}{slug} names task 5
//     titled "6 fix" and task 56 titled "fix" both task-56-fix. With a
//     hyphen between, the id is the whole run of digits where the
//     template puts it.
//   - In front of the id, or as a path segment of its own, it could
//     spell out what another deployment puts there: grain/{slug}/task-{id}
//     names task 5 titled "d4b120da" grain/d4b120da/task-5, deployment
//     d4b120da's task 5. After the id, at the end, it only ever follows
//     the name's fixed text and the task's own number.
//   - A {deployment} or {instance} next to it in the last segment lets
//     the two run together the same way: {deployment}-{id}-{slug} names
//     deployment x's task 5 titled "7 y" x-5-7-y, deployment x-5's task 7
//     titled "y". A '/' ends a deployment id, which a slug never holds.
//
// The hyphen is the one separator because it is the one the branch
// readers take a slug off after (NormalizeTaskID, revert_task's git-log
// search); a dot would also let a title of "lock" end the name in .lock,
// which git refuses, so the task could not be stored.
func checkSlugPlacement(template string) error {
	const slug = "{slug}"
	switch strings.Count(template, slug) {
	case 0:
		return nil
	case 1:
	default:
		return fmt.Errorf("branch template may contain {slug} only once")
	}
	if !strings.HasSuffix(template, "{id}-"+slug) {
		return fmt.Errorf("branch template may only end in {slug}, straight after {id} and a hyphen " +
			"(as in grain/{deployment}/task-{id}-{slug}), so a task's title can never make another task's branch name")
	}
	last := template[strings.LastIndex(template, "/")+1:]
	if strings.Contains(last, "{deployment}") || strings.Contains(last, "{instance}") {
		return fmt.Errorf("branch template must keep {deployment} and {instance} out of the path segment that ends in {slug} " +
			"(as in grain/{deployment}/task-{id}-{slug}), so a task's title can never make another deployment's branch name")
	}
	return nil
}

// expandSlug replaces {slug} in a branch template that checkSlugPlacement
// accepted with slug. An empty slug takes the hyphen in front of the
// placeholder with it, so task-{id}-{slug} for an untitled task is
// task-{id}, not task-{id}-.
func expandSlug(template, slug string) string {
	const placeholder = "{slug}"
	i := strings.Index(template, placeholder)
	if i < 0 {
		return template
	}
	if slug != "" {
		return template[:i] + slug + template[i+len(placeholder):]
	}
	start := i
	if start > 0 && template[start-1] == '-' {
		start--
	}
	return template[:start] + template[i+len(placeholder):]
}

// deploymentIDDefaultLen is how much of the installation id the default
// deployment id keeps: 8 hex digits is short enough to read in a branch
// name and leaves two installations a one in four billion chance of
// drawing the same one.
const deploymentIDDefaultLen = 8

// MaxDeploymentIDLen bounds a deployment id a system admin sets.
const MaxDeploymentIDLen = 32

var deploymentIDSafe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// EffectiveDeploymentID is the deployment id branch names use: configured
// when a system admin has set one, and otherwise the start of the random
// installation id, so every deployment has a distinct one from the moment
// its store is created without anyone choosing it.
func EffectiveDeploymentID(configured, instanceID string) string {
	if configured != "" {
		return configured
	}
	if len(instanceID) > deploymentIDDefaultLen {
		return instanceID[:deploymentIDDefaultLen]
	}
	return instanceID
}

// ValidateDeploymentID refuses a deployment id that could not sit in a
// branch name as one path segment: lowercase letters, digits and inner
// hyphens only, at most MaxDeploymentIDLen of them. Empty is valid and
// means the default.
func ValidateDeploymentID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > MaxDeploymentIDLen {
		return fmt.Errorf("must be at most %d characters", MaxDeploymentIDLen)
	}
	if !deploymentIDSafe.MatchString(id) {
		return fmt.Errorf("may contain only lowercase letters, digits and hyphens, and must start and end with a letter or digit")
	}
	return nil
}

// NormalizeTaskID normalizes a task ID or reference string into its canonical ID,
// stripping URL schemes/hosts, and common prefixes like "grain/task-",
// "grain/<deployment>/task-", "task-", "task/", or "#".
//
// A URL only names a task when it is a task page: its path (or, for a
// hash-routed UI, its fragment) has a "tasks", "task" or "t" segment,
// and the id is what follows it. Any other URL is returned as it was
// given, so it resolves to nothing and is refused or noted like any other
// unknown id. Taking the last segment of every URL instead read
// https://github.com/acme/widgets/pull/12 as task 12 -- a depends_on that
// silently blocked on, or a supersedes that closed, an unrelated task.
func NormalizeTaskID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	fromURL, fromBranch := false, false
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			ref, ok := taskPageRef(u.Fragment)
			if !ok {
				ref, ok = taskPageRef(u.Path)
			}
			if !ok {
				return s
			}
			s, fromURL = ref, true
		}
	}
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "?"); idx != -1 {
		s = s[:idx]
	}
	for {
		orig := s
		s = strings.TrimPrefix(s, "#")
		// A branch named by DefaultTaskBranchTemplate,
		// grain/<deployment>/task-<id>: the deployment segment is not
		// part of the id.
		if m := deploymentTaskBranch.FindStringSubmatch(s); m != nil {
			s = m[2]
		}
		s = strings.TrimPrefix(s, "grain/")
		if t := strings.TrimPrefix(s, "task-"); t != s {
			s, fromBranch = t, true
		}
		s = strings.TrimPrefix(s, "task/")
		s = strings.TrimPrefix(s, "task:")
		s = strings.TrimSpace(s)
		if s == orig {
			break
		}
	}
	if fromURL {
		// A page under the task's own, such as /tasks/12/runs.
		s, _, _ = strings.Cut(s, "/")
	}
	if fromBranch {
		// A branch named with {slug} (FormatTaskBranchName),
		// task-56-fix-header-parser: the title's words are not part of
		// the id, which task_sequence only ever makes digits.
		if m := sluggedTaskBranchID.FindStringSubmatch(s); m != nil {
			s = m[1]
		}
	}
	return s
}

// sluggedTaskBranchID matches what is left of a task-<id>-<slug> branch
// once NormalizeTaskID has taken its prefix off: a numeric id, then a
// hyphen and BranchSlug's lowercase letters, digits and hyphens.
var sluggedTaskBranchID = regexp.MustCompile(`^([0-9]+)-[a-z0-9][a-z0-9-]*$`)

// deploymentTaskBranch matches grain/<deployment>/task-<rest>, with the
// deployment segment shaped the way ValidateDeploymentID requires (or the
// hex default EffectiveDeploymentID derives).
var deploymentTaskBranch = regexp.MustCompile(`^grain/[a-z0-9]([a-z0-9-]*[a-z0-9])?/(task-.*)$`)

// taskPageRef is what follows the last "tasks", "task" or "t" segment of
// a URL path or fragment, and false when it has none.
func taskPageRef(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(parts) - 2; i >= 0; i-- {
		switch parts[i] {
		case "tasks", "task", "t":
			return strings.Join(parts[i+1:], "/"), true
		}
	}
	return "", false
}
