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

package orchestrator_test

// SyncReleaseQualification for suites that declare a `qualification:`
// stanza: a downstream repository that cuts no rc tags, qualified on the
// commit that synced its go.mod to an upstream release, with Stage 1 a
// commit status and a check run of its own name.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

const (
	edUpstream = "example.com/acme/core" // whose tags live in acme/core
	edRelease  = "v1.4.0"
	edCheck    = "grain/edition-qualification"
	edStem     = "edition-qualification"
)

var (
	edCoreSHA  = "0123456789ab" + strings.Repeat("0", 28) // core's v1.4.0
	edTrunkSHA = "aaaaaaaaaaaa" + strings.Repeat("0", 28) // a core commit no release was cut from
	edOldSHA   = strings.Repeat("b", 40)                  // the edition's sync to v1.3.0
	edSyncSHA  = strings.Repeat("c", 40)                  // the edition's sync to v1.4.0
	edHeadSHA  = strings.Repeat("d", 40)                  // the edition tracking core's trunk
	edItems    = []string{"edition-a", "edition-b", "edition-smoke"}
)

func edGoMod(version string) string {
	return "module example.com/acme/edition\n\ngo 1.26\n\nrequire " + edUpstream + " " + version + "\n"
}

// fakeEditionForge is the downstream repository's forge: everything the
// rc-tags forge answers, plus the upstream's tags, go.mod's history,
// files and commit statuses.
type fakeEditionForge struct {
	fakeQualificationForge
	coreTags []github.Tag
	commits  []github.Commit   // go.mod's, newest first
	gomods   map[string]string // sha -> go.mod
	statuses map[string][]github.CommitStatus
	asked    []string // repositories whose tags were listed
}

func (f *fakeEditionForge) ListTags(owner, repo string) ([]github.Tag, error) {
	f.asked = append(f.asked, owner+"/"+repo)
	if owner == "acme" && repo == "core" {
		return f.coreTags, nil
	}
	return f.tags, nil
}

func (f *fakeEditionForge) ListPathCommits(owner, repo, ref, path string) ([]github.Commit, error) {
	if path != "go.mod" || ref != "main" {
		return nil, nil
	}
	return f.commits, nil
}

func (f *fakeEditionForge) ReadFile(owner, repo, ref, path string) ([]byte, bool, error) {
	text, ok := f.gomods[ref]
	if !ok || path != "go.mod" {
		return nil, false, nil
	}
	return []byte(text), true, nil
}

func (f *fakeEditionForge) CommitStatuses(owner, repo, ref string) ([]github.CommitStatus, error) {
	return f.statuses[ref], nil
}

func (f *fakeEditionForge) setStage1(sha, state string) {
	if f.statuses == nil {
		f.statuses = map[string][]github.CommitStatus{}
	}
	f.statuses[sha] = []github.CommitStatus{{Context: "lint", State: "success"}, {Context: edCheck, State: state}}
}

// newEditionRepo declares acme/edition's edition-qualification suite,
// with its stanza, the way a sync of its .grain directory would.
func newEditionRepo(t *testing.T) (*model.Store, context.Context, model.RepoRef, *fakeEditionForge) {
	t.Helper()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "edition"}
	var declared model.RepoDeclarations
	var items []model.SuiteItem
	for _, name := range edItems {
		id := model.RepoDeclaredID(model.DeclaredTemplate, repo, name)
		declared.Templates = append(declared.Templates, model.DeclaredTemplateFile{
			Path: model.TemplateDeclarationDir + "/" + name + ".yml",
			Template: model.Template{
				ID: id, Name: name, Title: name + " for {{release}}", Body: "Qualify {{rc_tag}} ({{release}}) at {{rc_sha}}.",
				Fields: []model.TemplateField{{Name: "rc_tag"}, {Name: "rc_sha"}, {Name: "release"}},
			},
		})
		items = append(items, model.SuiteItem{TemplateID: id})
	}
	declared.Suites = []model.DeclaredSuiteFile{{
		Path: model.SuiteDeclarationDir + "/" + edStem + ".yml",
		Suite: model.Suite{
			ID: model.RepoDeclaredID(model.DeclaredSuite, repo, edStem), Name: "Edition qualification",
			Items: items, Mode: model.SuiteUntilClean, MaxPasses: 1,
		},
		Qualification: &model.SuiteQualification{
			Check: edCheck, Candidate: model.CandidateSyncedRelease, Stage1Status: edCheck, Upstream: edUpstream,
		},
	}}
	if _, err := store.SyncRepoDeclarations(ctx, repo, strings.Repeat("e", 40), declared, baseTime, time.UTC); err != nil {
		t.Fatalf("declaring the edition's suite: %v", err)
	}
	forge := &fakeEditionForge{
		fakeQualificationForge: fakeQualificationForge{
			app:      "grain-test",
			branches: map[string]bool{},
			tags:     []github.Tag{{Name: "v1.3.0", SHA: edOldSHA}},
		},
		coreTags: []github.Tag{
			{Name: "v1.4.0", SHA: edCoreSHA},
			{Name: "v1.4.0-rc.1", SHA: edCoreSHA},
			{Name: "v1.3.0", SHA: strings.Repeat("3", 40)},
		},
		commits: []github.Commit{{SHA: edHeadSHA}, {SHA: edSyncSHA}, {SHA: edOldSHA}},
		gomods: map[string]string{
			edHeadSHA: edGoMod("v1.4.1-0.20261106120000-aaaaaaaaaaaa"),
			edSyncSHA: edGoMod("v1.4.1-0.20261105120000-0123456789ab"),
			edOldSHA:  edGoMod("v1.3.0"),
		},
	}
	return store, ctx, repo, forge
}

func syncEdition(t *testing.T, ctx context.Context, store *model.Store, forge *fakeEditionForge, at time.Time) {
	t.Helper()
	if err := orchestrator.SyncReleaseQualification(ctx, store, forge, at); err != nil {
		t.Fatalf("SyncReleaseQualification: %v", err)
	}
}

func edTag(repo model.RepoRef, sha string) string {
	return model.QualificationTag(edStem, repo, edRelease+"@"+sha)
}

func editionTasks(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef, sha string) map[string][]string {
	t.Helper()
	runs, err := store.SuiteRunsTagged(ctx, edTag(repo, sha))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, run := range runs {
		for _, task := range run.Tasks {
			name := task.TemplateID[strings.LastIndex(task.TemplateID, ":")+1:]
			out[name] = append(out[name], task.TaskID)
		}
	}
	return out
}

func finishEditionTask(t *testing.T, ctx context.Context, store *model.Store, taskID, verdict, sha string, at time.Time) {
	t.Helper()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: taskID,
		Author: ownRunAuthor(taskID),
		Body: "Report.\n\nQualification-Verdict: " + verdict + "\nQualified-Tag: " + edRelease +
			"\nQualified-Commit: " + sha + "\n",
		CreatedAt: at,
	}); err != nil {
		t.Fatalf("closing note on %s: %v", taskID, err)
	}
	completeTask(t, ctx, store, taskID, at)
}

func TestSyncedReleaseQualificationWaitsForItsStage1Status(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newEditionRepo(t)
	syncEdition(t, ctx, store, forge, baseTime)
	forge.setStage1(edSyncSHA, "pending")
	syncEdition(t, ctx, store, forge, baseTime.Add(time.Minute))
	// A green nightly workflow is not this suite's Stage 1.
	forge.nightly, forge.anySHA = "success", true
	syncEdition(t, ctx, store, forge, baseTime.Add(2*time.Minute))
	if got := editionTasks(t, ctx, store, repo, edSyncSHA); len(got) != 0 {
		t.Fatalf("filed %v before the Stage 1 status was green, want nothing", got)
	}
	if len(forge.checks) != 0 {
		t.Fatalf("posted %+v before Stage 2 started, want nothing", forge.checks)
	}
}

func TestSyncedReleaseQualificationQualifiesTheSyncCommit(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newEditionRepo(t)
	forge.setStage1(edSyncSHA, "success")
	syncEdition(t, ctx, store, forge, baseTime)

	tasks := editionTasks(t, ctx, store, repo, edSyncSHA)
	if len(tasks) != 2 || len(tasks["edition-smoke"]) != 0 {
		t.Fatalf("got %v, want the two gating items and not the last", tasks)
	}
	c := forge.check(t)
	if c.Name != edCheck || c.Status != "in_progress" || forge.heads[c.ID] != edSyncSHA || c.ExternalID != edTag(repo, edSyncSHA) {
		t.Fatalf("check run is %+v on %s, want %s in_progress on the sync commit %s", c, forge.heads[c.ID], edCheck, edSyncSHA)
	}
	first, err := store.GetTask(ctx, tasks["edition-a"][0])
	if err != nil || first == nil {
		t.Fatalf("task: %v", err)
	}
	if want := "Qualify " + edRelease + " (" + edRelease + ") at " + edSyncSHA + "."; first.Body != want ||
		first.Title != "edition-a for "+edRelease || first.Priority != model.PriorityReleaseGate || first.AutoMerge {
		t.Fatalf("task is %+v, want body %q, the release in the title, the release-gate priority and no auto-merge", first, want)
	}

	at := baseTime.Add(time.Minute)
	for _, item := range edItems[:2] {
		finishEditionTask(t, ctx, store, tasks[item][0], "PASS", edSyncSHA, at)
	}
	syncEdition(t, ctx, store, forge, at)
	tasks = editionTasks(t, ctx, store, repo, edSyncSHA)
	if len(tasks["edition-smoke"]) != 1 {
		t.Fatalf("got %v, want the last item started once the others passed", tasks)
	}
	finishEditionTask(t, ctx, store, tasks["edition-smoke"][0], "PASS", edSyncSHA, at.Add(time.Minute))
	syncEdition(t, ctx, store, forge, at.Add(time.Minute))
	if c := forge.check(t); c.Status != "completed" || c.Conclusion == nil || *c.Conclusion != model.ConclusionSuccess {
		t.Fatalf("check run is %+v, want completed success", c)
	}
	for _, asked := range forge.asked {
		if asked != "acme/core" && asked != "acme/edition" {
			t.Errorf("listed %s's tags, want only the upstream's and the edition's own", asked)
		}
	}
}

// A footer naming the wrong commit fails the item as it would an rc
// candidate's: the flake retry, then red. No red-candidate notification
// is raised -- that machinery is about rc tags -- but the check run is.
func TestSyncedReleaseQualificationFailsOnTheCheckRunAlone(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newEditionRepo(t)
	forge.setStage1(edSyncSHA, "success")
	syncEdition(t, ctx, store, forge, baseTime)
	tasks := editionTasks(t, ctx, store, repo, edSyncSHA)
	at := baseTime.Add(time.Minute)
	finishEditionTask(t, ctx, store, tasks["edition-a"][0], "FAIL", edSyncSHA, at)
	syncEdition(t, ctx, store, forge, at)
	tasks = editionTasks(t, ctx, store, repo, edSyncSHA)
	if len(tasks["edition-a"]) != 2 {
		t.Fatalf("got %v, want edition-a re-run once", tasks)
	}
	finishEditionTask(t, ctx, store, tasks["edition-a"][1], "FAIL", edSyncSHA, at.Add(time.Minute))
	syncEdition(t, ctx, store, forge, at.Add(time.Minute))
	c := forge.check(t)
	if c.Status != "completed" || c.Conclusion == nil || *c.Conclusion != model.ConclusionFailure {
		t.Fatalf("check run is %+v, want completed failure", c)
	}
	open, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range open {
		if strings.HasPrefix(n.Subject, model.FamilyRCFailed+":") {
			t.Errorf("raised %s for a synced release, want no rc-failed notification", n.Subject)
		}
	}
}

func TestSyncedReleaseCandidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(f *fakeEditionForge)
		want  string // the candidate's sha, "" for none
	}{
		{name: "the sync behind a trunk pin", want: edSyncSHA},
		{name: "a release pinned by its own version", want: edSyncSHA, setup: func(f *fakeEditionForge) {
			f.gomods[edSyncSHA] = edGoMod(edRelease)
		}},
		{name: "a later go.mod change pinning the same release", want: edHeadSHA, setup: func(f *fakeEditionForge) {
			f.gomods[edHeadSHA] = edGoMod(edRelease)
		}},
		{name: "the release already tagged", setup: func(f *fakeEditionForge) {
			f.tags = append(f.tags, github.Tag{Name: edRelease, SHA: edSyncSHA})
		}},
		{name: "an older untagged release behind a tagged one", setup: func(f *fakeEditionForge) {
			f.tags = []github.Tag{{Name: edRelease, SHA: edSyncSHA}}
		}},
		{name: "no release pinned at all", setup: func(f *fakeEditionForge) {
			f.commits = f.commits[:1]
		}},
		{name: "a pseudo-version of a commit core cut only an rc from", setup: func(f *fakeEditionForge) {
			f.coreTags = f.coreTags[1:]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx, repo, forge := newEditionRepo(t)
			if tc.setup != nil {
				tc.setup(forge)
			}
			for _, sha := range []string{edHeadSHA, edSyncSHA, edOldSHA} {
				forge.setStage1(sha, "success")
			}
			syncEdition(t, ctx, store, forge, baseTime)
			if tc.want == "" {
				if len(forge.checks) != 0 {
					t.Fatalf("posted %+v, want no candidate", forge.checks)
				}
				return
			}
			if c := forge.check(t); forge.heads[c.ID] != tc.want {
				t.Fatalf("qualifying %s, want %s", forge.heads[c.ID], tc.want)
			}
			if got := editionTasks(t, ctx, store, repo, tc.want); len(got) != 2 {
				t.Fatalf("filed %v for %s, want the two gating items", got, tc.want)
			}
		})
	}
}

// A suite is a release gate by its stanza or by the release-qualification
// name, never by anything else; and the rc-tags machinery takes its check
// run's name from the stanza when it has one.
func TestOnlyANamedOrStanzaSuiteIsAReleaseGate(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	// The same suite under another name, with no stanza: not a gate.
	suite, err := store.GetSuite(ctx, model.RepoDeclaredID(model.DeclaredSuite, repo, model.ReleaseQualificationSuiteStem))
	if err != nil || suite == nil {
		t.Fatalf("suite: %v", err)
	}
	other := *suite
	other.ID = model.RepoDeclaredID(model.DeclaredSuite, model.RepoRef{Owner: "acme", Name: "gadgets"}, "sweep")
	if err := store.PutSuite(ctx, other); err != nil {
		t.Fatal(err)
	}
	// And the release-qualification suite itself, declared with a stanza
	// naming its own check run and Stage 1 workflow.
	if _, err := store.SyncRepoDeclarations(ctx, repo, strings.Repeat("e", 40), model.RepoDeclarations{
		Suites: []model.DeclaredSuiteFile{{
			Path: model.SuiteDeclarationDir + "/release-qualification.yml", Suite: *suite,
			Qualification: &model.SuiteQualification{Check: "grain/custom-qualification", Stage1Workflow: "nightly"},
		}},
	}, baseTime, time.UTC); err != nil {
		t.Fatalf("declaring the stanza: %v", err)
	}
	syncQualification(t, ctx, store, forge, baseTime)
	c := forge.check(t)
	if c.Name != "grain/custom-qualification" || c.ExternalID != model.ReleaseQualificationTag(repo, rqTag) {
		t.Fatalf("check run is %+v, want grain/custom-qualification for %s", c, rqTag)
	}
	runs, err := store.SuiteRunsTagged(ctx, model.QualificationTag("sweep", model.RepoRef{Owner: "acme", Name: "gadgets"}, rqTag))
	if err != nil || len(runs) != 0 {
		t.Fatalf("started %d runs of a suite with neither the name nor a stanza (%v), want none", len(runs), err)
	}
}
