package ci

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The acceptance criterion this whole split was asked for: a job whose
// requirement this deployment does not meet does not run, and the
// record says which name is missing and who does not provide it.
//
// What happened instead, before this, is on the record: `gemini-key`
// was dropped from a repository's row by a sync at 18:52 UTC on 20 Sep
// 2026 and every job needing it ran anyway, without it, failing several
// minutes later inside a step that named something else entirely.
func TestAJobDoesNotRunWithoutTheCapabilityItRequires(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on: push\njobs:\n" +
			"  needs-a-key:\n    capabilities: gemini-key\n    steps:\n      - run: true\n" +
			"  ordinary:\n    steps:\n      - run: true\n",
	})
	service.Capabilities = func(context.Context, string) ([]string, error) {
		return []string{"package-publish"}, nil
	}
	run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatal(err)
	}
	got := waitForRun(t, service, run.ID)

	var unmet, ordinary *JobRun
	for i := range got.Jobs {
		switch got.Jobs[i].JobID {
		case "needs-a-key":
			unmet = &got.Jobs[i]
		case "ordinary":
			ordinary = &got.Jobs[i]
		}
	}
	if unmet == nil || ordinary == nil {
		t.Fatalf("run reported jobs %+v", got.Jobs)
	}
	if unmet.Conclusion != ConclusionErrored {
		t.Errorf("a job with an unmet requirement concluded %q, want %q", unmet.Conclusion, ConclusionErrored)
	}
	want := `this job requires the capability "gemini-key" and this deployment does not provide it for acme/widgets`
	if unmet.Error != want {
		t.Errorf("the job's record says %q, want %q", unmet.Error, want)
	}
	if unmet.Sandbox != "" || len(backend.acquired) != 1 {
		t.Errorf("a job that cannot run spent a sandbox anyway (%q, %d acquired)", unmet.Sandbox, len(backend.acquired))
	}
	// The other half of declaring per job rather than per repository:
	// the rest of the suite still runs.
	if ordinary.Conclusion != ConclusionSuccess {
		t.Errorf("the job that needed nothing concluded %q -- a missing credential took the suite with it",
			ordinary.Conclusion)
	}
}

// A requirement that is met changes nothing about how the job runs --
// except that its sandbox is handed what it asked for and not the
// repository's whole grant.
func TestAMetRequirementNarrowsWhatTheJobIsGiven(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on: push\njobs:\n" +
			"  publisher:\n    cache: false\n    capabilities:\n      - package-publish\n    steps:\n      - run: true\n",
	})
	service.Capabilities = func(context.Context, string) ([]string, error) {
		return []string{"gemini-key", "package-publish"}, nil
	}
	run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, run.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %+v", got)
	}
	if len(backend.acquired) != 1 {
		t.Fatalf("acquired %d sandboxes", len(backend.acquired))
	}
	if got := backend.acquired[0].Capabilities; !reflect.DeepEqual(got, []string{"package-publish"}) {
		t.Errorf("the job was given %v, want only what it declared", got)
	}
}

// A job that declares nothing is given everything the deployment
// provides, exactly as every job was before the key existed. Quietly
// taking a credential away from a workflow that was working would be
// the same class of silent loss this change exists to stop.
func TestAJobThatDeclaresNothingKeepsWhatItAlwaysGot(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on: push\njobs:\n  test:\n    steps:\n      - run: true\n",
	})
	service.Capabilities = func(context.Context, string) ([]string, error) {
		return []string{"gemini-key", "package-publish"}, nil
	}
	run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, run.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %+v", got)
	}
	if got := backend.acquired[0].Capabilities; !reflect.DeepEqual(got, []string{"gemini-key", "package-publish"}) {
		t.Errorf("a job declaring nothing was given %v, want the whole provided set", got)
	}
}

// Except a scarce capability -- one admission mints before the sandbox
// exists, such as a GCP service-account key. Handing that to every job
// that declares nothing minted a key per job of every run, and ran the
// deployment into IAM's write quota (grain/task-281). A job that
// declares it, required or optional, still gets it.
func TestAScarceCapabilityGoesOnlyToTheJobsThatDeclareIt(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on: push\njobs:\n" +
			"  plain:\n    cache: false\n    steps:\n      - run: true\n" +
			"  smoke:\n    cache: false\n    capabilities: [gcp-key]\n    steps:\n      - run: true\n" +
			"  maybe:\n    cache: false\n    optional-capabilities: [gcp-key]\n    steps:\n      - run: true\n",
	})
	service.Engine.ExplicitOnlyCapability = func(id string) bool { return id == "gcp-key" }
	service.Capabilities = func(context.Context, string) ([]string, error) {
		return []string{"gemini-key", "gcp-key", "package-publish"}, nil
	}
	run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, run.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %+v", got)
	}
	// Sandbox names do not carry the job id, so this compares what the
	// three jobs were given as a set: one each.
	var given []string
	backend.mu.Lock()
	for _, req := range backend.acquired {
		given = append(given, strings.Join(req.Capabilities, ","))
	}
	backend.mu.Unlock()
	slices.Sort(given)
	want := []string{"gcp-key", "gcp-key", "gemini-key,package-publish"}
	if !reflect.DeepEqual(given, want) {
		t.Errorf("the jobs were given %q, want %q: gcp-key to the two that declare it, "+
			"and the rest of the set to the one that declares nothing", given, want)
	}
}

// Release secret names get the same treatment, and the message
// distinguishes the two ways one can go unmet: a deployment that does
// not hold the name, and an event that carries no release secrets at
// all however the repository is configured.
func TestASecretRequirementNamesWhichOfTheTwoWaysItIsUnmet(t *testing.T) {
	const workflow = "on:\n  push:\n  workflow_dispatch:\njobs:\n" +
		"  publish:\n    cache: false\n    secrets: NPM_TOKEN\n    steps:\n      - run: true\n"
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{".grain/workflows/test.yml": workflow})
	service.ReleaseSecrets = func(context.Context, string) ([]string, error) { return nil, nil }

	manual, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventManual})
	if err != nil {
		t.Fatal(err)
	}
	got := waitForRun(t, service, manual.ID)
	if len(got.Jobs) != 1 || got.Jobs[0].Conclusion != ConclusionErrored {
		t.Fatalf("manual run = %+v", got.Jobs)
	}
	want := `this job requires the release secret "NPM_TOKEN" and this deployment does not provide it for acme/widgets`
	if got.Jobs[0].Error != want {
		t.Errorf("manual run says %q, want %q", got.Jobs[0].Error, want)
	}

	service.ReleaseSecrets = func(context.Context, string) ([]string, error) { return []string{"NPM_TOKEN"}, nil }
	push, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatal(err)
	}
	got = waitForRun(t, service, push.ID)
	if len(got.Jobs) != 1 || got.Jobs[0].Conclusion != ConclusionErrored {
		t.Fatalf("push run = %+v", got.Jobs)
	}
	if !strings.Contains(got.Jobs[0].Error, "a push run places no release secrets") {
		t.Errorf("push run says %q -- it should name the event rather than blame the deployment", got.Jobs[0].Error)
	}

	manual2, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventManual})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, manual2.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("a met secret requirement still did not run: %+v", got.Jobs)
	}
}

// A run that could not meet a requirement writes nothing to the cache
// and reads nothing from it, in both directions: the answer "this
// cannot run here" is about this deployment's configuration and not
// about the commit, so it must not stand in for the commit later, and
// the grant arriving must not replay it.
func TestAnUnmetRequirementIsNeitherServedFromNorSavedToTheCache(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on: push\njobs:\n  needs-a-key:\n    capabilities: gemini-key\n    steps:\n      - run: true\n",
	})
	service.Engine.Cache = openCache(t, DirCacheOptions{})
	provided := []string{"gemini-key"}
	service.Capabilities = func(context.Context, string) ([]string, error) { return provided, nil }

	start := func() Run {
		t.Helper()
		run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventPush})
		if err != nil {
			t.Fatal(err)
		}
		return waitForRun(t, service, run.ID)
	}
	if got := start(); got.Conclusion != ConclusionSuccess {
		t.Fatalf("the first run, with the grant in place, = %+v", got)
	}
	// Take the grant away. The cached success must not answer for a run
	// that cannot place the credential.
	provided = nil
	if got := start(); got.Jobs[0].Conclusion != ConclusionErrored {
		t.Fatalf("a cached green answered for a job whose requirement is no longer met: %+v", got.Jobs[0])
	}
	// Give it back. The errored run must not have been saved over the
	// entry, and the job runs rather than replaying its own refusal.
	provided = []string{"gemini-key"}
	if got := start(); got.Conclusion != ConclusionSuccess {
		t.Fatalf("the job did not run once its requirement was met again: %+v", got.Jobs)
	}
}

func TestRequirementNamesAreCheckedForShapeOnly(t *testing.T) {
	// An unknown capability parses: the file cannot know what this
	// deployment has, and failing the parse would take every other job
	// in the workflow down with it.
	w, err := Parse(".grain/workflows/test.yml", []byte("on: push\njobs:\n  a:\n    capabilities: [never-heard-of-it]\n    steps:\n      - run: true\n"))
	if err != nil {
		t.Fatalf("an unknown capability failed the parse: %v", err)
	}
	if got := w.Jobs[0].RequiredCapabilities(); !reflect.DeepEqual(got, []string{"never-heard-of-it"}) {
		t.Errorf("requirements parsed as %v", got)
	}
	if _, err := Parse(".grain/workflows/test.yml", []byte("on: push\njobs:\n  a:\n    capabilities: ['']\n    steps:\n      - run: true\n")); err == nil {
		t.Errorf("an empty capability name was accepted")
	}
	if _, err := Parse(".grain/workflows/test.yml", []byte("on: push\njobs:\n  a:\n    secrets: [not-a-env-name]\n    steps:\n      - run: true\n")); err == nil {
		t.Errorf("a secret name that cannot be an environment variable was accepted")
	}
}

// A capability reference a job declares is its declaration's to ask for
// and the deployment's capability policy's to decide when the job's
// grants are minted (docs/capabilities-spec.md §4.2.2), so the
// repository's CI capabilities list neither has to name it nor hands
// one it names to a job that did not ask. The request names the job's
// run and its place in it, which the pass's attempt_ids are made of.
func TestAJobsCapabilityReferenceReachesItsGrantAdmission(t *testing.T) {
	const ref = "https://example.com/capabilities/gcp#gcp-key"
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on: push\njobs:\n" +
			"  first:\n    cache: false\n    steps:\n      - run: true\n" +
			"  keyed:\n    cache: false\n    capabilities:\n      - " + ref + "\n    steps:\n      - run: true\n",
	})
	service.Capabilities = func(context.Context, string) ([]string, error) {
		return []string{"gemini-key", "https://example.com/capabilities/other"}, nil
	}
	run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, run.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %+v", got)
	}
	if len(backend.acquired) != 2 {
		t.Fatalf("acquired %d sandboxes", len(backend.acquired))
	}
	for _, req := range backend.acquired {
		if req.RunID != run.ID {
			t.Errorf("request names run %q, want %q", req.RunID, run.ID)
		}
		switch {
		case slices.Contains(req.Capabilities, ref):
			if !reflect.DeepEqual(req.Capabilities, []string{ref}) || req.JobAttempt != 1 {
				t.Errorf("keyed job's request = %+v, want only its reference, as job 1", req)
			}
		default:
			if !reflect.DeepEqual(req.Capabilities, []string{"gemini-key"}) || req.JobAttempt != 0 {
				t.Errorf("first job's request = %+v, want the provided set without the reference, as job 0", req)
			}
		}
	}
}
