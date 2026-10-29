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
	"errors"
	"fmt"
	"sync"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// SandboxRebuilder is the optional half of Sandbox that can throw a
// sandbox's contents away and bring an empty one back under the same
// name -- what the recreate_sandbox tool (pkg/mcp) needs underneath it,
// and the one thing a run cannot do for itself from inside its own
// sandbox: an agent that has wedged its VM, filled its disk, or is
// sitting on a guest that stopped answering has no command it can run
// *in there* to fix any of it.
//
// Both backends implement it, and both do it by the same route their own
// Acquire already takes -- granuleSandbox by deleting its VM and creating
// it again, hostSandbox by removing its directory and making it again --
// so what comes back is the same empty sandbox this run started with
// rather than a repaired version of the broken one. Repair is not on
// offer here and deliberately so: the whole value of the operation is
// that nothing of the old sandbox survives it.
//
// The name is what makes this possible at all without the run's tools
// going stale. A sandbox is addressed by name (a directory path, or a
// granule VM whose container name follows from the VM name), never by a
// handle to the particular filesystem or guest behind it, so the tools
// already handed to this run -- and the ones its forked mcpserver holds
// in a separate process, which nothing here could reach to replace --
// address the new sandbox the moment it exists.
type SandboxRebuilder interface {
	// Rebuild destroys this sandbox and builds an empty one under the
	// same name, returning only once the replacement is usable. A
	// non-nil error leaves the sandbox in whatever state the failure
	// found it -- possibly gone -- and the run holding it should be
	// treated as having no usable sandbox at all.
	Rebuild(ctx context.Context) error
}

// SandboxRecreation is what one SandboxRecreations.Recreate call did:
// the sandbox it rebuilt, and how much of the setup grain had put into
// the old one it managed to put back.
//
// Restored and Warnings are prose, one entry per step, because the
// audience is the agent that asked (pkg/mcp's renderSandboxRecreation)
// and what it needs is not a status code but an account of what its
// sandbox now contains. They are deliberately not exhaustive of what was
// lost: everything the *agent* put in the old sandbox is gone by
// definition, which the tool's own description and its rendered answer
// both say outright rather than trying to enumerate.
type SandboxRecreation struct {
	// Sandbox is the rebuilt sandbox's own name -- the run's ID, which
	// is unchanged by the rebuild and is the whole reason the run's
	// existing tools still reach it.
	Sandbox string
	// CheckoutDir is where the task's repo was cloned again, relative to
	// the sandbox's working directory (CheckoutDir, "work"), or "" when
	// there was nothing to clone (a task with no target, or a deployment
	// running no git proxy) or the clone failed -- in which case
	// Warnings says so.
	CheckoutDir string
	// Restored names each piece of the run's setup that is back in
	// place, in the order it was restored.
	Restored []string
	// Warnings names each piece that is not, with the reason. A warning
	// is never fatal to the call: the rebuild has already happened by
	// the time any of these can be produced, and reporting a failed
	// re-clone as an error would hide from the caller both that its
	// sandbox is now empty and whatever else did get restored -- the
	// same reasoning OpenPullRequestForTask's own ChecksError follows.
	Warnings []string
}

// SandboxRecreations is the set of live runs whose sandbox can be
// destroyed and rebuilt on request, keyed by task ID.
//
// It exists because the run that wants this cannot ask its own sandbox
// for it. The agent reaches its sandbox through an mcpserver process
// (pkg/cli/mcpserver.go) that holds nothing but a transport into the
// guest; creating and destroying sandboxes is the daemon's, and only the
// daemon knows this run's shape, its proxy token, its capabilities and
// where its repo came from. So the tool asks the daemon over its REST
// API -- exactly the hop open_pull_request already makes, and for the
// same reason: this is a write, and writes stay grain's.
//
// Keyed by task rather than by run because the task id is all the asking
// process is ever told (mcpserver's own -task), and one task has at most
// one live run at a time (dispatch.Busy, InFlight.Busy).
//
// The zero value is not usable; NewSandboxRecreations builds one. A nil
// *SandboxRecreations is, though: every method tolerates one, so a
// deployment or test that wires none simply never offers the tool
// (Config.SandboxRecreations).
type SandboxRecreations struct {
	mu   sync.Mutex
	live map[string]*sandboxRecreation
}

// NewSandboxRecreations returns an empty registry, ready for runs to
// register themselves in as they are dispatched.
func NewSandboxRecreations() *SandboxRecreations {
	return &SandboxRecreations{live: map[string]*sandboxRecreation{}}
}

// register makes taskID's live run recreatable and returns the function
// that stops it being so, which the dispatch goroutine defers.
//
// Unregistering waits for a recreate already in flight to finish rather
// than cutting it off (rec.close), so the sandbox release that follows
// it in runOne can never delete a VM that is halfway through being
// rebuilt.
func (r *SandboxRecreations) register(taskID string, rec *sandboxRecreation) func() {
	if r == nil {
		return func() {}
	}
	r.mu.Lock()
	r.live[taskID] = rec
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		// Compared, not deleted blindly: a redispatch of the same task
		// registers under the same key, and a slow unregister from the
		// previous run must not drop the new run's entry.
		if r.live[taskID] == rec {
			delete(r.live, taskID)
		}
		r.mu.Unlock()
		rec.close()
	}
}

// lookup returns taskID's registered run, if it has one.
func (r *SandboxRecreations) lookup(taskID string) *sandboxRecreation {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live[taskID]
}

// lookupBySandbox returns the registered run whose sandbox is named
// sandboxName, if there is one.
func (r *SandboxRecreations) lookupBySandbox(sandboxName string) *sandboxRecreation {
	if r == nil || sandboxName == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.live {
		if rec != nil && rec.sandbox != nil && rec.sandbox.Name() == sandboxName {
			return rec
		}
	}
	return nil
}

// RecreateBySandbox rebuilds the sandbox of the live run holding sandboxName
// and restores its setup. It reports ok=false when no live run is registered
// under sandboxName.
func (r *SandboxRecreations) RecreateBySandbox(ctx context.Context, sandboxName string) (SandboxRecreation, bool, error) {
	rec := r.lookupBySandbox(sandboxName)
	if rec == nil {
		return SandboxRecreation{}, false, nil
	}
	out, err := rec.recreate(ctx)
	return out, true, err
}

// setMaterialized records the capabilities RunDispatch materialized for
// taskID's run, so a rebuild can write their sandbox-side placements
// back into the new sandbox.
//
// It is a second call rather than part of register because the two facts
// become known in different places: runOne has the sandbox, and
// RunDispatch (below it) is what resolves and materializes the grants.
// Re-materializing at rebuild time is not an option -- that mints fresh
// credentials and leases behind the back of the revoke this run will do
// exactly once -- so what gets rewritten is the very same already-minted
// material, which is idempotent.
//
// A no-op for a nil registry, an unregistered task, or a run with no
// capabilities, which is most of them.
func (r *SandboxRecreations) setMaterialized(taskID string, materialized []model.Materialized) {
	rec := r.lookup(taskID)
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.materialized = materialized
}

// setCapabilityPass records the pass materialized into taskID's run's
// sandbox, so a rebuild can apply it to the new one without minting
// again (docs/capabilities-spec.md §4.4.3).
func (r *SandboxRecreations) setCapabilityPass(taskID string, pass *passMaterialization) {
	rec := r.lookup(taskID)
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.capabilityPass = pass
}

// errNoLiveRun is what Recreate reports for a task with nothing running
// on it here. It is phrased for the agent that will read it: by the time
// a run's own tool call can arrive, the run is live by construction, so
// this really does mean "your run has ended" (or "it is being run by
// some other daemon process than the one you asked").
var errNoLiveRun = errors.New(
	"orchestrator: this task has no live run on this daemon, so there is no sandbox to rebuild")

// Recreate destroys the sandbox of taskID's live run and builds an empty
// one under the same name, then puts back as much of what grain itself
// had set up in the old one as it can: the git credentials pointing at
// the proxy, any credential files the task's capabilities placed, the
// task's attachments, and a fresh clone of its repo with its branch
// checked out.
//
// What it cannot put back is everything the agent did -- every edit,
// every installed package, every uncommitted change. That is not a
// shortcoming of this function, it is the operation: a sandbox worth
// throwing away is thrown away whole, and a run reaching for this has
// already decided that whatever state it was in was worth less than a
// clean one. Commits it had already pushed are untouched, because they
// are on the remote rather than in the sandbox, and the re-clone below
// checks the branch out again with them on it.
//
// Only the rebuild itself can fail the call. Everything after it is
// reported through SandboxRecreation.Warnings, because by then the old
// sandbox is already gone and the caller most needs to know what it is
// now sitting in front of.
//
// One recreate per run at a time: the second waits for the first. A run
// only ever has one agent driving it, so this is a guard against that
// agent issuing parallel tool calls rather than against contention
// between runs.
func (r *SandboxRecreations) Recreate(ctx context.Context, taskID string) (SandboxRecreation, error) {
	rec := r.lookup(taskID)
	if rec == nil {
		return SandboxRecreation{}, errNoLiveRun
	}
	return rec.recreate(ctx)
}

// sandboxRecreation is one live run's own share of the registry:
// everything Recreate needs to rebuild that run's sandbox and put its
// setup back, captured by runOne as the run starts.
//
// It holds the run's Sandbox itself, not a name to look one up by. The
// sandbox is what knows how to destroy and rebuild itself
// (SandboxRebuilder) and how to write a git credential into itself
// (Sandbox.ConfigureGitCredentials), and holding it is also what keeps
// the two ends of a rebuild -- this and the run's own release -- talking
// about the same object.
type sandboxRecreation struct {
	store   *model.Store
	cfg     Config
	task    model.Task
	sandbox Sandbox
	tools   []mcp.Tool

	// sandboxRoot and placer are the two routes a placement can take
	// back into this sandbox, exactly as runOne computed them for
	// applyPlacements the first time round.
	sandboxRoot string
	placer      SandboxPlacer

	// mint is Deps.MintSandboxToken, or nil in a deployment with no git
	// proxy -- in which case there were no credentials in the old
	// sandbox to put back either.
	mint func(sandbox string) (string, error)
	// credentialIdentity is Deps.CredentialGitIdentity: what the
	// rebuilt sandbox's commits carry, the account the run's git
	// credential acts as (runGitIdentity).
	credentialIdentity CredentialGitIdentityFunc

	// mu serialises recreates against each other and against close, so a
	// run's sandbox is never released out from under a rebuild.
	mu           sync.Mutex
	closed       bool
	materialized []model.Materialized
	// capabilityPass is the attempt's minted pass as runOne materialized
	// it, nil for a run holding no capability references.
	capabilityPass *passMaterialization
}

// close marks this run as no longer recreatable, waiting for any
// recreate in flight to finish first.
func (s *sandboxRecreation) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *sandboxRecreation) recreate(ctx context.Context) (SandboxRecreation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return SandboxRecreation{}, errNoLiveRun
	}

	rebuilder, ok := s.sandbox.(SandboxRebuilder)
	if !ok {
		return SandboxRecreation{}, fmt.Errorf(
			"orchestrator: run %s's sandbox (%T) has no way to destroy and rebuild itself",
			s.sandbox.Name(), s.sandbox)
	}
	if err := rebuilder.Rebuild(ctx); err != nil {
		return SandboxRecreation{}, fmt.Errorf(
			"orchestrator: rebuilding run %s's sandbox: %w", s.sandbox.Name(), err)
	}

	// From here on the old sandbox is gone whatever happens next, so
	// nothing below turns this into a failed call -- see Recreate's own
	// doc comment.
	out := SandboxRecreation{Sandbox: s.sandbox.Name()}
	s.restoreGitCredentials(ctx, &out)
	s.restorePackageCache(ctx, &out)
	s.restorePlacements(ctx, &out)
	s.restoreCapabilityPass(ctx, &out)
	s.restoreAttachments(ctx, &out)
	s.restoreCheckout(ctx, &out)
	return out, nil
}

// restoreGitCredentials mints this sandbox's proxy token again and
// writes it back -- the same pair runOne does before a run's first
// touch of the proxy, repeated because the credential file lived in the
// sandbox that was just destroyed.
//
// The token is re-minted rather than remembered: EnsureToken is
// idempotent per sandbox name (gitproxy.SandboxTokenStore), and the
// sandbox's name has not changed, so this hands back the same token the
// proxy already recognises for this run rather than a second one to keep
// track of.
func (s *sandboxRecreation) restoreGitCredentials(ctx context.Context, out *SandboxRecreation) {
	if s.mint == nil {
		return
	}
	token, err := s.mint(s.sandbox.Name())
	if err != nil {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("its git credentials could not be re-minted (%v) -- git push and git fetch will not work", err))
		return
	}
	gitIdentity := runGitIdentity(ctx, s.store, s.credentialIdentity, &s.task)
	if err := s.sandbox.ConfigureGitCredentials(ctx,
		s.cfg.GitRemoteBase+"/placeholder/placeholder.git", token, gitIdentity); err != nil {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("its git credentials could not be written back (%v) -- git push and git fetch will not work", err))
		return
	}
	out.Restored = append(out.Restored, "its git credentials for grain's git proxy")
}

// restorePackageCache points the rebuilt sandbox back at this
// deployment's package cache. The new sandbox is a new machine with a
// stock /etc in it, so the configuration the dispatch wrote is gone with
// the old one -- and a run that recreated its sandbox mid-task is
// exactly the one about to reinstall everything it had installed.
func (s *sandboxRecreation) restorePackageCache(ctx context.Context, out *SandboxRecreation) {
	prep := PrepareSandbox(ctx, s.cfg.PackageCacheSource(), s.sandbox)
	if prep.Err != nil {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("it could not be pointed back at grain's package cache (%v) -- it will "+
				"download its packages from the internet instead", prep.Err))
		return
	}
	if prep.Configured {
		out.Restored = append(out.Restored, "its package manager configuration for grain's package cache")
	}
}

// restorePlacements writes every sandbox-side placement this run's
// capabilities already materialized back into the new sandbox, through
// whichever of the two routes applyPlacements would have used the first
// time.
func (s *sandboxRecreation) restorePlacements(ctx context.Context, out *SandboxRecreation) {
	if len(s.materialized) == 0 {
		return
	}
	if err := applyPlacements(ctx, s.sandboxRoot, s.placer, s.materialized); err != nil {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("the credential files this task's capabilities placed could not be written back: %v", err))
		return
	}
	out.Restored = append(out.Restored, "the files this task's capabilities place in the sandbox")
}

// restoreCapabilityPass applies the attempt's cached pass to the new
// sandbox -- its port forwards, its placements, and its setup commands
// run again -- without minting (§4.4.3, §5.5.5). A failure does not end
// the attempt: it is reported here, naming the grant, its exit status and
// the tail of its output, so the agent knows which part of its setup is
// missing.
func (s *sandboxRecreation) restoreCapabilityPass(ctx context.Context, out *SandboxRecreation) {
	if s.capabilityPass == nil {
		return
	}
	err := s.capabilityPass.apply(ctx, s.sandbox)
	if err == nil {
		// The agent CLI's MCP proxies are on grain's own host and aimed
		// at the capability servers, so they still point at the same
		// /runs/<attempt_id>/mcp; check each still answers (§5.2.5).
		if eerr := s.capabilityPass.checkEndpoints(ctx); eerr != nil {
			out.Warnings = append(out.Warnings, eerr.Error())
		}
	}
	var setup *CapabilitySetupError
	switch {
	case err == nil:
		out.Restored = append(out.Restored, "this task's capability grants: their port forwards, files and setup commands")
	case errors.As(err, &setup):
		how := fmt.Sprintf("exited %d", setup.ExitCode)
		if setup.TimedOut {
			how = "timed out"
		}
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"the setup command %s of the capability %s %s in the new sandbox, so what it sets up is missing. "+
				"The tail of what it printed (the capability server's text, not grain's):\n\n%s",
			setup.Path, setup.Grant, how, setup.Output))
	default:
		out.Warnings = append(out.Warnings, fmt.Sprintf("this task's capability grants could not be set up again in the new sandbox: %v", err))
	}
}

// restoreAttachments writes the task's attachments back under
// AttachmentsDir. They are re-read from the store rather than
// remembered, so an attachment added to the task while the run was
// working lands here too -- the same files a redispatch would place.
func (s *sandboxRecreation) restoreAttachments(ctx context.Context, out *SandboxRecreation) {
	if s.store == nil {
		return
	}
	attachments, err := s.store.Attachments(ctx, s.task.ID)
	if err != nil {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("this task's attachments could not be read back out of grain's store: %v", err))
		return
	}
	if len(attachments) == 0 {
		return
	}
	if _, err := placeAttachments(ctx, s.tools, attachments); err != nil {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("this task's attachments could not be written back: %v", err))
		return
	}
	out.Restored = append(out.Restored,
		fmt.Sprintf("this task's %d attachment(s), under ./%s", len(attachments), AttachmentsDir))
}

// restoreCheckout clones the task's repo again, checks its branch out
// and runs the repo's own setup command in it, exactly as
// prepareCheckout does before a run's first turn -- including picking up
// whatever the run had already pushed to that branch, since
// prepareCheckout continues an existing remote branch rather than
// branching over it. That is what makes a rebuild cost a run only its
// *unpushed* work.
//
// The setup command has to run again here for the same reason it runs at
// all: the rebuild took the whole sandbox with it, `make deps` and the
// node_modules and the virtualenv included, and handing a run back a
// checkout in exactly the state model.RepoConfig.SetupCommand exists to
// prevent would be worse than the first time -- the run has already been
// told, at turn 1, that setup was done for it.
//
// It is re-read from the store rather than remembered from dispatch, the
// same way restoreAttachments re-reads the attachments: a setup command
// written while this run was working is the one a rebuilt sandbox should
// get.
func (s *sandboxRecreation) restoreCheckout(ctx context.Context, out *SandboxRecreation) {
	setup, err := resolveSetupCommand(ctx, s.store, s.task)
	if err != nil {
		// Not fatal to the re-clone: a repo whose setup command could not
		// be read still wants its checkout back, and the warning below
		// tells the run which half it is missing.
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("this repo's setup command could not be read out of grain's store (%v), "+
				"so the fresh checkout has not been set up", err))
		setup = ""
	}
	cloneURL, err := resolveCloneURL(ctx, s.store, s.task)
	if err != nil {
		// The deployment proxy remains a usable fallback for GitHub repos,
		// but it is deliberately not a fallback for an explicitly configured
		// remote: using it here would silently restore a different repository
		// than the one this run started in.
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("this repo's clone URL could not be read out of grain's store (%v), "+
				"so the fresh checkout could not be restored", err))
		return
	}
	// setupNotes' zero value: a rebuild happens mid-run, inside a tool
	// call the agent made, so the row's synopsis is the agent's own at
	// that moment and grain does not talk over it. It would also be
	// mis-attributed if it did -- agent_started_at is long since stamped,
	// which is precisely what makes a note read as the run's own
	// (model.RunActivity.BySetup).
	prepared, err := prepareCheckoutFrom(ctx, s.tools, s.cfg.GitRemoteBase, cloneURL, s.task, setup, setupNotes{}, s.cfg.settingsRepo())
	if err != nil {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("this task's repo could not be cloned again: %v", err))
		return
	}
	if prepared.Dir == "" {
		return
	}
	out.CheckoutDir = prepared.Dir
	out.Restored = append(out.Restored, fmt.Sprintf(
		"a fresh clone of %s at ./%s, with %s checked out (including anything you had already pushed to it)",
		s.task.Target, prepared.Dir, s.task.BranchName()))
	switch {
	case prepared.Setup == nil:
	case prepared.Setup.failed():
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"this repo's setup command (%s) failed in the fresh checkout, exit %d -- so it may be "+
				"missing dependencies or generated files, and a build failing for that reason is "+
				"this rather than your change. The last of what it printed:\n\n%s",
			prepared.Setup.Command, prepared.Setup.ExitCode, prepared.Setup.Output))
	default:
		out.Restored = append(out.Restored, fmt.Sprintf(
			"this repo's own setup command (%s), run again in that checkout and successful",
			prepared.Setup.Command))
	}
}
