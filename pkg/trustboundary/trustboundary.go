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
	"path"
	"sort"
	"strings"
)

// Version names this build's list. Bump it on any change to Categories, so
// a hold written under an older list is recognisable as one.
const Version = "trust-boundary/v8"

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

// StatePrivilegedTables are the settings tables (staterepo.SettingsTables)
// whose rows grant reach or remove a person from a decision: capabilities,
// repositories a task may read or write, secrets and env bound into a
// sandbox, the images and isolation sandboxes run with, scripts run in
// them, CI workflows, and the auto-merge and auto-approve switches on
// what files work. A change to any of them is StatePrivilege.
var StatePrivilegedTables = []string{
	// network_access, the k8s images, privilege and service account,
	// mcp_servers with $secret: bindings, capability_policy, the guest
	// setup script.
	"grain_config",
	// Default capabilities and a repo's setup command.
	"repo_config",
	// CI workflows run for a repo, with the capabilities they declare.
	"repo_workflow",
	// What a release has to pass before it is promoted, and whether it
	// is promoted without a person.
	"qualification_config", "qualification_item", "qualification_item_depends_on",
	// A template's target, auto-merge and network access, the repos it
	// reads and writes, and the capabilities it grants.
	"template", "template_read", "template_write", "template_grant",
	// A suite's auto-merge and auto-approve, and which templates run
	// under them.
	"suite", "suite_item",
	// The same for what a schedule files on its own.
	"schedule", "schedule_read", "schedule_write", "schedule_grant",
	// Sandboxes defined here: any image, any secret-store key as env.
	"standing_sandbox", "standing_sandbox_secret", "standing_sandbox_env",
	"persistent_sandbox",
	// The cloud identities credentials are minted from.
	"gcp_key",
	// The agent API's scope sets. Declaring one grants nobody anything,
	// but a set is granted by name and resolved when it is used, so
	// widening one already granted (adding deployment:* to ops/triage)
	// widens every grant of it -- a grant nobody made, unless a person
	// merges it.
	"api_scope_set",
}

// StateBenignTables are the settings tables that grant nothing --
// presentation, inputs, field declarations, id sequences -- and, after
// them, the non-settings tables whose bodies the repository carries as
// text grain writes for itself (memory, release notes). A change to
// their dump files alone merges like any other change.
//
// Every table in staterepo.SettingsTables is on exactly one of these two
// lists (TestEveryStateTableIsClassified): a new settings table has to
// be decided about before it can ship.
var StateBenignTables = []string{
	"custom_field",
	"template_field", "template_custom_field", "template_sequence",
	"suite_sequence",
	"board", "board_column", "board_panel", "board_section", "board_sequence",
	"schedule_custom_field", "schedule_sequence",
	"memory", "memory_document", "release_note",
}

// stateBenignPrefixes are the state repository's directories of text
// grain writes for itself and reads back as notes, not as grants.
var stateBenignPrefixes = []string{"memory/", "release-notes/"}

// ClassifyState classifies paths in a pull request against this
// deployment's own state repository, which the caller -- not the
// repository -- decides it is.
//
// It fails closed. A path is benign only if it is the dump file of a
// StateBenignTables table (tables/<table>.json), under memory/ or
// release-notes/, or the README; everything else -- the settings
// documents (settings/templates.yml and its siblings carry templates,
// suites and schedules with their grants), scripts/ and workflows/
// (bodies of grain_config's, repo_config's and repo_workflow's script
// columns), the dump file of any other table, the repository's own CI --
// is StatePrivilege. A layout grain adds later is held until it is listed
// here, rather than let through until somebody notices.
func ClassifyState(paths []string) []Match {
	benign := map[string]bool{"README.md": true}
	for _, t := range StateBenignTables {
		benign["tables/"+t+".json"] = true
	}
	set := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimPrefix(strings.TrimPrefix(path.Clean("/"+p), "/"), "./")
		if p == "" || p == "." || benign[p] || hasAnyPrefix(p, stateBenignPrefixes) {
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

func hasAnyPrefix(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}
