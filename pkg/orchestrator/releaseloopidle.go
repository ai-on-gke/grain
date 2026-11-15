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
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The continuous release loop keeps one release candidate in qualification
// whenever main carries new known-good fixes or features
// (design/playbook/releases.playbook.md, section 15).
//
// When no vX.Y.Z-rc.N without a -discarded, release or -pending marker has
// existed for ReleaseLoopIdleAfter (~2x a normal Stage 1 duration, 3h),
// while main has a tests.yml + release.yml green commit newer than the
// newest rc or release that carries a fix or feature, and neither a hold on
// the next cut (grain/rc-hold, an open [rc-blocker <rc>], the triage window
// of pipeline.rc-failed) nor a staged -pending release explains the wait,
// something has stalled the loop between candidates.
// reconcileReleaseLoopIdle raises pipeline.release-loop-idle:<repo>, naming
// whether the release-train schedules' newest tick is queued, starved or
// failed, and clears it as soon as an rc is cut (or nothing is owed).
//
// A repository opts in by declaring the train's schedules: any enabled
// schedule in overlap group release-train, filing template release-train,
// or watching a workflow on rc tags (.grain/schedules/release-train*.yml).

const (
	// ReleaseLoopIdleAfter is how long a repository running the continuous
	// release loop may go without a qualifying rc while main has an owed
	// known-good commit: ~2x a normal 60-90m Stage 1 run.
	ReleaseLoopIdleAfter = 3 * time.Hour

	// releaseLoopIdlePollInterval bounds the forge reads per repository
	// while still noticing a newly cut rc within a minute.
	releaseLoopIdlePollInterval = time.Minute

	// releaseLoopMaxLookback bounds the workflow-run reads when main's tip
	// is still running CI and an earlier commit since the floor is LKG.
	releaseLoopMaxLookback = 50
)

var releaseLoopIdleNotifier = model.PrincipalString(model.Principal{
	Kind: model.PrincipalAutomation,
	ID:   "release-loop-watchdog",
})

var conventionalSubjectPattern = regexp.MustCompile(`^([A-Za-z]+)(\([^)]*\))?(!)?: `)

var unreleasedDropPattern = regexp.MustCompile(`(?m)^Discard-Reason:\s*(daily-cap|held-over)\s*$`)

// releaseLoopIdleForge is the minimum forge surface the watchdog reads; a
// client without it raises nothing.
type releaseLoopIdleForge interface {
	github.TagLister
	CompareCommits(owner, repo, base, head string) ([]github.Commit, error)
	ListWorkflowRuns(owner, repo, headSHA string) ([]github.CheckRun, error)
}

type releaseLoopCheckRunner interface {
	ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error)
}

type releaseLoopCheckAppReader interface {
	CheckRunApp(owner, repo string) (string, error)
}

type releaseLoopWorkflowOnCommitReader interface {
	WorkflowRunsOnCommit(owner, repo, headSHA string) ([]github.WorkflowRun, error)
}

type releaseLoopBranchHeadReader interface {
	GetBranchHead(owner, repo, branch string) (*github.BranchHead, error)
}

// releaseLoopState is what the watchdog remembers of one repository
// between cycles; re-learnt after a restart from the open notification.
type releaseLoopState struct {
	polledAt  time.Time
	idleSince time.Time
	raised    bool
}

var (
	releaseLoopStates sync.Map // storeID|repo -> releaseLoopState
	releaseLoopMu     sync.Mutex
)

func releaseLoopKey(store *model.Store, repo model.RepoRef) string {
	return fmt.Sprintf("%d|%s", store.ID(), strings.ToLower(repo.String()))
}

func releaseLoopIdleSubject(repo model.RepoRef) string {
	return model.NotificationSubject(model.FamilyReleaseLoopIdle, strings.ToLower(repo.String()))
}

func releaseLoopIdleThreshold() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GRAIN_RELEASE_LOOP_IDLE_AFTER")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return ReleaseLoopIdleAfter
}

// isReleaseTrainSchedule reports whether s is one of a repository's
// release-train schedules (.grain/schedules/release-train.yml or
// .grain/schedules/release-train-on-stage1.yml).
func isReleaseTrainSchedule(s model.Schedule) bool {
	if s.Target.Owner == "" || s.Target.Name == "" {
		return false
	}
	if s.OverlapGroup == "release-train" {
		return true
	}
	if s.TemplateID != nil {
		tmpl := *s.TemplateID
		if tmpl == "release-train" || strings.HasSuffix(tmpl, ":release-train") {
			return true
		}
	}
	trig := s.Recurrence.GitHubWorkflowRun
	return s.Recurrence.Kind == model.RecurrenceGitHubWorkflowRun && trig != nil && trig.MatchesBranch("v1.0.0-rc.1", "")
}

// reconcileReleaseLoopIdle watches every repository opted in to the
// continuous release loop and raises or clears pipeline.release-loop-idle.
func reconcileReleaseLoopIdle(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Client == nil {
		return nil
	}
	forge, ok := deps.Client.(releaseLoopIdleForge)
	if !ok {
		return nil
	}
	schedules, err := deps.Store.ListSchedules(ctx)
	if err != nil {
		return err
	}
	type repoSchedules struct {
		repo      model.RepoRef
		schedules []model.Schedule
		enabled   bool
		base      string
	}
	byRepo := map[string]*repoSchedules{}
	var order []string
	for _, s := range schedules {
		if !isReleaseTrainSchedule(s) {
			continue
		}
		r := s.Target
		if model.IsConfigRepo(r) || gitproxy.RetiredConfigRepo(r.Host, r.Owner, r.Name) {
			continue
		}
		k := strings.ToLower(r.String())
		entry := byRepo[k]
		if entry == nil {
			entry = &repoSchedules{repo: r}
			byRepo[k] = entry
			order = append(order, k)
		}
		entry.schedules = append(entry.schedules, s)
		if s.Enabled {
			entry.enabled = true
			if entry.base == "" && s.Base != "" {
				entry.base = s.Base
			}
		}
	}
	sort.Strings(order)

	releaseLoopMu.Lock()
	defer releaseLoopMu.Unlock()

	var errs []error
	keepOpen := map[string]bool{}
	threshold := releaseLoopIdleThreshold()

	for _, k := range order {
		entry := byRepo[k]
		repo := entry.repo
		if !entry.enabled || isGitOnlyRepo(deps.Client, repo) {
			continue
		}
		if allowed, err := repoAllowedForAnyTeam(ctx, deps.Store, repo); err != nil {
			errs = append(errs, err)
			continue
		} else if !allowed {
			continue
		}
		branch := entry.base
		if branch == "" {
			branch = "main"
		}

		subject := releaseLoopIdleSubject(repo)
		stateKey := releaseLoopKey(deps.Store, repo)
		var st releaseLoopState
		if v, ok := releaseLoopStates.Load(stateKey); ok {
			st = v.(releaseLoopState)
		}
		if !st.polledAt.IsZero() && !now.Before(st.polledAt) && now.Sub(st.polledAt) < releaseLoopIdlePollInterval {
			if st.raised {
				keepOpen[subject] = true
			}
			continue
		}

		check, err := checkReleaseLoopIdle(ctx, deps.Store, deps.Client, forge, repo, branch, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("release-loop watchdog for %s: %w", repo, err))
			if st.raised {
				keepOpen[subject] = true
			}
			continue
		}
		if !check.owed {
			releaseLoopStates.Store(stateKey, releaseLoopState{polledAt: now})
			continue
		}

		if st.idleSince.IsZero() {
			if open, err := deps.Store.OpenNotification(ctx, subject); err == nil && open != nil {
				st.idleSince = open.RaisedAt
				st.raised = true
			} else {
				st.idleSince = now
			}
		}
		st.polledAt = now
		idleFor := now.Sub(st.idleSince)
		if idleFor < threshold {
			st.raised = false
			releaseLoopStates.Store(stateKey, st)
			continue
		}

		tickNote, tickTaskID, err := describeReleaseTrainTick(ctx, deps.Store, entry.schedules, repo, now)
		if err != nil {
			errs = append(errs, err)
			if st.raised {
				keepOpen[subject] = true
			}
			continue
		}
		st.raised = true
		releaseLoopStates.Store(stateKey, st)
		keepOpen[subject] = true

		floorDesc := "the start of history"
		if check.floorTag != "" {
			floorDesc = fmt.Sprintf("`%s` (%s)", check.floorTag, shortSHA(check.floorSHA))
		}
		body := fmt.Sprintf("No release candidate of %s has been in qualification for %s, while `%s` has "+
			"known-good (`tests.yml` + `release.yml` green) commit %s carrying a fix or feature newer than %s, "+
			"and neither `grain/rc-hold` nor a `-pending` release explains the wait.\n\n"+
			"Release-train schedules: %s.\n\n"+
			"This clears automatically once the next `vX.Y.Z-rc.N` is cut "+
			"(design/playbook/releases.playbook.md, section 15).",
			repo, humanDuration(idleFor.Round(time.Minute)), branch, shortSHA(check.lkgSHA), floorDesc, tickNote)

		errs = append(errs, ensurePipelineNotification(ctx, deps.Store, model.Notification{
			Subject:  subject,
			Family:   model.FamilyReleaseLoopIdle,
			Severity: model.NotificationWarning,
			Title: fmt.Sprintf("Release train on %s has had no rc in qualification for %s",
				repo, humanDuration(idleFor.Round(time.Minute))),
			Body:     body,
			TaskID:   tickTaskID,
			Repo:     repo.String(),
			RaisedBy: releaseLoopIdleNotifier,
			Actions:  []string{"investigate"},
		}, now))
	}

	open, err := deps.Store.ListNotifications(ctx, model.NotificationFilter{
		Open: true, Family: model.FamilyReleaseLoopIdle, Limit: 1000,
	})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, n := range open {
		if keepOpen[n.Subject] {
			continue
		}
		if err := clearPipelineNotification(ctx, deps.Store, n.Subject, releaseLoopIdleNotifier); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// releaseLoopCheck is one read of a repository: whether a release
// candidate is owed and not explained, and what made it owed.
type releaseLoopCheck struct {
	owed     bool
	floorTag string
	floorSHA string
	lkgSHA   string
}

// leftBehind reports whether rc can no longer be the candidate the train
// is working on: superseded by a newer rc or its own release, or a version
// at or below the newest release (a legacy rc nobody dropped).
func leftBehind(byName map[string]string, rc model.RCTag, newestPromoted string) bool {
	return superseded(byName, rc) || (newestPromoted != "" && !semverLess(newestPromoted, rc.Version()))
}

func checkReleaseLoopIdle(ctx context.Context, store *model.Store, rawClient github.Client,
	forge releaseLoopIdleForge, repo model.RepoRef, branch string, now time.Time) (releaseLoopCheck, error) {

	owner, name := repo.StoredOwner(), repo.Name
	tags, err := forge.ListTags(owner, name)
	if err != nil {
		return releaseLoopCheck{}, err
	}
	byName := make(map[string]string, len(tags))
	newestPromoted := ""
	for _, t := range tags {
		if t.Name == "" || t.SHA == "" {
			continue
		}
		byName[t.Name] = t.SHA
		if releaseTagPattern.MatchString(t.Name) && (newestPromoted == "" || semverLess(newestPromoted, t.Name)) {
			newestPromoted = t.Name
		}
	}

	// 1. An rc with no -discarded, release or -pending marker, that nothing
	//    has left behind, is in qualification right now.
	for _, t := range tags {
		rc, ok := model.ParseRCTag(t.Name)
		if !ok || t.SHA == "" {
			continue
		}
		if _, discarded := byName[t.Name+"-discarded"]; discarded {
			continue
		}
		if _, pending := byName[rc.Version()+"-pending"]; pending {
			continue
		}
		if leftBehind(byName, rc, newestPromoted) {
			continue
		}
		return releaseLoopCheck{}, nil
	}

	// 2. A staged vX.Y.Z-pending past the newest release waits on a person.
	for _, t := range tags {
		if m := pendingTagPattern.FindStringSubmatch(t.Name); m != nil {
			if _, promoted := byName[m[1]]; !promoted && (newestPromoted == "" || semverLess(newestPromoted, m[1])) {
				return releaseLoopCheck{}, nil
			}
		}
	}

	// 3. A dropped rc of an unreleased version whose next cut is held
	//    (rc_held): an open [rc-blocker <rc>], the triage-pending window of
	//    pipeline.rc-failed, or grain/rc-hold in progress on its commit.
	var newestDropped model.RCTag
	var newestDroppedSHA string
	hasDropped := false
	for _, t := range tags {
		rc, ok := model.ParseRCTag(t.Name)
		if !ok || t.SHA == "" {
			continue
		}
		if _, discarded := byName[t.Name+"-discarded"]; !discarded {
			continue
		}
		if _, pending := byName[rc.Version()+"-pending"]; pending {
			continue
		}
		if leftBehind(byName, rc, newestPromoted) {
			continue
		}
		held, err := releaseLoopRCHeldInStore(ctx, store, repo, t.Name, now)
		if err != nil {
			return releaseLoopCheck{}, err
		}
		if held {
			return releaseLoopCheck{}, nil
		}
		if !hasDropped || newestDropped.Less(rc) {
			newestDropped, newestDroppedSHA, hasDropped = rc, t.SHA, true
		}
	}
	if hasDropped {
		held, err := releaseLoopRCHeldOnForge(rawClient, repo, newestDroppedSHA)
		if err != nil {
			return releaseLoopCheck{}, err
		}
		if held {
			return releaseLoopCheck{}, nil
		}
	}

	// 4. The floor: the newest rc or release, passing over an rc the daily
	//    cap dropped unjudged (scripts/release-from-trunk.sh's
	//    unreleased_drop), whose commit stays eligible.
	floorTag, floorSHA := newestRCOrReleaseFloor(rawClient, repo, tags)

	// 5. A tests.yml + release.yml green commit on branch past the floor,
	//    with a fix or feature in the range up to it.
	if floorSHA == "" {
		headReader, ok := rawClient.(releaseLoopBranchHeadReader)
		if !ok {
			return releaseLoopCheck{}, nil
		}
		head, err := headReader.GetBranchHead(owner, name, branch)
		if err != nil || head == nil || head.SHA == "" {
			return releaseLoopCheck{}, err
		}
		green, err := stage0GreenOnCommit(rawClient, forge, repo, head.SHA)
		if err != nil || !green {
			return releaseLoopCheck{}, err
		}
		return releaseLoopCheck{owed: true, lkgSHA: head.SHA}, nil
	}

	commits, err := forge.CompareCommits(owner, name, floorSHA, branch)
	if err != nil {
		return releaseLoopCheck{}, err
	}
	firstWorthy := slices.IndexFunc(commits, commitWarrantsRelease)
	if firstWorthy < 0 {
		return releaseLoopCheck{}, nil
	}
	checked := 0
	for i := len(commits) - 1; i >= firstWorthy && checked < releaseLoopMaxLookback; i-- {
		sha := commits[i].SHA
		if sha == "" || sha == floorSHA {
			continue
		}
		checked++
		green, err := stage0GreenOnCommit(rawClient, forge, repo, sha)
		if err != nil {
			return releaseLoopCheck{}, err
		}
		if green {
			return releaseLoopCheck{owed: true, floorTag: floorTag, floorSHA: floorSHA, lkgSHA: sha}, nil
		}
	}
	return releaseLoopCheck{}, nil
}

// releaseLoopRCHeldInStore is the store's half of syncRCHold's rule: open
// blockers, or the triage window of an open pipeline.rc-failed the steward
// has not yet answered.
func releaseLoopRCHeldInStore(ctx context.Context, store *model.Store, repo model.RepoRef, rcTag string, now time.Time) (bool, error) {
	marker := model.RCBlockerMarker(rcTag)
	blockers, err := store.OpenTasksTitled(ctx, marker)
	if err != nil {
		return false, err
	}
	if len(blockers) > 0 {
		return true, nil
	}
	grace := releaseHoldGrace()
	if grace <= 0 {
		return false, nil
	}
	n, err := store.OpenNotification(ctx, model.NotificationSubject(model.FamilyRCFailed, rcTag))
	if err != nil || n == nil {
		return false, err
	}
	if !strings.EqualFold(n.Repo, repo.String()) || now.Sub(n.RaisedAt) >= grace {
		return false, nil
	}
	hadBlockers, err := store.HasTasksTitled(ctx, marker)
	if err != nil || hadBlockers {
		return false, err
	}
	triaged, err := stewardTriagedCandidate(ctx, store, rcTag, n.RaisedAt)
	if err != nil || triaged {
		return false, err
	}
	return true, nil
}

// releaseLoopRCHeldOnForge reports whether grain/rc-hold is in progress on
// sha, as the train reads it (rc_hold_summary).
func releaseLoopRCHeldOnForge(rawClient github.Client, repo model.RepoRef, sha string) (bool, error) {
	runner, ok := rawClient.(releaseLoopCheckRunner)
	if !ok || sha == "" {
		return false, nil
	}
	var app string
	if appReader, ok := rawClient.(releaseLoopCheckAppReader); ok {
		a, err := appReader.CheckRunApp(repo.StoredOwner(), repo.Name)
		if err != nil {
			return false, err
		}
		app = a
	}
	checks, err := runner.ListCheckRuns(repo.StoredOwner(), repo.Name, sha)
	if err != nil {
		return false, err
	}
	var latest *github.CheckRun
	for i := range checks {
		c := &checks[i]
		if c.Name != model.ReleaseHoldCheckName {
			continue
		}
		if app != "" && c.App != "" && c.App != app {
			continue
		}
		if latest == nil || c.ID >= latest.ID {
			latest = c
		}
	}
	return latest != nil && latest.Status != "completed", nil
}

// newestRCOrReleaseFloor is the newest release (vX.Y.Z) or rc
// (vX.Y.Z-rc.N) tag and its commit, skipping rcs the daily cap dropped.
func newestRCOrReleaseFloor(rawClient github.Client, repo model.RepoRef, tags []github.Tag) (string, string) {
	var tagRefs map[string]github.TagRef
	tagReader, hasTagReader := rawClient.(github.ReleaseTagReader)
	isUnreleasedDrop := func(rcName string) bool {
		if !hasTagReader {
			return false
		}
		if tagRefs == nil {
			tagRefs = map[string]github.TagRef{}
			if refs, err := tagReader.ListMatchingTags(repo.StoredOwner(), repo.Name, "v"); err == nil {
				for _, r := range refs {
					tagRefs[r.Name] = r
				}
			}
		}
		ref, ok := tagRefs[rcName+"-discarded"]
		if !ok || ref.ObjectType != "tag" || ref.ObjectSHA == "" {
			return false
		}
		at, err := tagReader.GetAnnotatedTag(repo.StoredOwner(), repo.Name, ref.ObjectSHA)
		if err != nil || at == nil {
			return false
		}
		return unreleasedDropPattern.MatchString(at.Message)
	}

	type floorCandidate struct {
		name  string
		sha   string
		ver   string
		isRel bool
		rcNum int
	}
	var best *floorCandidate
	better := func(a, b floorCandidate) bool {
		if a.ver != b.ver {
			return semverLess(b.ver, a.ver)
		}
		if a.isRel != b.isRel {
			return a.isRel
		}
		return a.rcNum > b.rcNum
	}
	for _, t := range tags {
		if t.SHA == "" {
			continue
		}
		var cand floorCandidate
		if releaseTagPattern.MatchString(t.Name) {
			cand = floorCandidate{name: t.Name, sha: t.SHA, ver: t.Name, isRel: true}
		} else if rc, ok := model.ParseRCTag(t.Name); ok {
			if isUnreleasedDrop(t.Name) {
				continue
			}
			cand = floorCandidate{name: t.Name, sha: t.SHA, ver: rc.Version(), rcNum: rc.RC}
		} else {
			continue
		}
		if best == nil || better(cand, *best) {
			c := cand
			best = &c
		}
	}
	if best == nil {
		return "", ""
	}
	return best.name, best.sha
}

// commitWarrantsRelease mirrors scripts/release-from-trunk.sh's bump_kind:
// commits that are only docs, chores, tests, ci, style or build wait, while
// fixes, features, perf, refactors, reverts, breaking changes and
// non-conventional commits warrant cutting a release candidate. A merge
// commit is judged by the pull request title on its first body line.
func commitWarrantsRelease(c github.Commit) bool {
	if commitIsBreaking(c) {
		return true
	}
	msg := strings.ReplaceAll(c.Message, "\r\n", "\n")
	subject, rest, _ := strings.Cut(msg, "\n")
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return false
	}
	if mergePullRequestPattern.MatchString(subject) ||
		strings.HasPrefix(subject, "Merge branch ") ||
		strings.HasPrefix(subject, "Merge remote-tracking branch ") {
		subject = ""
		for _, line := range strings.Split(rest, "\n") {
			if t := strings.TrimSpace(line); t != "" {
				subject = t
				break
			}
		}
		if subject == "" {
			return false
		}
	}
	m := conventionalSubjectPattern.FindStringSubmatch(subject)
	if m == nil || m[3] == "!" {
		return true
	}
	switch strings.ToLower(m[1]) {
	case "docs", "chore", "test", "tests", "ci", "style", "build":
		return false
	default:
		return true
	}
}

// stage0GreenOnCommit reports whether both tests.yml and release.yml have
// completed green on sha in runs of the repository itself (not a pull
// request's): Stage 0 of candidate-gate.sh select-lkg.
func stage0GreenOnCommit(rawClient github.Client, forge releaseLoopIdleForge, repo model.RepoRef, sha string) (bool, error) {
	owner, name := repo.StoredOwner(), repo.Name
	fullName := repo.Owner + "/" + repo.Name
	if wfReader, ok := rawClient.(releaseLoopWorkflowOnCommitReader); ok {
		runs, err := wfReader.WorkflowRunsOnCommit(owner, name, sha)
		if err != nil {
			return false, err
		}
		if len(runs) > 0 {
			seen := map[string]github.WorkflowRun{}
			for _, r := range runs {
				switch r.Event {
				case "push", "schedule", "workflow_dispatch", "":
				default:
					continue
				}
				if r.HeadRepo != "" && !strings.EqualFold(r.HeadRepo, fullName) && !strings.EqualFold(r.HeadRepo, owner+"/"+name) {
					continue
				}
				wf := stage0WorkflowKey(r.Path, r.Name)
				if wf == "" {
					continue
				}
				if prev, exists := seen[wf]; !exists || r.ID > prev.ID {
					seen[wf] = r
				}
			}
			tests, okT := seen["tests"]
			release, okR := seen["release"]
			return okT && okR && workflowRunGreen(tests) && workflowRunGreen(release), nil
		}
	}
	checks, err := forge.ListWorkflowRuns(owner, name, sha)
	if err != nil {
		return false, err
	}
	seen := map[string]github.CheckRun{}
	for _, c := range checks {
		wf := stage0WorkflowKey("", c.Name)
		if wf == "" {
			continue
		}
		if prev, exists := seen[wf]; !exists || c.ID >= prev.ID {
			seen[wf] = c
		}
	}
	return checkRunGreen(seen["tests"]) && checkRunGreen(seen["release"]), nil
}

func stage0WorkflowKey(wfPath, wfName string) string {
	for _, raw := range []string{wfPath, wfName} {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if i := strings.IndexByte(s, '@'); i >= 0 {
			s = s[:i]
		}
		base := strings.ToLower(path.Base(s))
		base = strings.TrimSuffix(strings.TrimSuffix(base, ".yml"), ".yaml")
		switch base {
		case "tests", "release":
			return base
		}
	}
	return ""
}

func workflowRunGreen(r github.WorkflowRun) bool {
	if r.Status != "completed" {
		return false
	}
	switch r.Conclusion {
	case "success", "neutral", "skipped":
		return true
	default:
		return false
	}
}

func checkRunGreen(c github.CheckRun) bool {
	if c.Status != "completed" || c.Conclusion == nil {
		return false
	}
	switch *c.Conclusion {
	case "success", "neutral", "skipped":
		return true
	default:
		return false
	}
}

// describeReleaseTrainTick says what the newest tick across repo's
// release-train schedules is doing -- queued, starved or failed above all
// -- and names its task, if it has one.
func describeReleaseTrainTick(ctx context.Context, store *model.Store, schedules []model.Schedule,
	repo model.RepoRef, now time.Time) (string, string, error) {

	var newestTask *model.Task
	var newestSched model.Schedule
	var failedSkip *model.ScheduleSkip
	var failedSkipSched model.Schedule

	for _, s := range schedules {
		t, err := store.NewestTaskWithTag(ctx, model.ScheduleFiringTag(s.ID))
		if err != nil {
			return "", "", err
		}
		if t != nil && (newestTask == nil || taskNewer(*t, *newestTask)) {
			newestTask, newestSched = t, s
		}
		skips, err := store.ScheduleSkips(ctx, s.ID)
		if err != nil {
			return "", "", err
		}
		for i := range skips {
			sk := skips[i]
			if sk.Reason == model.SkippedFiringFailed && sk.Outcome == model.ScheduleSkipHeld &&
				(failedSkip == nil || sk.Occurrence.After(failedSkip.Occurrence)) {
				failedSkip, failedSkipSched = &sk, s
			}
		}
	}
	// A tick whose schedule was since renamed still carries the group tag.
	if groupTag := model.ScheduleOverlapGroupTag(repo, "release-train"); groupTag != "" {
		t, err := store.NewestTaskWithTag(ctx, groupTag)
		if err != nil {
			return "", "", err
		}
		if t != nil && (newestTask == nil || taskNewer(*t, *newestTask)) {
			newestTask = t
			newestSched = model.Schedule{ID: "release-train"}
			for _, s := range schedules {
				if slices.Contains(t.Tags, model.ScheduleFiringTag(s.ID)) {
					newestSched = s
				}
			}
		}
	}

	if failedSkip != nil && (newestTask == nil || newestTask.CreatedAt == nil || failedSkip.Occurrence.After(*newestTask.CreatedAt)) {
		what := fmt.Sprintf("newest tick failed to fire on schedule %q (%s)", scheduleLabel(failedSkipSched), failedSkipSched.ID)
		if detail := strings.TrimSpace(failedSkip.Detail); detail != "" {
			what += ": " + detail
		}
		return what, "", nil
	}
	if newestTask == nil {
		return "no release-train tick has been filed yet", "", nil
	}

	st, err := store.State(ctx, newestTask.ID)
	if err != nil {
		return "", "", err
	}
	runs, err := store.Runs(ctx, newestTask.ID)
	if err != nil {
		return "", "", err
	}
	created := now
	if newestTask.CreatedAt != nil {
		created = *newestTask.CreatedAt
	}
	wait := max(now.Sub(created).Round(time.Minute), 0)
	link := fmt.Sprintf("[task %s](/tasks/%s)", newestTask.ID, newestTask.ID)
	schedName := scheduleLabel(newestSched)

	switch st {
	case model.StateQueued:
		starved, err := isReleaseTrainTickStarved(ctx, store, schedules, *newestTask, runs, now)
		if err != nil {
			return "", "", err
		}
		if starved {
			return fmt.Sprintf("newest tick %s (%q) is starved -- queued for %s without ever starting",
				link, schedName, humanDuration(wait)), newestTask.ID, nil
		}
		return fmt.Sprintf("newest tick %s (%q) is queued (waiting %s, priority %d)",
			link, schedName, humanDuration(wait), newestTask.Priority), newestTask.ID, nil
	case model.StateFailed:
		if detail := latestRunFailureDetail(runs); detail != "" {
			return fmt.Sprintf("newest tick %s (%q) failed: %s", link, schedName, detail), newestTask.ID, nil
		}
		return fmt.Sprintf("newest tick %s (%q) failed", link, schedName), newestTask.ID, nil
	}
	if why := closedTickFailureReason(ctx, store, newestTask.ID, runs); why != "" {
		return fmt.Sprintf("newest tick %s (%q) failed: %s", link, schedName, why), newestTask.ID, nil
	}
	switch st {
	case model.StateRunning:
		return fmt.Sprintf("newest tick %s (%q) is running (filed %s ago)",
			link, schedName, humanDuration(wait)), newestTask.ID, nil
	case model.StateAwaitingReply, model.StateDeferred, model.StateProposed:
		return fmt.Sprintf("newest tick %s (%q) is parked in `%s`, holding `overlap: skip` (filed %s ago)",
			link, schedName, st, humanDuration(wait)), newestTask.ID, nil
	default:
		return fmt.Sprintf("newest tick %s (%q) finished in `%s` (filed %s ago) without cutting an rc",
			link, schedName, st, humanDuration(wait)), newestTask.ID, nil
	}
}

// isReleaseTrainTickStarved is the scheduler's starved rule
// (model.ScheduleStarved) for a never-started tick.
func isReleaseTrainTickStarved(ctx context.Context, store *model.Store, schedules []model.Schedule,
	task model.Task, runs []model.Run, now time.Time) (bool, error) {

	if len(runs) > 0 {
		return false, nil
	}
	if task.CreatedAt != nil && now.Sub(*task.CreatedAt) >= model.ScheduleStarvedAfter {
		return true, nil
	}
	for _, s := range schedules {
		open, err := store.OpenNotification(ctx, model.NotificationSubject(model.FamilyScheduleStarved, s.ID))
		if err != nil {
			return false, err
		}
		if open != nil {
			return true, nil
		}
		skips, err := store.ScheduleSkips(ctx, s.ID)
		if err != nil {
			return false, err
		}
		for _, sk := range skips {
			if sk.TaskID == task.ID && sk.Outcome == model.ScheduleSkipHeld &&
				model.ScheduleStarved(sk.Occurrence, now, sk.Folded+1) {
				return true, nil
			}
		}
	}
	return false, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func latestRunFailureDetail(runs []model.Run) string {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Outcome == "failed" || runs[i].Outcome == "errored" {
			if d := strings.TrimSpace(runs[i].Detail); d != "" {
				return clip(d, 300)
			}
			return runs[i].Outcome
		}
	}
	return ""
}

// closedTickFailureReason is why a finished tick failed: its last run
// failed, or the train itself said RELEASE_TRAIN_RESULT=failed.
func closedTickFailureReason(ctx context.Context, store *model.Store, taskID string, runs []model.Run) string {
	if len(runs) > 0 {
		if last := runs[len(runs)-1]; last.Outcome == "failed" || last.Outcome == "errored" {
			return latestRunFailureDetail(runs)
		}
	}
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		return ""
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if strings.Contains(comments[i].Body, "RELEASE_TRAIN_RESULT=failed") {
			return clip(strings.TrimSpace(comments[i].Body), 300)
		}
	}
	return ""
}

func taskNewer(a, b model.Task) bool {
	var ta, tb time.Time
	if a.CreatedAt != nil {
		ta = *a.CreatedAt
	}
	if b.CreatedAt != nil {
		tb = *b.CreatedAt
	}
	if !ta.Equal(tb) {
		return ta.After(tb)
	}
	return a.ID > b.ID
}

func scheduleLabel(s model.Schedule) string {
	if s.Title != "" {
		return s.Title
	}
	if s.ID != "" {
		return s.ID
	}
	return "release-train"
}
