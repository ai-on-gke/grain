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

package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// stateManager owns this deployment's controller-local config repository.
type StateManager struct {
	dataDir string
	db      *sql.DB
	secrets *secrets.Store
	// forbidden is the live set of repositories the git proxy refuses to
	// every sandbox. It changes when the encrypted-secrets backup changes.
	forbidden *gitproxy.ForbiddenSet

	mu   sync.Mutex
	repo *staterepo.Repo
	// lastScopeSetErr is the last reason loadAPIScopeSets logged.
	lastScopeSetErr string
	// secretsRepo is where this deployment's encrypted secrets file is
	// backed up (pkg/secretsrepo), or nil for the deployments -- most of
	// them -- that keep it on this host alone. Held here so that the one
	// timer already exporting grain's configuration reconciles the
	// ciphertext with its own repository on the same tick, rather
	// than a second loop existing to push one file.
	secretsRepo *secretsrepo.Repo
	// lastSecretsErr is the last secrets-repo failure logged, so a
	// condition that persists -- a credential that no longer covers that
	// repository -- is one line in the journal rather than one every
	// thirty seconds. It is deliberately not lastErr: a backup that did
	// not happen is not the state repository failing to sync, and the
	// State pane must not say it is.
	lastSecretsErr string
	// lastErr is the most recent local configuration import or export
	// failure, reported in the pane as well as the daemon journal.
	lastErr error
	// lastCycle is when a cycle last finished, whether or not it did so
	// cleanly (ui.StateRepoStatus.SyncedAt). The pane's file browser
	// reads the working tree this loop writes, so "as of when" is part
	// of what a reader is looking at; a timestamp that has stopped
	// moving is the symptom, which is why a failed cycle stamps it too.
	lastCycle time.Time

	// resetNotice is whether this deployment is holding a settings-reset
	// notice it could not deliver anywhere but its own journal
	// (staterepo.Repo.UndeliveredResets), for the marker the rail carries
	// out to whoever never opens the State pane
	// (ui.Config.StateRepoResetNotice).
	//
	// Kept atomically because GET /api/config polls it without taking the
	// repository lock. It is refreshed wherever the repository is already
	// in hand: each cycle, status request, and dismissal.
	//
	// What that costs is staleness of at most one tick, on a fact that is
	// at most one tick old to begin with: the marker is written by the
	// same cycle that would refresh this.
	resetNotice atomic.Bool

	// failure is the sync failing, from the first failed cycle in a row
	// to the next clean one (ui.Config.StateRepoSyncFailure). Atomic for
	// resetNotice's reason: GET /api/config polls it without the lock.
	failure atomic.Pointer[ui.StateRepoSyncFailure]
	// lastSuccess is when a cycle last finished cleanly.
	lastSuccess time.Time

	// lastSettings is the state-repo.json snapshot m.repo was last reconciled
	// against, so a cycle only reconfigures the mirror when the file has
	// changed since NewStateManager or the previous reconcile.
	lastSettings staterepo.Settings

	// nudge asks for a cycle immediately after the merge queue merges a
	// native pull request into this deployment's own settings
	// (orchestrator.StateRepoGate).
	//
	// One slot, and a send that gives up rather than waits: the caller is
	// the reconciler's own goroutine, a cycle already runs every thirty
	// seconds, and two nudges arriving together want one cycle between
	// them, not two in a row.
	nudge chan struct{}
}

func NewStateManager(dataDir string, db *sql.DB, repo *staterepo.Repo, secretStore *secrets.Store, secretsRepo *secretsrepo.Repo, forbidden *gitproxy.ForbiddenSet) *StateManager {
	settings, _ := staterepo.LoadSettings(dataDir)
	m := &StateManager{
		dataDir: dataDir, db: db, repo: repo, secrets: secretStore, secretsRepo: secretsRepo,
		forbidden: forbidden, lastSettings: settings, nudge: make(chan struct{}, 1),
	}
	// Read once here so the rail's marker is right from the first paint
	// rather than from the first tick: a deployment restarted with a
	// notice already waiting on disk is exactly the one nobody has read
	// it on yet.
	m.refreshResetNotice(context.Background())
	return m
}

// stateRepoHooks is everything the reconcile loop has to know about
// this deployment's own state repository, in the functions it reads them
// through (orchestrator.Config's StateRepo, CheckImage, StateRepoCheck
// and StateRepoMerged).
//
// Functions keep the reconcile loop coupled to the manager that owns the
// local repository and let it observe runtime settings such as check image.
type stateRepoHooks struct {
	Repo       func() model.RepoRef
	CheckImage func() string
	Check      func() string
	Merged     func()
	// PullRequests overlays the external forge client with the controller's
	// native config-repository implementation.
	PullRequests func(github.Client, *model.Store) github.Client
}

// stateRepoHooksFor reads them off the manager that owns the repository.
func stateRepoHooksFor(m *StateManager) stateRepoHooks {
	return stateRepoHooks{
		Repo: m.settingsRepo, CheckImage: m.checkImage,
		// Config pull requests are controller-native and have no external
		// check name for the merge queue to wait on.
		Check: func() string { return "" }, Merged: m.Nudge,
		PullRequests: func(next github.Client, store *model.Store) github.Client {
			return &StateRepoPullRequestClient{Client: next, Manager: m, Store: store}
		},
	}
}

// Nudge asks the sync loop to apply a newly merged native config change.
func (m *StateManager) Nudge() {
	select {
	case m.nudge <- struct{}{}:
	default:
	}
}

// refreshResetNotice re-reads whether anything is waiting in the marker.
// Called wherever the repository is already in hand and the answer may
// have moved: after a cycle, which is what writes one; from status,
// which a person opening the pane triggers; and after a dismissal, which
// is what empties it.
//
// The manager's lock, where the caller holds it, is what makes reading
// m.repo safe here; the store is an atomic so that the poll on the other
// side needs neither.
func (m *StateManager) refreshResetNotice(ctx context.Context) {
	_, pending := m.repo.UndeliveredResets(ctx)
	m.resetNotice.Store(pending)
}

// resetNoticePending is ui.Config.StateRepoResetNotice: whether the
// State pane is carrying something a person has not read yet. No lock,
// no git, no file read -- see the field's own doc comment.
func (m *StateManager) resetNoticePending() bool { return m.resetNotice.Load() }

func (m *StateManager) noteLoadFailure(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastErr = err
}

func (m *StateManager) reconcileRepo(ctx context.Context) (staterepo.Settings, bool, error) {
	if m.repo == nil {
		return staterepo.Settings{}, false, nil
	}
	if m.dataDir == "" {
		return staterepo.Settings{Remote: m.repo.Remote(), Branch: m.repo.Branch()}, false, nil
	}
	settings, err := staterepo.LoadSettings(m.dataDir)
	if err != nil {
		return staterepo.Settings{}, false, err
	}
	if settings == m.lastSettings {
		return settings, false, nil
	}
	if err := m.repo.ConfigureMirror(ctx, settings.Remote, settings.Branch, RepoToken(m.dataDir, settings.Remote, settings.TokenFile)); err != nil {
		return settings, false, err
	}
	m.lastSettings = settings
	return settings, true, nil
}

// refreshForbidden updates the live proxy refusal after the encrypted-secrets
// backup repository changes. The controller-local config repository never
// contributes an external refusal.
func (m *StateManager) refreshForbidden(ctx context.Context) {
	if m.forbidden == nil {
		return
	}
	repos, err := ForbiddenRepos(ctx, m.dataDir)
	if err != nil {
		log.Printf("grain: cannot update the git proxy's secrets-repository refusal: %v", err)
		return
	}
	m.forbidden.Set(repos)
}

func (m *StateManager) sync(ctx context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cycle(ctx, false)
}

// syncAll is what a human asking for a sync means, and what the loop
// owes the repository on the way out: everything the repository carries,
// written out now. It differed from sync when grain's own record of
// itself was exported on a slower clock; it is staterepo.SyncAll either
// way, and that is where the difference would come back if one ever
// did.
func (m *StateManager) syncAll(ctx context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cycle(ctx, true)
}

// cycle first applies any native grain/config merge to the database, then
// exports the configuration and commits it to the same local repository.
// all is whether this is an explicit sync (staterepo.SyncAll) or a tick
// of the timer.
func (m *StateManager) cycle(ctx context.Context, all bool) (bool, error) {
	m.lastCycle = time.Now()
	// Whatever this cycle imports, the deployment's API scope sets are
	// installed from the database it leaves behind -- on its way out, so
	// a merged set is in effect by the end of the tick that took it up.
	defer m.loadAPIScopeSets(ctx)
	settings, changed, err := m.reconcileRepo(ctx)
	if err != nil {
		m.lastErr = err
		m.noteOutcome(err)
		return false, err
	}
	if strings.TrimSpace(settings.Remote) != "" {
		applied, applyErr := staterepo.SyncExternal(ctx, m.repo, m.db, changed || all)
		m.lastErr = applyErr
		m.syncSecrets(ctx)
		m.refreshResetNotice(ctx)
		m.noteOutcome(m.lastErr)
		return applied, m.lastErr
	}
	applied, applyErr := staterepo.Apply(ctx, m.repo, m.db, model.SchemaVersion)
	if recoverDivergedStateRepo(ctx, m.repo, applyErr) {
		applied, applyErr = staterepo.Apply(ctx, m.repo, m.db, model.SchemaVersion)
	}
	if errors.Is(applyErr, staterepo.ErrNotApplied) {
		// The working tree is at a commit the database has not taken up --
		// a dump this build cannot read, or rows it could not insert. The
		// export must not run: it would write the database over those
		// files and commit a silent revert of whatever was merged. Stopping
		// here leaves grain running on the database it
		// has, says why in the pane, and lets the next tick try again once
		// somebody has fixed the repository.
		m.lastErr = applyErr
		m.noteOutcome(applyErr)
		return false, applyErr
	}
	export := staterepo.Sync
	if all {
		export = staterepo.SyncAll
	}
	changed, syncErr := export(ctx, m.repo, m.db, model.SchemaVersion)
	m.lastErr = errors.Join(applyErr, syncErr)
	m.syncSecrets(ctx)
	// The pull above is what takes a merged settings change up, so it is
	// also the one thing in this process that can leave a reset notice
	// nobody has been told about (staterepo's announceResets). Asked here
	// so the rail's marker appears within a tick of the merge rather than
	// waiting for somebody to open the pane it points at -- which is the
	// person who is not going to.
	m.refreshResetNotice(ctx)
	m.noteOutcome(m.lastErr)
	return applied || changed, m.lastErr
}

// loadAPIScopeSets installs the database's API scope sets in this
// process (model.InstallAPIScopeSets). A set that does not resolve --
// one merged past a failing state check -- leaves the sets already in
// effect where they are; why is logged once per change of reason and
// shown in Settings (model.APIScopeSetsError).
func (m *StateManager) loadAPIScopeSets(ctx context.Context) {
	if m.db == nil {
		return
	}
	err := model.New(m.db).LoadAPIScopeSets(ctx)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg != m.lastScopeSetErr {
		m.lastScopeSetErr = msg
		if err != nil {
			log.Printf("grain: the API scope sets in tables/api_scope_set.json are not applied, "+
				"the previous ones stay in effect: %v", err)
		}
	}
}

// syncFailureGrace is how long the sync has to have been failing before
// it is reported outside the journal: long enough that one unreachable
// remote for a tick or two does not put a banner up, short enough that a
// deployment does not run a day without an export with nobody told.
const syncFailureGrace = 5 * time.Minute

// noteOutcome records a cycle's result for syncFailure: the first failure
// in a row starts the clock, a later one updates the reason, and a clean
// cycle clears it. Called by cycle while m.mu is held.
func (m *StateManager) noteOutcome(err error) {
	if err == nil {
		m.lastSuccess = time.Now()
		if m.failure.Swap(nil) != nil {
			log.Printf("grain: state sync is working again; settings are being exported to %s", m.settingsRepoLocked())
		}
		return
	}
	remote, _ := m.externalConfigRepo(false)
	next := &ui.StateRepoSyncFailure{Since: time.Now(), LastSuccess: m.lastSuccess, Error: err.Error(), Repo: m.settingsRepoLocked().String(), External: remote != ""}
	if prev := m.failure.Load(); prev != nil {
		next.Since = prev.Since
	}
	m.failure.Store(next)
}

// syncFailure is ui.Config.StateRepoSyncFailure: nil while the sync is
// working or has been failing for less than syncFailureGrace.
func (m *StateManager) syncFailure() *ui.StateRepoSyncFailure {
	f := m.failure.Load()
	if f == nil || time.Since(f.Since) < syncFailureGrace {
		return nil
	}
	out := *f
	return &out
}

// stateRepoSyncFailure is stateRepoResetNotice for syncFailure.
func stateRepoSyncFailure(stateRepo ui.StateRepoManager) func() *ui.StateRepoSyncFailure {
	m, ok := stateRepo.(*StateManager)
	if !ok || m == nil {
		return nil
	}
	return m.syncFailure
}

// syncSecrets carries this deployment's encrypted secrets file to the
// repository that backs it up, on the tick that already carries its
// state.
//
// Its failures are logged here rather than joined into what cycle
// returns, and that is the whole point of the separation: the error this
// manager remembers is what the State pane shows about the *state*
// repository, and a secrets repository grain could not reach is a
// different fact about a different remote. Reporting it as a state sync
// failure would have an operator looking at the wrong repository, and
// would mark a deployment whose state is pushing perfectly well as one
// that is not.
//
// Nothing is lost by leaving it to the next tick either way: the file on
// this host is the copy grain runs on, and the repository is a backup
// that is one push behind for thirty seconds rather than one that is
// wrong.
func (m *StateManager) syncSecrets(ctx context.Context) {
	repo, err := m.currentSecretsRepo(ctx)
	if err == nil && repo != nil {
		err = syncOpenSecretsRepo(ctx, repo)
	}
	switch {
	case err == nil:
		m.lastSecretsErr = ""
	case err.Error() != m.lastSecretsErr:
		m.lastSecretsErr = err.Error()
		log.Printf("grain: could not back this deployment's encrypted secrets up to %s: %v",
			secretsRepoRemote(m.dataDir), err)
	}
}

// currentSecretsRepo is the open repository <data-dir>/secrets-repo.json
// names right now, opened or re-opened when that file has changed under
// a running daemon and nil when it names nothing.
//
// Read per tick rather than once at startup for the reason the state
// repository is: an operator naming a secrets repository -- with
// `grain secrets repo`, or by writing the file a deploy writes -- has
// said something that should take effect on the next tick rather than at
// the next restart. Two small file reads is what that costs; the clone
// itself happens once, and every tick afterwards finds the repository it
// already has.
func (m *StateManager) currentSecretsRepo(ctx context.Context) (*secretsrepo.Repo, error) {
	settings, err := secretsrepo.LoadSettings(m.dataDir)
	if err != nil {
		return nil, err
	}
	if settings.Remote == "" {
		m.secretsRepo = nil
		return nil, nil
	}
	state, err := staterepo.LoadSettings(m.dataDir)
	if err != nil {
		return nil, err
	}
	if m.secretsRepo != nil && m.secretsRepo.Remote() == settings.Remote &&
		m.secretsRepo.File() == settings.FileNameFor(state.Remote) {
		return m.secretsRepo, nil
	}
	repo, err := openSecretsRepo(ctx, m.dataDir)
	if errors.Is(err, secretsrepo.ErrNoRemote) {
		m.secretsRepo = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.secretsRepo = repo
	return repo, nil
}

// settingsRepo is the stable controller-local identity of this
// deployment's configuration repository. Every installation exposes this
// target to agents through the controller's git proxy.
//
// "grain" is a virtual proxy owner rather than a GitHub account. The proxy
// is private to one controller, so grain/config unambiguously names that
// controller's local checkout.
// Compare supplies the native, read-only pull-request view for the virtual
// config repository. Both revisions are resolved to commit IDs before diffing,
// so a branch name can never be interpreted as a git option or revision
// expression.
func (m *StateManager) Compare(ctx context.Context, target model.RepoRef, base, head string) (ui.BranchComparison, error) {
	if !m.isInternalConfigRepo() || !target.Same(model.ConfigRepo()) {
		return ui.BranchComparison{}, fmt.Errorf("native comparison is not available for %s", target)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	repo := m.repo
	if repo == nil {
		return ui.BranchComparison{}, errors.New("config repository is not available")
	}
	dir := repo.Dir()
	if base == "" {
		base = repo.Branch()
	}
	resolve := func(branch string) (string, error) {
		if strings.TrimSpace(branch) == "" || strings.HasPrefix(branch, "-") {
			return "", fmt.Errorf("invalid branch name %q", branch)
		}
		if out, err := exec.CommandContext(ctx, "git", "check-ref-format", "--branch", branch).Output(); err != nil || strings.TrimSpace(string(out)) != branch {
			return "", fmt.Errorf("invalid branch name %q", branch)
		}
		for _, ref := range []string{"refs/heads/" + branch, "refs/remotes/origin/" + branch} {
			out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Output()
			if err == nil {
				return strings.TrimSpace(string(out)), nil
			}
		}
		return "", fmt.Errorf("branch %q does not exist", branch)
	}
	baseCommit, err := resolve(base)
	if err != nil {
		return ui.BranchComparison{}, err
	}
	headCommit, err := resolve(head)
	if err != nil {
		return ui.BranchComparison{}, err
	}
	mergeBase, err := localMergeBase(ctx, dir, baseCommit, headCommit)
	if err != nil {
		return ui.BranchComparison{}, fmt.Errorf("comparing %s and %s: %w", base, head, err)
	}
	// A base that already contains head is a merged branch, and its merge
	// base is head's own tip -- so the ordinary comparison is empty and
	// this view would go blank on every task it has finished. The fork
	// point is recovered from the merge commit instead (localForkPoint).
	merged := mergeBase == headCommit
	if merged {
		if forked := localForkPoint(ctx, dir, baseCommit, headCommit); forked != "" {
			mergeBase = forked
		}
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--no-ext-diff", "--find-renames", "--end-of-options", mergeBase+".."+headCommit, "--").Output()
	if err != nil {
		return ui.BranchComparison{}, fmt.Errorf("comparing %s and %s: %w", base, head, err)
	}
	const maxDiff = 2 << 20
	if len(out) > maxDiff {
		out = append(out[:maxDiff], []byte("\n\n[diff truncated at 2 MiB]\n")...)
	}
	return ui.BranchComparison{
		Base: base, Head: head, MergeBase: mergeBase, Merged: merged, Diff: string(out),
	}, nil
}

// Branches lists the config repository's branches from the controller's
// own checkout of it -- every branch the native pull-request view can
// compare (Compare resolves refs/heads/ and refs/remotes/origin/ alike),
// sorted and once each. It is the branch pickers' listing for
// grain/config, which is served by grain's git proxy and is nothing
// GitHub's API could be asked about.
func (m *StateManager) Branches(ctx context.Context) ([]string, error) {
	if m == nil {
		return nil, errors.New("config repository is not available")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.repo == nil {
		return nil, errors.New("config repository is not available")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", m.repo.Dir(), "for-each-ref",
		"--format=%(refname)", "refs/heads/", "refs/remotes/origin/")
	// A local read of refs needs git on PATH and nothing else of the
	// daemon's environment.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_TERMINAL_PROMPT=0"}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing the config repository's branches: %w", err)
	}
	seen := map[string]bool{}
	names := []string{}
	for _, ref := range strings.Fields(string(out)) {
		name, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok {
			name, _ = strings.CutPrefix(ref, "refs/remotes/origin/")
		}
		if name == "" || name == "HEAD" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// localMergeBase is `git merge-base`, the commit two revisions last had
// in common and the commit a three-dot diff between them is taken from.
// Both arguments are already resolved commit ids, so nothing here can be
// read as an option or a revision expression.
func localMergeBase(ctx context.Context, dir, baseCommit, headCommit string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--end-of-options", baseCommit, headCommit).Output()
	if err != nil {
		return "", fmt.Errorf("finding the merge base: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// localForkPoint is where head forked from base, for a head base already
// contains: the merge commit that brought it in names base as it stood
// beside head's own tip, and the merge base of that parent with head is
// the commit the branch began at. That is the comparison this view showed
// while the branch was open, and the one worth going on showing once it
// has landed (grain/task-375).
//
// The config repository's own merges are `git merge --no-ff`
// (stateRepoPullRequestClient.MergePullRequest), so the merge commit is
// always there to be found. Anything else -- a branch squashed or
// fast-forwarded in by hand, a history this cannot walk -- answers "",
// and the caller keeps the plain merge base rather than failing.
func localForkPoint(ctx context.Context, dir, baseCommit, headCommit string) string {
	// Oldest first, with each commit's parents, and only the commits that
	// descend from head: the merge that landed it is the first of those
	// naming head's tip as a parent.
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-list",
		"--reverse", "--parents", "--ancestry-path", "--end-of-options", headCommit+".."+baseCommit).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parents := strings.Fields(line)
		if len(parents) < 3 {
			continue
		}
		parents = parents[1:]
		if !slices.Contains(parents, headCommit) {
			continue
		}
		for _, parent := range parents {
			if parent == headCommit {
				continue
			}
			forked, err := localMergeBase(ctx, dir, parent, headCommit)
			if err != nil {
				return ""
			}
			return forked
		}
	}
	return ""
}

func (m *StateManager) ExternalConfigRepo() (remote, branch string) {
	return m.externalConfigRepo(true)
}

func (m *StateManager) externalConfigRepo(lock bool) (remote, branch string) {
	if m == nil {
		return "", ""
	}
	if m.dataDir != "" {
		if settings, err := staterepo.LoadSettings(m.dataDir); err == nil {
			remote = strings.TrimSpace(settings.Remote)
			branch = strings.TrimSpace(settings.Branch)
			if branch == "" && remote != "" {
				branch = staterepo.DefaultBranch
			}
			return remote, branch
		}
	}
	if lock {
		m.mu.Lock()
		defer m.mu.Unlock()
	}
	if m.repo == nil {
		return "", ""
	}
	remote = strings.TrimSpace(m.repo.Remote())
	branch = strings.TrimSpace(m.repo.Branch())
	if branch == "" && remote != "" {
		branch = staterepo.DefaultBranch
	}
	return remote, branch
}

func (m *StateManager) isInternalConfigRepo() bool {
	if m == nil || m.repo == nil {
		return false
	}
	remote, _ := m.ExternalConfigRepo()
	return remote == ""
}

func (m *StateManager) settingsRepo() model.RepoRef {
	return m.settingsRepoWithLock(true)
}

func (m *StateManager) settingsRepoLocked() model.RepoRef {
	return m.settingsRepoWithLock(false)
}

func (m *StateManager) settingsRepoWithLock(lock bool) model.RepoRef {
	if m == nil || m.repo == nil {
		return model.RepoRef{}
	}
	remote, _ := m.externalConfigRepo(lock)
	if remote != "" {
		if owner, name, ok := repoFromRemote(remote); ok {
			return model.RepoRef{Owner: owner, Name: name}
		}
		return model.RepoRef{}
	}
	return model.ConfigRepo()
}

// checkImage is the container image a run changing grain/config uses for
// validation. An operator pin in state-repo.json wins; otherwise the
// current grain image is used. An unreadable settings file falls back to
// staterepo.DefaultCheckImage.
func (m *StateManager) checkImage() string {
	settings, err := staterepo.LoadSettings(m.dataDir)
	if err != nil {
		return ""
	}
	return checkImage(settings)
}

// SettingsChanges is ui.SettingsHistoryReader: when one of this
// deployment's own parameters last moved, for the metrics report's
// "changes" section (pkg/metrics' changes.go on why that section exists).
//
// It is this type's job because this is what owns the repository the
// history lives in -- the same reason Tree and File are here -- and it
// converts staterepo's own shape onto pkg/metrics' in the process, the way
// cycleTimesAdapter does for the reconcile loop's ticks: neither package
// imports the other, and cmd/grain is the one place both are in scope.
//
// A deployment with no repository yet answers with nothing rather than an
// error. There is genuinely no history there, and the section already
// distinguishes "nothing to read" from "nothing changed" by whether it was
// handed a reader at all.
func (m *StateManager) SettingsChanges(ctx context.Context, since time.Time) ([]metrics.SettingsChange, error) {
	m.mu.Lock()
	repo := m.repo
	m.mu.Unlock()
	if repo == nil {
		return nil, nil
	}
	changes, err := repo.SettingsChanges(ctx, since, 0)
	if err != nil {
		return nil, err
	}
	out := make([]metrics.SettingsChange, 0, len(changes))
	for _, c := range changes {
		change := metrics.SettingsChange{
			At: c.At, Commit: c.Commit, Subject: c.Subject,
			Paths: c.Paths, Truncated: c.Truncated,
			Fields: make([]metrics.SettingsField, 0, len(c.Fields)),
		}
		for _, f := range c.Fields {
			change.Fields = append(change.Fields, metrics.SettingsField{
				Path: f.Path, Name: f.Name, From: f.From, To: f.To,
			})
		}
		out = append(out, change)
	}
	return out, nil
}

// Tree lists this deployment's exported configuration for the State
// pane's file browser.
//
// The listing is taken off the working tree the export loop writes, at
// the head that status reports, so that what a reader sees and what the
// pane says they are seeing are the same cycle's work.
func (m *StateManager) Tree(ctx context.Context) (ui.StateRepoTree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.repo == nil {
		return ui.StateRepoTree{}, errors.New("config repository is not available")
	}
	tree, err := staterepo.BrowseTree(m.repo.Dir())
	if err != nil {
		return ui.StateRepoTree{}, err
	}
	out := ui.StateRepoTree{Truncated: tree.Truncated}
	out.Head, _ = m.repo.Head(ctx)
	for _, entry := range tree.Entries {
		out.Entries = append(out.Entries, ui.StateRepoEntry{Path: entry.Path, Dir: entry.Dir, Size: entry.Size})
	}
	return out, nil
}

// File reads one exported file.
//
// Which paths are readable is pkg/staterepo's answer and not this
// package's: the allowlist, the symlink resolution and the refusal that
// names what is browsable all live beside the layout constants they are
// derived from. What happens here is the translation of its two
// refusals into the two this API distinguishes -- forbidden and absent
// -- so that pkg/ui needs no import of the package that knows where
// grain keeps its state.
func (m *StateManager) File(ctx context.Context, path string, limit int64) (ui.StateRepoFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.repo == nil {
		return ui.StateRepoFile{}, errors.New("config repository is not available")
	}
	file, err := staterepo.BrowseFile(m.repo.Dir(), path, limit)
	switch {
	case errors.Is(err, staterepo.ErrNotBrowsable):
		return ui.StateRepoFile{}, &ui.StateRepoPathRefused{Reason: err.Error()}
	case errors.Is(err, fs.ErrNotExist):
		return ui.StateRepoFile{}, &ui.StateRepoPathMissing{Reason: err.Error()}
	case err != nil:
		return ui.StateRepoFile{}, err
	}
	out := ui.StateRepoFile{
		Path: file.Path, Size: file.Size, Content: file.Text,
		Truncated: file.Truncated, Binary: file.Binary,
	}
	out.Head, _ = m.repo.Head(ctx)
	return out, nil
}

func (m *StateManager) Status(ctx context.Context) (ui.StateRepoStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status(ctx), nil
}

// status is Status' body, for the callers that already hold the lock.
// Nothing here fails the request: a working tree that cannot answer
// (mid-adopt, or on a disk that has gone away) yields a status with the
// fields it could fill and an Error naming what it could not, which is
// more use to an operator than a 500.
func (m *StateManager) status(ctx context.Context) ui.StateRepoStatus {
	out := ui.StateRepoStatus{
		Available:          true,
		Remote:             m.repo.Remote(),
		Branch:             m.repo.Branch(),
		Dir:                m.repo.Dir(),
		BuildSchemaVersion: model.SchemaVersion,
	}
	if state, err := staterepo.LoadSettings(m.dataDir); err == nil && state != m.lastSettings {
		out.Remote = state.Remote
		if state.Branch != "" {
			out.Branch = state.Branch
		}
	}
	if strings.TrimSpace(out.Remote) != "" {
		out.Mode = "external"
		if owner, name, ok := repoFromRemote(out.Remote); ok {
			out.Repository = owner + "/" + name
		} else {
			out.Repository = out.Remote
		}
	} else {
		out.Mode = "internal"
		out.Repository = model.ConfigRepo().String()
	}
	out.Head, _ = m.repo.Head(ctx)
	if v, err := staterepo.ReadSchemaVersion(m.repo.Dir()); err == nil {
		out.SchemaVersion = v
	}
	if m.secrets != nil {
		out.SecretsKeyFile = m.secrets.KeyFile()
		if pub, err := m.secrets.PublicKey(); err == nil {
			out.SecretsPublicKey = pub
		}
		if recipient, err := m.secrets.FileRecipient(); err == nil {
			out.SecretsFileRecipient = recipient
		}
		// Reported whether or not the key is merely absent: "this host
		// cannot read its own secrets file" is one condition with one fix,
		// and splitting it into a missing-key case and a wrong-key case
		// would only make the pane say it twice.
		if err := m.secrets.Check(); err != nil {
			out.SecretsError = err.Error()
		}
	}
	// And whether that file exists anywhere but this disk, read off the
	// settings rather than off the open repository: a deployment that has
	// just been pointed at one should see it in the pane on the next
	// paint, not on the tick that clones it.
	if settings, err := secretsrepo.LoadSettings(m.dataDir); err == nil && settings.Remote != "" {
		out.SecretsRepo = settings.Remote
		state, err := staterepo.LoadSettings(m.dataDir)
		if err != nil {
			state = staterepo.Settings{}
		}
		out.SecretsRepoFile = settings.FileNameFor(state.Remote)
		out.SecretsRepoBranch = settings.Branch
		if out.SecretsRepoBranch == "" {
			out.SecretsRepoBranch = secretsrepo.DefaultBranch
		}
	}
	// And what a merge took away that this deployment had nowhere to say
	// out loud, from a marker of the same kind and for the same reason:
	// the notice grain posts on the pull request a settings change
	// arrived in never reaches a deployment with no remote, no credential
	// or no pull request, and the pane is the only place left that
	// somebody looks at (docs/state-pane-reset-notice.md). Read here as
	// well as into the atomic above, since a person on this pane wants
	// the values and not only the fact that there are some.
	notices, pending := m.repo.UndeliveredResets(ctx)
	if pending {
		out.ResetNotices = resetNotices(notices)
		out.ResetNoticesDropped = notices.Dropped
	}
	// The same read the rail's marker is answered from, kept rather than
	// asked for twice.
	m.resetNotice.Store(pending)
	if snapPath, snapAt, err := staterepo.LatestSnapshot(m.dataDir); err == nil && snapPath != "" {
		out.LastSnapshot = snapPath
		if !snapAt.IsZero() {
			out.LastSnapshotAt = &snapAt
		}
	}
	out.SyncedAt = m.lastCycle
	if m.lastErr != nil {
		out.Error = m.lastErr.Error()
	}
	return out
}

// DismissResetNotices is a person on the State pane saying they have
// read what a merge took away.
//
// It deletes the marker rather than turning a flag over, and there is
// nothing here to take back -- see the interface's own doc comment, and
// docs/state-pane-reset-notice.md for why an event and a condition
// (AcknowledgeLocalOnly, above) clear differently. The values survive in
// this deployment's journal either way, which is what makes a dismissal
// cost nothing but a scroll.
//
// A failure is reported rather than swallowed: the pane redraws from
// what this returns, and a dismissal that silently did not happen is a
// notice that comes back on the next poll with nothing to say why.
func (m *StateManager) DismissResetNotices(ctx context.Context) (ui.StateRepoStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.repo.ClearUndeliveredResets(ctx); err != nil {
		return ui.StateRepoStatus{}, err
	}
	return m.status(ctx), nil
}

// resetNotices is staterepo's marker in the shape the pane renders. A
// straight field-for-field copy, because the two structs are the same
// fact: pkg/ui does not import pkg/staterepo, and the alternative --
// this package handing the pane a type out of the package that produced
// it -- would make every surface of the UI depend on where grain keeps
// its state.
func resetNotices(kept staterepo.UndeliveredResets) []ui.ResetNotice {
	out := make([]ui.ResetNotice, 0, len(kept.Notices))
	for _, n := range kept.Notices {
		notice := ui.ResetNotice{
			At:         n.At,
			Commit:     n.Commit,
			Reason:     n.Reason,
			Failed:     n.Failed,
			MoreValues: n.MoreValues,
		}
		for _, v := range n.Values {
			notice.Values = append(notice.Values, ui.ResetValue{
				Path: v.Path, Column: v.Column, Row: v.Row, From: v.From, To: v.To,
			})
		}
		out = append(out, notice)
	}
	return out
}

// stateRepoResetNotice is ui.Config.StateRepoResetNotice for the manager
// this daemon actually built, or nil for a UI wired to something else --
// which reports false, the same nil-means-unavailable contract every
// other polled field on that config has. A type assertion because
// startUIServer is handed the interface: the boolean is not part of it,
// deliberately, since the whole point of it is to be answerable without
// the lock every method on that interface takes.
func stateRepoResetNotice(stateRepo ui.StateRepoManager) func() bool {
	m, ok := stateRepo.(*StateManager)
	if !ok || m == nil {
		return nil
	}
	return m.resetNoticePending
}

func (m *StateManager) ImportSecretsKey(ctx context.Context, key string) (ui.StateRepoStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.secrets == nil {
		return ui.StateRepoStatus{}, errors.New("this deployment has no secret store to import a key into")
	}
	parsed, err := secrets.ParseKey(key)
	if err != nil {
		return ui.StateRepoStatus{}, err
	}
	if err := m.secrets.ImportKey(parsed); err != nil {
		return ui.StateRepoStatus{}, err
	}
	return m.status(ctx), nil
}

// SetSecretsRepo names the repository this deployment's encrypted
// secrets file is backed up to, or stops backing it up -- `grain secrets
// repo` reached from the State pane instead of from a terminal.
//
// It writes <data-dir>/secrets-repo.json and stops there, exactly as
// that command does: the clone, the first push and a restore are the
// daemon's, on the tick after this (currentSecretsRepo re-reads the file
// every cycle), so nothing here needs the network and nothing here can
// hang a browser on an unreachable remote. A repository that cannot be
// reached shows up as the journal line syncSecrets logs, a tick later,
// rather than as a failed button press.

// SetRemote names the external Git repository this deployment's
// controller-local grain/config repository mirrors to, or stops mirroring
// it -- `grain state repo` reached from the State pane instead of from a
// terminal.
func (m *StateManager) SetRemote(ctx context.Context, req ui.StateRepoRemoteRequest) (ui.StateRepoStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings, err := staterepo.LoadSettings(m.dataDir)
	if err != nil {
		return ui.StateRepoStatus{}, err
	}
	was := settings.Remote
	remote := strings.TrimSpace(req.Remote)
	if remote != "" {
		sec, err := secretsrepo.LoadSettings(m.dataDir)
		if err != nil {
			return ui.StateRepoStatus{}, err
		}
		if err := RefuseStateRepoAsSecretsRepo(sec.Remote, remote); err != nil {
			return ui.StateRepoStatus{}, err
		}
	}
	settings.Remote = remote
	if branch := strings.TrimSpace(req.Branch); branch != "" {
		if err := staterepo.ValidateBranch(branch); err != nil {
			return ui.StateRepoStatus{}, err
		}
		settings.Branch = branch
	}
	switch {
	case req.Token != "":
		path, err := writeStateRepoToken(m.dataDir, req.Token)
		if err != nil {
			return ui.StateRepoStatus{}, err
		}
		settings.TokenFile = path
	case remote == "" || !sameRemote(remote, was):
		settings.TokenFile = ""
	}
	if err := staterepo.SaveSettings(m.dataDir, settings); err != nil {
		return ui.StateRepoStatus{}, err
	}
	if m.repo != nil {
		if err := m.repo.ConfigureMirror(ctx, settings.Remote, settings.Branch, RepoToken(m.dataDir, settings.Remote, settings.TokenFile)); err != nil {
			return ui.StateRepoStatus{}, err
		}
		m.lastSettings = settings
		if settings.Remote != "" && m.db != nil {
			if _, err := staterepo.SyncExternal(ctx, m.repo, m.db, true); err == nil {
				m.lastCycle = time.Now()
				m.lastErr = nil
			}
		}
	}
	return m.status(ctx), nil
}

func (m *StateManager) SetSecretsRepo(ctx context.Context, req ui.SecretsRepoRequest) (ui.StateRepoStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings, err := secretsrepo.LoadSettings(m.dataDir)
	if err != nil {
		return ui.StateRepoStatus{}, err
	}
	was := settings.Remote
	remote := strings.TrimSpace(req.Remote)
	if remote != "" {
		state, err := staterepo.LoadSettings(m.dataDir)
		if err != nil {
			return ui.StateRepoStatus{}, err
		}
		if err := RefuseStateRepoAsSecretsRepo(remote, state.Remote); err != nil {
			return ui.StateRepoStatus{}, err
		}
	}
	settings.Remote = remote
	if branch := strings.TrimSpace(req.Branch); branch != "" {
		if err := staterepo.ValidateBranch(branch); err != nil {
			return ui.StateRepoStatus{}, err
		}
		settings.Branch = branch
	}
	switch {
	case req.Token != "":
		path, err := writeSecretsRepoToken(m.dataDir, req.Token)
		if err != nil {
			return ui.StateRepoStatus{}, err
		}
		settings.TokenFile = path
	case remote == "" || !sameRemote(remote, was):
		// A credential named for the repository this deployment has just
		// stopped using is not a credential for the one it has just been
		// given, and leaving it named would have every push fail on a
		// token that covers something else rather than falling back to
		// this deployment's own GitHub credential ladder. The file itself
		// is left on disk, unread: an operator who names the old
		// repository again has lost nothing.
		settings.TokenFile = ""
	}
	if err := secretsrepo.SaveSettings(m.dataDir, settings); err != nil {
		return ui.StateRepoStatus{}, err
	}
	// The open repository is a clone of whatever was named before, and no
	// change of URL makes it a clone of this one. Dropped rather than
	// re-pointed, so the next tick opens the repository the settings now
	// name -- a fresh clone under <data-dir>/secrets-repo, which
	// secretsrepo.Open makes for itself.
	m.secretsRepo = nil
	// And the git proxy is told now rather than at the next restart: from
	// the moment the settings name this repository it is one no sandbox
	// may clone, whatever a task's scope says. Naming none lifts the
	// refusal on the repository that is no longer grain's ciphertext.
	m.refreshForbidden(ctx)
	return m.status(ctx), nil
}

// Sync applies a native grain/config merge and exports all database tiers
// immediately instead of waiting for the next timer tick.
func (m *StateManager) Sync(ctx context.Context) (ui.StateRepoStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.cycle(ctx, true); err != nil {
		return ui.StateRepoStatus{}, err
	}
	return m.status(ctx), nil
}

func writeStateRepoToken(dataDir, token string) (string, error) {
	return writeRepoToken(dataDir, "state-repo-token", token)
}

func writeSecretsRepoToken(dataDir, token string) (string, error) {
	return writeRepoToken(dataDir, "secrets-repo-token", token)
}

func writeRepoToken(dataDir, name, token string) (string, error) {
	dir := SecretsDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("preparing %s: %w", dir, err)
	}
	path := dir + "/" + name
	if err := os.WriteFile(path, []byte(strings.TrimSpace(token)+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}
