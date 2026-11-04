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
		"pkg/ui/client.go", "pkg/ui/tasks.go", "README.md", "scripts/build-cli.sh",
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
		"pkg/ui/agent_api_gate.go":               "auth-routes",
		"pkg/ui/route_scopes.go":                 "auth-routes",
		"pkg/ui/agent_api.go":                    "auth-routes",
		"pkg/ui/agent_api_tools.go":              "auth-routes",
		"pkg/model/agent_api.go":                 "auth-routes",
		"pkg/ui/config_refusal.go":               "auth-routes",
		"pkg/ui/persistent_sandboxes.go":         "auth-routes",
		"pkg/ui/accesscode.go":                   "auth-routes",
		"pkg/ui/single_tenant.go":                "auth-routes",
		"pkg/ui/allowlist.go":                    "auth-routes",
		"pkg/ui/verifier.go":                     "auth-routes",
		"pkg/policy/policy.go":                   "auth-routes",
		"pkg/jwks/jwks.go":                       "auth-routes",
		"pkg/redact/redact.go":                   "capabilities-secrets",
		"pkg/k8s/pod.go":                         "exec-sandbox",
		"pkg/hostd/server.go":                    "exec-sandbox",
		"pkg/localexec/exec.go":                  "exec-sandbox",
		"pkg/runhost/host.go":                    "exec-sandbox",
		"pkg/sandboximage/build.go":              "exec-sandbox",
		"third_party/granule/pkg/forward/mux.go": "exec-sandbox",
		"pkg/daemon/run.go":                      "exec-sandbox",
		"pkg/daemon/sandboxnet.go":               "exec-sandbox",
		"pkg/staterepo/sync.go":                  "state-repo",
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
	"third_party/granule/pkg/guestexec/", "third_party/granule/pkg/forward/",
	"pkg/daemon/run.go", "pkg/daemon/sandboxnet.go",
	"pkg/ui/server.go", "pkg/ui/auth.go", "pkg/orchestrator/securityhold.go",
	"pkg/gitproxy/", "pkg/secrets/", "pkg/ci/", "pkg/workflows/", "deploy/kubernetes/",
	"pkg/ui/hostd_connect.go", "pkg/ui/hostds.go", "pkg/ui/taskhostd.go",
	"pkg/daemon/hostd_registry.go", "pkg/daemon/hostd_route.go", "pkg/daemon/hostexec.go",
	"pkg/model/hostd.go", "pkg/ui/messages.go", "pkg/daemon/mcpserver_hostexec.go",
	"pkg/model/repo_access.go", "pkg/daemon/ci.go", "pkg/daemon/ci_policy.go",
	"pkg/daemon/ci_capability_pass.go", "pkg/daemon/capabilities.go",
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
		"pkg/ui/hostd_connect.go":          "auth-routes",
		"pkg/ui/hostds.go":                 "auth-routes",
		"pkg/ui/taskhostd.go":              "auth-routes",
		"pkg/daemon/hostd_registry.go":     "auth-routes",
		"pkg/daemon/hostd_route.go":        "auth-routes",
		"pkg/daemon/hostexec.go":           "auth-routes",
		"pkg/model/hostd.go":               "auth-routes",
		"pkg/ui/messages.go":               "auth-routes",
		"pkg/daemon/mcpserver_hostexec.go": "capabilities-secrets",
		"pkg/model/repo_access.go":         "capabilities-secrets",
		"pkg/daemon/ci.go":                 "capabilities-secrets",
		"pkg/daemon/ci_policy.go":          "capabilities-secrets",
		"pkg/daemon/ci_capability_pass.go": "capabilities-secrets",
		"pkg/daemon/capabilities.go":       "capabilities-secrets",
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
		"pkg/ui":     {"hostdConnectIdentity", "messagesSince"},
		"pkg/daemon": {"registryResolver", "SelectHostd", "grantRepoAccess", "ciRepoAccessAdmitted", "CheckCIJobRepoAccessPolicy"},
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
		"scripts/release-images.sh", "scripts/release-from-trunk.sh",
		"scripts/release-notes.sh", "scripts/candidate-gate.sh", "scripts/release.conf",
		"scripts/patch-tag-guard.sh", "scripts/build-version.sh", "scripts/grain-ci-host.sh",
		".grain/workflows/release.yml", ".github/workflows/release.yml",
		".grain/workflows/promote.yml", ".github/workflows/promote.yml",
		".grain/workflows/release-train.yml", ".github/workflows/release-train.yml",
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
	for _, want := range []string{"`vX.Y.Z` or `latest`", "`permissions:`", "`capabilities:`", "secrets", "dispatch",
		"anybody but a person", "RELEASE_MAJOR"} {
		if !strings.Contains(brief, want) {
			t.Errorf("release-machinery's review brief does not mention %s:\n%s", want, brief)
		}
	}
}
