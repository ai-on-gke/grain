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

// Package trustboundary classifies a change by the paths it touches, against
// a fixed list of the files that make up grain's own trust boundary: who may
// call what, what a sandbox may run, where credentials live, how grain is
// deployed, and what grain's own CI and merge gate read.
//
// The list is code, and deliberately so. It decides whether a change may
// merge itself (orchestrator's security hold, #2425 Layer 2a), and a list a
// repository could declare for itself -- a `.grain/` file, say -- is one a
// change could edit in the same pull request it is trying to get past it.
// So the operator controls it the way they control the rest of grain's
// behaviour: by reviewing a pull request against this file and upgrading.
// Version moves whenever the list does, so a hold recorded under one list
// says which list it was.
package trustboundary

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Version names this build's list. Bump it on any change to Categories, so
// a hold written under an older list is recognisable as one.
const Version = "trust-boundary/v9"

// Category is one kind of trust-boundary path.
type Category struct {
	// ID is short and stable: what a hold records and what a reviewer is
	// told the change touched.
	ID string
	// Description is what the category guards, for the reviewer.
	Description string
	// RequiresHuman is set on the paths a clean automated review is not
	// enough for: what grain's own gate reads to decide that a red build
	// is not this change's fault. A change that edits those can never
	// merge itself, reviewed or not -- a human presses Submit.
	RequiresHuman bool
	// OwnerApproval narrows RequiresHuman to who: only an admin of the
	// repository's owning team, or a system admin, may press that Submit
	// (a Submit by anybody else is refused). Set on what decides who may
	// release everything else -- a repository's own trigger declarations
	// (grain/task-1714) -- since a change that could be let through by
	// whoever it is trying to get past would guard nothing.
	OwnerApproval bool

	// prefixes are directory prefixes ("pkg/gitproxy/"), files are exact
	// repository paths, and bases are file-name patterns matched against
	// the last path element in any directory (path.Match syntax).
	prefixes []string
	files    []string
	bases    []string
}

// Categories is the list, in the order a reviewer is told about them. The
// first category a path matches is the one it is reported under, so the
// narrower entries come first: securityhold.go before pkg/orchestrator/ as
// a whole, and the ".grain/" suppressions before ".grain/".
//
// v8 added release-promotion (grain/task-1236): the person-only step
// that makes a pending release official -- .grain/workflows/promote-release.yml,
// the scripts it runs, and the route that starts it. A change that let an
// agent or a schedule push vX.Y.0 and `latest` must not merge itself.
//
// v7 added state-privilege (grain/task-1025, finding 1 of the
// sandbox/host boundary audit): a pull request against the deployment's
// own state repository is classified by ClassifyState as well, because
// what merges there is applied to the running daemon on the next tick.
//
// v6 added what the 2.4 security audit found on the trust boundary
// outside v5's paths (grain/task-947, filed by task 907): the agent API's
// scope table, gate and its Dangerous scopes, the external-config-repo
// refusal, the persistent-sandbox staging proxy, the workstation policy
// enforcement point and issuer key fetching (auth-routes); credential
// redaction (capabilities-secrets); the sandbox pod spec, the host process
// server and host-script executor, the per-run host, the derived guest
// image, the vendored granule guest agent, exec wire and port-forward mux,
// and the daemon wiring that decides what is forwarded into guests
// (exec-sandbox); and the state repository sync (state-repo).
//
// v5 dropped cmd/grain-tunnel-gateway/ from credential-proxy
// (grain/task-2099): the tunnel gateway is gone, and the rendezvous relay
// is the one remote-access edge.
//
// v4 added merge-rules (grain/task-1659), a repository's own
// `.grain/merge-rules.yml` (and its old name `.grain/sensitive.yml`),
// which decides what CI gates a merge and whether it may merge itself.
//
// v3 added path-triggers (grain/task-1714), a repository's own
// `.grain/triggers`, which only its owners may release.
//
// v2 widened v1 to what the 2026-10-01 repo-wide review found on the
// trust boundary outside v1's paths: auth middleware and pkg/access,
// capability and secret handling, the whole MCP tool surface, the relay
// binaries, the code that decides green (pkg/ci, checkpost,
// repository declarations), the security tests themselves, image upgrades,
// and this list.
var Categories = []Category{
	{
		ID:            "hold-policy",
		Description:   "this list and the security hold that enforces it",
		RequiresHuman: true,
		// The list is operator-controlled only for as long as a change to
		// it cannot merge itself: one that drops a path from it would be
		// classified by the very list it shortens.
		prefixes: []string{"pkg/trustboundary/"},
		files: []string{
			"pkg/orchestrator/securityhold.go",
			// The repository-declared half of the list, and who may
			// release what it holds (grain/task-1714).
			"pkg/orchestrator/repoguard.go", "pkg/model/sensitive_path.go", "pkg/ui/approvals.go",
		},
	},
	{
		ID:          "auth-routes",
		Description: "authentication, principals, request guards and the UI/API route table",
		prefixes:    []string{"pkg/access/", "pkg/iapreconciler/", "pkg/policy/", "pkg/jwks/"},
		files: []string{
			"pkg/ui/server.go", "pkg/ui/principal.go", "pkg/ui/access.go",
			"pkg/ui/auth.go", "pkg/ui/agent_keys.go", "pkg/ui/github_tokens.go",
			"pkg/ui/csrf.go", "pkg/ui/hostguard.go", "pkg/ui/security_headers.go",
			"pkg/ui/team_scope.go",
			// The agent API: which scope each route needs, which scopes
			// are Dangerous, and the escalation and self-protection rails.
			"pkg/ui/agent_api_gate.go", "pkg/ui/route_scopes.go", "pkg/ui/agent_api.go",
			"pkg/ui/agent_api_tools.go", "pkg/model/agent_api.go",
			"pkg/ui/config_refusal.go", "pkg/ui/persistent_sandboxes.go",
		},
	},
	{
		ID:          "capabilities-secrets",
		Description: "capability grants, secret storage and the cloud identities credentials are minted from",
		prefixes:    []string{"pkg/capability/", "pkg/secrets/", "pkg/secretsrepo/", "pkg/gcpsetup/", "pkg/redact/"},
	},
	{
		ID:          "exec-sandbox",
		Description: "what a sandbox may run, the tools an agent may call, and what the orchestrator does with a run's output",
		prefixes: []string{
			"pkg/orchestrator/", "pkg/mcp/", "pkg/granule/", "pkg/safepath/",
			"pkg/k8s/", "pkg/hostd/", "pkg/localexec/", "pkg/runhost/", "pkg/sandboximage/",
			"third_party/granule/",
		},
		// What every backend is wired with, what is forwarded into
		// guests and which git-proxy host sandboxes are told.
		files: []string{"pkg/daemon/run.go", "pkg/daemon/sandboxnet.go"},
	},
	{
		ID:          "credential-proxy",
		Description: "proxies and relays that hold or forward credentials",
		prefixes: []string{
			"pkg/gitproxy/", "pkg/modelproxy/", "pkg/pkgcache/", "pkg/relay/", "pkg/tunnel/",
			"cmd/grain-relay/", "cmd/grain-connect/",
		},
	},
	{
		ID:          "merge-gate",
		Description: "what decides a build is green and which checks a change owes: CI planning and execution, check reports, repository declarations",
		prefixes:    []string{"pkg/ci/", "pkg/workflows/", "pkg/grit/", "cmd/grit/", "pkg/checkpost/", "cmd/checkpost/", "pkg/repodecl/"},
	},
	{
		ID:          "security-tests",
		Description: "the security regression suite, which every PR's gate runs",
		prefixes:    []string{"tests/security/"},
	},
	{
		ID:          "deploy",
		Description: "deployment and infrastructure definitions (terraform, kubernetes)",
		prefixes:    []string{"deploy/"},
		bases:       []string{"*.tf", "*.tfvars"},
	},
	{
		ID:          "github-workflows",
		Description: "GitHub Actions workflows",
		prefixes:    []string{".github/workflows/"},
	},
	{
		ID:            "ci-suppressions",
		Description:   "CI baselines and quarantined tests, which tell grain's gate which failures to ignore",
		RequiresHuman: true,
		prefixes:      []string{".grain/baselines/"},
		files:         []string{".grain/quarantine.yml", ".grain/quarantine.yaml"},
	},
	{
		ID: "path-triggers",
		Description: "the repository's own path triggers, which decide what its pull requests are held for " +
			"and who may release them",
		RequiresHuman: true,
		OwnerApproval: true,
		// Its rules are read from the base, so a change cannot loosen the
		// rule judging it -- but once merged it judges everything after
		// it, so it is released only by the people who own the repository.
		// The templates those triggers file are guarded the same way, by
		// orchestrator's repoGuard: which ones they are is the
		// repository's to say, so they cannot be listed here.
		prefixes: []string{".grain/triggers/"},
	},
	{
		ID: "merge-rules",
		Description: "the repository's own merge rules, which decide per path which CI gates a merge, " +
			"whether it may merge itself and how long the queue waits on CI",
		RequiresHuman: true,
		OwnerApproval: true,
		// Read from the base like the triggers, so a change cannot loosen
		// the rules judging it; once merged it judges everything after it.
		files: []string{".grain/merge-rules.yml", ".grain/merge-rules.yaml", ".grain/sensitive.yml"},
	},
	{
		ID: "release-promotion",
		Description: "the person-only approval that makes a pending release official: the workflow that " +
			"pushes vX.Y.0 and latest, the release scripts it runs, and the route that starts it",
		RequiresHuman: true,
		files: []string{
			".grain/workflows/promote-release.yml", ".grain/workflows/promote.yml",
			"scripts/release-from-trunk.sh", "scripts/release-images.sh",
			"pkg/ui/release_approval.go",
		},
	},
	{
		ID:          "grain-config",
		Description: "the repository's own grain declarations: CI workflows, schedules, suites, templates, settings",
		prefixes:    []string{".grain/"},
	},
	{
		ID:          "state-repo",
		Description: "the state repository sync, which writes this deployment's settings, templates, schedules and suites",
		prefixes:    []string{"pkg/staterepo/"},
	},
	{
		ID:          "sandbox-images",
		Description: "Granulefiles and Dockerfiles: what every sandbox and deployment image is built from",
		bases:       []string{"Granulefile", "Granulefile.*", "Dockerfile", "Dockerfile.*", "*.Dockerfile"},
	},
	{
		ID:          "upgrade-images",
		Description: "how a deployment picks, assembles and runs the images it upgrades to",
		prefixes:    []string{"pkg/upgrade/", "pkg/lifecycle/"},
	},
	{
		ID:          "setup-scripts",
		Description: "setup scripts run with the deployment's own privileges",
		files:       []string{"scripts/setup.sh"},
	},
}

// Match is one category a change touched and the paths that put it there.
type Match struct {
	Category Category
	Paths    []string
}

// Classify returns the categories paths touch, in Categories' order, each
// with the paths (sorted, deduplicated) that matched it. Nil means the
// change touches nothing on the list.
//
// Paths are repository-relative with forward slashes, as GitHub reports
// them. A leading "./" or "/" is ignored rather than trusted to mean
// something else.
func Classify(paths []string) []Match {
	byCategory := map[string]map[string]bool{}
	for _, p := range paths {
		p = strings.TrimPrefix(strings.TrimPrefix(path.Clean("/"+p), "/"), "./")
		if p == "" || p == "." {
			continue
		}
		if c, ok := categoryOf(p); ok {
			if byCategory[c] == nil {
				byCategory[c] = map[string]bool{}
			}
			byCategory[c][p] = true
		}
	}
	var out []Match
	for _, c := range Categories {
		set := byCategory[c.ID]
		if len(set) == 0 {
			continue
		}
		m := Match{Category: c}
		for p := range set {
			m.Paths = append(m.Paths, p)
		}
		sort.Strings(m.Paths)
		out = append(out, m)
	}
	return out
}

// OwnerApproval reports whether any of matches is a category only the
// repository's owners may release.
func OwnerApproval(matches []Match) bool {
	for _, m := range matches {
		if m.Category.OwnerApproval {
			return true
		}
	}
	return false
}

// RequiresHuman reports whether any of matches is a category a clean
// automated review cannot release.
func RequiresHuman(matches []Match) bool {
	for _, m := range matches {
		if m.Category.RequiresHuman {
			return true
		}
	}
	return false
}

func categoryOf(p string) (string, bool) {
	// File names are compared case-insensitively: "dockerfile" builds an
	// image as surely as "Dockerfile" does.
	base := strings.ToLower(path.Base(p))
	for _, c := range Categories {
		for _, f := range c.files {
			if p == f {
				return c.ID, true
			}
		}
		for _, pre := range c.prefixes {
			if strings.HasPrefix(p, pre) {
				return c.ID, true
			}
		}
		for _, pat := range c.bases {
			if ok, _ := path.Match(strings.ToLower(pat), base); ok {
				return c.ID, true
			}
		}
	}
	return "", false
}

// StatePrivilege is the category ClassifyState reports: a change to the
// deployment's own state repository that grants reach, binds a secret or
// loosens isolation. What merges there is applied to the running daemon
// on its next tick (staterepo.Apply), so a change that could merge itself
// would be a grant nobody made. No review releases it: a person presses
// Submit.
var StatePrivilege = Category{
	ID: "state-privilege",
	Description: "this deployment's own settings that grant capabilities, repositories or secrets, " +
		"or set what a sandbox runs and how it is isolated -- applied to the running daemon once merged",
	RequiresHuman: true,
}

// ConfigPolicyPath is the repository-relative path of the config repository's
// auto-merge exception policy. It is evaluated from the config repository's
// base branch (main), never from an unmerged pull request head, and is itself
// listed in NeverAutoMergePaths so only a human Submit can modify it.
const ConfigPolicyPath = "settings/config-policy.yml"

// NeverAutoMergePaths is the hardcoded ceiling of config-repository path globs
// that always require a human Submit in grain/config, even if a pattern in
// settings/config-policy.yml (auto_merge_paths) would otherwise match them.
var NeverAutoMergePaths = []string{
	// The exception policy itself: only a human may change what auto-merges.
	ConfigPolicyPath,
	// Declarative settings documents that define capabilities, task creation
	// defaults, templates, schedules, and suites.
	"settings/capabilities.yml",
	"settings/new-task-form.yml",
	"settings/templates.yml",
	"settings/schedules.yml",
	"settings/suites.yml",
	// Core configuration, scope sets, credentials, backends, console sessions,
	// and workflow tables.
	"tables/grain_config.json",
	"tables/repo_config.json",
	"tables/api_scope_set.json",
	"tables/git_credential.json",
	"tables/repo_git_credential.json",
	"tables/agent_backend.json",
	"tables/console_session.json",
	"tables/repo_workflow.json",
	// Extracted scripts, CI workflows, and schema version.
	"scripts/**",
	"workflows/**",
	".github/**",
	".grain/**",
	"schema-version",
}

// ConfigPolicy is the parsed representation of settings/config-policy.yml.
type ConfigPolicy struct {
	AutoMergePaths []string `yaml:"auto_merge_paths"`
}

// ParseConfigPolicy parses settings/config-policy.yml syntax and validates
// that every entry in auto_merge_paths is a well-formed relative path glob.
// An empty or comment-only document parses as a policy with no exceptions.
func ParseConfigPolicy(data []byte) (ConfigPolicy, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return ConfigPolicy{}, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var policy ConfigPolicy
	if err := dec.Decode(&policy); err != nil {
		if errors.Is(err, io.EOF) {
			return ConfigPolicy{}, nil
		}
		return ConfigPolicy{}, fmt.Errorf("parse %s: %w", ConfigPolicyPath, err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return ConfigPolicy{}, fmt.Errorf("parse %s: multiple YAML documents are not allowed", ConfigPolicyPath)
	} else if !errors.Is(err, io.EOF) {
		return ConfigPolicy{}, fmt.Errorf("parse %s: %w", ConfigPolicyPath, err)
	}
	for i, raw := range policy.AutoMergePaths {
		if err := validateStateGlob(raw); err != nil {
			return ConfigPolicy{}, fmt.Errorf("%s: auto_merge_paths[%d] %q: %w", ConfigPolicyPath, i, raw, err)
		}
		policy.AutoMergePaths[i] = strings.TrimSpace(raw)
	}
	return policy, nil
}

// ValidateConfigPolicy validates a parsed ConfigPolicy, rejecting invalid
// globs and any auto_merge_paths entry that matches or overlaps a path in
// NeverAutoMergePaths.
func ValidateConfigPolicy(policy ConfigPolicy) error {
	for i, raw := range policy.AutoMergePaths {
		if err := validateStateGlob(raw); err != nil {
			return fmt.Errorf("%s: auto_merge_paths[%d] %q: %w", ConfigPolicyPath, i, raw, err)
		}
		pat := strings.TrimSpace(raw)
		for _, never := range NeverAutoMergePaths {
			if stateGlobsOverlap(pat, never) {
				return fmt.Errorf("%s: auto_merge_paths[%d] %q matches NeverAutoMergePaths entry %q (never allowed to auto-merge)", ConfigPolicyPath, i, pat, never)
			}
		}
	}
	return nil
}

// CheckConfigPolicy parses settings/config-policy.yml and validates it against
// NeverAutoMergePaths.
func CheckConfigPolicy(data []byte) (ConfigPolicy, error) {
	policy, err := ParseConfigPolicy(data)
	if err != nil {
		return ConfigPolicy{}, err
	}
	if err := ValidateConfigPolicy(policy); err != nil {
		return ConfigPolicy{}, err
	}
	return policy, nil
}

// IsNeverAutoMergePath reports whether filePath matches any entry in
// NeverAutoMergePaths.
func IsNeverAutoMergePath(filePath string) bool {
	p := cleanStatePath(filePath)
	if p == "" {
		return false
	}
	for _, never := range NeverAutoMergePaths {
		if MatchStatePath(never, p) {
			return true
		}
	}
	return false
}

// ClassifyState classifies paths in a pull request against this deployment's
// own state repository (grain/config), which the caller -- not the repository
// -- decides it is.
//
// Every changed path in grain/config requires a human Submit (StatePrivilege,
// RequiresHuman: true) by default. A path is exempt only when it matches one
// of the opt-in globs in autoMergePaths (loaded from settings/config-policy.yml
// on the config repository's base branch) AND does not match any entry in
// NeverAutoMergePaths, which is evaluated first as a hardcoded ceiling.
func ClassifyState(paths []string, autoMergePaths ...string) []Match {
	set := map[string]bool{}
	for _, raw := range paths {
		p := cleanStatePath(raw)
		if p == "" {
			continue
		}
		if IsNeverAutoMergePath(p) {
			set[p] = true
			continue
		}
		if matchesAnyStateGlob(p, autoMergePaths) {
			continue
		}
		set[p] = true
	}
	if len(set) == 0 {
		return nil
	}
	m := Match{Category: StatePrivilege}
	for p := range set {
		m.Paths = append(m.Paths, p)
	}
	sort.Strings(m.Paths)
	return []Match{m}
}

func cleanStatePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(strings.TrimPrefix(path.Clean("/"+p), "/"), "./")
	if p == "" || p == "." {
		return ""
	}
	return p
}

func matchesAnyStateGlob(filePath string, patterns []string) bool {
	for _, raw := range patterns {
		pat := strings.TrimSpace(raw)
		if pat == "" || validateStateGlob(pat) != nil {
			continue
		}
		if MatchStatePath(pat, filePath) {
			return true
		}
	}
	return false
}

func validateStateGlob(pattern string) error {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return errors.New("empty path glob")
	}
	if strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return errors.New("path glob must be relative and must not start or end with '/'")
	}
	if strings.Contains(p, "\\") {
		return errors.New("path glob must use '/' separators")
	}
	segs := strings.Split(p, "/")
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid path segment %q", seg)
		}
		if seg == "**" {
			continue
		}
		if strings.Contains(seg, "**") {
			return fmt.Errorf("'**' must be a full path segment in %q", seg)
		}
		if _, err := path.Match(seg, "x"); err != nil {
			return fmt.Errorf("invalid glob syntax in segment %q: %w", seg, err)
		}
	}
	return nil
}

// MatchStatePath reports whether a slash-separated path matches pattern.
// Single-segment wildcards (*, ?, [...]) follow path.Match; a "**" segment
// matches zero or more path segments.
func MatchStatePath(pattern, filePath string) bool {
	pat := strings.Trim(strings.TrimSpace(pattern), "/")
	p := cleanStatePath(filePath)
	if pat == "" || p == "" {
		return false
	}
	return matchStateSegments(strings.Split(pat, "/"), strings.Split(p, "/"))
}

func matchStateSegments(patSegs, pathSegs []string) bool {
	if len(patSegs) == 0 {
		return len(pathSegs) == 0
	}
	if patSegs[0] == "**" {
		if matchStateSegments(patSegs[1:], pathSegs) {
			return true
		}
		return len(pathSegs) > 0 && matchStateSegments(patSegs, pathSegs[1:])
	}
	if len(pathSegs) == 0 {
		return false
	}
	ok, err := path.Match(patSegs[0], pathSegs[0])
	if err != nil || !ok {
		return false
	}
	return matchStateSegments(patSegs[1:], pathSegs[1:])
}

// stateGlobsOverlap reports whether there exists at least one concrete
// non-empty relative path matched by both glob a and glob b.
func stateGlobsOverlap(a, b string) bool {
	a = strings.Trim(strings.TrimSpace(a), "/")
	b = strings.Trim(strings.TrimSpace(b), "/")
	if a == "" || b == "" {
		return false
	}
	return segmentsOverlap(strings.Split(a, "/"), strings.Split(b, "/"))
}

func segmentsOverlap(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) > 0 && a[0] == "**" {
		if segmentsOverlap(a[1:], b) {
			return true
		}
		if len(b) > 0 {
			return segmentsOverlap(a, b[1:])
		}
		return false
	}
	if len(b) > 0 && b[0] == "**" {
		if segmentsOverlap(a, b[1:]) {
			return true
		}
		if len(a) > 0 {
			return segmentsOverlap(a[1:], b)
		}
		return false
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if !singleSegmentOverlaps(a[0], b[0]) {
		return false
	}
	return segmentsOverlap(a[1:], b[1:])
}

func singleSegmentOverlaps(a, b string) bool {
	if a == b || a == "*" || b == "*" {
		return true
	}
	if ok, err := path.Match(a, b); err == nil && ok {
		return true
	}
	if ok, err := path.Match(b, a); err == nil && ok {
		return true
	}
	return false
}
