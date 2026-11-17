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

// Stage 2 of release-candidate qualification, and the bridge that takes
// its verdict to GitHub (design/playbook/releases.playbook.md §2, §3,
// §4, §12 and §14; implementation gap 6).
//
// For every repository that declares a `release-qualification` suite,
// this reads the repository's tags for its newest `vX.Y.Z-rc.N` that has
// been neither discarded nor promoted. Once that tag's Stage 1 run
// (`nightly.yml`) is green on rc_sha, it starts the suite against it at
// model.PriorityReleaseGate, with the candidate's rc_tag and rc_sha
// filled into every item's template. It then judges each item
// objectively (model.JudgeQualificationAttempt), re-runs a first product
// failure once and an infrastructure casualty up to three times, files
// the suite's last item only once every other item has passed, and posts
// the outcome as check run `grain/release-qualification` on rc_sha.
//
// # Other release gates
//
// The same machinery qualifies any repo-declared suite whose file
// carries a `qualification:` stanza (model.SuiteQualification), which
// replaces the four things above that are otherwise fixed: the check
// run's name, how the candidate is found, and Stage 1 -- a workflow run
// by name, or a commit status by context. With `candidate:
// synced-release` a repository that cuts no rc tags of its own -- one
// that builds on another module's releases -- is qualified on the newest
// commit of its default branch that changed go.mod to pin a release
// (`vX.Y.Z`) of that module it has not tagged itself yet
// (syncedReleaseCandidate). Its items get the release as rc_tag and
// release, and the commit as rc_sha. The verdict, the re-runs and the
// last-item-after-the-others rule are the same; the red-candidate
// notifications and holds (releaseredrc.go) are about rc tags, and are
// not raised for it. A suite without the stanza is a gate only when it
// is named `release-qualification`, and then with every default.
//
// Stage 1 is watched as well as waited on (releasestage1.go): a
// candidate that goes red there, flakes a job, or stalls on
// infrastructure raises the same notifications and hold a Stage 2 verdict
// does, with or without a GitHub App to post check runs as.
//
// Everything here is outbound: grain asks GitHub, through the
// repository's own forge connection, and nothing on a GitHub runner has
// to reach grain. That is what lets the release train drop
// GRAIN_API_URL / GRAIN_API_TOKEN (§14).
//
// # State
//
// There is no table of its own. Every Stage 2 attempt is a set of suite
// runs tagged model.QualificationAttemptTag(baseTag, attempt) (baseTag for
// attempt 1, baseTag#2, #3, ... after an infrastructure stall), so the
// item runs of that attempt are Store.SuiteRunsTagged's answer; and the
// verdict, once reached, is the completed check run itself -- grain's own,
// posted by the GitHub App its forge connection is (latestAttemptCheckRun).
// A candidate whose newest check run concluded success or failure is final
// and never looked at again (a red candidate is qualified again by cutting
// rc.(N+1)). One that concluded neutral (stalled on infrastructure) waits
// while pipeline.release-infra-stalled is open and unacknowledged, then
// starts a fresh Stage 2 attempt and a new check run on the same commit
// once a person acknowledges or clears the stall.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ReleaseQualificationForge is every GitHub call the reconciler makes,
// all of them on the repository's own forge connection. github.RESTClient
// is one; a client lacking any of it is a deployment this does nothing
// for.
type ReleaseQualificationForge interface {
	github.TagLister
	github.CheckRunWriter
	ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error)
	// CheckRunApp is the GitHub App grain posts check runs on owner/repo
	// as (github.RESTClient.CheckRunApp), or "" when it cannot tell.
	CheckRunApp(owner, repo string) (string, error)
	ListWorkflowRuns(owner, repo, headSHA string) ([]github.CheckRun, error)
	BranchExists(owner, repo, branch string) (bool, error)
	DefaultBranch(owner, repo string) (string, error)
}

// releaseQualificationInterval is how often the cycle step asks GitHub.
// A candidate qualifies for an hour or more, so a minute of lag costs
// nothing, and a cycle every few seconds would spend two reads per
// repository each time for no answer that could have changed.
const releaseQualificationInterval = time.Minute

var releaseQualificationPolled sync.Map // *model.Store -> time.Time

// reconcileReleaseQualification is SyncReleaseQualification, at most once
// per releaseQualificationInterval.
func reconcileReleaseQualification(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Client == nil {
		return nil
	}
	forge, ok := deps.Client.(ReleaseQualificationForge)
	if !ok {
		return nil
	}
	if last, ok := releaseQualificationPolled.Load(deps.Store); ok && now.Sub(last.(time.Time)) < releaseQualificationInterval {
		return nil
	}
	releaseQualificationPolled.Store(deps.Store, now)
	return SyncReleaseQualification(ctx, deps.Store, forge, now)
}

// SyncReleaseQualification advances Stage 2 for every repo-declared
// suite that is a release gate: one whose file carries a
// `qualification:` stanza (model.SuiteQualification), or -- with every
// default, exactly as before the stanza existed -- one named
// `release-qualification`. One suite's error does not stop the others.
func SyncReleaseQualification(ctx context.Context, store *model.Store, forge ReleaseQualificationForge, now time.Time) error {
	suites, err := store.ListSuites(ctx)
	if err != nil {
		return err
	}
	stanzas, err := store.SuiteQualifications(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, suite := range suites {
		repo, stem, ok := repoDeclaredSuite(suite.ID)
		if !ok {
			continue
		}
		stanza, declared := stanzas[suite.ID]
		if !declared && stem != model.ReleaseQualificationSuiteStem {
			continue
		}
		gate := qualificationGate{repo: repo, stem: stem, suite: suite, cfg: stanza.WithDefaults()}
		var err error
		switch gate.cfg.Candidate {
		case model.CandidateSyncedRelease:
			err = syncSyncedReleaseQualification(ctx, store, forge, gate, now)
		default:
			err = syncRepoReleaseQualification(ctx, store, forge, gate, now)
		}
		if err != nil {
			what := "release qualification"
			if stem != model.ReleaseQualificationSuiteStem {
				what = stem
			}
			errs = append(errs, fmt.Errorf("%s of %s: %w", what, repo, err))
		}
	}
	return errors.Join(errs...)
}

// qualificationGate is one release-gate suite of one repository, with
// its stanza's defaults applied.
type qualificationGate struct {
	repo  model.RepoRef
	stem  string
	suite model.Suite
	cfg   model.SuiteQualification
}

// qualificationCandidate is the one commit a gate is qualifying.
type qualificationCandidate struct {
	// label is what every item's Qualified-Tag footer must name, and what
	// rc_tag is filled with: the rc tag, or for a synced release the
	// release itself.
	label string
	// release is the `vX.Y.Z` the candidate would ship as.
	release string
	sha     string
	// key tells this candidate's attempts and check run apart from every
	// other's: the rc tag, or release@sha for a synced release, since a
	// later go.mod change pinning the same release is a new candidate.
	key string
	// notifyKey is the key of the gate's pipeline notifications about it.
	notifyKey string
	// rc is set for an rc-tags candidate only: the red-candidate
	// machinery (releaseredrc.go) is about rc tags.
	rc *model.RCTag
}

// repoDeclaredSuite is the repository and file stem a repo-declared
// suite's id names (model.RepoDeclaredID).
func repoDeclaredSuite(id string) (model.RepoRef, string, bool) {
	prefix := model.RepoDeclaredPrefix + string(model.DeclaredSuite) + ":"
	if !strings.HasPrefix(id, prefix) {
		return model.RepoRef{}, "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(id, prefix), ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return model.RepoRef{}, "", false
	}
	return model.RepoRef{Owner: parts[0], Name: parts[1]}, parts[2], true
}

// newestCandidate is the newest `vX.Y.Z-rc.N` among tags that has no
// `-discarded` marker and whose version has not been promoted -- the one
// candidate qualifying at a time (playbook, "Lifecycle").
func newestCandidate(tags []github.Tag) (model.RCTag, string, bool) {
	names := make(map[string]bool, len(tags))
	for _, t := range tags {
		names[t.Name] = true
	}
	var best model.RCTag
	var sha string
	found := false
	for _, t := range tags {
		rc, ok := model.ParseRCTag(t.Name)
		if !ok || t.SHA == "" || names[t.Name+"-discarded"] || names[rc.Version()] {
			continue
		}
		if !found || best.Less(rc) {
			best, sha, found = rc, t.SHA, true
		}
	}
	return best, sha, found
}

// goModPins caches what go.mod at one commit pins a module at: a
// commit's go.mod never changes, so each is read once.
var goModPins sync.Map // redRCKey(store, repo, sha, module) -> goModPinEntry

type goModPinEntry struct {
	version string
	ok      bool
}

// goModPin is the version go.mod at sha pins module at.
func goModPin(store *model.Store, reader github.FileReader, repo model.RepoRef, sha, module string) (string, bool, error) {
	key := redRCKey(store, repo.String(), sha, module)
	if v, ok := goModPins.Load(key); ok {
		e := v.(goModPinEntry)
		return e.version, e.ok, nil
	}
	data, found, err := reader.ReadFile(repo.StoredOwner(), repo.Name, sha, "go.mod")
	if err != nil {
		return "", false, fmt.Errorf("reading go.mod at %s: %w", shortSHA(sha), err)
	}
	var e goModPinEntry
	if found {
		e.version, e.ok = model.GoModPin(data, module)
	}
	goModPins.Store(key, e)
	return e.version, e.ok, nil
}

// syncedReleaseCandidate is a synced-release gate's candidate: the
// newest commit on the repository's default branch that changed go.mod
// and whose go.mod pins a release (`vX.Y.Z`) of the upstream module --
// provided the repository has not tagged that release itself yet. A
// go.mod change pinning anything but a release (a commit of the
// upstream's trunk, say) is passed over for the one before it; the newest
// one that does pin a release decides, so an older release the
// repository never shipped is not qualified once a newer one is synced.
//
// It is a commit that changed go.mod, rather than the branch head, so
// the candidate holds still while it qualifies; and a red verdict is
// retried by landing another go.mod change that pins the same release.
func syncedReleaseCandidate(store *model.Store, forge ReleaseQualificationForge, gate qualificationGate) (qualificationCandidate, bool, error) {
	lister, canList := forge.(github.PathCommitLister)
	reader, canRead := forge.(github.FileReader)
	if !canList || !canRead {
		return qualificationCandidate{}, false, errors.New("this forge connection cannot list go.mod's commits or read files, which a synced-release candidate is found from")
	}
	upstream, err := gate.cfg.UpstreamRepo()
	if err != nil {
		return qualificationCandidate{}, false, err
	}
	upTags, err := forge.ListTags(upstream.StoredOwner(), upstream.Name)
	if err != nil {
		return qualificationCandidate{}, false, fmt.Errorf("listing %s's tags: %w", upstream, err)
	}
	releases := map[string]string{}
	for _, t := range upTags {
		if _, ok := model.ParseReleaseTag(t.Name); ok && t.SHA != "" {
			releases[t.Name] = t.SHA
		}
	}
	own, err := forge.ListTags(gate.repo.StoredOwner(), gate.repo.Name)
	if err != nil {
		return qualificationCandidate{}, false, fmt.Errorf("listing tags: %w", err)
	}
	tagged := tagCommits(own)
	branch, err := forge.DefaultBranch(gate.repo.StoredOwner(), gate.repo.Name)
	if err != nil || branch == "" {
		branch = "main"
	}
	commits, err := lister.ListPathCommits(gate.repo.StoredOwner(), gate.repo.Name, branch, "go.mod")
	if err != nil {
		return qualificationCandidate{}, false, fmt.Errorf("listing go.mod's commits on %s: %w", branch, err)
	}
	for _, c := range commits {
		version, ok, err := goModPin(store, reader, gate.repo, c.SHA, gate.cfg.Upstream)
		if err != nil {
			return qualificationCandidate{}, false, err
		}
		if !ok {
			continue
		}
		release, ok := model.PinnedRelease(version, releases)
		if !ok {
			continue
		}
		if _, done := tagged[release]; done {
			return qualificationCandidate{}, false, nil
		}
		return qualificationCandidate{
			label: release, release: release, sha: c.SHA,
			key:       release + "@" + c.SHA,
			notifyKey: gate.stem + "@" + release,
		}, true, nil
	}
	return qualificationCandidate{}, false, nil
}

// latestCheckRun is the newest check run named name that app posted for
// externalID, or nil. One of the same name from anything else -- an
// Actions workflow with checks: write, another installed app, or an
// earlier discarded candidate on the same commit -- is not this
// candidate's verdict, and neither ends qualification nor is updated as
// if it were.
func latestCheckRun(runs []github.CheckRun, name, app, externalID string) *github.CheckRun {
	var out *github.CheckRun
	for i := range runs {
		if runs[i].Name == name && runs[i].App == app && runs[i].ExternalID == externalID &&
			(out == nil || runs[i].ID > out.ID) {
			out = &runs[i]
		}
	}
	return out
}

// parseQualificationAttempt parses extID as baseTag (attempt 1) or
// baseTag#<n> (attempt n >= 2).
func parseQualificationAttempt(extID, baseTag string) (int, bool) {
	if extID == baseTag {
		return 1, true
	}
	rest, ok := strings.CutPrefix(extID, baseTag+"#")
	if !ok || rest == "" {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 2 || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}

// latestAttemptCheckRun is the highest Stage 2 attempt number app posted
// as name for baseTag and the newest check run within that attempt (or 1,
// nil when app has posted none yet).
func latestAttemptCheckRun(runs []github.CheckRun, name, app, baseTag string) (int, *github.CheckRun) {
	bestAttempt := 1
	var out *github.CheckRun
	for i := range runs {
		r := &runs[i]
		if r.Name != name || r.App != app {
			continue
		}
		attempt, ok := parseQualificationAttempt(r.ExternalID, baseTag)
		if !ok {
			continue
		}
		if out == nil || attempt > bestAttempt || (attempt == bestAttempt && r.ID > out.ID) {
			bestAttempt = attempt
			out = r
		}
	}
	return bestAttempt, out
}

// infraStallActive reports whether notifyKey's pipeline.release-infra-stalled
// notification is still open and unacknowledged.
func infraStallActive(ctx context.Context, store *model.Store, notifyKey string) (bool, error) {
	open, err := store.OpenNotification(ctx, model.NotificationSubject(model.FamilyReleaseInfraStalled, notifyKey))
	if err != nil {
		return false, err
	}
	return open != nil && open.AckedAt == nil, nil
}

// stage1Green reports whether Stage 1 has succeeded on sha: the newest
// run of its workflow (GitHub lists runs newest first), or the newest
// status of its context.
func stage1Green(forge ReleaseQualificationForge, repo model.RepoRef, sha string, cfg model.SuiteQualification) (bool, error) {
	if cfg.Stage1Status != "" {
		reader, ok := forge.(github.CommitStatusReader)
		if !ok {
			return false, fmt.Errorf("this forge connection cannot read commit statuses, and Stage 1 is status %s", cfg.Stage1Status)
		}
		statuses, err := reader.CommitStatuses(repo.StoredOwner(), repo.Name, sha)
		if err != nil {
			return false, err
		}
		for _, s := range statuses {
			if s.Context == cfg.Stage1Status {
				return s.State == "success", nil
			}
		}
		return false, nil
	}
	runs, err := forge.ListWorkflowRuns(repo.StoredOwner(), repo.Name, sha)
	if err != nil {
		return false, err
	}
	for _, r := range runs {
		if r.Name != cfg.Stage1Workflow {
			continue
		}
		return r.Status == "completed" && r.Conclusion != nil && *r.Conclusion == "success", nil
	}
	return false, nil
}

func syncRepoReleaseQualification(ctx context.Context, store *model.Store, forge ReleaseQualificationForge,
	gate qualificationGate, now time.Time) error {

	repo := gate.repo
	tags, err := forge.ListTags(repo.StoredOwner(), repo.Name)
	if err != nil {
		return fmt.Errorf("listing tags: %w", err)
	}
	commits := tagCommits(tags)
	rc, sha, ok := newestCandidate(tags)
	current := ""
	if ok {
		current = rc.String()
	}
	holds := errors.Join(syncDiscardedStage1(ctx, store, forge, gate, commits, now),
		syncRedRCHolds(ctx, store, forge, repo, commits, current, now))
	if !ok {
		return holds
	}
	cand := qualificationCandidate{
		label: rc.String(), release: rc.Version(), sha: sha,
		key: rc.String(), notifyKey: rc.String(), rc: &rc,
	}
	return errors.Join(holds, qualifyCandidate(ctx, store, forge, gate, commits, cand, now))
}

// syncSyncedReleaseQualification is a synced-release gate's Stage 2.
// There is no red-candidate machinery here (releaseredrc.go is about rc
// tags): a red verdict is the completed check run, which the repository's
// own release reads.
func syncSyncedReleaseQualification(ctx context.Context, store *model.Store, forge ReleaseQualificationForge,
	gate qualificationGate, now time.Time) error {

	cand, ok, err := syncedReleaseCandidate(store, forge, gate)
	if err != nil {
		return err
	}
	current := ""
	if ok {
		current = cand.notifyKey
	}
	stale := clearStaleInfraStalled(ctx, store, gate.repo, gate.stem+"@", current)
	if !ok {
		return stale
	}
	return errors.Join(stale, qualifyCandidate(ctx, store, forge, gate, nil, cand, now))
}

// clearStaleInfraStalled clears every open pipeline.release-infra-stalled
// of repo whose key starts with prefix and is not current's.
func clearStaleInfraStalled(ctx context.Context, store *model.Store, repo model.RepoRef, prefix, current string) error {
	open, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true, Limit: 1000})
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range open {
		family, key, _ := strings.Cut(n.Subject, ":")
		if family != model.FamilyReleaseInfraStalled || !strings.EqualFold(n.Repo, repo.String()) ||
			!strings.HasPrefix(key, prefix) || key == current {
			continue
		}
		errs = append(errs, clearPipelineNotification(ctx, store, n.Subject, releaseQualificationNotifier))
	}
	return errors.Join(errs...)
}

// qualifyCandidate advances Stage 2 of cand, the candidate gate is now
// qualifying. commits is the repository's tags, for an rc-tags
// candidate's red-candidate notification.
func qualifyCandidate(ctx context.Context, store *model.Store, forge ReleaseQualificationForge,
	gate qualificationGate, commits map[string]string, cand qualificationCandidate, now time.Time) error {

	repo, checkName, rcTag, sha := gate.repo, gate.cfg.Check, cand.label, cand.sha
	app, err := forge.CheckRunApp(repo.StoredOwner(), repo.Name)
	if err != nil {
		return fmt.Errorf("reading which GitHub App posts %s on %s: %w", checkName, rcTag, err)
	}
	baseTag := model.QualificationTag(gate.stem, repo, cand.key)
	if app == "" {
		// No check run of grain's can be told from anybody else's, so Stage 2
		// cannot run; Stage 1's notifications need none, and are raised
		// all the same (releasestage1.go).
		q := rcQualification{store: store, forge: forge, repo: repo, suite: gate.suite, rcTag: rcTag, sha: sha,
			tag: model.QualificationAttemptTag(baseTag, 1), now: now, check: checkName, release: cand.release,
			notifyKey: cand.notifyKey}
		_, stage1 := q.watchStage1(ctx, gate.cfg, commits, cand.rc, 1)
		return errors.Join(stage1, fmt.Errorf("not qualifying %s: its forge connection is not a GitHub App grain can name, so its own %s could not be told from one anything else posted",
			rcTag, checkName))
	}
	checks, err := forge.ListCheckRuns(repo.StoredOwner(), repo.Name, sha)
	if err != nil {
		return fmt.Errorf("reading checks on %s: %w", rcTag, err)
	}
	attempt, check := latestAttemptCheckRun(checks, checkName, app, baseTag)
	if check != nil && check.Status == "completed" {
		if check.Conclusion == nil || *check.Conclusion != model.ConclusionNeutral {
			return nil
		}
		nextTag := model.QualificationAttemptTag(baseTag, attempt+1)
		nextRuns, err := store.SuiteRunsTagged(ctx, nextTag)
		if err != nil {
			return err
		}
		if len(nextRuns) == 0 {
			stalled, err := infraStallActive(ctx, store, cand.notifyKey)
			if err != nil || stalled {
				return err
			}
		}
		attempt++
		check = nil
	}
	tag := model.QualificationAttemptTag(baseTag, attempt)

	suite := gate.suite
	items := make([]string, 0, len(suite.Items))
	for _, it := range suite.Items {
		items = append(items, it.TemplateID)
	}
	runs, err := store.SuiteRunsTagged(ctx, tag)
	if err != nil {
		return err
	}
	q := rcQualification{store: store, forge: forge, repo: repo, suite: suite, rcTag: rcTag, sha: sha, tag: tag, now: now,
		check: checkName, release: cand.release, notifyKey: cand.notifyKey}
	if check != nil {
		q.checkID = check.ID
	}
	qualifyingTitle := fmt.Sprintf("Qualifying %s", rcTag)
	if attempt > 1 {
		qualifyingTitle = fmt.Sprintf("Qualifying %s (attempt %d)", rcTag, attempt)
	}

	if len(runs) == 0 {
		green, err := q.watchStage1(ctx, gate.cfg, commits, cand.rc, attempt)
		if err != nil || !green {
			return err
		}
		gates := items
		if len(items) > 1 {
			gates = items[:len(items)-1]
		}
		if err := q.start(ctx, gates); err != nil {
			return err
		}
		if attempt > 1 {
			if err := clearPipelineNotification(ctx, store,
				model.NotificationSubject(model.FamilyReleaseInfraStalled, q.notifyKey), releaseQualificationNotifier); err != nil {
				return err
			}
		}
		started := fmt.Sprintf("Stage 1 (%s) is green on %s; Stage 2 started: %s.",
			gate.cfg.Stage1Name(), sha, strings.Join(gates, ", "))
		if attempt > 1 {
			started = fmt.Sprintf("Stage 1 (%s) is green on %s; Stage 2 attempt %d started: %s.",
				gate.cfg.Stage1Name(), sha, attempt, strings.Join(gates, ", "))
		}
		return q.post("in_progress", "", qualifyingTitle, started)
	}

	attempts, err := q.judge(ctx, runs)
	if err != nil {
		return err
	}
	d := model.DecideReleaseQualification(items, attempts, model.DefaultQualificationPolicy)
	summary := strings.Join(d.Lines, "\n")
	// The notifications go up before the verdict is posted: a candidate
	// with a completed check run is not looked at again (or, for neutral,
	// waits on the open stall notification), so a raise that failed after
	// it would never be retried.
	switch d.Conclusion {
	case model.ConclusionFailure:
		if cand.rc != nil {
			if err := q.raiseRCFailed(ctx, commits, *cand.rc, stage2Failure(q.check), d.Lines); err != nil {
				return err
			}
		}
	case model.ConclusionNeutral:
		if err := q.raiseInfraStalled(ctx, d.Lines[len(d.Lines)-1], true); err != nil {
			return err
		}
	case "":
		if err := q.trackInfra(ctx, items, attempts); err != nil {
			return err
		}
		if err := q.noteFlakeRetries(ctx, d.Rerun, attempts); err != nil {
			return err
		}
	}
	if d.Conclusion != "" && d.Conclusion != model.ConclusionNeutral {
		if err := clearPipelineNotification(ctx, store,
			model.NotificationSubject(model.FamilyReleaseInfraStalled, q.notifyKey), releaseQualificationNotifier); err != nil {
			return err
		}
	}
	if d.Conclusion != "" {
		return q.post("completed", d.Conclusion,
			fmt.Sprintf("Release qualification of %s: %s", rcTag, d.Conclusion), summary)
	}
	for _, item := range d.Rerun {
		if err := q.start(ctx, []string{item}); err != nil {
			return err
		}
	}
	if d.StartFinal {
		if err := q.start(ctx, items[len(items)-1:]); err != nil {
			return err
		}
	}
	if q.checkID == 0 {
		return q.post("in_progress", "", qualifyingTitle, summary)
	}
	return nil
}

// rcQualification is one candidate's Stage 2, for the length of one
// reconcile.
type rcQualification struct {
	store   *model.Store
	forge   ReleaseQualificationForge
	repo    model.RepoRef
	suite   model.Suite
	rcTag   string // the candidate's label (qualificationCandidate.label)
	sha     string
	tag     string
	checkID int64
	now     time.Time
	// check is the check run's name, release the version the candidate
	// would ship as, and notifyKey the key of its notifications
	// (qualificationCandidate's).
	check     string
	release   string
	notifyKey string
}

// start files items as one suite run of their own. The suite is run as
// the playbook requires whatever its declaration says -- until_clean,
// one pass, no auto-merge -- so a copy of it that drifted from
// .grain/suites/release-qualification.yml still cannot turn a pull
// request into a pass.
func (q *rcQualification) start(ctx context.Context, items []string) error {
	suite := q.suite
	suite.Items = make([]model.SuiteItem, 0, len(items))
	for _, item := range items {
		suite.Items = append(suite.Items, model.SuiteItem{TemplateID: item})
	}
	suite.Mode, suite.MaxPasses, suite.Count, suite.AutoMerge = model.SuiteUntilClean, 1, 0, false
	base, err := q.forge.DefaultBranch(q.repo.StoredOwner(), q.repo.Name)
	if err != nil || base == "" {
		base = "main"
	}
	_, err = q.store.CreateSuiteRunWith(ctx, suite, q.repo, base, model.SuiteRunOptions{
		Tag:      q.tag,
		Fields:   map[string]string{"rc_tag": q.rcTag, "rc_sha": q.sha, "release": q.release},
		Priority: model.PriorityReleaseGate,
	}, q.now)
	if err != nil {
		return fmt.Errorf("starting %s for %s: %w", strings.Join(items, ", "), q.rcTag, err)
	}
	return nil
}

// judge reads every attempt of every item, oldest first.
func (q *rcQualification) judge(ctx context.Context, runs []model.SuiteRun) (map[string][]model.ItemVerdict, error) {
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	out := map[string][]model.ItemVerdict{}
	for _, run := range runs {
		for _, st := range run.Tasks {
			a, err := q.attempt(ctx, st)
			if err != nil {
				return nil, err
			}
			out[st.TemplateID] = append(out[st.TemplateID], model.JudgeQualificationAttempt(a, q.rcTag, q.sha))
		}
	}
	return out, nil
}

// attempt gathers what one task's verdict is read from. Only a task that
// has finished is asked about its closing note, its scratch branch and
// its children: a running task's are not its answer yet.
func (q *rcQualification) attempt(ctx context.Context, st model.SuiteTaskStatus) (model.QualificationAttempt, error) {
	a := model.QualificationAttempt{
		TaskID: st.TaskID, TemplateID: st.TemplateID, State: st.State,
		OpenedPullRequest: st.OpenedPullRequest, Proposed: st.Proposed,
	}
	switch st.State {
	case model.StateCompleted, model.StateAwaitingReply, model.StateAwaitingSubmit, model.StateFailed:
	default:
		return a, nil
	}
	runs, err := q.store.Runs(ctx, st.TaskID)
	if err != nil {
		return a, err
	}
	if len(runs) > 0 {
		a.LastOutcome = runs[len(runs)-1].Outcome
	}
	if st.State == model.StateFailed {
		return a, nil
	}
	comments, err := q.store.Comments(ctx, st.TaskID)
	if err != nil {
		return a, err
	}
	a.Note = closingNote(comments, st.TaskID)
	leaked, err := q.forge.BranchExists(q.repo.StoredOwner(), q.repo.Name, model.QualificationScratchBranch(st.TaskID))
	if err != nil {
		return a, fmt.Errorf("checking %s's scratch branch: %w", st.TaskID, err)
	}
	a.LeakedScratchRef = leaked
	if err := q.children(ctx, &a); err != nil {
		return a, err
	}
	return a, nil
}

// children sorts what a's task proposed into scratch fixtures and
// defects.
//
// Exercising the merge queue or base-branch triage takes pull requests,
// and a qualification task gets them the only way a task can: by
// proposing tasks whose branch is its own qualification/<task-id>
// scratch branch, so they merge there and never on the default branch.
// Those are the experiment, not a finding, and are not the follow-up
// SuiteTaskStatus.Proposed counts -- but every one of them has to have
// finished by the time the task does: one still proposed, queued or
// running, or failed and waiting to be dispatched again, is scratch
// state left behind (playbook §2, condition 3). Anything proposed on any other
// branch is a defect the run found, and fails the item whatever state it
// is in.
func (q *rcQualification) children(ctx context.Context, a *model.QualificationAttempt) error {
	if !a.Proposed {
		return nil
	}
	a.Proposed = false
	scratch := model.QualificationScratchBranch(a.TaskID)
	ids, err := q.store.TasksWithLinkTarget(ctx, model.LinkProposedBy, a.TaskID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		child, err := q.store.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if child == nil {
			continue
		}
		if child.Base != scratch && !strings.HasPrefix(child.Base, scratch+"/") {
			a.Proposed = true
			continue
		}
		state, err := q.store.State(ctx, id)
		if err != nil {
			return err
		}
		switch state {
		case model.StateProposed, model.StateQueued, model.StateRunning, model.StateFailed:
			a.OpenChildren = append(a.OpenChildren, id)
		}
	}
	return nil
}

// closingNote is the newest comment taskID's own runs wrote that carries
// a verdict footer, or failing that the newest one they wrote -- which
// then fails for having none.
//
// Only the task's own runs count. Anybody else in the thread -- a person,
// a scratch fixture or any api:task-admin task writing in with
// `grain task message` (ui.Client.PostTaskNote) -- could otherwise post
// a PASS footer after the run's own FAIL and have it read as the verdict.
func closingNote(comments []model.Comment, taskID string) string {
	var newest string
	found := false
	for i := len(comments) - 1; i >= 0; i-- {
		if !ownRunComment(comments[i], taskID) {
			continue
		}
		if strings.Contains(comments[i].Body, "Qualification-Verdict:") {
			return comments[i].Body
		}
		if !found {
			newest, found = comments[i].Body, true
		}
	}
	return newest
}

// ownRunComment reports whether c is something taskID's own run said:
// grain relaying it on the run's behalf (finish.go's relayComment), the
// way a closing note is stored.
func ownRunComment(c model.Comment, taskID string) bool {
	by := c.Author.OnBehalfOf
	return c.Author.Actor.Kind == model.PrincipalAutomation && by != nil &&
		by.Kind == model.PrincipalAgent && by.ID == taskID
}

// post writes the check run on rc_sha: updating the one already there,
// or creating it.
func (q *rcQualification) post(status, conclusion, title, summary string) error {
	run := github.CheckRunWrite{
		Name:       q.check,
		HeadSHA:    q.sha,
		Status:     status,
		Conclusion: conclusion,
		ExternalID: q.tag,
		Output:     &github.CheckRunOutput{Title: title, Summary: summary},
	}
	now := q.now.UTC()
	if status == "completed" {
		run.CompletedAt = &now
	} else {
		run.StartedAt = &now
	}
	if q.checkID != 0 {
		if err := q.forge.UpdateCheckRun(q.repo.StoredOwner(), q.repo.Name, q.checkID, run); err != nil {
			return fmt.Errorf("updating %s on %s: %w", q.check, q.rcTag, err)
		}
		return nil
	}
	id, err := q.forge.CreateCheckRun(q.repo.StoredOwner(), q.repo.Name, run)
	if err != nil {
		return fmt.Errorf("posting %s on %s: %w", q.check, q.rcTag, err)
	}
	q.checkID = id
	return nil
}
