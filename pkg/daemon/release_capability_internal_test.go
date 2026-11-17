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
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/imagesigning"
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/registrypush"
	"github.com/ai-on-gke/grain/pkg/capability/release"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// These hold the in-process release server (pkg/capability/release) to
// the image-signing, registry-push and package-publish providers it
// retired: a CI job holding the old bare grant, resolved by the retired
// provider through ResolveCICapabilities, and one holding the grant
// schema migration 101 rewrote it to, minted through the job's grant
// admission, get the same files on the same events and are refused on
// the same ones.

const releaseTestKey = "-----BEGIN ENCRYPTED SIGSTORE PRIVATE KEY-----\nabc\n-----END ENCRYPTED SIGSTORE PRIVATE KEY-----\n"

type releaseTestCredentials map[string]string

func (c releaseTestCredentials) Resolve(_ context.Context, name string) (string, error) {
	if v, ok := c[name]; ok {
		return v, nil
	}
	return "", fmt.Errorf("secret %q is not set", name)
}

type releaseTestLadder struct{ fail bool }

func (l releaseTestLadder) PackageCredential(registry, owner, repo string) (packagepublish.Credential, error) {
	if l.fail {
		return packagepublish.Credential{}, errors.New("no GitHub credential covers " + owner + "/" + repo)
	}
	if registry == "" {
		registry = "ghcr.io"
	}
	return packagepublish.Credential{Registry: registry, Username: owner + "-bot", Token: "tok-" + repo, Name: "default"}, nil
}

func releaseTestSecrets() releaseTestCredentials {
	return releaseTestCredentials{
		imagesigning.KeyCredential:      releaseTestKey,
		imagesigning.PasswordCredential: "hunter2\n",
		registrypush.CredentialName:     `{"registry":"registry.example.com","username":"pusher","token":"push-token"}`,
	}
}

// releaseLegacyRegistry is the registry with the retired providers in it,
// as CapabilityProviders built it before they were retired.
func releaseLegacyRegistry(ladder packagepublish.CredentialSource) *model.CapabilityRegistry {
	return model.NewCapabilityRegistry(
		imagesigning.New(imagesigning.Config{}),
		registrypush.NewWithConfig(registrypush.Config{}),
		packagepublish.New(packagepublish.Config{Credentials: ladder}),
	)
}

type releaseInProcessServers struct{}

func (releaseInProcessServers) Lookup(id string) (*protocol.Server, error) {
	return grainserver.Lookup(nil)(id)
}

// releaseLegacyOutcome is what the retired provider gave a job of event:
// the files it placed, whether it refused the job, or neither (it was not
// handed the grant at all).
func releaseLegacyOutcome(t *testing.T, reg *model.CapabilityRegistry, creds model.CredentialResolver, event ci.Event, trusted bool, name string) (files []string, refused bool) {
	t.Helper()
	ctx := context.Background()
	cc, resolved, err := ResolveCICapabilities(ctx, reg, creds, model.RepoRef{Owner: "acme", Name: "widgets"}, "c0123abcd-2", event, trusted, []string{name})
	if err != nil {
		t.Fatalf("legacy %s on %s: %v", name, event, err)
	}
	for _, r := range resolved {
		if r.Grant.Capability == name && r.Resolution.Refused {
			return nil, true
		}
	}
	materialized, err := model.MaterializeGrants(ctx, reg, cc, resolved)
	if err != nil {
		t.Fatalf("legacy %s on %s: materializing: %v", name, event, err)
	}
	for _, m := range materialized {
		for _, p := range m.Materialization.Placements {
			files = append(files, fmt.Sprintf("%s 0%s %q", p.Path, p.EffectiveMode(), p.Content))
		}
	}
	sort.Strings(files)
	return files, false
}

// releaseRewrittenOutcome is what the release server gives a job of
// event holding grant, through the job's grant admission.
func releaseRewrittenOutcome(t *testing.T, creds model.CredentialResolver, cfg *model.Config, event ci.Event, trusted bool, grant string) (files []string, refused bool) {
	t.Helper()
	g := &CiGrantReservations{
		Capabilities: func() *model.CapabilityRegistry { return model.NewCapabilityRegistry() },
		Credentials:  creds,
		Servers:      func() orchestrator.CapabilityServers { return releaseInProcessServers{} },
		Config:       func(context.Context) (*model.Config, error) { return cfg, nil },
	}
	req := ci.SandboxRequest{
		Name: "c0123abcd-2", Repo: "acme/widgets", Ref: "main", Event: event, TrustedSchedule: trusted,
		RunID: "0123abcd", JobAttempt: 2, Capabilities: []string{grant},
	}
	done, err := g.Acquire(context.Background(), req, nil)
	if err != nil {
		if !strings.Contains(err.Error(), "refused") {
			t.Fatalf("rewritten %s on %s: %v", grant, event, err)
		}
		return nil, true
	}
	defer done()
	pass := g.Pass(req.Name)
	if pass == nil {
		return nil, false
	}
	for _, p := range pass.Placements() {
		files = append(files, fmt.Sprintf("%s %s %q", p.Path, p.Mode, p.Content))
	}
	sort.Strings(files)
	return files, false
}

var releaseTestEvents = []struct {
	event   ci.Event
	trusted bool
}{
	{ci.EventPush, false},
	{ci.EventPullRequest, false},
	{ci.EventRelease, false},
	{ci.EventManual, false},
	{ci.EventSchedule, false},
	{ci.EventSchedule, true},
}

func TestReleaseServerMigrationFidelity(t *testing.T) {
	t.Cleanup(func() { release.Configure(packagepublish.Config{}) })
	for _, broken := range []bool{false, true} {
		creds, ladder := releaseTestSecrets(), releaseTestLadder{fail: broken}
		if broken {
			creds = releaseTestCredentials{}
		}
		release.Configure(packagepublish.Config{Credentials: ladder})
		reg := releaseLegacyRegistry(ladder)
		for _, name := range []string{"image-signing", "registry-push", "package-publish"} {
			rewritten := model.RetiredGrantsBecame(name)[0]
			if !strings.HasPrefix(rewritten, model.ReleaseCapabilityServerID+"#"+name) {
				t.Fatalf("%s is rewritten to %q, want the release server's #%s", name, rewritten, name)
			}
			for _, e := range releaseTestEvents {
				t.Run(fmt.Sprintf("%s/%s/trusted=%v/broken=%v", name, e.event, e.trusted, broken), func(t *testing.T) {
					oldFiles, oldRefused := releaseLegacyOutcome(t, reg, creds, e.event, e.trusted, name)
					newFiles, newRefused := releaseRewrittenOutcome(t, creds, &model.Config{DeploymentID: "dep1"}, e.event, e.trusted, rewritten)
					if oldRefused != newRefused {
						t.Fatalf("refused: legacy %v, rewritten %v", oldRefused, newRefused)
					}
					if strings.Join(oldFiles, "\n") != strings.Join(newFiles, "\n") {
						t.Errorf("placements differ:\nlegacy:    %q\nrewritten: %q", oldFiles, newFiles)
					}
					// A bare name the migration did not reach (a
					// repository's .grain) is no alias any longer: the
					// job is refused, naming what to declare instead,
					// rather than left waiting for a provider.
					if bareFiles, bareRefused := releaseRewrittenOutcome(t, creds, &model.Config{DeploymentID: "dep1"}, e.event, e.trusted, name); !bareRefused {
						t.Errorf("the retired %s got %q, want the job refused", name, bareFiles)
					}
				})
			}
		}
	}
}

// The release server's event rules are grain's, not the deployment's: a
// capability policy rule that tries to allow image-signing on a
// pull_request does not hand a pull-request job the signing key, and
// Permits refuses it whatever the rule says. A policy can still narrow.
func TestReleaseServerRulesCannotBeLoosenedByPolicy(t *testing.T) {
	creds := releaseTestSecrets()
	signing := model.ImageSigningRef + "={}"
	loosened := &model.Config{DeploymentID: "dep1", CapabilityPolicy: model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: model.ImageSigningRef, RunKinds: []string{model.CapabilityRunCI, model.CapabilityRunTask}, Events: []string{"pull_request", "push", "release"}, AllowUntrusted: true},
	}}}
	if err := loosened.CapabilityPolicy.Validate(); err != nil {
		t.Fatalf("the loosening rule is not even valid: %v", err)
	}
	for _, event := range []ci.Event{ci.EventPullRequest, ci.EventPush} {
		files, refused := releaseRewrittenOutcome(t, creds, loosened, event, false, signing)
		if len(files) != 0 {
			t.Errorf("a %s job was handed %q under a policy allowing image-signing on it", event, files)
		}
		if refused {
			t.Errorf("a %s job was refused rather than simply not handed image-signing", event)
		}
		err := loosened.CapabilityPolicy.Permits(signing, model.CapabilitySubject{
			Repo: &model.RepoRef{Owner: "acme", Name: "widgets"}, Kind: model.CapabilityRunCI, Event: string(event),
		})
		if err == nil || !strings.Contains(err.Error(), "no capability policy rule can widen it") {
			t.Errorf("Permits(image-signing, %s) = %v, want grain's own rule's refusal", event, err)
		}
	}
	for _, kind := range []string{model.CapabilityRunTask, model.CapabilityRunProcess} {
		err := loosened.CapabilityPolicy.Permits(signing, model.CapabilitySubject{Kind: kind})
		if err == nil || !strings.Contains(err.Error(), "is a CI capability, not a task grant") {
			t.Errorf("Permits(image-signing, %s run) = %v, want the CI-only refusal", kind, err)
		}
		if v := loosened.CapabilityPolicy.VerdictForKind(signing, kind); v != model.CapabilityPolicyCIOnly {
			t.Errorf("VerdictForKind(image-signing, %s) = %q, want %q", kind, v, model.CapabilityPolicyCIOnly)
		}
	}
	// A task holding it, in either spelling, is refused before anything
	// is minted.
	for _, grant := range []string{"image-signing", signing} {
		resolved, err := model.ResolveGrants(context.Background(), model.NewCapabilityRegistry(), model.CapabilityContext{
			Task: model.Task{Grants: []model.Grant{{Capability: grant, Via: model.GrantByLabel}}},
		})
		if err != nil || len(resolved) != 1 || !resolved[0].Resolution.Refused {
			t.Errorf("a task holding %s resolved to %+v, %v; want it refused as CI-only", grant, resolved, err)
		}
	}
	// The release event is still admitted -- and a policy may narrow it.
	if files, _ := releaseRewrittenOutcome(t, creds, loosened, ci.EventRelease, false, signing); len(files) != 2 {
		t.Errorf("a release job got %q, want the key and its password", files)
	}
	denied := &model.Config{DeploymentID: "dep1", CapabilityPolicy: model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: model.ImageSigningRef, Repos: []string{"acme/widgets"}, Deny: true},
	}}}
	g := &CiGrantReservations{
		Capabilities: func() *model.CapabilityRegistry { return model.NewCapabilityRegistry() },
		Credentials:  creds,
		Servers:      func() orchestrator.CapabilityServers { return releaseInProcessServers{} },
		Config:       func(context.Context) (*model.Config, error) { return denied, nil },
	}
	_, err := g.Acquire(context.Background(), ci.SandboxRequest{
		Name: "c0123abcd-2", Repo: "acme/widgets", Event: ci.EventRelease, RunID: "0123abcd", JobAttempt: 2,
		Capabilities: []string{signing},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "denies") {
		t.Errorf("a release job under a policy denying image-signing: %v, want the policy's denial", err)
	}
}

// A real daemon hands CI grant admission the attached registry
// (capabilityServerLookup), which tracks mcp_servers only: the release
// server is in-process (capability.Register) and is never in it. A
// workflow_dispatch job holding release#package-publish was refused with
// "capability https://grain.dev/capabilities/release is not configured in
// mcpServers" on every attempt -- every repository whose ci_capabilities
// migration 101 rewrote lost its publish job
// (docs/test-runs/2026-11-04-capabilities.md, F4). The tests above stood
// grainserver.Lookup in for the registry, so none of them saw it.
func TestCIJobMintsAnInProcessReleaseGrantWithOnlyTheAttachedRegistry(t *testing.T) {
	t.Cleanup(func() { release.Configure(packagepublish.Config{}) })
	ladder := releaseTestLadder{}
	// As the daemon configures it (configureCapabilities): with the
	// refresh route, whose docker credential helper is placed "0755" --
	// the four-digit form a "0" prefix turned into "00755", which the
	// protocol refused for the whole mint (F5).
	release.Configure(packagepublish.Config{Credentials: ladder, Refresh: packagepublish.NewRefresher(),
		RefreshURL: func() string { return "http://10.0.0.1:4242" }})
	registry := servers.New(servers.Options{})
	t.Cleanup(registry.Close)
	g := &CiGrantReservations{
		Capabilities: func() *model.CapabilityRegistry { return model.NewCapabilityRegistry() },
		Credentials:  releaseTestSecrets(),
		Servers:      func() orchestrator.CapabilityServers { return registry },
		Config:       func(context.Context) (*model.Config, error) { return &model.Config{DeploymentID: "dep1"}, nil },
	}
	req := ci.SandboxRequest{
		Name: "c0123abcd-2", Repo: "acme/widgets", Ref: "main", Event: ci.EventManual,
		RunID: "0123abcd", JobAttempt: 2, Capabilities: []string{model.PackagePublishRef + "={}"},
	}
	done, err := g.Acquire(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("a workflow_dispatch job holding release#package-publish: %v", err)
	}
	defer done()
	pass := g.Pass(req.Name)
	if pass == nil || len(pass.Placements()) == 0 {
		t.Fatalf("pass = %+v, want the package-publish files", pass)
	}
	modes := map[string]string{}
	for _, p := range pass.Placements() {
		modes[p.Path] = p.Mode
	}
	if got := modes[packagepublish.HelperPath]; got != "0755" {
		t.Errorf("%s mode = %q, want 0755 (placements %v)", packagepublish.HelperPath, got, modes)
	}
	if got := modes[packagepublish.RefreshTokenPath]; got != "0600" {
		t.Errorf("%s mode = %q, want 0600", packagepublish.RefreshTokenPath, got)
	}

	// With no registry at all, an in-process grant still mints, and an
	// external one is refused as before.
	g.Servers = nil
	req.Name, req.JobAttempt = "c0123abcd-3", 3
	done2, err := g.Acquire(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("with no attached registry: %v", err)
	}
	done2()
	req.Name, req.JobAttempt = "c0123abcd-4", 4
	req.Capabilities = []string{"https://github.com/acme/caps#build={}"}
	if _, err := g.Acquire(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "attaches no capability servers") {
		t.Fatalf("an external reference with no registry: %v, want the no-servers refusal", err)
	}
}

// Serving in-process servers to CI grant admission (F4) opens every
// in-process server to a CI job, not only the release server: grain's own
// methods are minted for a job whose repository's CI capabilities name them.
// They stay behind the same gates an external reference is: a run triggered
// by untrusted input is refused one the policy has not opted in with
// allowUntrusted, and a policy rule's deny holds, with or without an attached
// registry.
func TestCIJobInProcessGrantsStayBehindThePolicy(t *testing.T) {
	health := model.GrainHealthGrant
	for _, tc := range []struct {
		name    string
		servers bool
		policy  model.CapabilityPolicy
		event   ci.Event
		untrust bool
		wantErr string
	}{
		{name: "untrusted pull request, attached registry", servers: true, event: ci.EventPullRequest, untrust: true,
			wantErr: "not granted to runs triggered by untrusted input"},
		{name: "untrusted pull request, no registry", event: ci.EventPullRequest, untrust: true,
			wantErr: "not granted to runs triggered by untrusted input"},
		{name: "a deny rule on a trusted push", servers: true, event: ci.EventPush,
			policy:  model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{{Capability: model.GrainHealthRef, Deny: true}}},
			wantErr: "capability policy denies"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := tc.policy
			g := &CiGrantReservations{
				Capabilities: func() *model.CapabilityRegistry { return model.NewCapabilityRegistry() },
				Credentials:  releaseTestSecrets(),
				Config: func(context.Context) (*model.Config, error) {
					return &model.Config{DeploymentID: "dep1", CapabilityPolicy: policy}, nil
				},
			}
			if tc.servers {
				registry := servers.New(servers.Options{})
				t.Cleanup(registry.Close)
				g.Servers = func() orchestrator.CapabilityServers { return registry }
			}
			req := ci.SandboxRequest{
				Name: "c0123abcd-1", Repo: "acme/widgets", Ref: "main", Event: tc.event,
				RunID: "0123abcd", JobAttempt: 1, Capabilities: []string{health}, Untrusted: tc.untrust,
			}
			done, err := g.Acquire(context.Background(), req, nil)
			if err == nil {
				done()
				t.Fatalf("%s was admitted, want %q", health, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if pass := g.Pass(req.Name); pass != nil {
				t.Fatalf("a refused job holds a pass: %+v", pass)
			}
		})
	}
}
