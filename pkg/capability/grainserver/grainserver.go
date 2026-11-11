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

// Package grainserver holds grain's in-process capability servers
// (design/design/unified-grants.design.md §1, §4), answered by grain itself rather
// than by an external MCP server subprocess:
//
//   - https://grain.dev/capabilities/grain: the host- and controller-side
//     diagnostic, source and database grants.
//   - https://grain.dev/capabilities/repo: repository access. #read {repo}
//     and #write {repo} are a CI job's access to another repository
//     (formerly repo-read:<repo> and repo-write:<repo>), CI-only and
//     event-gated by the reference itself (model.IsRepoAccessGrant,
//     model.RepoAccessAdmitsCIEvent). #write's other value,
//     GitPushAnyRefGrant (formerly git-push-any-ref), the git proxy reads
//     off the live task (model.Store.GitPushAnyRef). #credential {credential}
//     selects a named git credential under secrets/github for a task
//     (formerly github-credential:<name>).
//     #target {repo, base}, and #read {repo} / #write {repo, base} with a
//     base, are a task's own repository, Reads and Writes read as grants
//     (model.TaskRepoGrants); a task's storage stays its repo fields, and
//     an API request's grants: list is sorted back into them
//     (model.SplitRequestGrants).
//   - https://grain.dev/capabilities/api: grain's own agent API, one
//     method per scope, preset and scope set of docs/agent-api.md, each
//     with an optional {repo} (formerly api:<scope>[@owner/name],
//     model.CanonicalGrant). Nothing is minted: the grant is served as
//     the run's api_* tools.
//   - https://grain.dev/capabilities/github: GitHub operations answered by
//     grain itself: reading issues and comments with the run's team git
//     credential (#read-issues, formerly read-issues), and provisioning a
//     private single-use sandbox repository via the deployment's GitHub App
//     (#sandbox-repo, formerly github-sandbox).
package grainserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/capability/githubtoken"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// ID is the canonical capability_id of the in-process grain server.
	ID = model.GrainCapabilityServerID

	// JournalMethod lets a task read grain's redacted host journal (formerly grain-log).
	JournalMethod = "journal"
	JournalRef    = model.GrainJournalRef
	JournalGrant  = model.GrainJournalGrant

	// LegacyGrainLog is the retired in-process capability name for JournalGrant.
	LegacyGrainLog = "grain-log"

	// HealthMethod lets a task read host/sandbox probes and local state snapshot (formerly deployment-health).
	HealthMethod = "health"
	HealthRef    = model.GrainHealthRef
	HealthGrant  = model.GrainHealthGrant

	// LegacyDeploymentHealth is the retired in-process capability name for api:deployment:read + HealthGrant.
	LegacyDeploymentHealth = "deployment-health"

	// SQLMethod lets a task run SQL against Grain's database (formerly db-read / db-write).
	SQLMethod     = model.GrainSQLMethod
	SQLRef        = model.GrainSQLRef
	SQLReadGrant  = model.GrainSQLReadGrant
	SQLWriteGrant = model.GrainSQLWriteGrant

	// HostExecMethod lets a task run shell commands on grain's own host,
	// each confirmed by a live reply in its chat (formerly self-repair).
	// Only a person may grant it (model's RequirePersonGrant).
	HostExecMethod = model.GrainHostExecMethod
	HostExecRef    = model.GrainHostExecRef
	HostExecGrant  = model.GrainHostExecGrant

	// LegacySelfRepair is the retired in-process capability name for HostExecGrant.
	LegacySelfRepair = "self-repair"

	// CIMethod puts the branches a task pushes through the repository's
	// own .grain/workflows and holds the merge queue on the verdict
	// (formerly grain-ci). grain grants it from a repository's CI Mode;
	// nobody attaches it by hand (grainci.Refusal).
	CIMethod = model.GrainCIMethod
	CIRef    = model.GrainCIRef
	CIGrant  = model.GrainCIGrant

	// LegacyGrainCI is the retired in-process capability name for CIGrant.
	LegacyGrainCI = model.GrainCICapability

	// SourceMethod lets a task read grain's own baked-in source checkout
	// (grain-src/ in its sandbox, src:// and src://core/... through the
	// sandbox CLI) -- formerly the source half of self-debug, whose REST
	// half is the api:self-debug preset. It is team-scoped
	// (admincaps.TeamScoped): held deployment-wide, it also reads every
	// repository's CI and the whole of config://.
	SourceMethod = model.GrainSourceMethod
	SourceRef    = model.GrainSourceRef
	SourceGrant  = model.GrainSourceGrant

	// LegacySelfDebug is the retired in-process capability name for
	// api:self-debug + SourceGrant.
	LegacySelfDebug = model.SelfDebugCapability

	// RepoID is the canonical capability_id of the in-process repo server.
	RepoID = model.RepoCapabilityServerID

	// RepoReadMethod is a CI job's read access to another repository
	// (formerly repo-read:<repo>).
	RepoReadMethod = model.RepoReadMethod
	RepoReadRef    = model.RepoReadRef

	// RepoWriteMethod is a CI job's write access to another repository
	// (formerly repo-write:<repo>), or widens which refs of the
	// repositories a task may write its run may push (formerly
	// git-push-any-ref).
	RepoWriteMethod    = model.RepoWriteMethod
	RepoWriteRef       = model.RepoWriteRef
	GitPushAnyRefGrant = model.GitPushAnyRefGrant

	// LegacyGitPushAnyRef is the retired in-process capability name for GitPushAnyRefGrant.
	LegacyGitPushAnyRef = model.GitPushAnyRefCapability

	// GitHubID is the canonical capability_id of the in-process github server.
	GitHubID = model.GitHubCapabilityServerID

	// ReadIssuesMethod lets a task read GitHub issues and comments on the
	// repositories its run may reach (formerly read-issues).
	ReadIssuesMethod = model.GitHubReadIssuesMethod
	ReadIssuesRef    = model.GitHubReadIssuesRef
	ReadIssuesGrant  = model.GitHubReadIssuesGrant

	// LegacyReadIssues is the retired in-process capability name for ReadIssuesGrant.
	LegacyReadIssues = "read-issues"

	// SandboxRepoMethod provisions a private, single-use GitHub sandbox
	// repository for a run via this deployment's GitHub App (formerly github-sandbox).
	SandboxRepoMethod   = model.GitHubSandboxRepoMethod
	SandboxRepoRef      = model.GitHubSandboxRepoRef
	SandboxRepoGrant    = model.GitHubSandboxRepoGrant
	LegacyGitHubSandbox = model.LegacyGitHubSandboxCapability

	// RepoCredentialMethod selects a named git credential under secrets/github
	// for a task's git proxy, PR opener, and git identity (formerly github-credential:<name>).
	RepoCredentialMethod = model.RepoCredentialMethod
	RepoCredentialRef    = model.RepoCredentialRef

	// RepoTargetMethod is a task's own repository and base branch, read
	// as a grant (formerly, and still stored as, its repo and base fields).
	RepoTargetMethod = model.RepoTargetMethod
	RepoTargetRef    = model.RepoTargetRef

	// APIID is the canonical capability_id of the in-process agent API server.
	APIID = model.APICapabilityServerID
)

var (
	serverOnce sync.Once
	serverInst *protocol.Server

	repoMu            sync.RWMutex
	repoCredentialsFn func() []string

	githubOnce    sync.Once
	githubInst    *protocol.Server
	configMapOnce sync.Once
	configMapInst *protocol.Server
)

// RepoCredentialGrant returns the canonical repo#credential grant for name.
func RepoCredentialGrant(name string) string { return model.RepoCredentialGrant(name) }

// ParseRepoCredentialGrant parses a canonical repo#credential grant.
func ParseRepoCredentialGrant(grant string) (string, bool) {
	return model.ParseRepoCredentialGrant(grant)
}

// ConfigureRepoCredentials sets the callback returning the deployment's
// configured named git credentials under secrets/github (empty non-nil slice
// when a credential directory is configured with no named credentials; nil when
// unconfigured). It returns a restore function for tests.
func ConfigureRepoCredentials(fn func() []string) func() {
	repoMu.Lock()
	prev := repoCredentialsFn
	repoCredentialsFn = fn
	repoMu.Unlock()
	return func() {
		repoMu.Lock()
		repoCredentialsFn = prev
		repoMu.Unlock()
	}
}

func configuredRepoCredentials() []string {
	repoMu.RLock()
	fn := repoCredentialsFn
	repoMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// callerClock is the in-process methods' clock. An external server
// holds a pass's deadline to its own clock; this one runs inside grain,
// whose orchestrator set that deadline from the clock it runs on (a test
// world's, say), so there is no second clock to hold it to.
func callerClock() time.Time { return time.Time{} }

func buildServer() *protocol.Server {
	journal, err := protocol.NewMethod(protocol.MethodSpec{
		Name: JournalMethod,
		Description: "Let this task read a bounded slice of grain's own systemd journal on this " +
			"host -- the daemon's log and the kernel's -- with known credentials redacted before " +
			"it leaves. Separate from self-debug on purpose: the journal carries whatever anything " +
			"here has ever logged, which is far more than grain's source code.",
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", JournalMethod, err))
	}
	health, err := protocol.NewMethod(protocol.MethodSpec{
		Name: HealthMethod,
		Description: "Read bounded controller-side host RAM, swap, disks and load, live sandbox/VM " +
			"counts and per-sandbox resource readings, and the local synchronized deployment-state " +
			"snapshot, without host shell or sandbox network access.",
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", HealthMethod, err))
	}
	sqlMethod, err := protocol.NewMethod(protocol.MethodSpec{
		Name: SQLMethod,
		Description: "Execute SQL against Grain's database: read-only queries by default " +
			"(write: false), or read and write statements when write: true (dangerous).",
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"write": map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "Allow write statements (INSERT, UPDATE, DELETE, DDL) as well as read-only SQL queries.",
				},
			},
		},
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", SQLMethod, err))
	}
	hostExec, err := protocol.NewMethod(protocol.MethodSpec{
		Name: HostExecMethod,
		Description: "Let this task run commands on grain's own host -- restart services, edit config, " +
			"call the grain CLI -- each one needing a live reply in the task's chat before it runs " +
			"(dangerous: only a person may grant it, and only an interactive task gets the tool).",
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", HostExecMethod, err))
	}
	ciMethod, err := protocol.NewMethod(protocol.MethodSpec{
		Name: CIMethod,
		Description: "Run this repository's own .grain/workflows in grain's sandboxes for the branches " +
			"this task pushes, and hold the merge queue until they pass. grain grants it from the " +
			"repository's CI Mode (\"Use Grain CI\"); it is not attached by hand.",
		// Nothing is placed in the sandbox: grain CI and the merge
		// queue read the grant off the task (grainci.Granted).
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", CIMethod, err))
	}
	source, err := protocol.NewMethod(protocol.MethodSpec{
		Name: SourceMethod,
		Description: "Let this task read grain's own source checkout -- the tree this deployment runs, " +
			"placed read-only at grain-src/ in the sandbox and readable as src:// (and src://core/...) " +
			"through the grain CLI -- and, held deployment-wide, every repository's CI and the whole of " +
			"config://, to help it debug or explain grain's own behavior. Read-only. Task records, metrics " +
			"and deployment status are the api:self-debug preset (formerly, with this, self-debug).",
		// Nothing is placed by the mint: the orchestrator places
		// grain-src/ for a task holding the grant (placeGrainSource),
		// and the namespace reads it off the run's grants.
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", SourceMethod, err))
	}
	cs, err := protocol.NewCapabilityServer("grain", journal, health, sqlMethod, hostExec, ciMethod, source)
	if err != nil {
		panic(fmt.Sprintf("grainserver: building capability server: %v", err))
	}
	srv, err := protocol.NewServer(context.Background(), ID, cs.InProcessCaller())
	if err != nil {
		panic(fmt.Sprintf("grainserver: attaching in-process server: %v", err))
	}
	return srv
}

// repoArg is the schema of the one repository a #read or #write grant
// of CI access names: owner/name, or host/owner/name off the default host.
var repoArg = map[string]any{
	"type":        "string",
	"pattern":     `^[^/\s]+(/[^/\s]+){1,2}$`,
	"description": "The repository, owner/name (or host/owner/name), the CI job may reach through the git proxy.",
}

// taskRepoArgs is the schema of a task's own repository grant: one
// repository and the base branch in it, "" for its default branch.
func taskRepoArgs() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"repo", "base"},
		"properties": map[string]any{
			"repo": map[string]any{
				"type":        "string",
				"pattern":     repoArg["pattern"],
				"description": "The repository, owner/name (or host/owner/name).",
			},
			"base": map[string]any{
				"type":        "string",
				"description": "The base branch work lands on; empty for the repository's default branch.",
			},
		},
	}
}

// apiArgs is the schema of an api grant: an optional repository it is
// limited to (formerly the @owner/name of api:<scope>@owner/name).
func apiArgs() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"repo": map[string]any{
				"type":        "string",
				"pattern":     repoArg["pattern"],
				"description": "Limit the grant to this repository, owner/name; omit for every repository.",
			},
		},
	}
}

// buildAPIServer is the in-process https://grain.dev/capabilities/api
// server: a method for every scope (model.APIScopes), preset
// (model.APIPresets) and scope set (model.APIScopeSetsInEffect) of
// docs/agent-api.md. A dangerous scope or person-only set is said so in
// its description; who may grant one is model.IsDangerousGrant's, which
// reads the reference through model.CanonicalGrant.
func buildAPIServer() *protocol.Server {
	mint := func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
		return &protocol.MintResponse{Status: protocol.StatusOK}, nil
	}
	var methods []*protocol.Method
	added := map[string]bool{}
	add := func(name, description string) {
		if added[name] || !protocol.ValidMethod(name) {
			return
		}
		added[name] = true
		m, err := protocol.NewMethod(protocol.MethodSpec{
			Name:        name,
			Description: description,
			Args:        apiArgs(),
			// Nothing is placed in the sandbox: the run is served the
			// api_* tools its api grants reach (model.APIGrantsOf).
			Mint: mint,
			Now:  callerClock,
		})
		if err != nil {
			panic(fmt.Sprintf("grainserver: building api#%s: %v", name, err))
		}
		methods = append(methods, m)
	}
	for _, s := range model.APIScopes() {
		desc := s.Description
		if s.Dangerous {
			desc += " (dangerous: only a person may grant it, and every call carries a reason)"
		}
		add(string(s.Scope), desc)
	}
	for _, p := range model.APIPresets() {
		scopes := make([]string, len(p.Scopes))
		for i, s := range p.Scopes {
			scopes[i] = string(s)
		}
		add(p.Name, p.Description+". Preset of: "+strings.Join(scopes, ", ")+".")
	}
	for _, set := range model.APIScopeSetsInEffect() {
		scopes := make([]string, len(set.Resolved))
		for i, s := range set.Resolved {
			scopes[i] = string(s)
		}
		desc := set.Description
		if desc == "" {
			desc = "API scope set"
		}
		desc += ". Scope set of: " + strings.Join(scopes, ", ") + "."
		if set.Person {
			desc += " (dangerous: only a person may grant it)"
		}
		add(set.Name, desc)
	}
	cs, err := protocol.NewCapabilityServer("api", methods...)
	if err != nil {
		panic(fmt.Sprintf("grainserver: building api capability server: %v", err))
	}
	srv, err := protocol.NewServer(context.Background(), APIID, cs.InProcessCaller())
	if err != nil {
		panic(fmt.Sprintf("grainserver: attaching in-process api server: %v", err))
	}
	return srv
}

// APIServer returns the in-process https://grain.dev/capabilities/api
// server reflecting the currently installed scope sets as well as core's
// scopes and presets.
func APIServer() *protocol.Server {
	return buildAPIServer()
}

// APIStatus is Status for the in-process api server.
func APIStatus() servers.Status { return statusOf(APIID, APIServer()) }

// titled is schema with a title, for a oneOf variant to be offered by.
func titled(schema map[string]any, title string) map[string]any {
	schema["title"] = title
	return schema
}

// repoAccessArgs is the schema of a grant naming one repository.
func repoAccessArgs() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"repo"},
		"properties":           map[string]any{"repo": repoArg},
	}
}

func validSortedCredentialNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !githubtoken.ValidName(n) || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func repoCredentialArgs(configured []string) (map[string]any, []string) {
	prop := map[string]any{
		"type":        "string",
		"pattern":     githubtoken.NamePattern,
		"not":         map[string]any{"const": "anonymous"},
		"description": "Named GitHub credential under secrets/github (<name>.token, <name>.app.json, or <name>.ssh.json) to authenticate this task's git operations with.",
	}
	var valid []string
	if configured != nil {
		valid = validSortedCredentialNames(configured)
		enum := make([]any, len(valid))
		for i, n := range valid {
			enum[i] = n
		}
		prop["enum"] = enum
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"credential"},
		"properties":           map[string]any{"credential": prop},
	}, valid
}

func buildRepoServer() *protocol.Server {
	return buildRepoServerFor(configuredRepoCredentials())
}

// RepoServerForCredentials builds an in-process https://grain.dev/capabilities/repo
// server validating #credential against the given configured credential names
// (when non-nil).
func RepoServerForCredentials(configured []string) *protocol.Server {
	return buildRepoServerFor(configured)
}

func buildRepoServerFor(configured []string) *protocol.Server {
	read, err := protocol.NewMethod(protocol.MethodSpec{
		Name: RepoReadMethod,
		Description: "Let a CI job fetch one other repository, read-only, through the git proxy with the " +
			"deployment's own git credential (formerly repo-read:<repo>). Held as a capability it is CI only: " +
			"a task is refused it. In a task's grants: list it is one of the task's read-only repositories " +
			"(still stored as its reads).",
		Args: repoAccessArgs(),
		// Nothing is placed in the sandbox: the git proxy's read scope
		// is widened to the repository for the job's sandbox
		// (daemon's CiSandbox.grantRepoAccess).
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building repo#%s: %v", RepoReadMethod, err))
	}
	write, err := protocol.NewMethod(protocol.MethodSpec{
		Name: RepoWriteMethod,
		Description: "Either {repo, base}: one more repository and base branch this task may land work in " +
			"and file proposals for (a write target; still stored as the task's writes). Or {repo}: let a CI " +
			"job push to one other repository through the git proxy, on a " +
			"release, a workflow_dispatch or grain's own scheduled run only (formerly repo-write:<repo>; CI " +
			"only). Or {refs, force}: let this task's run push any branch or tag of the repositories it may " +
			"write -- the default branch, force-pushes and deletions included -- rather than only its own " +
			"task branch, with the team's own git credential (formerly git-push-any-ref). Standing jobs " +
			"declare the refs they push under `push:` in their .grain template instead.",
		Args: map[string]any{
			"title": "Kind of write",
			// Each variant's title is what the UI's variant picker offers.
			"oneOf": []any{titled(repoAccessArgs(), "CI push to another repo"), titled(taskRepoArgs(), "Write target (repo + base)"), map[string]any{
				"title":                "Push any branch/tag (refs + force)",
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"refs", "force"},
				"properties": map[string]any{
					"refs": map[string]any{
						"type":        "array",
						"description": "Ref patterns the run may update beyond its own task branch. Only every branch and every tag together is honoured so far.",
						"items":       map[string]any{"type": "string", "enum": []any{"refs/heads/*", "refs/tags/*"}},
						"minItems":    2,
						"maxItems":    2,
						"uniqueItems": true,
					},
					"force": map[string]any{
						"type":        "boolean",
						"const":       true,
						"description": "Allow force-pushes and deletions of those refs as well as fast-forwards.",
					},
				},
			}},
		},
		// Nothing is placed in the sandbox: the git proxy reads the grant
		// off the live task when a push names another ref, or has its
		// write scope widened for a CI job's sandbox.
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building repo#%s: %v", RepoWriteMethod, err))
	}
	credArgs, validCreds := repoCredentialArgs(configured)
	hasConfiguredList := configured != nil
	credentialMethod, err := protocol.NewMethod(protocol.MethodSpec{
		Name: RepoCredentialMethod,
		Description: "Authenticate this task's git proxy operations, pull request creation, and commit identity " +
			"with the named credential under secrets/github instead of the deployment's default git connection " +
			"(formerly github-credential:<name>).",
		Args: credArgs,
		// Nothing is placed in the sandbox: the git proxy, PR opener, and git
		// identity read the grant off the live task (Store.TaskGitCredentialOn).
		Mint: func(_ context.Context, _ string, _ time.Time, args map[string]any) (*protocol.MintResponse, error) {
			name, _ := args["credential"].(string)
			if !githubtoken.ValidName(name) {
				return &protocol.MintResponse{
					Status: protocol.StatusRefused,
					Reason: fmt.Sprintf("invalid git credential name %q", name),
				}, nil
			}
			if hasConfiguredList && !slices.Contains(validCreds, name) {
				return &protocol.MintResponse{
					Status: protocol.StatusRefused,
					Reason: fmt.Sprintf("git credential %q is not configured under secrets/github", name),
				}, nil
			}
			return &protocol.MintResponse{
				Status:        protocol.StatusOK,
				PromptSection: githubtoken.PromptSection(name),
			}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building repo#%s: %v", RepoCredentialMethod, err))
	}
	target, err := protocol.NewMethod(protocol.MethodSpec{
		Name: RepoTargetMethod,
		Description: "The repository and base branch this task works on: cloned to work/ in its sandbox, " +
			"and where its branch and pull request go (still stored as the task's repo and base).",
		Args: taskRepoArgs(),
		// Nothing is placed by the mint: the orchestrator clones the
		// task's target from the task row, which is where it is stored.
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building repo#%s: %v", RepoTargetMethod, err))
	}
	cs, err := protocol.NewCapabilityServer("repo", read, write, credentialMethod, target)
	if err != nil {
		panic(fmt.Sprintf("grainserver: building repo capability server: %v", err))
	}
	srv, err := protocol.NewServer(context.Background(), RepoID, cs.InProcessCaller())
	if err != nil {
		panic(fmt.Sprintf("grainserver: attaching in-process repo server: %v", err))
	}
	return srv
}

func buildGitHubServer() *protocol.Server {
	readIssues, err := protocol.NewMethod(protocol.MethodSpec{
		Name: ReadIssuesMethod,
		Description: "Let this task read issues and comments from GitHub on the repositories its run may " +
			"reach, read-only, with the team's own git credential (formerly read-issues).",
		// Nothing is placed in the sandbox: the git proxy (/cli/issues) and
		// grain mcpserver (list_issues, read_issue) read the grant off the
		// task and answer with the run's team git credential.
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building github#%s: %v", ReadIssuesMethod, err))
	}
	sandboxRepo, err := protocol.NewMethod(githubsandbox.MethodSpec())
	if err != nil {
		panic(fmt.Sprintf("grainserver: building github#%s: %v", SandboxRepoMethod, err))
	}
	cs, err := protocol.NewCapabilityServer("github", readIssues, sandboxRepo)
	if err != nil {
		panic(fmt.Sprintf("grainserver: building github capability server: %v", err))
	}
	srv, err := protocol.NewServer(context.Background(), GitHubID, cs.InProcessCaller())
	if err != nil {
		panic(fmt.Sprintf("grainserver: attaching in-process github server: %v", err))
	}
	return srv
}

// Server returns the singleton in-process https://grain.dev/capabilities/grain server.
func Server() *protocol.Server {
	serverOnce.Do(func() {
		serverInst = buildServer()
	})
	return serverInst
}

// RepoServer returns the in-process https://grain.dev/capabilities/repo server
// reflecting the currently configured named git credentials (if any).
func RepoServer() *protocol.Server {
	return buildRepoServer()
}

// GitHubServer returns the singleton in-process https://grain.dev/capabilities/github server.
func GitHubServer() *protocol.Server {
	githubOnce.Do(func() {
		githubInst = buildGitHubServer()
	})
	return githubInst
}

// Status reports the in-process grain server's method details in the same shape
// servers.Registry.Statuses returns for external servers, so GET /api/capability-methods
// lists its methods without registering a subprocess in grain_config.mcp_servers.
func Status() servers.Status { return statusOf(ID, Server()) }

// RepoStatus is Status for the in-process repo server.
func RepoStatus() servers.Status { return statusOf(RepoID, RepoServer()) }

// GitHubStatus is Status for the in-process github server.
func GitHubStatus() servers.Status { return statusOf(GitHubID, GitHubServer()) }

// RepoStatusForCredentials is RepoStatus validating #credential against the
// given configured credential names (when non-nil).
func RepoStatusForCredentials(configured []string) servers.Status {
	return statusOf(RepoID, RepoServerForCredentials(configured))
}

// Statuses is every in-process server's Status: core's own, then one for
// each server a build outside core contributed methods to
// (capability.Register), ordered by ID.
func Statuses() []servers.Status {
	out := []servers.Status{Status(), RepoStatus(), GitHubStatus(), APIStatus()}
	for _, id := range capability.MethodServers() {
		srv, ok, err := capability.MethodServer(id)
		if !ok {
			continue
		}
		if err != nil {
			out = append(out, servers.Status{ID: id, State: servers.Failed, Transport: "in-process", Error: err.Error()})
			continue
		}
		out = append(out, statusOf(id, srv))
	}
	return out
}

// IsInProcessID reports whether capabilityID is one of the in-process
// servers: core's own, or one a build contributed methods to
// (capability.Register).
func IsInProcessID(capabilityID string) bool {
	return capabilityID == ID || capabilityID == RepoID || capabilityID == GitHubID || capabilityID == APIID ||
		capability.IsMethodServer(capabilityID)
}

func statusOf(id string, srv *protocol.Server) servers.Status {
	names := make([]string, 0, len(srv.Methods))
	for name := range srv.Methods {
		names = append(names, name)
	}
	sort.Strings(names)
	details := make([]servers.MethodDetail, 0, len(names))
	for _, name := range names {
		m := srv.Methods[name]
		details = append(details, servers.MethodDetail{
			Name:        name,
			Description: m.Description,
			Args:        m.ArgsSchema,
			Releasable:  m.Releasable,
			Delivery:    m.MCPDelivery,
		})
	}
	return servers.Status{
		ID:            id,
		State:         servers.Attached,
		Transport:     "in-process",
		Methods:       names,
		MethodDetails: details,
	}
}

func defaultConfigMapServer() *protocol.Server {
	configMapOnce.Do(func() {
		cm, err := intree.NewEmbeddedBootstrapServer(callerClock)
		if err != nil {
			panic(fmt.Sprintf("grainserver: building default config-map server: %v", err))
		}
		srv, err := protocol.NewServer(context.Background(), model.ConfigMapCapabilityServerID, cm.InProcessCaller())
		if err != nil {
			panic(fmt.Sprintf("grainserver: attaching default config-map server: %v", err))
		}
		configMapInst = srv
	})
	return configMapInst
}

// Lookup wraps fallback so ID, RepoID and GitHubID always resolve to the in-process
// servers, and a server with methods contributed through capability.Register
// to the in-process server built from them, even when fallback is nil or
// comes from a registry that only tracks mcp_servers. When fallback is nil,
// ConfigMapCapabilityServerID (and its legacy skill alias) resolves to an
// unconfigured in-process config-map server so built-in bootstrap playbook
// grants work without a subprocess registry.
func Lookup(fallback protocol.Lookup) protocol.Lookup {
	return LookupWithRepoCredentials(fallback, nil)
}

// LookupWithRepoCredentials is Lookup, overriding the configured #credential
// names on RepoID when repoCredentials is non-nil (otherwise falling back to
// ConfigureRepoCredentials).
func LookupWithRepoCredentials(fallback protocol.Lookup, repoCredentials []string) protocol.Lookup {
	return func(capabilityID string) (*protocol.Server, error) {
		switch capabilityID {
		case ID:
			return buildServer(), nil
		case RepoID:
			if repoCredentials != nil {
				return buildRepoServerFor(repoCredentials), nil
			}
			return buildRepoServer(), nil
		case GitHubID:
			// The one server, not one built afresh: #sandbox-repo's leases
			// are its protocol.Method's own, so a release_ answered by a
			// server built for the release would know of no lease and never
			// delete the repository (capability.MethodServer's doc comment).
			return GitHubServer(), nil
		case APIID:
			return APIServer(), nil
		}
		if srv, ok, err := capability.MethodServer(capabilityID); ok {
			return srv, err
		}
		if fallback != nil {
			return fallback(capabilityID)
		}
		if capabilityID == model.ConfigMapCapabilityServerID || capabilityID == model.SkillCapabilityServerID {
			return defaultConfigMapServer(), nil
		}
		return nil, fmt.Errorf("capability %s is not configured in mcpServers", capabilityID)
	}
}

// IsInProcessGrant reports whether grant is a capability reference targeting
// an in-process server -- core's own, or one a build contributed methods to
// (capability.Register) -- which opens no external control- or data-plane
// network listener.
func IsInProcessGrant(grant string) bool {
	if !model.IsCapabilityRef(grant) {
		return false
	}
	ref, err := protocol.ParseRef(model.CapabilityGrantRef(grant))
	return err == nil && IsInProcessID(ref.CapabilityID)
}

// IsJournalGrant reports whether grant names grain#journal (recorded or bare)
// or the retired bare grain-log alias.
func IsJournalGrant(grant string) bool { return model.IsGrainJournalGrant(grant) }

// HasJournal reports whether any grant in grants opens grain#journal.
func HasJournal(grants []string) bool { return model.HasGrainJournal(grants) }

// IsHealthGrant reports whether grant names grain#health (recorded or bare)
// or the retired bare deployment-health alias.
func IsHealthGrant(grant string) bool { return model.IsGrainHealthGrant(grant) }

// HasHealth reports whether any grant in grants opens grain#health.
func HasHealth(grants []string) bool { return model.HasGrainHealth(grants) }

// IsSQLGrant reports whether grant names grain#sql (read or write) or a retired db-read/db-write alias.
func IsSQLGrant(grant string) bool { return model.IsGrainSQLGrant(grant) }

// IsSQLWriteGrant reports whether grant names grain#sql with write: true or the retired db-write alias.
func IsSQLWriteGrant(grant string) bool { return model.IsGrainSQLWriteGrant(grant) }

// HasSQL reports whether any grant in grants opens grain#sql (read or write).
func HasSQL(grants []string) bool { return model.HasGrainSQL(grants) }

// HasSQLWrite reports whether any grant in grants opens grain#sql with write: true.
func HasSQLWrite(grants []string) bool { return model.HasGrainSQLWrite(grants) }

// IsHostExecGrant reports whether grant names grain#host-exec or the retired self-repair alias.
func IsHostExecGrant(grant string) bool { return model.IsGrainHostExecGrant(grant) }

// HasHostExec reports whether any grant in grants opens grain#host-exec.
func HasHostExec(grants []string) bool { return model.HasGrainHostExec(grants) }

// IsSourceGrant reports whether grant names grain#source (recorded or
// bare) or the retired bare self-debug alias.
func IsSourceGrant(grant string) bool { return model.IsGrainSourceGrant(grant) }

// HasSource reports whether any grant in grants opens grain#source.
func HasSource(grants []string) bool { return model.HasGrainSource(grants) }

// IsCIGrant reports whether grant names grain#ci (recorded or bare) or
// the retired grain-ci alias.
func IsCIGrant(grant string) bool { return model.IsGrainCIGrant(grant) }

// IsGitPushAnyRefGrant reports whether grant is GitPushAnyRefGrant or the
// retired bare git-push-any-ref alias.
func IsGitPushAnyRefGrant(grant string) bool { return model.IsGitPushAnyRefGrant(grant) }

// IsReadIssuesGrant reports whether grant names github#read-issues (recorded
// or bare) or the retired bare read-issues alias.
func IsReadIssuesGrant(grant string) bool { return model.IsGitHubReadIssuesGrant(grant) }

// HasReadIssues reports whether any grant in grants opens github#read-issues.
func HasReadIssues(grants []string) bool { return model.HasGitHubReadIssues(grants) }

// IsSandboxRepoGrant reports whether grant names github#sandbox-repo (recorded
// or bare) or the retired bare github-sandbox alias.
func IsSandboxRepoGrant(grant string) bool { return model.IsGitHubSandboxRepoGrant(grant) }

// HasSandboxRepo reports whether any grant in grants opens github#sandbox-repo.
func HasSandboxRepo(grants []string) bool { return model.HasGitHubSandboxRepo(grants) }

// CheckSourceReadable reports whether this process's own account can
// actually read srcDir, grain's own source checkout grain#source serves
// -- not merely whether the directory is there, and not merely whether
// its root lists.
//
// A deployment that has no source checkout at all (srcDir == "") has
// nothing to check and returns nil.
func CheckSourceReadable(srcDir string) error {
	if srcDir == "" {
		return nil
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("grain's own source checkout at %s cannot be listed by this process (uid %d): %w -- "+
			"grain#source cannot stage grain-src/",
			srcDir, os.Geteuid(), err)
	}
	// One directory below the root, and one file in it: the cheapest pair
	// that distinguishes "staged readable" from "the root is the only
	// readable thing in it". Which entry is immaterial, so this takes the
	// first that answers either way.
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		below := filepath.Join(srcDir, entry.Name())
		if _, err := os.ReadDir(below); err != nil {
			return fmt.Errorf("grain's own source checkout at %s lists at its root but %s cannot be read by "+
				"this process (uid %d): %w -- grain#source can read nothing below that root. "+
				"The tree was staged with modes this account cannot use (scripts/export-source.sh, and "+
				"setup.sh's extract-host-binaries.sh)", srcDir, below, os.Geteuid(), err)
		}
		return nil
	}
	return nil
}
