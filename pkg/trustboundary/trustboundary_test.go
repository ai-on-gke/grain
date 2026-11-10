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

package trustboundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func categoryIDs(ms []Match) []string {
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.Category.ID)
	}
	return ids
}

func TestClassifyEveryCategoryTheIssueNames(t *testing.T) {
	cases := map[string]string{
		"pkg/ui/server.go":                "auth-routes",
		"pkg/ui/principal.go":             "auth-routes",
		"pkg/ui/access.go":                "auth-routes",
		"pkg/mcp/sandbox_tools.go":        "exec-sandbox",
		"pkg/orchestrator/finish.go":      "exec-sandbox",
		"pkg/gitproxy/proxy.go":           "credential-proxy",
		"pkg/modelproxy/x.go":             "credential-proxy",
		"pkg/pkgcache/dns.go":             "credential-proxy",
		"pkg/relay/relay.go":              "credential-proxy",
		"pkg/tunnel/mux/mux.go":           "credential-proxy",
		"deploy/terraform/gcp/iam.tf":     "deploy",
		"elsewhere/main.tf":               "deploy",
		".github/workflows/ci.yml":        "github-workflows",
		".grain/baselines/go.json":        "ci-suppressions",
		".grain/quarantine.yml":           "ci-suppressions",
		".grain/workflows/ci.yml":         "grain-config",
		".grain/triggers/payments.yml":    "path-triggers",
		".grain/merge-rules.yml":          "merge-rules",
		".grain/sensitive.yml":            "merge-rules",
		".grain/settings.yml":             "grain-config",
		"Granulefile":                     "sandbox-images",
		"Granulefile.guest-base":          "sandbox-images",
		"Dockerfile":                      "sandbox-images",
		"Dockerfile.relay":                "sandbox-images",
		"tools/build/dockerfile":          "sandbox-images",
		"scripts/setup.sh":                "setup-scripts",
		"scripts/install-grain.sh":        "setup-scripts",
		"./pkg/gitproxy/../gitproxy/a.go": "credential-proxy",
	}
	for p, want := range cases {
		got := categoryIDs(Classify([]string{p}))
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
	}
}

// One path per place v2 added, each where the 2026-10-01 repo-wide review
// found a trust-boundary problem that v1 would have let merge itself.
func TestClassifyWhatTheRepoWideReviewFoundOutsideV1(t *testing.T) {
	cases := map[string]string{
		"pkg/trustboundary/trustboundary.go": "hold-policy",
		"pkg/orchestrator/securityhold.go":   "hold-policy",
		"pkg/ui/auth.go":                     "auth-routes",
		"pkg/ui/csrf.go":                     "auth-routes",
		"pkg/ui/hostguard.go":                "auth-routes",
		"pkg/ui/security_headers.go":         "auth-routes",
		"pkg/ui/team_scope.go":               "auth-routes",
		"pkg/ui/github_tokens.go":            "auth-routes",
		"pkg/access/store.go":                "auth-routes",
		"pkg/iapreconciler/reconcile.go":     "auth-routes",
		"pkg/capability/grants.go":           "capabilities-secrets",
		"pkg/secrets/store.go":               "capabilities-secrets",
		"pkg/secretsrepo/repo.go":            "capabilities-secrets",
		"pkg/gcpsetup/iam.go":                "capabilities-secrets",
		"pkg/mcp/team_tools.go":              "exec-sandbox",
		"pkg/granule/firewall.go":            "exec-sandbox",
		"pkg/safepath/within.go":             "exec-sandbox",
		"cmd/grain-relay/main.go":            "credential-proxy",
		"cmd/grain-connect/main.go":          "credential-proxy",
		"pkg/ci/plan.go":                     "merge-gate",
		"pkg/workflows/plan.go":              "merge-gate",
		"pkg/grit/engine/engine.go":          "merge-gate",
		"cmd/grit/main.go":                   "merge-gate",
		"pkg/checkpost/sign.go":              "merge-gate",
		"cmd/checkpost/main.go":              "merge-gate",
		"pkg/repodecl/decl.go":               "merge-gate",
		"tests/security/routes_test.go":      "security-tests",
		"pkg/upgrade/image.go":               "upgrade-images",
		"pkg/lifecycle/assemble.go":          "upgrade-images",
	}
	for p, want := range cases {
		got := categoryIDs(Classify([]string{p}))
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
	}
}

func TestClassifyLeavesOrdinaryPathsAlone(t *testing.T) {
	for _, p := range []string{
		"pkg/ui/client.go", "pkg/ui/tasks.go", "README.md",
		"docs/deploy.md", "pkg/orchestratorx/a.go", "web/src/App.tsx",
		"pkg/cifault/fault.go", "tests/e2e/x_test.go", "pkg/ui/auth_test_helpers.txt",
	} {
		if got := Classify([]string{p}); got != nil {
			t.Errorf("Classify(%q) = %v, want nothing", p, categoryIDs(got))
		}
	}
}

func TestClassifyGroupsSortsAndOrdersByCategory(t *testing.T) {
	got := Classify([]string{
		"README.md", "pkg/relay/b.go", ".grain/quarantine.yml", "pkg/relay/a.go", "pkg/ui/server.go", "pkg/relay/a.go",
	})
	if ids := categoryIDs(got); !reflect.DeepEqual(ids, []string{"auth-routes", "credential-proxy", "ci-suppressions"}) {
		t.Fatalf("categories = %v", ids)
	}
	if !reflect.DeepEqual(got[1].Paths, []string{"pkg/relay/a.go", "pkg/relay/b.go"}) {
		t.Errorf("credential-proxy paths = %v", got[1].Paths)
	}
	if !RequiresHuman(got) {
		t.Error("a change touching .grain/quarantine.yml must require a human")
	}
	if RequiresHuman(got[:2]) {
		t.Error("auth-routes and credential-proxy alone are releasable by a clean review")
	}
}

func TestTheListAndItsHoldNeedAHuman(t *testing.T) {
	for _, p := range []string{"pkg/trustboundary/trustboundary.go", "pkg/orchestrator/securityhold.go"} {
		if got := Classify([]string{p}); !RequiresHuman(got) {
			t.Errorf("Classify(%q) = %v: a change to the list or its hold must never merge itself", p, categoryIDs(got))
		}
	}
	if got := Classify([]string{"pkg/orchestrator/finish.go"}); RequiresHuman(got) {
		t.Errorf("the rest of pkg/orchestrator is released by a clean review, got %v", categoryIDs(got))
	}
}

// The GitHub CI path filter decides which of the Tests workflow's jobs run,
// and a skipped job does not fail a pull request, so an edit to it could
// skip the jobs that would have caught the rest of its change
// (2026-11-01 security review, finding 2). It waits for a person.
func TestTheGitHubCIPathFilterNeedsAHuman(t *testing.T) {
	got := Classify([]string{"scripts/github-ci-changes.sh"})
	if ids := categoryIDs(got); !reflect.DeepEqual(ids, []string{"ci-suppressions"}) {
		t.Errorf("Classify(scripts/github-ci-changes.sh) = %v, want [ci-suppressions]", ids)
	}
	if !RequiresHuman(got) {
		t.Error("a change to scripts/github-ci-changes.sh must require a human")
	}
}

// The merge rules decide what CI and whose Submit every other change
// needs, so an edit to them is released only by the repository's owners
// (grain/task-1659) -- under either of the file's names.
func TestTheMergeRulesNeedTheRepositorysOwners(t *testing.T) {
	for _, p := range []string{".grain/merge-rules.yml", ".grain/merge-rules.yaml", ".grain/sensitive.yml"} {
		got := Classify([]string{p})
		if !RequiresHuman(got) || !OwnerApproval(got) {
			t.Errorf("Classify(%q) = %v: an edit to the merge rules must wait for the repository's owners",
				p, categoryIDs(got))
		}
	}
}

// One path per place v6 added, each where the 2.4 security audit
// (grain/task-907) found a subsystem on the trust boundary that v5 would
// have let merge itself.
func TestClassifyWhatThe24AuditFoundOutsideV5(t *testing.T) {
	cases := map[string]string{
		"pkg/ui/agent_api_gate.go":                    "auth-routes",
		"pkg/ui/route_scopes.go":                      "auth-routes",
		"pkg/ui/agent_api.go":                         "auth-routes",
		"pkg/ui/agent_api_tools.go":                   "auth-routes",
		"pkg/model/agent_api.go":                      "auth-routes",
		"pkg/ui/config_refusal.go":                    "auth-routes",
		"pkg/ui/persistent_sandboxes.go":              "auth-routes",
		"pkg/ui/accesscode.go":                        "auth-routes",
		"pkg/ui/single_tenant.go":                     "auth-routes",
		"pkg/ui/allowlist.go":                         "auth-routes",
		"pkg/ui/verifier.go":                          "auth-routes",
		"pkg/policy/policy.go":                        "auth-routes",
		"pkg/jwks/jwks.go":                            "auth-routes",
		"pkg/redact/redact.go":                        "capabilities-secrets",
		"pkg/k8s/pod.go":                              "exec-sandbox",
		"pkg/hostd/server.go":                         "exec-sandbox",
		"pkg/localexec/exec.go":                       "exec-sandbox",
		"pkg/runhost/host.go":                         "exec-sandbox",
		"pkg/sandboximage/build.go":                   "exec-sandbox",
		"third_party/granule/pkg/guestforward/mux.go": "exec-sandbox",
		"pkg/daemon/run.go":                           "exec-sandbox",
		"pkg/daemon/sandboxnet.go":                    "exec-sandbox",
		"pkg/staterepo/sync.go":                       "state-repo",
	}
	for p, want := range cases {
		got := categoryIDs(Classify([]string{p}))
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
	}
	if got := Classify([]string{"pkg/daemon/daemon.go"}); got != nil {
		t.Errorf("the rest of pkg/daemon is not on the list, got %v", categoryIDs(got))
	}
}

// knownBoundaryFiles are real files in this repository that sit on the
// trust boundary. Each must be classified, so a subsystem that lands on
// the boundary without a list entry fails here rather than being found by
// the next audit -- add it to this list when it lands, and to Categories.
var knownBoundaryFiles = []string{
	"pkg/ui/accesscode.go", "pkg/ui/single_tenant.go",
	"pkg/ui/allowlist.go", "pkg/ui/verifier.go",
	"pkg/ui/agent_api_gate.go", "pkg/ui/route_scopes.go", "pkg/ui/agent_api.go",
	"pkg/ui/agent_api_tools.go", "pkg/model/agent_api.go",
	"pkg/ui/config_refusal.go", "pkg/ui/persistent_sandboxes.go",
	"scripts/github-ci-changes.sh",
	"pkg/policy/", "pkg/jwks/", "pkg/hostd/", "pkg/localexec/", "pkg/runhost/",
	"pkg/k8s/", "pkg/sandboximage/", "pkg/staterepo/", "pkg/redact/",
	"third_party/granule/pkg/agent/", "third_party/granule/pkg/execwire/",
	"third_party/granule/pkg/guestexec/", "third_party/granule/pkg/guestforward/",
	"pkg/daemon/run.go", "pkg/daemon/sandboxnet.go",
	"pkg/ui/server.go", "pkg/ui/auth.go", "pkg/orchestrator/securityhold.go",
	"pkg/gitproxy/", "pkg/secrets/", "pkg/ci/", "pkg/workflows/", "deploy/kubernetes/",
	"pkg/ui/hostd_connect.go", "pkg/ui/hostds.go", "pkg/ui/connection_hostd.go",
	"pkg/daemon/hostd_registry.go", "pkg/daemon/hostd_route.go", "pkg/daemon/hostexec.go",
	"pkg/model/hostd.go", "pkg/ui/messages.go", "pkg/daemon/mcpserver_hostexec.go",
	"pkg/daemon/capability_servers.go", "pkg/daemon/capability_servers_hostd.go",
	"pkg/ui/capability_servers.go",
	"pkg/ui/access_roles.go", "pkg/model/api_scope_set.go", "pkg/ui/external_cli.go",
	"pkg/ui/local_script_approvals.go", "pkg/daemon/ui_server.go",
	"pkg/model/repo_access.go", "pkg/daemon/ci.go", "pkg/daemon/ci_policy.go",
	"pkg/daemon/ci_capability_pass.go", "pkg/daemon/capabilities.go",
	"pkg/playbooks/", "pkg/model/capability_ref.go",
	"pkg/model/capability_catalogue.go", "pkg/model/template_grants.go",
	"pkg/model/owner_trust.go", "pkg/model/external_agent_token.go",
	"pkg/ui/external_agents.go",
	"pkg/webauth/", "pkg/hubenv/", "pkg/hub/", "cmd/grain-hub/", "cmd/grain-registry/",
	"pkg/hub/signer.go", "pkg/hub/server/router.go", "pkg/hub/server/proxy.go",
	"pkg/hub/store/delegation.go", "pkg/hub/delegation_admin.go", "pkg/hub/server.go",
	"pkg/model/human_review_hold.go", "pkg/ui/human_review.go",
	"pkg/model/director_floor.go", "pkg/ui/on_behalf_of.go",
	"scripts/build-cli.sh",
	"pkg/model/canonical_grant.go", "pkg/model/auto_submit.go",
	"pkg/ui/cli_install.sh", "pkg/ui/cli_install.go",
	"scripts/buildvcs.sh", "scripts/configure-dockerhub-auth.sh", "scripts/release-image-repos.sh",
	"scripts/granule/", "scripts/release-patch.sh",
	"scripts/grain-instance-init.sh", "scripts/bake-gce-release-image.sh",
	"pkg/hostinit/", "pkg/installtrack/",
}

// Entries ending in "/" are directories: every .go file under them, as it
// is on disk, must be classified.
func TestEveryKnownBoundaryFileIsClassified(t *testing.T) {
	root := "../.."
	for _, entry := range knownBoundaryFiles {
		var paths []string
		if strings.HasSuffix(entry, "/") {
			err := filepath.WalkDir(filepath.Join(root, entry), func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return err
				}
				paths = append(paths, filepath.ToSlash(rel))
				return nil
			})
			if err != nil {
				t.Errorf("%s: %v", entry, err)
				continue
			}
			if len(paths) == 0 {
				t.Errorf("%s has no files: the known-boundary list is out of date", entry)
			}
		} else {
			if _, err := os.Stat(filepath.Join(root, entry)); err != nil {
				t.Errorf("%s: %v -- the known-boundary list is out of date", entry, err)
				continue
			}
			paths = []string{entry}
		}
		for _, p := range paths {
			if Classify([]string{p}) == nil {
				t.Errorf("Classify(%q) = nothing, but it is a known trust-boundary file", p)
			}
		}
	}
}

// One path per place v11 added (2026-11-03 security review finding 2): the
// hostd registry, the host-exec confirmation and CI repository access, none
// of which v10 held, so a change to who may do what there merged itself.
func TestHostdAndCIRepoAccessGatesAreClassified(t *testing.T) {
	cases := map[string]string{
		"pkg/ui/hostd_connect.go":                "auth-routes",
		"pkg/ui/hostds.go":                       "auth-routes",
		"pkg/ui/connection_hostd.go":             "auth-routes",
		"pkg/daemon/hostd_registry.go":           "auth-routes",
		"pkg/daemon/hostd_route.go":              "auth-routes",
		"pkg/daemon/hostexec.go":                 "auth-routes",
		"pkg/model/hostd.go":                     "auth-routes",
		"pkg/ui/messages.go":                     "auth-routes",
		"pkg/daemon/mcpserver_hostexec.go":       "capabilities-secrets",
		"pkg/daemon/capability_servers.go":       "capabilities-secrets",
		"pkg/daemon/capability_servers_hostd.go": "capabilities-secrets",
		"pkg/ui/capability_servers.go":           "capabilities-secrets",
		"pkg/model/repo_access.go":               "capabilities-secrets",
		"pkg/daemon/ci.go":                       "capabilities-secrets",
		"pkg/daemon/ci_policy.go":                "capabilities-secrets",
		"pkg/daemon/ci_capability_pass.go":       "capabilities-secrets",
		"pkg/daemon/capabilities.go":             "capabilities-secrets",
	}
	for p, want := range cases {
		got := categoryIDs(Classify([]string{p}))
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
	}

	// The gates themselves, found by name rather than by path, so moving
	// one into a file the list does not name fails here instead of
	// silently unclassifying it again.
	gates := map[string][]string{
		"pkg/model": {
			"RepoAccessAdmitsCIEvent", "RepoReadTarget", "RepoWriteTarget", "IsGitPushAnyRefGrant",
			"HostdForToken", "CreateHostd", "RotateHostdToken", "NormalizeHostdOwner",
		},
		"pkg/ui":     {"hostdConnectIdentity", "normalizeConnectionHostd", "normalizeServerHostd", "messagesSince"},
		"pkg/daemon": {"registryResolver", "SelectHostd", "placedOn", "grantRepoAccess", "ciRepoAccessAdmitted", "CheckCIJobRepoAccessPolicy"},
	}
	for dir, names := range gates {
		defined := funcFiles(t, dir)
		for _, name := range names {
			file, ok := defined[name]
			if !ok {
				t.Errorf("%s: no func or method %s -- update this test to where it went", dir, name)
				continue
			}
			if Classify([]string{file}) == nil {
				t.Errorf("%s is defined in %s, which Classify leaves unclassified", name, file)
			}
		}
	}
}

// One path per file v15 added (2026-11-04 security review finding 2): each
// decides who may do what -- the controller MCP endpoint's caller access,
// the hostd install script run as root, the CI grant rule, the auto-merge
// guard, the sign-in allowlist's refusals and the person-only access
// decision -- and v14 classified none of them, so a change to any merged
// itself.
func TestNewAuthorizationFilesAreClassified(t *testing.T) {
	cases := map[string]string{
		"pkg/ui/mcp_endpoint.go":          "auth-routes",
		"pkg/model/allowed_principals.go": "auth-routes",
		"pkg/ui/access_principals.go":     "auth-routes",
		"pkg/ui/task_access_request.go":   "auth-routes",
		"pkg/model/ci_grant_rule.go":      "capabilities-secrets",
		"pkg/model/task_auto_merge.go":    "hold-policy",
		"pkg/ui/hostd_install.sh":         "setup-scripts",
		"pkg/ui/hostd_install.go":         "setup-scripts",
	}
	for p, want := range cases {
		got := categoryIDs(Classify([]string{p}))
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
	}
	// Control: an ordinary pkg/ui file is still nothing.
	if got := Classify([]string{"pkg/ui/actionscache.go"}); got != nil {
		t.Errorf("Classify(pkg/ui/actionscache.go) = %v, want nothing", categoryIDs(got))
	}

	// The gates by name, as in TestHostdAndCIRepoAccessGatesAreClassified.
	gates := map[string][]string{
		"pkg/model": {
			"NormalizeAllowedPrincipalEntry", "RegisterCIGrantRule", "CIGrantRuleFor",
			"guardAutoMerge", "restoreAfterHolds",
		},
		"pkg/ui": {
			"resolveMCPCallerAccess", "allowsRoute", "dispatchSubrequest", "filingContextTool",
			"hostdInstallRoute", "hostdInstallCallerHasAccess", "renderHostdInstallScript",
			"pendingAccessToDecide", "refuseAgentAccessDecision",
		},
	}
	for dir, names := range gates {
		defined := funcFiles(t, dir)
		for _, name := range names {
			file, ok := defined[name]
			if !ok {
				t.Errorf("%s: no func or method %s -- update this test to where it went", dir, name)
				continue
			}
			if Classify([]string{file}) == nil {
				t.Errorf("%s is defined in %s, which Classify leaves unclassified", name, file)
			}
		}
	}
}

// v26: the workstation CLI installer, served unauthenticated and piped into
// sh like its hostd counterpart, and the helpers the release builds and
// workflows run or source. v25 classified none of them, so a change to any
// merged itself.
func TestCLIInstallerAndReleaseHelpersAreClassified(t *testing.T) {
	if Version != "trust-boundary/v28" {
		t.Errorf("Version = %q, want trust-boundary/v28", Version)
	}
	for p, want := range map[string]string{
		"pkg/ui/cli_install.sh":               "setup-scripts",
		"pkg/ui/cli_install.go":               "setup-scripts",
		"scripts/buildvcs.sh":                 "release-machinery",
		"scripts/configure-dockerhub-auth.sh": "release-machinery",
		"scripts/release-image-repos.sh":      "release-machinery",
	} {
		got := categoryIDs(Classify([]string{p}))
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
	}
	defined := funcFiles(t, "pkg/ui")
	for _, name := range []string{"cliInstallRoute", "renderCLIInstallScript"} {
		file, ok := defined[name]
		if !ok {
			t.Errorf("pkg/ui: no func or method %s -- update this test to where it went", name)
			continue
		}
		if Classify([]string{file}) == nil {
			t.Errorf("%s is defined in %s, which Classify leaves unclassified", name, file)
		}
	}
}

// funcFiles maps each func and method name declared in dir's non-test Go
// files (dir relative to the repository root) to the repository path of
// the file declaring it.
func funcFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("../..", dir))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join("../..", dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				out[fn.Name.Name] = dir + "/" + name
			}
		}
	}
	return out
}

// v12 (design/playbook/releases.playbook.md §0.3): the approval and what
// decides who may act without it wait for a person; the release jobs and
// scripts, which cannot reach the approval, are released by a clean review
// that is told what to check on them.
func TestReleasePromotionIsNarrowedToTheApprovalPath(t *testing.T) {
	human := map[string]string{
		".grain/workflows/promote-release.yml":  "release-promotion",
		".github/workflows/promote-release.yml": "release-promotion",
		"pkg/ui/release_approval.go":            "release-promotion",
		".grain/templates/release-train.yml":    "release-promotion",
		"scripts/release-from-trunk.sh":         "release-promotion",
		"scripts/release.conf":                  "release-promotion",
		"scripts/release-train-task.sh":         "release-promotion",
		".grain/schedules/release-train.yml":    "release-promotion",
		".github/workflows/publish-release.yml": "release-promotion",
		"pkg/trustboundary/trustboundary.go":    "hold-policy",
		".grain/triggers/release-promotion.yml": "path-triggers",
		".grain/triggers/release-machinery.yml": "path-triggers",
		".grain/merge-rules.yml":                "merge-rules",
	}
	for p, want := range human {
		got := Classify([]string{p})
		if ids := categoryIDs(got); !reflect.DeepEqual(ids, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, ids, want)
		}
		if !RequiresHuman(got) {
			t.Errorf("%s no longer waits for a person", p)
		}
	}

	reviewed := []string{
		"scripts/release-images.sh",
		"scripts/release-notes.sh", "scripts/candidate-gate.sh",
		"scripts/patch-tag-guard.sh", "scripts/build-version.sh", "scripts/grain-ci-host.sh",
		".grain/workflows/release.yml", ".github/workflows/release.yml",
		".grain/workflows/promote.yml", ".github/workflows/promote.yml",
		".grain/workflows/release-train.yml", ".github/workflows/release-train.yml",
		// v21: the release-asset publisher and the workflow that runs it
		// on the train's tag pushes; v24: the CLI archive builder whose
		// output they publish on every release.
		"scripts/release-assets.sh", "scripts/build-cli.sh", ".github/workflows/release-train-assets.yml",
		// v26: the helpers the release builds and workflows run or source.
		"scripts/buildvcs.sh", "scripts/configure-dockerhub-auth.sh", "scripts/release-image-repos.sh",
		// v28: the guest-image cache-key and runtime build helpers under
		// scripts/granule/ and the patch-release branch/tag script.
		"scripts/granule/build-granule-image.sh", "scripts/granule/guest-inputs.sh",
		"scripts/granule/README.md", "scripts/release-patch.sh",
	}
	for _, p := range reviewed {
		got := Classify([]string{p})
		if ids := categoryIDs(got); !reflect.DeepEqual(ids, []string{"release-machinery"}) {
			t.Errorf("Classify(%q) = %v, want [release-machinery]", p, ids)
		}
		if RequiresHuman(got) {
			t.Errorf("%s waits for a person; a clean security review should release it", p)
		}
	}

	var machinery Category
	for _, c := range Categories {
		if c.ID == "release-machinery" {
			machinery = c
		}
	}
	brief := strings.Join(machinery.Review, "\n")
	for _, want := range []string{"`vX.Y.Z` or `latest`", "`permissions:`", "secrets", "dispatch",
		"anybody but a person", "RELEASE_MAJOR"} {
		if !strings.Contains(brief, want) {
			t.Errorf("release-machinery's review brief does not mention %s:\n%s", want, brief)
		}
	}
}

// One path per file v17 added (2026-11-05 security review finding 2, task
// 3168): the role gate, the built-in scope sets, the gma_ token's inbox
// reach, the approval-callback tokens and the daemon's UI listeners. v16
// classified none of them, so a change to any merged itself.
func TestRoleGateScopeSetsAndApprovalCallbacksAreClassified(t *testing.T) {
	if Version != "trust-boundary/v28" {
		t.Errorf("Version = %q, want trust-boundary/v28", Version)
	}
	for _, p := range []string{
		"pkg/ui/access_roles.go",
		"pkg/ui/request_actor.go",
		"pkg/ui/host_enroll.go",
		"pkg/model/api_scope_set.go",
		"pkg/model/host_enroll.go",
		"pkg/ui/external_cli.go",
		"pkg/ui/local_script_approvals.go",
		"pkg/daemon/ui_server.go",
	} {
		if got := categoryIDs(Classify([]string{p})); !reflect.DeepEqual(got, []string{"auth-routes"}) {
			t.Errorf("Classify(%q) = %v, want [auth-routes]", p, got)
		}
	}

	// The gates by name, as in TestHostdAndCIRepoAccessGatesAreClassified.
	gates := map[string][]string{
		"pkg/model": {"APIScopeSetNamed"},
		"pkg/ui": {
			"resolveAccessRoles", "roleDenies", "roleGate", "roleScopeOf", "authorizeRoleCall",
			"admitVerifiedPrincipal",
			"MintToken", "RevokeToken", "CheckToken", "approvalCallbackRouteTask",
			"authenticateApprovalCallback", "handleAnswerLocalScriptApproval",
			"inboxReadable", "CheckReply", "handleReplyTask",
		},
		"pkg/daemon": {"startUIServer", "localScriptCallbackServer"},
	}
	for dir, names := range gates {
		defined := funcFiles(t, dir)
		for _, name := range names {
			file, ok := defined[name]
			if !ok {
				t.Errorf("%s: no func or method %s -- update this test to where it went", dir, name)
				continue
			}
			if Classify([]string{file}) == nil {
				t.Errorf("%s is defined in %s, which Classify leaves unclassified", name, file)
			}
		}
	}
}

// v19 (task 3388): the embedded bootstrap playbooks moved out of
// pkg/capability/ in task 3148 (pkg/playbooks/) and the capability
// reference/policy, catalogue and template-grant admission files in
// pkg/model/ classify under capabilities-secrets.
func TestPlaybooksAndCapabilityAdmissionFilesAreClassified(t *testing.T) {
	if Version != "trust-boundary/v28" {
		t.Errorf("Version = %q, want trust-boundary/v28", Version)
	}
	for _, p := range []string{
		"pkg/playbooks/playbooks.go",
		"pkg/playbooks/gcp-capabilities.md",
		"pkg/playbooks/gke-sandbox-cluster.md",
		"pkg/model/capability_ref.go",
		"pkg/model/capability_catalogue.go",
		"pkg/model/template_grants.go",
	} {
		if got := categoryIDs(Classify([]string{p})); !reflect.DeepEqual(got, []string{"capabilities-secrets"}) {
			t.Errorf("Classify(%q) = %v, want [capabilities-secrets]", p, got)
		}
	}

	defined := funcFiles(t, "pkg/model")
	for _, name := range []string{
		"Permits", "UncoveredCapabilityRefs", "ValidateGrants",
		"IsDangerousGrant", "TemplateWithinTask", "ReviewTemplateWithinTask", "ReviewGrants",
	} {
		file, ok := defined[name]
		if !ok {
			t.Errorf("pkg/model: no func or method %s -- update this test to where it went", name)
			continue
		}
		if got := categoryIDs(Classify([]string{file})); !reflect.DeepEqual(got, []string{"capabilities-secrets"}) {
			t.Errorf("%s is defined in %s, Classify = %v, want [capabilities-secrets]", name, file, got)
		}
	}
}

// v20 (task 3026): the external-agent owner-trust policy and the gma_
// bearer token model and lifecycle handlers classify under auth-routes, while
// the provenance-only X-Grain-Client / FiledVia helpers stay unclassified.
func TestOwnerTrustAndExternalAgentTokenFilesAreClassified(t *testing.T) {
	if Version != "trust-boundary/v28" {
		t.Errorf("Version = %q, want trust-boundary/v28", Version)
	}
	for _, p := range []string{
		"pkg/model/owner_trust.go",
		"pkg/model/external_agent_token.go",
		"pkg/ui/external_agents.go",
	} {
		if got := categoryIDs(Classify([]string{p})); !reflect.DeepEqual(got, []string{"auth-routes"}) {
			t.Errorf("Classify(%q) = %v, want [auth-routes]", p, got)
		}
	}

	// Control: X-Grain-Client / FiledVia is provenance-only (XC-15) and
	// consulted by no scope, repository, approval or owner-trust decision.
	for _, p := range []string{
		"pkg/model/grain_client.go",
		"pkg/ui/grain_client.go",
	} {
		if got := Classify([]string{p}); got != nil {
			t.Errorf("Classify(%q) = %v, want nothing", p, categoryIDs(got))
		}
	}

	// The gates by name, as in TestNewAuthorizationFilesAreClassified.
	gates := map[string][]string{
		"pkg/model": {
			"NormalizeOwnerTrust", "OwnerTrusted", "OwnerTrustedForTask",
			"OwnerPrincipal", "AllowsRepo", "NormalizeExternalAgentSpec",
			"ResolveExternalAgentGrants", "GetExternalAgentTokenByHash",
			"RotateExternalAgentToken", "RevokeExternalAgentToken", "RevokeAllExternalAgentTokens",
		},
		"pkg/ui": {
			"canIssuePersonExternalAgentToken", "externalAgentTokenOwner", "selfServiceLimits",
			"CreateExternalAgentToken", "RotateExternalAgentToken",
			"authenticateExternalAgent", "ExternalAgentAPITools", "handleMCP",
		},
	}
	for dir, names := range gates {
		defined := funcFiles(t, dir)
		for _, name := range names {
			file, ok := defined[name]
			if !ok {
				t.Errorf("%s: no func or method %s -- update this test to where it went", dir, name)
				continue
			}
			if got := categoryIDs(Classify([]string{file})); !reflect.DeepEqual(got, []string{"auth-routes"}) {
				t.Errorf("%s is defined in %s, Classify = %v, want [auth-routes]", name, file, got)
			}
		}
	}
}

// v22 (task 3479, found in the security review of task 3284): grain-registry's
// front door. pkg/webauth/ verifies IAP and registry-assertion JWTs, admits
// principals and checks proxy-auth CSRF (auth-routes); pkg/hub/ and
// cmd/grain-hub/ decide visibility and delegation, sign assertions,
// strip edge credentials before a tunnel and authenticate grc_ credentials
// (credential-proxy). v21 classified none of them, so a change to any merged
// itself.
func TestHubFrontDoorIsClassified(t *testing.T) {
	if Version != "trust-boundary/v28" {
		t.Errorf("Version = %q, want trust-boundary/v28", Version)
	}
	for p, want := range map[string]string{
		"pkg/webauth/iap.go":           "auth-routes",
		"pkg/webauth/hub.go":           "auth-routes",
		"pkg/webauth/admission.go":     "auth-routes",
		"pkg/webauth/csrf.go":          "auth-routes",
		"pkg/hub/signer.go":            "credential-proxy",
		"pkg/hub/server/router.go":     "credential-proxy",
		"pkg/hub/server/proxy.go":      "credential-proxy",
		"pkg/hub/server/tunnel.go":     "credential-proxy",
		"pkg/hub/store/store.go":       "credential-proxy",
		"pkg/hub/providers/gcp/kms.go": "credential-proxy",
		"cmd/grain-hub/main.go":        "credential-proxy",
	} {
		if got := categoryIDs(Classify([]string{p})); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
	}

	// Control: the registry's React app is UI, not the front door.
	if got := Classify([]string{"ui-hub/src/App.jsx"}); got != nil {
		t.Errorf("Classify(ui-hub/src/App.jsx) = %v, want nothing", categoryIDs(got))
	}
}

// v23 (task 3329, finding 1 of the 2026-11-08 security review of main):
// the web-auth and registry identity-asserting paths the review found
// unclassified at d7dde0c2 (classified since by v22), and what v22 still
// left out -- the human-review hold an agent may raise but never release,
// the director's Tier 3 floor, and agent on-behalf-of attribution.
func TestWebauthAndHubIdentityFilesAreClassified(t *testing.T) {
	if Version != "trust-boundary/v28" {
		t.Errorf("Version = %q, want trust-boundary/v28", Version)
	}
	for p, want := range map[string]string{
		"pkg/webauth/iap.go":             "auth-routes",
		"pkg/webauth/hub.go":             "auth-routes",
		"pkg/webauth/csrf.go":            "auth-routes",
		"pkg/webauth/admission.go":       "auth-routes",
		"pkg/webauth/groups.go":          "auth-routes",
		"pkg/hub/server/router.go":       "credential-proxy",
		"pkg/hub/server/proxy.go":        "credential-proxy",
		"pkg/hub/signer.go":              "credential-proxy",
		"pkg/hub/store/delegation.go":    "credential-proxy",
		"pkg/hub/delegation_admin.go":    "credential-proxy",
		"pkg/hub/server.go":              "credential-proxy",
		"pkg/model/human_review_hold.go": "auth-routes",
		"pkg/ui/human_review.go":         "auth-routes",
		"pkg/model/director_floor.go":    "auth-routes",
		"pkg/ui/on_behalf_of.go":         "auth-routes",
		// Contrast: the pkg/ui files the web-auth code moved out of.
		"pkg/ui/csrf.go":      "auth-routes",
		"pkg/ui/verifier.go":  "auth-routes",
		"pkg/ui/allowlist.go": "auth-routes",
	} {
		if got := categoryIDs(Classify([]string{p})); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
	}

	// The gates by name, as in TestNewAuthorizationFilesAreClassified, so
	// a move out of these files fails here rather than at the next audit.
	gates := map[string][]string{
		"pkg/model": {"ReleaseHumanReviewHold", "WithinDirectorFloor", "DirectorFloorRefusal"},
		"pkg/ui":    {"agentOnBehalfOf"},
	}
	for dir, names := range gates {
		defined := funcFiles(t, dir)
		for _, name := range names {
			file, ok := defined[name]
			if !ok {
				t.Errorf("%s: no func or method %s -- update this test to where it went", dir, name)
				continue
			}
			if got := categoryIDs(Classify([]string{file})); !reflect.DeepEqual(got, []string{"auth-routes"}) {
				t.Errorf("%s is defined in %s, Classify = %v, want [auth-routes]", name, file, got)
			}
		}
	}
}

// v25 (task 3644, finding 1 of the 2026-11-08 second security review of
// main, task 3584): pkg/model/canonical_grant.go, which every grant
// predicate reads through since #927, matched nothing, so a pull request
// touching only it merged with no security review. auto_submit.go
// (EscalatedCapabilities) is the same shape.
func TestCanonicalGrantIsClassified(t *testing.T) {
	if Version != "trust-boundary/v28" {
		t.Errorf("Version = %q, want trust-boundary/v28", Version)
	}
	for _, p := range []string{"pkg/model/canonical_grant.go", "pkg/model/auto_submit.go"} {
		matches := Classify([]string{p})
		if len(matches) != 1 || matches[0].Category.ID != "capabilities-secrets" {
			t.Errorf("Classify(%q) = %v, want exactly one match in capabilities-secrets", p, categoryIDs(matches))
		}
	}

	// The predicates by name, so a move out of these files fails here
	// rather than at the next audit.
	defined := funcFiles(t, "pkg/model")
	for _, name := range []string{
		"CanonicalGrant", "CanonicalGrantKey", "LegacyGrantString", "StoredGrantSpelling",
		"SplitRequestGrants", "EscalatedCapabilities",
	} {
		file, ok := defined[name]
		if !ok {
			t.Errorf("pkg/model: no func or method %s -- update this test to where it went", name)
			continue
		}
		if got := categoryIDs(Classify([]string{file})); !reflect.DeepEqual(got, []string{"capabilities-secrets"}) {
			t.Errorf("%s is defined in %s, Classify = %v, want [capabilities-secrets]", name, file, got)
		}
	}
}

// v27 (task 3762, security review of task 3710): the Grain Hub rename
// extracted GRAIN_HUB_* / GRAIN_REGISTRY_* lookups into pkg/hubenv/
// (read by pkg/webauth/, pkg/daemon/ and pkg/hub/) and kept
// cmd/grain-registry/ as a compatibility shim binary for one release.
func TestHubRenameShimAndEnvFilesAreClassified(t *testing.T) {
	if Version != "trust-boundary/v28" {
		t.Errorf("Version = %q, want trust-boundary/v28", Version)
	}
	for p, want := range map[string]string{
		"pkg/hubenv/hubenv.go":       "auth-routes",
		"cmd/grain-registry/main.go": "credential-proxy",
		"pkg/registry/server.go":     "credential-proxy",
	} {
		if got := categoryIDs(Classify([]string{p})); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
	}
}

// v28 (task 3926, finding 3.1 of task 3905): sibling guest-image build helpers
// (scripts/granule/), the patch-release branch/tag script
// (scripts/release-patch.sh), hosted Grain Hub GCE VM bake and boot scripts
// (scripts/grain-instance-init.sh, scripts/bake-gce-release-image.sh), host
// initialization (pkg/hostinit/) and install-track image repository selection
// (pkg/installtrack/).
func TestGranuleReleasePatchGCEInitHostinitAndInstalltrackAreClassified(t *testing.T) {
	if Version != "trust-boundary/v28" {
		t.Errorf("Version = %q, want trust-boundary/v28", Version)
	}
	cases := map[string]string{
		// scripts/granule/ prefix (real files + synthetic child to pin prefix matching)
		"scripts/granule/build-granule-image.sh": "release-machinery",
		"scripts/granule/guest-inputs.sh":        "release-machinery",
		"scripts/granule/README.md":              "release-machinery",
		"scripts/granule/new-helper.sh":          "release-machinery",
		// Exact script in release-machinery
		"scripts/release-patch.sh": "release-machinery",
		// Exact GCE VM init/bake scripts in setup-scripts
		"scripts/grain-instance-init.sh":    "setup-scripts",
		"scripts/bake-gce-release-image.sh": "setup-scripts",
		// pkg/hostinit/ prefix (real files + synthetic child to pin prefix matching)
		"pkg/hostinit/hostinit.go":      "exec-sandbox",
		"pkg/hostinit/steps.go":         "exec-sandbox",
		"pkg/hostinit/k8s_granule.go":   "exec-sandbox",
		"pkg/hostinit/gke_sandboxes.go": "exec-sandbox",
		"pkg/hostinit/new_step.go":      "exec-sandbox",
		// pkg/installtrack/ prefix (real files + nested package + synthetic child)
		"pkg/installtrack/installtrack.go":                      "upgrade-images",
		"pkg/installtrack/installtracktest/installtracktest.go": "upgrade-images",
		"pkg/installtrack/new_track.go":                         "upgrade-images",
	}
	for p, want := range cases {
		matches := Classify([]string{p})
		if got := categoryIDs(matches); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Classify(%q) = %v, want [%s]", p, got, want)
		}
		if RequiresHuman(matches) {
			t.Errorf("Classify(%q) requires human approval; want releasable by a clean security review", p)
		}
	}

	// Pin key entrypoints by AST function name so moving them out of
	// pkg/hostinit or pkg/installtrack into an unclassified package fails here.
	gates := map[string]struct {
		want  string
		names []string
	}{
		"pkg/hostinit": {
			want:  "exec-sandbox",
			names: []string{"Init", "RegisterAllowedHosts"},
		},
		"pkg/installtrack": {
			want:  "upgrade-images",
			names: []string{"RegisterTrack", "ResolveLegacyRepository", "ResolveLegacyImageRef"},
		},
	}
	for dir, spec := range gates {
		defined := funcFiles(t, dir)
		for _, name := range spec.names {
			file, ok := defined[name]
			if !ok {
				t.Errorf("%s: no func or method %s -- update this test to where it went", dir, name)
				continue
			}
			if got := categoryIDs(Classify([]string{file})); !reflect.DeepEqual(got, []string{spec.want}) {
				t.Errorf("%s is defined in %s, Classify = %v, want [%s]", name, file, got, spec.want)
			}
		}
	}
}
