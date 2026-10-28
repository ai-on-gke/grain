package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/capability/registrypush"
	"github.com/ai-on-gke/grain/pkg/capability/reporead"
	"github.com/ai-on-gke/grain/pkg/capability/repowrite"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

type ciCredentialJSON string

func (c ciCredentialJSON) Resolve(context.Context, string) (string, error) { return string(c), nil }

type ciTestCapability struct {
	cancel context.CancelFunc
	model.BaseCapability
	id              string
	refuse          bool
	fail            bool
	revokeFail      bool
	limited         int
	attempts        int
	minted, revoked int
	last            model.CapabilityContext
}

func (p *ciTestCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: p.id, Provision: model.ProvisionMint}
}
func (*ciTestCapability) ScarceResourceProvider() {}
func (p *ciTestCapability) Resolve(context.Context, model.CapabilityContext) (model.Resolution, error) {
	if p.refuse {
		return model.RefusedBecause("credential missing"), nil
	}
	return model.Honoured(), nil
}
func (p *ciTestCapability) Materialize(_ context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	p.attempts++
	if p.limited > 0 {
		p.limited--
		return model.Materialization{}, &model.ResourceExhaustedError{
			Resource: "test CI keys", RetryAfter: time.Millisecond, Err: errors.New("none free"),
		}
	}
	if p.cancel != nil {
		p.cancel()
		return model.Materialization{}, context.Canceled
	}
	if p.fail {
		return model.Materialization{}, errors.New("mint failed")
	}
	p.minted++
	p.last = cc
	return model.Materialization{
		Lease:      &model.Lease{Capability: p.id, Resource: cc.Run.ID},
		Placements: []model.Placement{{Side: model.SideSandbox, Path: "/home/debian/.ci-key", Content: "job-only-key"}},
	}, nil
}

func TestCICapabilityWaitsForAResourceInsteadOfFailingTheJob(t *testing.T) {
	t.Parallel()
	fake := &fakeSandbox{name: "ci-waits"}
	adapter, _, _ := newCIAdapter(fake)
	sb := &ciPlacingSandbox{fakeSandbox: fake, root: t.TempDir()}
	adapter.Sandboxes = ciCapabilityBackend{sb}
	cap := &ciTestCapability{id: "test-key", limited: 1}
	adapter.Capabilities = func() *model.CapabilityRegistry { return model.NewCapabilityRegistry(cap) }
	grants := &daemon.CiGrantReservations{Capabilities: adapter.Capabilities}
	adapter.Grants = grants
	req := ci.SandboxRequest{
		Name: fake.name, Repo: "acme/widgets", Ref: "main", Capabilities: []string{"test-key"},
	}
	release, err := grants.Acquire(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("reserving CI grants: %v", err)
	}
	defer release()

	job, err := adapter.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("CI job failed instead of waiting for its grant: %v", err)
	}
	if cap.attempts != 2 || cap.minted != 1 {
		t.Fatalf("capability attempts = %d, mints = %d; want one wait then one mint", cap.attempts, cap.minted)
	}
	if err := job.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func (p *ciTestCapability) Revoke(ctx context.Context, _ model.CapabilityContext, _ model.Lease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.revoked++
	if p.revokeFail {
		return errors.New("revoke failed")
	}
	return nil
}

type ciPlacingSandbox struct {
	*fakeSandbox
	root string
	fail bool
}

func (s *ciPlacingSandbox) PlaceFile(_ context.Context, path, content, mode string) error {
	if s.fail {
		return errors.New("placement failed")
	}
	path = filepath.Join(s.root, strings.TrimPrefix(path, "/"))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0600)
}

type ciCapabilityBackend struct{ sandbox orchestrator.Sandbox }

func (b ciCapabilityBackend) Acquire(context.Context, string, orchestrator.Shape) (orchestrator.Sandbox, error) {
	return b.sandbox, nil
}

func TestCICapabilitiesMaterializePerJobAndRevoke(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"ci-first", "ci-second"} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeSandbox{name: name}
			adapter, _, scopes := newCIAdapter(fake)
			sb := &ciPlacingSandbox{fakeSandbox: fake, root: t.TempDir()}
			adapter.Sandboxes = ciCapabilityBackend{sb}
			cap := &ciTestCapability{id: "test-key"}
			unused := &ciTestCapability{id: "unselected-key"}
			adapter.Capabilities = func() *model.CapabilityRegistry { return model.NewCapabilityRegistry(cap, unused) }
			job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: name, Repo: "acme/widgets", Ref: "main", Capabilities: []string{"test-key"}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(sb.root, "home/debian/.ci-key")
			content, err := os.ReadFile(path)
			if err != nil || string(content) != "job-only-key" {
				t.Fatalf("placement: %q, %v", content, err)
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0600 {
				t.Fatalf("credential permissions: %v", info.Mode())
			}
			if cap.last.Run.ID != name || cap.last.Task.Target.String() != "acme/widgets" || unused.minted != 0 {
				t.Fatalf("wrong scope: %+v; unselected mints %d", cap.last, unused.minted)
			}
			if cap.revoked != 0 {
				t.Fatal("revoked before job finished")
			}
			if err := job.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			if cap.revoked != 1 || !fake.released {
				t.Fatalf("cleanup: revoked %d, released %v", cap.revoked, fake.released)
			}
			if _, ok := scopes.Repo(name); ok {
				t.Fatal("read scope survived release")
			}
		})
	}
}

func TestCICapabilitiesCleanupOnPreparationFailure(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"refused", "partial-mint", "placement", "unregistered", "unsupported", "revoke", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			fake := &fakeSandbox{name: "ci-failure"}
			adapter, _, scopes := newCIAdapter(fake)
			sb := &ciPlacingSandbox{fakeSandbox: fake, root: t.TempDir(), fail: scenario == "placement"}
			adapter.Sandboxes = ciCapabilityBackend{sb}
			first := &ciTestCapability{id: "first", refuse: scenario == "refused", revokeFail: scenario == "revoke"}
			second := &ciTestCapability{id: "second", fail: true}
			ids := []string{"first"}
			providers := []model.CapabilityProvider{first, second}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				second.cancel = cancel
				ids = append(ids, "second")
			}
			if scenario == "partial-mint" {
				ids = append(ids, "second")
			}
			if scenario == "unregistered" {
				ids = append(ids, "missing")
			}
			if scenario == "unsupported" {
				providers = append(providers, ciUnsupportedCapability{})
				ids = append(ids, "agent-tool")
			}
			adapter.Capabilities = func() *model.CapabilityRegistry { return model.NewCapabilityRegistry(providers...) }
			job, err := adapter.Acquire(ctx, ci.SandboxRequest{Name: fake.name, Repo: "acme/widgets", Ref: "main", Capabilities: ids})
			if scenario == "revoke" {
				if err != nil {
					t.Fatal(err)
				}
				err = job.Release(context.Background())
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !fake.released || first.revoked != first.minted {
				t.Fatalf("cleanup: released %v, minted %d revoked %d", fake.released, first.minted, first.revoked)
			}
			if _, ok := scopes.Repo(fake.name); ok {
				t.Fatal("scope survived setup failure")
			}
		})
	}
}

type ciUnsupportedCapability struct{ model.BaseCapability }

func (ciUnsupportedCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: "agent-tool", Provision: model.ProvisionGrant}
}

func TestCICapabilitiesDefaultDoesNotResolveProviders(t *testing.T) {
	t.Parallel()
	fake := &fakeSandbox{}
	adapter, _, _ := newCIAdapter(fake)
	adapter.Capabilities = func() *model.CapabilityRegistry { t.Fatal("default CI asked for providers"); return nil }
	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-default", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCIRepoCapabilitiesUseLiveRepoSettingsOnly(t *testing.T) {
	t.Parallel()
	store, db, err := daemon.OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	config := model.RepoConfig{Repo: repo, DefaultCapabilities: []string{"unrelated-agent-default"}, CICapabilities: []string{"gcp-key"}}
	if err := store.PutRepoConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	read := daemon.CiRepoCapabilities(store)
	for _, name := range []string{"acme/widgets", "ACME/Widgets"} {
		got, err := read(ctx, name)
		if err != nil || !reflect.DeepEqual(got, []string{"gcp-key"}) {
			t.Fatalf("%s: %v %v", name, got, err)
		}
	}
	got, err := read(ctx, "acme/other")
	if err != nil || len(got) != 0 {
		t.Fatalf("other repo got %v %v", got, err)
	}
	config.CICapabilities = nil
	if err := store.PutRepoConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	got, err = read(ctx, "acme/widgets")
	if err != nil || len(got) != 0 {
		t.Fatalf("cleared CI grants: %v %v", got, err)
	}
}

func TestReleaseOnlyCICapabilityIsAbsentFromCallerRuns(t *testing.T) {
	t.Parallel()
	reg := model.NewCapabilityRegistry(registrypush.New())
	creds := ciCredentialJSON(`{"registry":"ghcr.io","username":"bot","token":"secret"}`)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	for _, event := range []ci.Event{ci.EventPush, ci.EventPullRequest} {
		_, ordinary, err := daemon.ResolveCICapabilities(context.Background(), reg, creds, repo, "run", event, false,
			[]string{registrypush.CapabilityName})
		if err != nil || len(ordinary) != 0 {
			t.Fatalf("%s capability resolution = %+v, %v", event, ordinary, err)
		}
	}
	// Honoured, not merely present: a CI-only capability is refused
	// outright on the task path (model.CapabilitySpec.CIOnly), and a
	// release job is the case that must not have been caught by it.
	_, release, err := daemon.ResolveCICapabilities(context.Background(), reg, creds, repo, "run", ci.EventRelease, false,
		[]string{registrypush.CapabilityName})
	if err != nil || len(release) != 1 || release[0].Resolution.Refused {
		t.Fatalf("release capability resolution = %+v, %v", release, err)
	}
}

func TestPackagePublishAndRepoWriteStripWritePermissionsOnUntrustedEvents(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	reg := model.NewCapabilityRegistry(
		packagepublish.New(packagepublish.Config{}),
		repowrite.New(repo),
	)
	creds := ciCredentialJSON(`{"registry":"ghcr.io","username":"bot","token":"secret"}`)

	requestedCaps := []string{"package-publish", "repo-write:acme/widgets"}

	// Untrusted events (pull_request, push) must strip package-publish and repo-write
	for _, event := range []ci.Event{ci.EventPullRequest, ci.EventPush} {
		cc, resolved, err := daemon.ResolveCICapabilities(context.Background(), reg, creds, repo, "ci-job", event, false, requestedCaps)
		if err != nil {
			t.Fatalf("event %s returned unexpected error: %v", event, err)
		}
		if len(cc.Task.Grants) != 0 {
			t.Fatalf("event %s expected 0 grants, got: %+v", event, cc.Task.Grants)
		}
		if len(resolved) != 0 {
			t.Fatalf("event %s expected 0 resolved capabilities, got: %+v", event, resolved)
		}
	}

	// Trusted events (release, workflow_dispatch) must retain package-publish and repo-write
	for _, event := range []ci.Event{ci.EventRelease, ci.EventManual} {
		cc, _, err := daemon.ResolveCICapabilities(context.Background(), reg, creds, repo, "ci-job", event, false, requestedCaps)
		if err != nil {
			t.Fatalf("event %s returned unexpected error: %v", event, err)
		}
		if len(cc.Task.Grants) != 2 {
			t.Fatalf("event %s expected 2 grants, got: %+v", event, cc.Task.Grants)
		}
		for _, g := range cc.Task.Grants {
			if g.Capability != "package-publish" && g.Capability != "repo-write:acme/widgets" {
				t.Errorf("unexpected grant for %s: %s", event, g.Capability)
			}
		}
	}
}

func TestReleaseSecretsMaterializeOnlyForReleaseAndManualJobs(t *testing.T) {
	t.Parallel()
	fake := &fakeSandbox{name: "release"}
	adapter, _, _ := newCIAdapter(fake)
	sb := &ciPlacingSandbox{fakeSandbox: fake, root: t.TempDir()}
	adapter.Sandboxes = ciCapabilityBackend{sb}
	adapter.Credentials = ciCredentialJSON("private-value")

	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name: fake.name, Repo: "acme/widgets", Ref: "main", Event: ci.EventRelease,
		ReleaseSecrets: []string{"NPM_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	placed := filepath.Join(sb.root, strings.TrimPrefix(ci.ReleaseSecretPath("NPM_TOKEN"), "/"))
	content, err := os.ReadFile(placed)
	if err != nil || string(content) != "private-value" {
		t.Fatalf("release secret placement = %q, %v", content, err)
	}
	info, err := os.Stat(placed)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("release secret mode = %v, %v", info.Mode(), err)
	}
	if err := job.Release(context.Background()); err != nil {
		t.Fatal(err)
	}

	ordinary := &fakeSandbox{name: "push"}
	adapter, _, _ = newCIAdapter(ordinary)
	adapter.Sandboxes = ciCapabilityBackend{&ciPlacingSandbox{fakeSandbox: ordinary, root: t.TempDir()}}
	adapter.Credentials = ciCredentialJSON("private-value")
	if _, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name: ordinary.name, Repo: "acme/widgets", Ref: "main", Event: ci.EventPush,
		ReleaseSecrets: []string{"NPM_TOKEN"},
	}); err == nil {
		t.Fatal("ordinary push accepted release secrets")
	}
	if !ordinary.released {
		t.Fatal("sandbox holding a refused release secret was not released")
	}
}

type ciGCPKeyCapability struct {
	model.BaseCapability
	minted, revoked int
}

func (c *ciGCPKeyCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: "gcp-key", Provision: model.ProvisionMint}
}
func (c *ciGCPKeyCapability) Resolve(context.Context, model.CapabilityContext) (model.Resolution, error) {
	return model.Honoured(), nil
}
func (c *ciGCPKeyCapability) Materialize(_ context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	c.minted++
	return model.Materialization{
		Lease: &model.Lease{Capability: "gcp-key", Resource: cc.Run.ID},
		Placements: []model.Placement{
			{Side: model.SideSandbox, Path: gcpkey.SandboxKeyPath, Content: `{"type":"service_account","project_id":"ci-test"}`},
		},
	}, nil
}
func (c *ciGCPKeyCapability) Revoke(context.Context, model.CapabilityContext, model.Lease) error {
	c.revoked++
	return nil
}

type ciGitHubSandboxCapability struct {
	model.BaseCapability
	minted, revoked int
}

func (c *ciGitHubSandboxCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: "github-sandbox", Provision: model.ProvisionMint}
}
func (c *ciGitHubSandboxCapability) Resolve(context.Context, model.CapabilityContext) (model.Resolution, error) {
	return model.Honoured(), nil
}
func (c *ciGitHubSandboxCapability) Materialize(_ context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	c.minted++
	return model.Materialization{
		Lease: &model.Lease{Capability: "github-sandbox", Resource: cc.Run.ID},
		Placements: []model.Placement{
			{Side: model.SideSandbox, Path: githubsandbox.SandboxTokenPath, Content: "ghs_test_token_walkthrough_123"},
			{Side: model.SideSandbox, Path: githubsandbox.SandboxRepoPath, Content: cc.Task.Target.String()},
		},
	}, nil
}
func (c *ciGitHubSandboxCapability) Revoke(context.Context, model.CapabilityContext, model.Lease) error {
	c.revoked++
	return nil
}

func TestCICapabilityMountingAndGHCRPruneExecution(t *testing.T) {
	t.Parallel()
	fake := &fakeSandbox{name: "ci-walkthrough"}
	adapter, _, _ := newCIAdapter(fake)
	sb := &ciPlacingSandbox{fakeSandbox: fake, root: t.TempDir()}
	adapter.Sandboxes = ciCapabilityBackend{sb}

	gcpCap := &ciGCPKeyCapability{}
	ghCap := &ciGitHubSandboxCapability{}
	adapter.Capabilities = func() *model.CapabilityRegistry {
		return model.NewCapabilityRegistry(gcpCap, ghCap)
	}

	// 1. Acquire job declaring capabilities: [gcp-key, github-sandbox]
	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name:         fake.name,
		Repo:         "bwsalmon/grain",
		Ref:          "grain/task-323",
		Capabilities: []string{"gcp-key", "github-sandbox"},
	})
	if err != nil {
		t.Fatalf("acquire job with capabilities: %v", err)
	}

	// 2. Verify credential files are provisioned at standard paths with 0600 mode and non-empty content
	checkPlacement := func(sandboxPath string, expectedContent string) {
		fullPath := filepath.Join(sb.root, strings.TrimPrefix(sandboxPath, "/"))
		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("failed to read mounted file %s: %v", sandboxPath, err)
		}
		if len(content) == 0 {
			t.Fatalf("mounted file %s is empty", sandboxPath)
		}
		if expectedContent != "" && string(content) != expectedContent {
			t.Fatalf("mounted file %s content = %q, want %q", sandboxPath, string(content), expectedContent)
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			t.Fatalf("stat %s: %v", fullPath, err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("mounted file %s permissions = %v, want 0600", sandboxPath, info.Mode().Perm())
		}
	}

	checkPlacement(gcpkey.SandboxKeyPath, `{"type":"service_account","project_id":"ci-test"}`)
	checkPlacement(githubsandbox.SandboxTokenPath, "ghs_test_token_walkthrough_123")
	checkPlacement(githubsandbox.SandboxRepoPath, "bwsalmon/grain")

	if gcpCap.minted != 1 || ghCap.minted != 1 {
		t.Fatalf("expected 1 mint each, got gcp=%d, gh=%d", gcpCap.minted, ghCap.minted)
	}

	// 3. Release job and assert cleanup
	if err := job.Release(context.Background()); err != nil {
		t.Fatalf("release job: %v", err)
	}
	if gcpCap.revoked != 1 || ghCap.revoked != 1 {
		t.Fatalf("expected 1 revocation each, got gcp=%d, gh=%d", gcpCap.revoked, ghCap.revoked)
	}

	// 4. Verify job omitting declared capabilities does not mount credential artifacts
	plainFake := &fakeSandbox{name: "ci-no-caps"}
	plainAdapter, _, _ := newCIAdapter(plainFake)
	plainSB := &ciPlacingSandbox{fakeSandbox: plainFake, root: t.TempDir()}
	plainAdapter.Sandboxes = ciCapabilityBackend{plainSB}
	plainAdapter.Capabilities = func() *model.CapabilityRegistry {
		return model.NewCapabilityRegistry(gcpCap, ghCap)
	}

	plainJob, err := plainAdapter.Acquire(context.Background(), ci.SandboxRequest{
		Name:         plainFake.name,
		Repo:         "bwsalmon/grain",
		Ref:          "grain/task-323",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("acquire plain job: %v", err)
	}

	for _, p := range []string{gcpkey.SandboxKeyPath, githubsandbox.SandboxTokenPath, githubsandbox.SandboxRepoPath} {
		fullPath := filepath.Join(plainSB.root, strings.TrimPrefix(p, "/"))
		if _, err := os.Stat(fullPath); !os.IsNotExist(err) {
			t.Fatalf("unrequested credential %s was mounted in plain sandbox", p)
		}
	}

	if err := plainJob.Release(context.Background()); err != nil {
		t.Fatalf("release plain job: %v", err)
	}
}

func TestCICrossRepoReadCapabilityGrantsReadScope(t *testing.T) {
	t.Parallel()
	fake := &fakeSandbox{name: "ci-cross-repo"}
	adapter, _, scopes := newCIAdapter(fake)
	adapter.Sandboxes = ciCapabilityBackend{&ciPlacingSandbox{fakeSandbox: fake, root: t.TempDir()}}
	adapter.Capabilities = func() *model.CapabilityRegistry {
		reg := model.NewCapabilityRegistry()
		reg.RegisterDynamic(reporead.DynamicProvider)
		return reg
	}

	req := ci.SandboxRequest{
		Name:         fake.name,
		Repo:         "acme/widgets",
		Ref:          "main",
		Event:        ci.EventPush,
		Capabilities: []string{"repo-read:acme/tools"},
	}

	job, err := adapter.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	granted := scopes.Repos(fake.name)
	if len(granted) != 2 {
		t.Fatalf("granted repos = %v, want 2", granted)
	}

	hasWidgets := false
	hasTools := false
	for _, r := range granted {
		if r.Owner == "acme" && r.Name == "widgets" {
			hasWidgets = true
		}
		if r.Owner == "acme" && r.Name == "tools" {
			hasTools = true
		}
	}
	if !hasWidgets || !hasTools {
		t.Fatalf("granted repos = %v; want acme/widgets and acme/tools", granted)
	}

	if err := job.Release(context.Background()); err != nil {
		t.Fatal(err)
	}

	if remaining := scopes.Repos(fake.name); len(remaining) != 0 {
		t.Fatalf("scopes survived release: %v", remaining)
	}
}

func TestCICrossRepoWriteCapabilityGatingAndScopes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		event     ci.Event
		wantWrite bool
	}{
		{event: ci.EventPullRequest, wantWrite: false},
		{event: ci.EventPush, wantWrite: false},
		{event: ci.EventRelease, wantWrite: true},
		{event: ci.EventManual, wantWrite: true},
	} {
		tc := tc
		t.Run(string(tc.event), func(t *testing.T) {
			fake := &fakeSandbox{name: "ci-cross-repo-write-" + string(tc.event)}
			adapter, _, _ := newCIAdapter(fake)
			writeScopes := adapter.WriteScopes
			adapter.Sandboxes = ciCapabilityBackend{&ciPlacingSandbox{fakeSandbox: fake, root: t.TempDir()}}
			adapter.Capabilities = func() *model.CapabilityRegistry {
				reg := model.NewCapabilityRegistry()
				reg.RegisterDynamic(repowrite.DynamicProvider)
				return reg
			}

			req := ci.SandboxRequest{
				Name:         fake.name,
				Repo:         "acme/widgets",
				Ref:          "main",
				Event:        tc.event,
				Capabilities: []string{"repo-write:acme/tools"},
			}

			job, err := adapter.Acquire(context.Background(), req)
			if err != nil {
				t.Fatalf("Acquire failed: %v", err)
			}

			grantedWrites := writeScopes.Repos(fake.name)
			if tc.wantWrite {
				if len(grantedWrites) != 1 || grantedWrites[0].Owner != "acme" || grantedWrites[0].Name != "tools" {
					t.Fatalf("granted write repos = %v, want [acme/tools]", grantedWrites)
				}
			} else {
				if len(grantedWrites) != 0 {
					t.Fatalf("untrusted event %s granted write repos: %v", tc.event, grantedWrites)
				}
			}

			if err := job.Release(context.Background()); err != nil {
				t.Fatal(err)
			}

			if remaining := writeScopes.Repos(fake.name); len(remaining) != 0 {
				t.Fatalf("write scopes survived release: %v", remaining)
			}
		})
	}
}

func TestTrustedScheduleHoldsRepoWriteButNotPackagePublish(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	reg := model.NewCapabilityRegistry(
		packagepublish.New(packagepublish.Config{}),
		repowrite.New(repo),
	)
	creds := ciCredentialJSON(`{"registry":"ghcr.io","username":"bot","token":"secret"}`)
	requestedCaps := []string{"package-publish", "repo-write:acme/widgets"}

	// A caller's own `-event schedule` run is untrusted: nothing write-scoped.
	cc, _, err := daemon.ResolveCICapabilities(context.Background(), reg, creds, repo, "ci-job", ci.EventSchedule, false, requestedCaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Task.Grants) != 0 {
		t.Fatalf("untrusted schedule grants = %+v, want none", cc.Task.Grants)
	}

	// The run grain's cron fired holds repo-write, and only the providers
	// that opted in: package-publish stays release/dispatch only.
	cc, _, err = daemon.ResolveCICapabilities(context.Background(), reg, creds, repo, "ci-job", ci.EventSchedule, true, requestedCaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Task.Grants) != 1 || cc.Task.Grants[0].Capability != "repo-write:acme/widgets" {
		t.Fatalf("trusted schedule grants = %+v, want only repo-write:acme/widgets", cc.Task.Grants)
	}

	// The flag means nothing on any other event.
	cc, _, err = daemon.ResolveCICapabilities(context.Background(), reg, creds, repo, "ci-job", ci.EventPush, true, requestedCaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Task.Grants) != 0 {
		t.Fatalf("push with trustedSchedule grants = %+v, want none", cc.Task.Grants)
	}
}

// A scheduled workflow declaring repo-write for its own repository holds
// write through the proxy and needs no entry in the deployment's CI
// capabilities: the declaration is the allowance and the deployment's
// git connection the authority (grain/task-1654).
func TestDeclaredRepoAccessRunsAsTheTeamOnlyWhenTheEventMayCarryIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		event           ci.Event
		trustedSchedule bool
		capabilities    []string
		wantWrite       bool
		wantTeam        bool
	}{
		{name: "grain's own schedule", event: ci.EventSchedule, trustedSchedule: true,
			capabilities: []string{"repo-write:acme/widgets"}, wantWrite: true, wantTeam: true},
		{name: "a caller's -event schedule", event: ci.EventSchedule,
			capabilities: []string{"repo-write:acme/widgets"}},
		{name: "a pull request", event: ci.EventPullRequest,
			capabilities: []string{"repo-write:acme/widgets"}},
		{name: "declared read on a push", event: ci.EventPush,
			capabilities: []string{"repo-read:acme/tools"}, wantTeam: true},
		{name: "nothing declared on grain's own schedule", event: ci.EventSchedule, trustedSchedule: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeSandbox{name: "ci-team-" + strings.ReplaceAll(tc.name, " ", "-")}
			adapter, _, _ := newCIAdapter(fake)
			adapter.Sandboxes = ciCapabilityBackend{&ciPlacingSandbox{fakeSandbox: fake, root: t.TempDir()}}
			adapter.Teams = gitproxy.NewSandboxTeams()
			var asked []string
			adapter.TeamGitCredential = func(_ context.Context, team, owner, repo string) (string, error) {
				asked = append(asked, team+" "+owner+"/"+repo)
				return "infra-cred", nil
			}
			adapter.Capabilities = func() *model.CapabilityRegistry {
				reg := model.NewCapabilityRegistry()
				reg.RegisterDynamic(repowrite.DynamicProvider)
				reg.RegisterDynamic(reporead.DynamicProvider)
				return reg
			}

			job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
				Name: fake.name, Repo: "acme/widgets", Ref: "main", Event: tc.event,
				TrustedSchedule: tc.trustedSchedule, Capabilities: tc.capabilities,
			})
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			writes := adapter.WriteScopes.Repos(fake.name)
			if tc.wantWrite != (len(writes) == 1) {
				t.Errorf("write scopes = %v, want write %v", writes, tc.wantWrite)
			}
			// Every CI sandbox is registered as the deployment's (the
			// default team's row holds its git connections, and a CI run
			// has no team of its own since stage 4c), declared access or
			// not; what the declaration and the event decide is whether
			// the credential is checked for it and whether it holds write.
			team, registered := adapter.Teams.Team(fake.name)
			if !registered || team != model.DefaultTeamID {
				t.Errorf("registered as %q (%v), want %s", team, registered, model.DefaultTeamID)
			}
			if !tc.wantTeam && len(asked) != 0 {
				t.Errorf("the team's credential was checked for access this event may not carry: %v", asked)
			}
			if tc.wantTeam && len(asked) == 0 {
				t.Error("the team's credential was never checked before the checkout")
			}
			if err := job.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, ok := adapter.Teams.Team(fake.name); ok {
				t.Error("team registration survived release")
			}
		})
	}
}

func TestDeclaredRepoWriteFailsClearlyWhenTheTeamHasNoCredential(t *testing.T) {
	t.Parallel()
	fake := &fakeSandbox{name: "ci-team-no-credential"}
	adapter, _, _ := newCIAdapter(fake)
	adapter.Sandboxes = ciCapabilityBackend{&ciPlacingSandbox{fakeSandbox: fake, root: t.TempDir()}}
	adapter.Teams = gitproxy.NewSandboxTeams()
	adapter.TeamGitCredential = func(_ context.Context, team, _, _ string) (string, error) {
		return "", &model.NoTeamGitCredentialError{Team: team}
	}
	adapter.Capabilities = func() *model.CapabilityRegistry {
		reg := model.NewCapabilityRegistry()
		reg.RegisterDynamic(repowrite.DynamicProvider)
		return reg
	}

	_, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name: fake.name, Repo: "acme/widgets", Ref: "main", Event: ci.EventSchedule,
		TrustedSchedule: true, Capabilities: []string{"repo-write:acme/widgets"},
	})
	if err == nil {
		t.Fatal("a job whose team has no credential was handed a sandbox")
	}
	// The refusal names the deployment's git connections and the page
	// that sets them (docs/single-tenant-reset.md, stage 1).
	for _, want := range []string{"acme/widgets", "Settings → Git"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "admin") {
		t.Errorf("error %q sends the team to an admin", err)
	}
	if _, ok := adapter.Teams.Team(fake.name); ok {
		t.Error("a refused job's sandbox was registered as its team")
	}
	if writes := adapter.WriteScopes.Repos(fake.name); len(writes) != 0 {
		t.Errorf("a refused job holds write scopes %v", writes)
	}
}
