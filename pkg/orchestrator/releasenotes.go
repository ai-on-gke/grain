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

// Outbound filing of per-release docs/releases/vX.Y.Z.md pull requests
// (design/playbook/releases.playbook.md §8).
//
// Under continuous releases and GitHub Actions promotion, neither
// scripts/release-train-task.sh (which runs with RELEASE_NOTES_TASK=off) nor
// publish-release.yml / promote-release.yml (which run on ephemeral hosted
// runners with no GRAIN_API_TOKEN) calls POST /api/tasks/from-template to file
// .grain/templates/release-notes.yml. Without docs/releases/vX.Y.Z.md on main,
// binaries built from main lack the release's embedded notes in
// pkg/releasenotes (`grain release-notes` and GET /api/upgrade/release-notes).
//
// For every target repository whose .grain/templates/release-notes.yml has
// synced into the store, this reconciler polls the repository's release tags
// outbound and files one task from that template whenever a promoted vX.Y.Z
// tag (or a staged vX.Y.Z-pending tag waiting for approval) has no
// docs/releases/vX.Y.Z.md on the template's base branch and no existing task.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	releaseNotesTemplateStem = "release-notes"
	releaseNotesPollInterval = 10 * time.Minute

	// ReleaseNotesTaskMarkerTag marks every task reconcileReleaseNotes files.
	ReleaseNotesTaskMarkerTag = "release-notes-task"
)

var releaseNotesFiler = model.Principal{Kind: model.PrincipalAutomation, ID: "release-notes"}

var (
	releaseNotesPolled        sync.Map // store id | repo string -> time.Time
	releaseNotesMerged        sync.Map // store id | repo string | branch | version -> bool
	releaseNotesSeriesSettled sync.Map // store id | repo string | branch | M.m | highest -> bool (documented)
)

// ReleaseNotesVersionTag keys open release-notes tasks by repository and
// version so at most one task for a version is in flight at a time.
func ReleaseNotesVersionTag(repo model.RepoRef, version string) string {
	return "release-notes:" + strings.ToLower(repo.String()) + ":" + version
}

// ReleaseNotesCommitTag keys every release-notes task ever filed by
// (repository, version, peeled commit) so a task that ran and closed without
// merging (nothing_needed or operator cancellation) is not re-filed on every
// subsequent poll, while a re-staged release at a new commit still gets one.
func ReleaseNotesCommitTag(repo model.RepoRef, version, commit string) string {
	return ReleaseNotesVersionTag(repo, version) + ":" + commit
}

func releaseNotesPollKey(store *model.Store, repo model.RepoRef) string {
	return fmt.Sprintf("%d|%s", store.ID(), strings.ToLower(repo.String()))
}

func releaseNotesMergedKey(store *model.Store, repo model.RepoRef, branch, version string) string {
	return fmt.Sprintf("%d|%s|%s|%s", store.ID(), strings.ToLower(repo.String()), branch, version)
}

func releaseNotesSeriesKey(store *model.Store, repo model.RepoRef, branch string, major, minor int, highest string) string {
	return fmt.Sprintf("%d|%s|%s|%d.%d|%s", store.ID(), strings.ToLower(repo.String()), branch, major, minor, highest)
}

func reconcileReleaseNotes(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Client == nil {
		return nil
	}
	tags, ok := deps.Client.(github.ReleaseTagReader)
	if !ok {
		return nil
	}
	files, ok := deps.Client.(github.FileReader)
	if !ok || files == nil {
		return nil
	}
	templates, err := deps.Store.ListTemplates(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing templates for release notes: %w", err)
	}
	type declaredNotes struct {
		repo model.RepoRef
		tmpl model.Template
	}
	var declared []declaredNotes
	for _, tmpl := range templates {
		repo, ok := repoDeclaredReleaseNotesTemplate(tmpl)
		if !ok {
			continue
		}
		declared = append(declared, declaredNotes{repo: repo, tmpl: tmpl})
	}
	if len(declared) == 0 {
		return nil
	}

	globalCfg, err := deps.Store.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading config for release notes: %w", err)
	}
	var catalogue *model.CapabilityCatalogue
	if globalCfg != nil {
		catalogue = globalCfg.CapabilityPolicy.Catalogue
	}
	configs, err := deps.Store.ListRepoConfigs(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading repo configs for release notes: %w", err)
	}
	targets, err := deps.Store.TargetRepos(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading target repos for release notes: %w", err)
	}
	byRepo := make(map[string]*model.RepoConfig, len(configs))
	for i := range configs {
		cfg := &configs[i]
		byRepo[strings.ToLower(cfg.Repo.String())] = cfg
	}

	var errs []error
	for _, d := range declared {
		repo := d.repo
		if repo.Owner == "" || repo.Name == "" || model.IsConfigRepo(repo) || gitproxy.RetiredConfigRepo(repo.Host, repo.Owner, repo.Name) {
			continue
		}
		cfg := byRepo[strings.ToLower(repo.String())]
		if !model.RunsWorkItem(cfg, model.ReleaseItem) {
			continue
		}
		if !model.RepoAllowed(targets, repo) {
			continue
		}
		if isGitOnlyRepo(deps.Client, repo) {
			continue
		}
		key := releaseNotesPollKey(deps.Store, repo)
		if last, ok := releaseNotesPolled.Load(key); ok && now.Sub(last.(time.Time)) < releaseNotesPollInterval {
			continue
		}
		releaseNotesPolled.Store(key, now)
		if err := syncRepoReleaseNotes(ctx, deps, tags, files, catalogue, targets, repo, d.tmpl, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func repoDeclaredReleaseNotesTemplate(tmpl model.Template) (model.RepoRef, bool) {
	if tmpl.Target != nil {
		repo := *tmpl.Target
		if tmpl.ID == model.RepoDeclaredID(model.DeclaredTemplate, repo, releaseNotesTemplateStem) {
			return repo, true
		}
		return model.RepoRef{}, false
	}
	prefix := model.RepoDeclaredPrefix + string(model.DeclaredTemplate) + ":"
	rest, ok := strings.CutPrefix(tmpl.ID, prefix)
	if !ok {
		return model.RepoRef{}, false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] != releaseNotesTemplateStem {
		return model.RepoRef{}, false
	}
	return model.RepoRef{Owner: parts[0], Name: parts[1]}, true
}

func syncRepoReleaseNotes(ctx context.Context, deps Deps, tags github.ReleaseTagReader, files github.FileReader,
	catalogue *model.CapabilityCatalogue, targets []string, repo model.RepoRef, tmpl model.Template, now time.Time) error {

	baseBranch := strings.TrimSpace(tmpl.Base)
	if baseBranch == "" {
		def, err := deps.Client.DefaultBranch(repo.StoredOwner(), repo.Name)
		if err != nil {
			return fmt.Errorf("orchestrator: reading %s's default branch for release notes: %w", repo, github.Readable(err))
		}
		baseBranch = strings.TrimSpace(def)
		if baseBranch == "" {
			baseBranch = "main"
		}
	}

	refs, err := tags.ListMatchingTags(repo.StoredOwner(), repo.Name, "v")
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's release tags for release notes: %w", repo, github.Readable(err))
	}

	releasedTags := map[string]github.TagRef{}
	released := map[string]bool{}
	pendingTags := map[string]github.TagRef{}
	retracted := map[string]bool{}
	for _, t := range refs {
		switch {
		case releaseTagPattern.MatchString(t.Name):
			releasedTags[t.Name] = t
			released[t.Name] = true
		case pendingTagPattern.MatchString(t.Name):
			m := pendingTagPattern.FindStringSubmatch(t.Name)
			pendingTags[m[1]] = t
		case retractedTagPattern.MatchString(t.Name):
			m := retractedTagPattern.FindStringSubmatch(t.Name)
			retracted[m[1]] = true
		}
	}

	candidates := map[string]github.TagRef{}
	for v, t := range releasedTags {
		if !retracted[v] {
			candidates[v] = t
		}
	}
	for v, t := range pendingTags {
		if !released[v] && !retracted[v] {
			candidates[v] = t
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	versions := make([]string, 0, len(candidates))
	for v := range candidates {
		versions = append(versions, v)
	}
	slices.SortFunc(versions, func(a, b string) int {
		switch {
		case semverLess(a, b):
			return -1
		case semverLess(b, a):
			return 1
		default:
			return 0
		}
	})

	missing, errs := missingReleaseNotesVersions(ctx, deps.Store, files, repo, baseBranch, versions)
	if len(missing) == 0 {
		return errors.Join(errs...)
	}

	var (
		confLoaded    bool
		neverPromotes map[string]bool
	)
	for _, version := range missing {
		open, err := hasOpenReleaseNotesTask(ctx, deps.Store, repo, tmpl.ID, version)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: checking open release-notes tasks for %s %s: %w", repo, version, err))
			continue
		}
		if open {
			continue
		}

		if !confLoaded {
			neverPromotes = readReleaseNotesNeverPromote(files, repo, baseBranch)
			confLoaded = true
		}
		if neverPromotes[version] {
			continue
		}

		notesPath := "docs/releases/" + version + ".md"
		tag := candidates[version]
		commit, _, err := peelPendingTag(tags, repo, tag)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading %s's %s tag: %w", repo, tag.Name, github.Readable(err)))
			continue
		}
		if commit == "" {
			continue
		}

		commitTag := ReleaseNotesCommitTag(repo, version, commit)
		alreadyFiled, err := deps.Store.HasTaskWithTag(ctx, commitTag)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: checking filed release-notes tag %s: %w", commitTag, err))
			continue
		}
		if alreadyFiled {
			continue
		}

		prev := newestNotesBase(released, retracted, neverPromotes, version)
		comp, err := deps.Store.TemplateComposition(ctx, tmpl, map[string]string{
			"version":          version,
			"previous_version": prev,
		}, repo, baseBranch, true)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: composing release-notes task for %s %s: %w", repo, version, err))
			continue
		}
		if comp.Target == nil || !comp.Target.Same(repo) {
			continue
		}
		holdReasons := applyReleaseNotesCatalogue(catalogue, repo, tmpl, &comp)
		if len(holdReasons) > 0 || !releaseNotesCompositionMayAutoApprove(comp, repo, targets) {
			comp.Approved = false
		}

		id, err := deps.Store.NewTaskID(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: allocating id for %s %s release notes: %w", repo, version, err))
			continue
		}
		orderKey, err := deps.Store.OrderKeyForNewTask(ctx, false)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: allocating order key for %s %s release notes: %w", repo, version, err))
			continue
		}

		task := comp.Task(id, releaseNotesFiler, model.ReasonSchedule, now)
		if len(holdReasons) > 0 {
			task.Body += fmt.Sprintf("\n\n_Filed for a person to approve: %s._", strings.Join(holdReasons, "; "))
		}
		task.Body += fmt.Sprintf("\n\n---\n\ngrain: filed automatically from `.grain/templates/release-notes.yml` because `%s` (`%s`) on `%s` has no `%s` on `%s`.",
			tag.Name, shortCommit(commit), repo, notesPath, baseBranch)
		task.OrderKey = orderKey
		task.Tags = []string{
			ReleaseNotesTaskMarkerTag,
			ReleaseNotesVersionTag(repo, version),
			commitTag,
		}
		if err := deps.Store.PutTask(ctx, task); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: filing release-notes task for %s %s: %w", repo, version, err))
			continue
		}
		if err := parkUnreached(ctx, deps.Store, deps.GitReach, []string{id}, now); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("orchestrator: filed release-notes task %s for %s %s (%s, previous %q)",
			id, repo, tag.Name, shortCommit(commit), prev)
	}
	return errors.Join(errs...)
}

type releaseMinorSeries struct {
	major, minor int
	versions     []string // descending patch order (newest first)
}

// missingReleaseNotesVersions inspects versions (sorted ascending) by
// (major, minor) series from newest to oldest, returning the versions in
// ascending order that still need docs/releases/<version>.md on baseBranch.
//
// Once a patch in a (major, minor) series has docs/releases/<version>.md on
// baseBranch, older patches in that same series are superseded and skipped,
// while newer patches in that series (such as a hotfix vX.Y.Z cut on top of a
// documented vX.Y.(Z-1)) are still filed. An older (major, minor) series that
// sits below a newer documented series and has neither any docs/releases/ file
// nor any previously filed release-notes task (for example, pre-train v3.0.*
// tags rolled into v3.1.0.md) is treated as historical pre-notes history and
// not backfilled.
func missingReleaseNotesVersions(ctx context.Context, store *model.Store, files github.FileReader,
	repo model.RepoRef, baseBranch string, versions []string) ([]string, []error) {

	var series []releaseMinorSeries
	for i := len(versions) - 1; i >= 0; i-- {
		v := versions[i]
		parts, ok := parseSemverParts(v)
		if !ok {
			continue
		}
		if len(series) == 0 || series[len(series)-1].major != parts[0] || series[len(series)-1].minor != parts[1] {
			series = append(series, releaseMinorSeries{
				major:    parts[0],
				minor:    parts[1],
				versions: []string{v},
			})
			continue
		}
		series[len(series)-1].versions = append(series[len(series)-1].versions, v)
	}

	var (
		hasNewerDocumentedSeries bool
		missing                  []string
		errs                     []error
	)
	for _, s := range series {
		highest := s.versions[0]
		sKey := releaseNotesSeriesKey(store, repo, baseBranch, s.major, s.minor, highest)
		if cachedDoc, ok := releaseNotesSeriesSettled.Load(sKey); ok {
			if cachedDoc.(bool) {
				hasNewerDocumentedSeries = true
				continue
			}
			if hasNewerDocumentedSeries {
				continue
			}
		}

		var (
			seriesDocumented bool
			seriesErr        bool
			seriesMissing    []string
		)
		for idx, v := range s.versions {
			found, err := hasReleaseNotesOnBranch(store, files, repo, baseBranch, v)
			if err != nil {
				errs = append(errs, fmt.Errorf("orchestrator: checking docs/releases/%s.md on %s %s: %w",
					v, repo, baseBranch, github.Readable(err)))
				seriesErr = true
				break
			}
			if found {
				seriesDocumented = true
				if idx == 0 {
					releaseNotesSeriesSettled.Store(sKey, true)
				}
				break
			}
			seriesMissing = append(seriesMissing, v)
		}
		if seriesErr {
			continue
		}
		if seriesDocumented {
			missing = append(missing, seriesMissing...)
			hasNewerDocumentedSeries = true
			continue
		}
		if !hasNewerDocumentedSeries {
			missing = append(missing, seriesMissing...)
			continue
		}

		tracked := false
		for _, v := range s.versions {
			filed, err := store.HasTaskWithTag(ctx, ReleaseNotesVersionTag(repo, v))
			if err != nil {
				errs = append(errs, fmt.Errorf("orchestrator: checking release-notes history for %s %s: %w", repo, v, err))
				seriesErr = true
				break
			}
			if filed {
				tracked = true
				break
			}
		}
		if seriesErr {
			continue
		}
		if tracked {
			missing = append(missing, seriesMissing...)
			continue
		}
		releaseNotesSeriesSettled.Store(sKey, false)
	}

	slices.Reverse(missing)
	return missing, errs
}

func hasReleaseNotesOnBranch(store *model.Store, files github.FileReader, repo model.RepoRef, baseBranch, version string) (bool, error) {
	mergedKey := releaseNotesMergedKey(store, repo, baseBranch, version)
	if _, ok := releaseNotesMerged.Load(mergedKey); ok {
		return true, nil
	}
	notesPath := "docs/releases/" + version + ".md"
	_, found, err := files.ReadFile(repo.StoredOwner(), repo.Name, baseBranch, notesPath)
	if err != nil {
		return false, err
	}
	if found {
		releaseNotesMerged.Store(mergedKey, true)
	}
	return found, nil
}

func readReleaseNotesNeverPromote(files github.FileReader, repo model.RepoRef, baseBranch string) map[string]bool {
	_, _, never := readReleaseConf(files, repo, baseBranch)
	return never
}

// applyReleaseNotesCatalogue enforces settings/capabilities.yml at filing time
// the same way ui.Client.CreateTask and relayProposedTasks do: a refused
// default grant is dropped, and any grant the catalogue marks requires_person
// (which catalogueHold does not re-check at dispatch) or refuses outright
// returns a hold reason so the task lands unapproved for a person.
func applyReleaseNotesCatalogue(catalogue *model.CapabilityCatalogue, repo model.RepoRef, tmpl model.Template, comp *model.Composition) []string {
	if catalogue == nil || len(comp.Grants) == 0 {
		return nil
	}
	kept := make([]model.Grant, 0, len(comp.Grants))
	var needsPerson, refused []string
	for _, g := range comp.Grants {
		gc := model.GrantContext{
			Repo:         &repo,
			TemplateID:   tmpl.ID,
			TemplateName: tmpl.Name,
			Kind:         model.CatalogueKindTask,
			Grants:       []string{g.Capability},
		}
		verdict, err := catalogue.ValidateGrants(gc)
		if err != nil {
			if g.Via == model.GrantByDefault {
				continue
			}
			refused = append(refused, err.Error())
		}
		kept = append(kept, g)
		for _, req := range verdict.RequiresPerson {
			if !slices.Contains(needsPerson, req) {
				needsPerson = append(needsPerson, req)
			}
		}
	}
	comp.Grants = kept
	var whys []string
	if len(needsPerson) > 0 {
		whys = append(whys, model.CatalogueRequiresPersonNote(needsPerson))
	}
	if len(refused) > 0 {
		whys = append(whys, "settings/capabilities.yml refuses what it asked for ("+strings.Join(refused, "; ")+")")
	}
	return whys
}

func releaseNotesCompositionMayAutoApprove(comp model.Composition, repo model.RepoRef, targets []string) bool {
	if model.NormalizeAdmission(comp.Admission).AtLeast(model.AdmissionBypass) {
		return false
	}
	if comp.KeepRunning || comp.Interactive || model.NormalizeHosting(comp.Hosting) == model.HostingConsole {
		return false
	}
	if comp.Priority > model.DefaultTaskPriority {
		return false
	}
	if comp.ChangeTarget != nil || len(comp.Reviewers) > 0 {
		return false
	}
	for _, g := range comp.Grants {
		if model.IsPersonOnlyGrant(g.Capability) || model.IsAdminOnlyGrant(g.Capability) {
			return false
		}
	}
	for _, w := range comp.Writes {
		if !w.Repo.Same(repo) {
			return false
		}
	}
	for _, r := range comp.Reads {
		if model.IsConfigRepo(r) || gitproxy.RetiredConfigRepo(r.Host, r.Owner, r.Name) || !model.RepoAllowed(targets, r) {
			return false
		}
	}
	return true
}

func hasOpenReleaseNotesTask(ctx context.Context, store *model.Store, repo model.RepoRef, templateID, version string) (bool, error) {
	openByTag, err := store.HasOpenTaskWithTag(ctx, ReleaseNotesVersionTag(repo, version))
	if err != nil {
		return false, err
	}
	if openByTag {
		return true, nil
	}
	ids, err := store.OpenTasksTitled(ctx, version)
	if err != nil {
		return false, err
	}
	notesPath := "docs/releases/" + version + ".md"
	for _, id := range ids {
		t, err := store.GetTask(ctx, id)
		if err != nil {
			return false, err
		}
		if t == nil || t.Target == nil || !t.Target.Same(repo) {
			continue
		}
		if !mentionsReleaseNotesVersion(t.Title, version) {
			continue
		}
		lowerTitle := strings.ToLower(strings.TrimSpace(t.Title))
		if t.TemplateID == templateID ||
			strings.Contains(t.Body, notesPath) ||
			strings.HasPrefix(lowerTitle, "docs(releases):") ||
			strings.Contains(lowerTitle, "release notes") {
			return true, nil
		}
	}
	return false, nil
}

// mentionsReleaseNotesVersion reports whether s mentions version as a release
// version token (vX.Y.Z, vX.Y.Z-pending, or vX.Y.Z.md), rejecting partial
// prefix matches such as vX.Y.Z-rc.1, vX.Y.Z1 or vX.Y.Z.1.
func mentionsReleaseNotesVersion(s, version string) bool {
	isTokenByte := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') ||
			b == '.' || b == '_' || b == '-' || b == '+'
	}
	offset := 0
	for offset <= len(s)-len(version) {
		idx := strings.Index(s[offset:], version)
		if idx < 0 {
			return false
		}
		pos := offset + idx
		offset = pos + len(version)
		if pos > 0 && isTokenByte(s[pos-1]) {
			continue
		}
		rest := s[pos+len(version):]
		if rest == "" {
			return true
		}
		if after, ok := strings.CutPrefix(rest, "-pending"); ok {
			if after == "" || !isTokenByte(after[0]) {
				return true
			}
			continue
		}
		if after, ok := strings.CutPrefix(rest, ".md"); ok {
			if after == "" || !isTokenByte(after[0]) {
				return true
			}
			continue
		}
		if !isTokenByte(rest[0]) {
			return true
		}
	}
	return false
}
