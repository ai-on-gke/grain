package ci

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestServiceCICapabilitiesAreTrustedAndInvalidateCache(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{".grain/workflows/test.yml": "on: push\njobs:\n  test:\n    steps:\n      - run: true\n"})
	service.Engine.Cache = openCache(t, DirCacheOptions{})
	req := Request{Repo: "acme/widgets", Ref: "main", Event: EventPush, Capabilities: []string{"caller-injected"}}
	run := func() Run {
		t.Helper()
		started, err := service.Start(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		result := waitForRun(t, service, started.ID)
		if result.Conclusion != ConclusionSuccess {
			t.Fatalf("run: %+v", result)
		}
		return result
	}
	first := run()
	if len(backend.acquired[0].Capabilities) != 0 {
		t.Fatal("caller granted itself capabilities")
	}
	configured := []string{"openai-proxy", "gcp-key", "openai-proxy"}
	service.Capabilities = func(context.Context, string) ([]string, error) { return configured, nil }
	second := run()
	if second.Key == first.Key || len(backend.acquired) != 2 {
		t.Fatal("capability change reused previous run/cache")
	}
	if !reflect.DeepEqual(backend.acquired[1].Capabilities, []string{"gcp-key", "openai-proxy"}) {
		t.Fatalf("grants: %v", backend.acquired[1].Capabilities)
	}
	configured = []string{"gcp-key", "openai-proxy"}
	third := run()
	if third.Key != second.Key || len(backend.acquired) != 2 {
		t.Fatal("same set with different order missed cache")
	}
	service.Capabilities = func(context.Context, string) ([]string, error) { return nil, errors.New("settings unavailable") }
	if _, err := service.Start(context.Background(), req); err == nil {
		t.Fatal("settings failure silently removed grants")
	}
}

func TestServiceReleaseSecretsComeOnlyFromTrustedSettingsAndEligibleEvents(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on:\n  push:\n  workflow_dispatch:\n  release:\njobs:\n  test:\n    cache: false\n    secrets: [NPM_TOKEN, SIGNING_KEY]\n    steps:\n      - run: true\n",
	})
	service.ReleaseSecrets = func(context.Context, string) ([]string, error) {
		return []string{"NPM_TOKEN", "SIGNING_KEY"}, nil
	}

	manual, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "main", Event: EventManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, manual.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("manual run = %+v", got)
	}
	if !reflect.DeepEqual(backend.acquired[0].ReleaseSecrets, []string{"NPM_TOKEN", "SIGNING_KEY"}) {
		t.Fatalf("manual release secrets = %v", backend.acquired[0].ReleaseSecrets)
	}

	push, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "main", Event: EventPush,
		ReleaseSecrets: []string{"CALLER_INJECTED"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The job declares the secrets it needs, so a push -- which carries
	// none, whatever the caller put on the request -- does not run it.
	got := waitForRun(t, service, push.ID)
	if len(got.Jobs) != 1 || got.Jobs[0].Conclusion != ConclusionErrored ||
		!strings.Contains(got.Jobs[0].Error, "a push run places no release secrets") {
		t.Fatalf("push run = %+v", got.Jobs)
	}
	if len(backend.acquired) != 1 {
		t.Fatalf("push acquired a sandbox: %+v", backend.acquired[1:])
	}
}

// A run whose caller named no event still answers like a manual
// dispatch, but it is not handed the release secrets an explicit manual
// dispatch is: leaving Event empty is the least-privileged case, not the
// secrets-eligible one (#2391).
func TestServiceReleaseSecretsNeedAnExplicitEvent(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on:\n  workflow_dispatch:\njobs:\n  test:\n    cache: false\n    secrets: NPM_TOKEN\n    steps:\n      - run: true\n",
	})
	asked := false
	service.ReleaseSecrets = func(context.Context, string) ([]string, error) {
		asked = true
		return []string{"NPM_TOKEN"}, nil
	}

	unnamed, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	// The job declares the secret it reads, so a run that names no event
	// -- which carries none -- does not run it, and says why.
	got := waitForRun(t, service, unnamed.ID)
	if got.Event != EventManual || len(got.Jobs) != 1 || got.Jobs[0].Conclusion != ConclusionErrored ||
		!strings.Contains(got.Jobs[0].Error, "a run that names no event places no release secrets") {
		t.Fatalf("run with no event = %+v", got)
	}
	if asked {
		t.Fatal("a run with no event read the repository's release secrets")
	}
	if len(backend.acquired) != 0 {
		t.Fatalf("a run with no event acquired a sandbox: %+v", backend.acquired)
	}

	manual, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventManual})
	if err != nil {
		t.Fatal(err)
	}
	if manual.ID == unnamed.ID {
		t.Fatal("an explicit manual dispatch was handed the run that named no event")
	}
	if got := waitForRun(t, service, manual.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("manual run = %+v", got)
	}
	if !reflect.DeepEqual(backend.acquired[0].ReleaseSecrets, []string{"NPM_TOKEN"}) {
		t.Fatalf("manual release secrets = %v", backend.acquired[0].ReleaseSecrets)
	}
}

// Repository access is the job's own to declare (grain/task-1654): a
// `repo-read:` or `repo-write:` on the job is kept with no entry in the
// deployment's per-repository list, and an entry there reaches no job
// that did not declare it. Whether the declared access is actually
// granted for an event, and what the team's credential lets it do, is the
// sandbox's and the proxy's to answer.
func TestServiceRepoAccessComesFromTheJobsDeclarationNotTheDeploymentsList(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on:\n  push:\n  pull_request:\n  workflow_dispatch:\n" +
			"jobs:\n" +
			"  declared:\n    cache: false\n    capabilities: [\"repo-read:acme/shared-lib\", \"repo-write:acme/widgets\", gcp-key]\n    steps:\n      - run: true\n" +
			"  undeclared:\n    cache: false\n    steps:\n      - run: true\n",
	})
	service.Capabilities = func(context.Context, string) ([]string, error) {
		// What an operator granted before the declaration was enough:
		// the repository access in it is a no-op now.
		return []string{"repo-write:acme/tools", "repo-read:acme/other", "gcp-key"}, nil
	}

	for _, event := range []Event{EventPush, EventPullRequest, EventManual} {
		run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: event})
		if err != nil {
			t.Fatal(err)
		}
		if got := waitForRun(t, service, run.ID); got.Conclusion != ConclusionSuccess {
			t.Fatalf("%s run = %+v", event, got)
		}
	}

	if len(backend.acquired) != 6 {
		t.Fatalf("acquired sandboxes = %d, want 6", len(backend.acquired))
	}
	declared := []string{"repo-read:acme/shared-lib", "repo-write:acme/widgets", "gcp-key"}
	undeclared := []string{"gcp-key"}
	var holding, without int
	for i, acq := range backend.acquired {
		switch {
		case reflect.DeepEqual(acq.Capabilities, declared):
			holding++
		case reflect.DeepEqual(acq.Capabilities, undeclared):
			without++
		default:
			t.Errorf("acquired[%d].Capabilities = %v, want %v for the declaring job or %v for the other",
				i, acq.Capabilities, declared, undeclared)
		}
	}
	if holding != 3 || without != 3 {
		t.Errorf("%d declaring and %d undeclaring jobs, want 3 of each", holding, without)
	}
}

// A job that declares only repository access runs on a repository whose
// deployment list names nothing at all -- the case of the nightly
// backport-triage workflow, which errored every night for want of an
// operator's grant.
func TestAJobDeclaringRepoWriteRunsWithNoDeploymentGrant(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/nightly.yml": "on:\n  workflow_dispatch:\njobs:\n  triage:\n    cache: false\n" +
			"    capabilities: [\"repo-write:acme/widgets\"]\n    steps:\n      - run: true\n",
	})
	service.Capabilities = func(context.Context, string) ([]string, error) { return []string{"gemini-key"}, nil }

	run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventManual})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, run.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %+v", got)
	}
	if len(backend.acquired) != 1 || !reflect.DeepEqual(backend.acquired[0].Capabilities, []string{"repo-write:acme/widgets"}) {
		t.Fatalf("acquired = %+v, want one job holding only repo-write:acme/widgets", backend.acquired)
	}
}

// release-gate-read is the release workflow's own to declare
// (docs/design/release-from-trunk.md §6): a job naming it runs with no
// entry in the deployment's per-repository list, and the provider's event
// gate and repository list -- the sandbox's to apply -- are what bound
// it. An entry in that list reaches no job that did not declare it.
func TestAJobDeclaringReleaseGateReadRunsWithNoDeploymentGrant(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/release-train.yml": "on:\n  workflow_dispatch:\njobs:\n" +
			"  cut-or-promote:\n    cache: false\n" +
			"    capabilities: [\"repo-write:acme/widgets\", release-gate-read, \"api:ci:read\"]\n    steps:\n      - run: true\n" +
			"  other:\n    cache: false\n    steps:\n      - run: true\n",
	})
	service.Capabilities = func(context.Context, string) ([]string, error) {
		return []string{model.ReleaseGateReadCapability, "gemini-key"}, nil
	}

	run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventManual})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, run.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %+v", got)
	}
	if len(backend.acquired) != 2 {
		t.Fatalf("acquired = %+v, want two jobs", backend.acquired)
	}
	want := []string{"repo-write:acme/widgets", model.ReleaseGateReadCapability, "api:ci:read"}
	var declaring, other int
	for i, acq := range backend.acquired {
		switch {
		case reflect.DeepEqual(acq.Capabilities, want):
			declaring++
		case reflect.DeepEqual(acq.Capabilities, []string{"gemini-key"}):
			other++
		default:
			t.Errorf("acquired[%d].Capabilities = %v, want %v for the declaring job or only gemini-key for the other",
				i, acq.Capabilities, want)
		}
	}
	if declaring != 1 || other != 1 {
		t.Errorf("%d declaring and %d other jobs, want one of each", declaring, other)
	}
}

// The declared lifecycle image reaches a job as GRAIN_LIFECYCLE_IMAGE,
// and only in a repository that declares one
// (docs/task-lifecycle-sandbox.md, "The build is a release job").
//
// It is the one new thing grain supplies to the build. Everything else
// about publishing a lifecycle image is an ordinary release job: the
// sandbox, the log, the live view, and package-publish's credential --
// which is resolved for the job's own repository, never one a workflow
// file names, and is the reason this variable is filled by Service from
// the declaration rather than read out of the workflow.
func TestLifecycleImageReachesAJobOnlyWhenOneIsDeclared(t *testing.T) {
	declared := runEnv(Request{Repo: "acme/billing", LifecycleImage: "ghcr.io/acme/billing/grain-lifecycle"},
		".grain/workflows/lifecycle.yml", "image")
	if got := declared[model.LifecycleImageEnv]; got != "ghcr.io/acme/billing/grain-lifecycle" {
		t.Errorf("%s = %q", model.LifecycleImageEnv, got)
	}
	// A repository that declares none finds nothing rather than an empty
	// string it might push to.
	none := runEnv(Request{Repo: "acme/widgets"}, ".grain/workflows/tests.yml", "unit")
	if _, present := none[model.LifecycleImageEnv]; present {
		t.Errorf("%s is set in a repository with no lifecycle: %v", model.LifecycleImageEnv, none)
	}
	// And because runEnv is what Fingerprint hashes, a repository that
	// starts declaring one re-runs rather than reusing a cached verdict
	// reached without it.
	if declared[model.LifecycleImageEnv] == none[model.LifecycleImageEnv] {
		t.Error("the two runs are indistinguishable to the job key")
	}
}

// A job that declares no `secrets:` is handed no release secrets, even
// on an event that carries them: release secrets are not grandfathered
// into every job the way capabilities are (issue #2389).
func TestAJobDeclaringNoSecretsGetsNoReleaseSecrets(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on:\n  workflow_dispatch:\njobs:\n  test:\n    cache: false\n    steps:\n      - run: true\n",
	})
	service.ReleaseSecrets = func(context.Context, string) ([]string, error) {
		return []string{"NPM_TOKEN", "SIGNING_KEY"}, nil
	}
	run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventManual})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, run.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %+v", got)
	}
	if got := backend.acquired[0].ReleaseSecrets; len(got) != 0 {
		t.Fatalf("a job declaring no secrets was given %v", got)
	}
}

// A run asked for with WithholdReleaseSecrets -- the guest-cache warmer's
// -- carries none, even to a job that declares one, and even though its
// event is workflow_dispatch.
func TestAWithheldRunCarriesNoReleaseSecrets(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on:\n  workflow_dispatch:\njobs:\n  warm:\n    cache: false\n    steps:\n      - run: true\n",
	})
	asked := false
	service.ReleaseSecrets = func(context.Context, string) ([]string, error) {
		asked = true
		return []string{"NPM_TOKEN"}, nil
	}
	run, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "task-branch", Event: EventManual, WithholdReleaseSecrets: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, run.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %+v", got)
	}
	if asked {
		t.Error("a withheld run still read the repository's release secrets")
	}
	if got := backend.acquired[0].ReleaseSecrets; len(got) != 0 {
		t.Fatalf("a withheld run was given %v", got)
	}
}

// A release job is what prunes the registry, and it is told which
// images this deployment runs so that it keeps them (cmd/ghcr-prune;
// grain:sha-7781f61 was pruned while deployed and every config check
// went red). Only a release is told: the variable is part of each job's
// key, and handing it to every push would throw away the whole cache on
// each upgrade. Nor can a caller name images of its own.
func TestOnlyAReleaseIsToldWhichImagesAreDeployed(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/test.yml": "on:\n  push:\n  release:\njobs:\n  test:\n    cache: false\n    steps:\n      - run: true\n",
	})
	service.DeployedImages = func() []string {
		return []string{"ghcr.io/acme/widgets/grain:sha-7781f61", "ghcr.io/acme/widgets/grain:pinned"}
	}
	envOf := func(event Event) map[string]string {
		t.Helper()
		before := len(backend.commands)
		started, err := service.Start(context.Background(), Request{
			Repo: "acme/widgets", Ref: "main", Event: event, TrustedRelease: event == EventRelease,
			DeployedImages: []string{"caller/injected:tag"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := waitForRun(t, service, started.ID); got.Conclusion != ConclusionSuccess {
			t.Fatalf("%s run = %+v", event, got)
		}
		backend.mu.Lock()
		defer backend.mu.Unlock()
		if len(backend.commands) <= before {
			t.Fatalf("%s run ran no command", event)
		}
		return backend.commands[len(backend.commands)-1].Env
	}
	if got := envOf(EventRelease)[DeployedImagesEnv]; got != "ghcr.io/acme/widgets/grain:sha-7781f61,ghcr.io/acme/widgets/grain:pinned" {
		t.Errorf("release %s = %q", DeployedImagesEnv, got)
	}
	if got, present := envOf(EventPush)[DeployedImagesEnv]; present {
		t.Errorf("push %s = %q, want it unset", DeployedImagesEnv, got)
	}
}

// A trusted release of a branch the repository has not reviewed into --
// a task's branch above all -- is refused before anything is read or
// placed (grain/task-949). Its workflows are read at that branch, so
// running it would hand the repository's release secrets and publishing
// capabilities to whoever wrote the branch. The default branch is still
// released with them, and so is a ref a person vouched for by hand.
func TestATrustedReleaseOfABranchNotReviewedIntoIsRefused(t *testing.T) {
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{
		".grain/workflows/release.yml": "on:\n  release:\njobs:\n  publish:\n    cache: false\n    secrets: [NPM_TOKEN]\n    steps:\n      - run: true\n",
	})
	service.Capabilities = func(context.Context, string) ([]string, error) { return []string{"gcp-key"}, nil }
	secretsRead := 0
	service.ReleaseSecrets = func(context.Context, string) ([]string, error) {
		secretsRead++
		return []string{"NPM_TOKEN"}, nil
	}
	var asked []string
	service.ReleaseRefRefusal = func(_ context.Context, repo, ref string) (string, error) {
		asked = append(asked, ref)
		if ref == "main" {
			return "", nil
		}
		return ref + " is a task's branch, whose workflows its author controls", nil
	}

	taskBranch := "grain/049f1e09/task-123"
	_, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: taskBranch, Event: EventRelease, TrustedRelease: true,
	})
	if err == nil || !strings.Contains(err.Error(), "is a task's branch") ||
		!strings.Contains(err.Error(), "refusing a trusted release") {
		t.Fatalf("a trusted release of %s = %v, want it refused and told why", taskBranch, err)
	}
	if len(backend.acquired) != 0 || secretsRead != 0 {
		t.Fatalf("the refused release acquired %d sandboxes and read the release secrets %d times",
			len(backend.acquired), secretsRead)
	}
	if !reflect.DeepEqual(asked, []string{taskBranch}) {
		t.Fatalf("asked about %v, want %s", asked, taskBranch)
	}

	// An error asking is a refusal too, not a pass.
	refusal := service.ReleaseRefRefusal
	service.ReleaseRefRefusal = func(context.Context, string, string) (string, error) {
		return "", errors.New("settings unavailable")
	}
	if _, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "release-1", Event: EventRelease, TrustedRelease: true,
	}); err == nil {
		t.Fatal("a release whose ref could not be checked was started anyway")
	}
	service.ReleaseRefRefusal = refusal

	// The default branch, named, is still a trusted release.
	before := len(backend.acquired)
	main, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "main", Event: EventRelease, TrustedRelease: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, main.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("main's release = %+v", got)
	}
	secrets := false
	for _, sb := range backend.acquired[before:] {
		if reflect.DeepEqual(sb.ReleaseSecrets, []string{"NPM_TOKEN"}) {
			secrets = true
		}
	}
	if !secrets {
		t.Fatalf("main's release was not handed its release secret: %+v", backend.acquired[before:])
	}

	// And a person vouching for the ref is not asked again.
	asked = nil
	vouched, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "hotfix", Event: EventRelease, TrustedRelease: true, ReleaseRefVouched: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForRun(t, service, vouched.ID); got.Conclusion != ConclusionSuccess {
		t.Fatalf("a vouched release = %+v", got)
	}
	if len(asked) != 0 {
		t.Fatalf("a release a person vouched for was refused-checked anyway: %v", asked)
	}
}
