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

// cmd/graind (this file's original home, before bwsalmon/agents#313
// folded it into cmd/grain as the "daemon" subcommand) had no tests at
// all before bwsalmon/agents#265 -- every other binary here (cmd/
// mcpserver, cmd/ui, now mcpserver.go and, before bwsalmon/agents#363
// folded it into daemon.go, ui.go) was the same "no test files" gap, but
// graind was the one that issue asked about: the process that wires a
// real embedded SQLite store, a real
// in-process git proxy, a real Gemini client and the capability registry
// together and runs them on a timer. These tests cover the small pieces
// of that wiring run() itself does not delegate to an already-tested
// package -- readTrimmedFile, credentialTokenSource,
// capabilityProviders, openStore and startGitProxy -- against real
// temp-directory material, the same "real embedded SQLite, not a fake"
// discipline pkg/model/sqlite's own tests hold to.
// daemon_live_test.go covers run() itself, end to end, against a real
// Gemini API key.

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestReadTrimmedFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("  secret-value\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readTrimmedFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret-value" {
		t.Fatalf("readTrimmedFile = %q, want %q", got, "secret-value")
	}
}

func TestReadTrimmedFileMissing(t *testing.T) {
	t.Parallel()
	if _, err := readTrimmedFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error reading a file that does not exist")
	}
}

func TestCredentialTokenSourceTokenFor(t *testing.T) {
	t.Parallel()
	writeCredentials := func(t *testing.T, dir string, patterns map[string]string, tokens map[string]string) *gitproxy.CredentialSet {
		t.Helper()
		data, err := json.Marshal(patterns)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		for name, token := range tokens {
			if err := os.WriteFile(filepath.Join(dir, name+".token"), []byte(token), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		set, err := gitproxy.LoadCredentialSet(dir)
		if err != nil {
			t.Fatal(err)
		}
		return set
	}

	t.Run("no ladder configured at all", func(t *testing.T) {
		set, err := gitproxy.LoadCredentialSet(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		src := credentialTokenSource{credentials: set}
		if tok := src.TokenFor("acme", "widgets"); tok != nil {
			t.Fatalf("TokenFor with no ladder = %v, want nil", tok)
		}
	})

	t.Run("anonymous credential resolves but carries no token", func(t *testing.T) {
		set := writeCredentials(t, t.TempDir(), map[string]string{"*": "anonymous"}, nil)
		src := credentialTokenSource{credentials: set}
		if tok := src.TokenFor("acme", "widgets"); tok != nil {
			t.Fatalf("TokenFor for an anonymous credential = %v, want nil", tok)
		}
	})

	t.Run("a named credential resolves its token file", func(t *testing.T) {
		set := writeCredentials(t, t.TempDir(), map[string]string{"acme/*": "bot"}, map[string]string{"bot": "sekret\n"})
		// The ladder above is not read: the deployment's git connection
		// (teamCredential) names the credential, or nothing does.
		src := credentialTokenSource{credentials: set}
		if tok := src.TokenFor("acme", "widgets"); tok != nil {
			t.Fatalf("TokenFor through the ladder alone = %v, want nil", tok)
		}
		src.teamCredential = func(_, owner, _ string) (string, bool) { return "bot", owner == "acme" }
		tok := src.TokenFor("acme", "widgets")
		if tok == nil || *tok != "sekret" {
			t.Fatalf("TokenFor(acme, widgets) = %v, want \"sekret\"", tok)
		}
		if tok := src.TokenFor("other", "repo"); tok != nil {
			t.Fatalf("TokenFor for an unmatched repo = %v, want nil", tok)
		}
	})
}

func TestCapabilityProviders(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			name: "no gcp project configured still registers the built-in capabilities and the three model proxies",
			cfg:  Config{},
			want: []string{"github-sandbox", "self-debug", "git-push-any-ref", "bootstrap-playbooks", "grain-ci", "registry-push", "console", "read-issues", "priority-dispatch", "durable", "package-publish", "image-signing", "release-gate-read", "claude-proxy", "openai-proxy", "gemini-proxy"},
		},
		{
			name: "an agent service account without a gcp project does not add either gcp capability",
			cfg:  Config{GcpServiceAccountEmail: "agent@proj.iam.gserviceaccount.com"},
			want: []string{"github-sandbox", "self-debug", "git-push-any-ref", "bootstrap-playbooks", "grain-ci", "registry-push", "console", "read-issues", "priority-dispatch", "durable", "package-publish", "image-signing", "release-gate-read", "claude-proxy", "openai-proxy", "gemini-proxy"},
		},
		{
			name: "a gcp project with no agent service account only adds gemini-key to the built-in capabilities",
			cfg:  Config{GcpProject: "proj"},
			want: []string{"gemini-key", "github-sandbox", "self-debug", "git-push-any-ref", "bootstrap-playbooks", "grain-ci", "registry-push", "console", "read-issues", "priority-dispatch", "durable", "package-publish", "image-signing", "release-gate-read", "claude-proxy", "openai-proxy", "gemini-proxy"},
		},
		{
			name: "a gcp project with an agent service account adds both gcp capabilities to the built-in capabilities",
			cfg:  Config{GcpProject: "proj", GcpServiceAccountEmail: "agent@proj.iam.gserviceaccount.com"},
			want: []string{"gcp-key", "gemini-key", "github-sandbox", "self-debug", "git-push-any-ref", "bootstrap-playbooks", "grain-ci", "registry-push", "console", "read-issues", "priority-dispatch", "durable", "package-publish", "image-signing", "release-gate-read", "claude-proxy", "openai-proxy", "gemini-proxy"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providers := CapabilityProviders(tc.cfg, nil, NewModelProxies(), nil, nil)
			var got []string
			for _, p := range providers {
				got = append(got, p.Spec().Name)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("capabilityProviders(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}

// reapCapabilities picks what to sweep by type-asserting each registered
// provider to model.Reaper, so a capability minting a resource with no
// native TTL is only actually swept if the provider a real daemon builds
// implements that interface -- an equivalent package-level function is
// not reached, however well it works. gemini-key was exactly that gap
// (grain/task-140): its "clean up after 24 hours if leaked" backstop had
// no caller outside a test, so a key minted for a run whose controller
// died before the lease was written was never deleted by anything.
//
// The three named here each mint a bearer resource GCP or GitHub will
// never expire on its own; a future MINT provider whose resource does
// expire by itself needs no Reap, which is why this lists names rather
// than asserting over every model.ProvisionMint spec.
func TestEveryLeakableCapabilityIsReaped(t *testing.T) {
	t.Parallel()
	cfg := Config{GcpProject: "proj", GcpServiceAccountEmail: "agent@proj.iam.gserviceaccount.com"}
	reaped := map[string]bool{}
	for _, p := range CapabilityProviders(cfg, nil, NewModelProxies(), nil, nil) {
		if _, ok := p.(model.Reaper); ok {
			reaped[p.Spec().Name] = true
		}
	}
	for _, name := range []string{"gcp-key", "gemini-key", "github-sandbox"} {
		if !reaped[name] {
			t.Errorf("capability %q is registered but does not implement model.Reaper: "+
				"reapCapabilities skips it entirely, so a resource it minted for a run whose record was "+
				"lost outlives its 24-hour bound forever", name)
		}
	}
}

// gitHubCredentialLadder is what run() reads the deployment's named
// tokens from, once, before either half of the process is up -- the
// ladder's own directory under -data-dir, the same place the default
// token lives. newLiveConfig takes the ladder itself and reads the names
// off it, so this covers the pair together.
func TestGitHubCredentialLadderNamesTheExtraTokens(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	githubDir := filepath.Join(dataDir, "secrets", "github")
	if err := os.MkdirAll(githubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bot", "release-bot"} {
		if err := os.WriteFile(filepath.Join(githubDir, name+".token"), []byte("tok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ladder, err := GitHubCredentialLadder(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	got := NewLiveConfig(nil, nil, Config{}, ladder).GitHubTokens()
	// Every stored token is one a task may name, whichever connections
	// also name it.
	if want := []string{"bot", "release-bot"}; !slices.Equal(got, want) {
		t.Fatalf("named tokens = %v, want %v", got, want)
	}

	// A deployment that has never had a GitHub credential configured is
	// not an error here -- it simply offers no named tokens.
	empty, err := GitHubCredentialLadder(t.TempDir())
	if err != nil {
		t.Fatalf("gitHubCredentialLadder on an empty data dir: %v", err)
	}
	if got := NewLiveConfig(nil, nil, Config{}, empty).GitHubTokens(); len(got) != 0 {
		t.Fatalf("named tokens on an empty data dir = %v, want none", got)
	}
}

// A named GitHub token is a capability of its own (grain/task-117), and
// the two ends of that -- the picker row a human ticks and the provider
// a dispatch resolves it against -- are built from the same list of
// names in startUIServer and capabilityProviders. This is that pairing:
// the ids have to match exactly, or granting a token would be refused at
// dispatch as a capability no provider is registered for.
func TestCapabilityProvidersRegistersEachNamedGitHubToken(t *testing.T) {
	t.Parallel()
	names := []string{"release-bot", "docs-bot"}
	var registered []string
	for _, p := range CapabilityProviders(Config{}, nil, NewModelProxies(), names, nil) {
		registered = append(registered, p.Spec().Name)
	}
	for _, capability := range ui.GitHubTokenCapabilities(names) {
		if !slices.Contains(registered, capability.ID) {
			t.Errorf("the picker offers %q but no provider is registered for it (registered: %v)",
				capability.ID, registered)
		}
	}
	// And nothing extra: a deployment with no named tokens registers
	// exactly what it did before this existed (TestCapabilityProviders).
	if len(registered) != len(CapabilityProviders(Config{}, nil, NewModelProxies(), nil, nil))+len(names) {
		t.Errorf("registered %v, want the fixed set plus one provider per name %v", registered, names)
	}
}

// Every provider this deployment registers has to have a
// ui.OfferedCapabilities row, and every row has to have a provider --
// the two hand-maintained lists whose drift left gcp-key and
// github-sandbox with providers no task could ever reach, and
// "scratch-repo" with a row no provider answered to. pkg/ui's own
// TestOfferedCapabilitiesCoversEveryShippedCapability ties that listing
// to the catalog of providers grain ships; this ties it to the registry
// a real daemon actually builds, which is the pair the gap was in.
//
// The config is fully populated on purpose: capabilityProviders gates
// gcp-key and gemini-key on it (TestCapabilityProviders above covers
// the gated cases), and gating one off is a deployment being
// unconfigured, not the picker and the registry disagreeing.
func TestEveryRegisteredCapabilityIsGrantable(t *testing.T) {
	t.Parallel()
	cfg := Config{GcpProject: "proj", GcpServiceAccountEmail: "agent@proj.iam.gserviceaccount.com"}
	var registered []string
	ciOnly := map[string]bool{}
	for _, p := range CapabilityProviders(cfg, nil, NewModelProxies(), nil, nil) {
		registered = append(registered, p.Spec().Name)
		ciOnly[p.Spec().Name] = p.Spec().CIOnly
	}
	var offered []string
	for _, c := range ui.OfferedCapabilities() {
		offered = append(offered, c.ID)
	}
	for _, name := range registered {
		// Except a CI-only one (model.CapabilitySpec.CIOnly): it is
		// registered because a repository's CI jobs are granted it, and
		// deliberately not offered to tasks -- grain/task-382, and
		// ui.TestCIOnlyCapabilitiesAreNotOfferedToTasks is the half of
		// this pair that holds it to that.
		if ciOnly[name] {
			continue
		}
		// And except grain-ci, which grain grants and nobody attaches.
		// It is registered because tasks hold it -- every task on a
		// repository whose CI Mode is "Use Grain CI", and grain's own
		// review templates -- but the repository's CI Mode is what
		// grants it, so a hand-attached grain-ci was accepted and then
		// did nothing. It is refused by name instead (grainci.Refusal;
		// task 2015, after 6172e1205 removed an uncommented exclusion
		// here). TestGrainCIIsNotOfferedAndIsRefusedByName in pkg/ui
		// is the half of this that holds it to that.
		if name == grainci.CapabilityName {
			continue
		}
		if !slices.Contains(offered, name) {
			t.Errorf("capability %q is registered but ui.OfferedCapabilities does not offer it: "+
				"every attempt to attach it -- from the UI's picker or `grain create -capability` -- is rejected as "+
				"\"unknown capability\", so no task can be granted it and model.ResolveGrants is never reached", name)
		}
	}
	for _, id := range offered {
		if !slices.Contains(registered, id) {
			t.Errorf("ui.OfferedCapabilities offers %q but a fully configured deployment registers no provider for it: "+
				"a task granted it is refused at dispatch (\"no provider is registered for capability\")", id)
		}
	}
}

func TestOpenStore(t *testing.T) {
	t.Parallel()
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()
	if store == nil {
		t.Fatal("openStore returned a nil store alongside a nil error")
	}
	// A fresh store has applied its schema, not merely opened a
	// connection: any read against it -- Ready is the one loop.Cycle
	// itself calls every tick -- must succeed rather than error on a
	// missing table.
	if _, err := store.Ready(context.Background()); err != nil {
		t.Fatalf("Ready against a freshly opened store: %v", err)
	}
}

// TestOpenStorePersistsAcrossReopen simulates a graind restart: the same
// -data-dir opened a second time, after the first connection closed,
// must still see what the first one wrote -- the same openStore call
// exercised a second time against a directory that already has a
// database, which is the shape every real restart takes and
// TestOpenStore's fresh-directory case alone does not cover.
func TestOpenStorePersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	store1, db1, err := OpenStore("sqlite", "", dir)
	if err != nil {
		t.Fatalf("openStore (first): %v", err)
	}
	task := model.Task{
		ID:      "restart-check",
		Intent:  model.IntentImplement,
		Origin:  model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}}, Reason: model.ReasonDirect},
		Binding: model.BindingDirective,
		Target:  &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	if err := store1.PutTask(context.Background(), task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("closing the first connection: %v", err)
	}

	store2, db2, err := OpenStore("sqlite", "", dir)
	if err != nil {
		t.Fatalf("openStore (second, same dir): %v", err)
	}
	defer db2.Close()
	got, err := store2.GetTask(context.Background(), "restart-check")
	if err != nil {
		t.Fatalf("GetTask after reopen: %v", err)
	}
	if got == nil {
		t.Fatal("task written before the restart did not survive reopening the same -data-dir")
	}
}

// TestLoadConfigSeedsAFreshStoreFromFlags is the first-run case: nothing
// has written grain_config yet, so loadConfig both returns the flags
// untouched and writes them as the seed a UI or a CLI would read next.
func TestLoadConfigSeedsAFreshStoreFromFlags(t *testing.T) {
	t.Parallel()
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	flagCfg := Config{
		MaxWorkers: 2, PollInterval: time.Minute,
		// agentFramework is named rather than left zero because it is
		// the one field that never round-trips verbatim: the store
		// normalizes it on read (model.NormalizeAgentFramework), so an
		// empty flag comes back as AgentFrameworkAntigravity.
		AgentFramework: model.AgentFrameworkAntigravity,
		GeminiModel:    "gemini-2.5-pro", MaxAgentTurns: 10,
		GithubHost: "github.example.com", GithubInsecureHTTP: true,
		GcpProject: "proj", GcpServiceAccountEmail: "agent@proj.iam.gserviceaccount.com",
	}
	got, err := LoadConfig(ctx, store, flagCfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !reflect.DeepEqual(got, flagCfg) {
		t.Fatalf("loadConfig on a fresh store = %+v, want the flags unchanged: %+v", got, flagCfg)
	}

	stored, err := store.GetConfig(ctx)
	if err != nil || stored == nil {
		t.Fatalf("GetConfig after seeding: (%+v, %v)", stored, err)
	}
	if !reflect.DeepEqual(*stored, flagCfg.ToModelConfig()) {
		t.Fatalf("seeded config = %+v, want %+v", *stored, flagCfg.ToModelConfig())
	}
}

// TestLoadConfigSeedsTheTaskDefaultsOn covers the two settings the seed
// above has no flag for: ApprovedByDefault and AutoMergeByDefault default
// on (model.DefaultConfig), and the row loadConfig writes binds every
// column, so seeding from flags that say nothing about either has to
// store what a deployment that has never chosen them runs rather than the
// Go zero value of the field.
func TestLoadConfigSeedsTheTaskDefaultsOn(t *testing.T) {
	t.Parallel()
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	flagCfg := Config{
		MaxWorkers: 2, PollInterval: time.Minute,
		AgentFramework: model.AgentFrameworkAntigravity,
		GeminiModel:    "gemini-2.5-pro", GithubHost: "github.com",
	}
	if _, err := LoadConfig(ctx, store, flagCfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	stored, err := store.GetConfig(ctx)
	if err != nil || stored == nil {
		t.Fatalf("GetConfig after seeding: (%+v, %v)", stored, err)
	}
	if !stored.ApprovedByDefault || !stored.AutoMergeByDefault {
		t.Fatalf("seeded ApprovedByDefault/AutoMergeByDefault = %v/%v, want true/true",
			stored.ApprovedByDefault, stored.AutoMergeByDefault)
	}
}

// TestLoadConfigPrefersTheStoreOverFlagsOnceOneExists is the restart
// case: a UI or a CLI wrote a config through the store, and the next
// start must run with that rather than whatever the flags on this
// particular invocation happen to say.
func TestLoadConfigPrefersTheStoreOverFlagsOnceOneExists(t *testing.T) {
	t.Parallel()
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	stored := model.Config{
		MaxWorkers: 1, PollInterval: 5 * time.Second,
		// Named for the same reason as in the test above: GetConfig
		// normalizes this field, so a stored "" reads back as
		// AgentFrameworkAntigravity and would not compare equal.
		MaxAgentTurns: 99,
		GitHubHost:    "github.com", GitHubInsecureHTTP: false,
		GCPProject: "stored-proj", GCPServiceAccountEmail: "stored@stored-proj.iam.gserviceaccount.com",
	}
	if err := store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	flagCfg := Config{
		DataDir:    "/should/be/left/alone",
		MaxWorkers: 4, PollInterval: time.Hour,
		GeminiModel: "ignored", MaxAgentTurns: -1,
		GithubHost: "ignored.example.com", GithubInsecureHTTP: true,
		GcpProject: "ignored-proj", GcpServiceAccountEmail: "ignored@example.com",
	}
	got, err := LoadConfig(ctx, store, flagCfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !reflect.DeepEqual(got, flagCfg.WithModelConfig(stored)) {
		t.Fatalf("loadConfig with a stored config = %+v, want the stored fields applied: %+v",
			got, flagCfg.WithModelConfig(stored))
	}
	// -data-dir is not a grain_config field at all -- loadConfig must
	// leave it exactly as the flags set it, on both branches.
	if got.DataDir != flagCfg.DataDir {
		t.Fatalf("loadConfig changed dataDir to %q, want it left alone", got.DataDir)
	}
}

// TestLoadConfigLogsEveryOverriddenFlag is bwsalmon/agents#574: the
// store-wins behavior TestLoadConfigPrefersTheStoreOverFlagsOnceOneExists
// covers used to be silent, which cost real debugging time chasing a flag
// that looked like it had no effect. Every seedOnly field the flags and
// the store disagree on must get its own log line; fields that agree must
// not.
func TestLoadConfigLogsEveryOverriddenFlag(t *testing.T) {
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	stored := model.Config{
		MaxWorkers: 1, PollInterval: 5 * time.Second,
		// Named for the same reason as in the test above: GetConfig
		// normalizes this field, so a stored "" reads back as
		// AgentFrameworkAntigravity and would not compare equal.
		MaxAgentTurns: 99,
		GitHubHost:    "github.com", GitHubInsecureHTTP: false,
		GCPProject: "stored-proj", GCPServiceAccountEmail: "stored@stored-proj.iam.gserviceaccount.com",
	}
	if err := store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	// Agrees with the store on everything except -github-host and
	// -poll-interval, so only those two should be logged.
	flagCfg := Config{
		PollInterval:  stored.PollInterval + time.Minute,
		MaxAgentTurns: stored.MaxAgentTurns,
		GithubHost:    "ignored.example.com", GithubInsecureHTTP: stored.GitHubInsecureHTTP,
		GcpProject: stored.GCPProject, GcpServiceAccountEmail: stored.GCPServiceAccountEmail,
	}

	var logs strings.Builder
	prevLog := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prevLog)

	if _, err := LoadConfig(ctx, store, flagCfg); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	got := logs.String()
	for _, want := range []string{
		"ignoring -poll-interval=1m5s, stored config already has 5s",
		"ignoring -github-host=ignored.example.com, stored config already has github.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("loadConfig log output = %q, want it to contain %q", got, want)
		}
	}
	for _, unwanted := range []string{"-gemini-model", "-claude-model", "-max-agent-turns", "-github-insecure-http", "-gcp-project", "-gcp-agent-service-account", "-target-repos"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("loadConfig log output = %q, should not mention %q since the flag and the store agree", got, unwanted)
		}
	}
}

func TestStartGitProxyServesAndStops(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()

	url, _, stop, err := StartGitProxy(dataDir, store, "example.com", false, NewDirect(""), nil)
	if err != nil {
		t.Fatalf("startGitProxy: %v", err)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("startGitProxy URL = %q, want an http://127.0.0.1:<port> loopback address", url)
	}

	resp, err := http.Get(url + "/owner/repo.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatalf("the git proxy did not answer at all: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if err := stop(context.Background()); err != nil {
		t.Fatalf("stopping the git proxy: %v", err)
	}
	if _, err := http.Get(url); err == nil {
		t.Fatal("expected the git proxy to stop accepting connections once stopped")
	}
}

// TestStartGitProxyAdvertisesHostOverLoopback covers bwsalmon/agents#567:
// a granule VM's guest has its own loopback, unrelated to this process's, so
// -granule-git-proxy-host asks startGitProxy to bind every interface instead
// of just loopback and hand back a URL naming that host (here, loopback
// again, standing in for a real granule deployment's docker bridge gateway
// address) rather than whatever it actually bound to.
func TestStartGitProxyAdvertisesHostOverLoopback(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()

	url, _, stop, err := StartGitProxy(dataDir, store, "example.com", false, NewDirect("127.0.0.1"), nil)
	if err != nil {
		t.Fatalf("startGitProxy: %v", err)
	}
	defer stop(context.Background())
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("startGitProxy URL = %q, want an http://127.0.0.1:<port> URL naming advertiseHost", url)
	}

	resp, err := http.Get(url + "/owner/repo.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatalf("the git proxy did not answer at the advertised host: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// The System pane's three power buttons (grain/task-40): each is a
// command line this deployment was given, and each has to run *its own*
// -- powering the host off when an operator asked for a service restart
// is the mix-up worth catching here, and the wiring that could cause it
// is three near-identical lines in startUIServer.
//
// The defaults are asserted as data rather than run: `sudo systemctl
// poweroff` is not a thing a test may execute, and what is worth pinning
// down about them is only that they name three different actions.
func TestPowerCommandsEachRunTheirOwnArgv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tc := range []struct {
		name  string
		build func([]string) func(context.Context) error
	}{
		{"reboot", rebootHost},
		{"shutdown", shutdownHost},
		{"restart", restartDaemon},
	} {
		marker := filepath.Join(dir, tc.name)
		if err := tc.build([]string{"touch", marker})(context.Background()); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("%s ran something else: %v", tc.name, err)
		}
	}

	// And the fallback, since a deployment that passes no flag at all
	// (anything not built by scripts/setup.sh) gets the default rather
	// than a button that runs nothing.
	fallback := filepath.Join(dir, "fallback")
	if err := hostCommand(nil, []string{"touch", fallback})(context.Background()); err != nil {
		t.Fatalf("hostCommand fallback: %v", err)
	}
	if _, err := os.Stat(fallback); err != nil {
		t.Fatalf("hostCommand did not fall back to the default: %v", err)
	}

	defaults := map[string][]string{
		"reboot":   defaultRebootCmd,
		"shutdown": defaultShutdownCmd,
		"restart":  defaultRestartCmd,
	}
	seen := map[string]string{}
	for name, argv := range defaults {
		line := strings.Join(argv, " ")
		if other, dup := seen[line]; dup {
			t.Fatalf("%s and %s both default to %q", name, other, line)
		}
		seen[line] = name
	}
}
