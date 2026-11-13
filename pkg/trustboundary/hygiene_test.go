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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	modelImportPath       = "github.com/ai-on-gke/grain/pkg/model"
	secretsImportPath     = "github.com/ai-on-gke/grain/pkg/secrets"
	secretsRepoImportPath = "github.com/ai-on-gke/grain/pkg/secretsrepo"
	tbImportPath          = "github.com/ai-on-gke/grain/pkg/trustboundary"
)

// authAllowlistEntry is one justified exception to
// TestAuthorizationBearingFilesAreClassified: either a directory prefix
// (ending in "/") or an exact repository-relative path.
//
// Because this file lives under pkg/trustboundary/, any pull request that adds
// an entry here is itself classified under hold-policy (RequiresHuman: true)
// and cannot merge itself without a person's Submit.
type authAllowlistEntry struct {
	path   string
	reason string
}

var unclassifiedAuthAllowlist = []authAllowlistEntry{
	{
		path:   "pkg/cli/",
		reason: "workstation CLI subcommands run locally by the operator (grain secrets, grain state, grain github-app); daemon and sandbox enforcement live in pkg/ui, pkg/daemon and pkg/model",
	},
	{
		path:   "pkg/demo/",
		reason: "local grain demo synthetic fixtures and throwaway keys",
	},
	{
		path:   "pkg/daemon/agents.go",
		reason: "general agent CLI and model-catalog wiring; calls secretStore.Resolve in AgentCredential",
	},
	{
		path:   "pkg/model/compose.go",
		reason: "template, schedule and suite composition assembly; calls APIGrantsOf.Allows to avoid duplicating default ci-read",
	},
	{
		path:   "pkg/model/new_task_form.go",
		reason: "UI New-Task form metadata; calls CapabilityCatalogue.ValidateGrants to mark unavailable options",
	},
	{
		path:   "pkg/model/store.go",
		reason: "general SQLite/Postgres Store; projects task read flags via ParseAPIGrant and APIGrantsOf",
	},
	{
		path:   "pkg/model/task.go",
		reason: "core Task struct and helpers; gates were split out in v11 so ordinary task edits do not hold",
	},
	{
		path:   "pkg/ui/client.go",
		reason: "pinned unclassified by TestClassifyLeavesOrdinaryPathsAlone; general Client CRUD calling classified predicates",
	},
	{
		path:   "pkg/ui/composition.go",
		reason: "read-only UI composition preview carrying []model.Grant",
	},
	{
		path:   "pkg/ui/httpclient.go",
		reason: "workstation CLI REST client signing local service-account OAuth2 JWTs when dialing IAP",
	},
	{
		path:   "pkg/ui/memory.go",
		reason: "memory CRUD handlers calling APIGrantsOf.Allows(\"memory:write\", repo)",
	},
	{
		path:   "pkg/ui/notifications.go",
		reason: "notification CRUD handlers calling APIGrantsOf.Allows for the agent.<id>: subject prefix",
	},
	{
		path:   "pkg/ui/schedules.go",
		reason: "schedule CRUD handlers carrying []model.Grant and secretsrepo error formatting",
	},
	{
		path:   "pkg/ui/tasks.go",
		reason: "pinned unclassified by TestClassifyLeavesOrdinaryPathsAlone; task list/detail/config handlers trimming response fields via Allows",
	},
	{
		path:   "pkg/ui/teams.go",
		reason: "deployment/team settings CRUD calling APIGrantsOf on broadcast",
	},
	{
		path:   "pkg/ui/templates.go",
		reason: "template CRUD carrying []model.Grant and filtering ListTemplates via Allows",
	},
	{
		path:   "scripts/agentd-compat.sh",
		reason: "CI compatibility harness building past grain-agentd tags locally into a temporary directory (gated by agentd-contract.yml)",
	},
}

var routeScopeIdents = map[string]bool{
	"routeScopes":           true,
	"humanRouteScopes":      true,
	"routeAuthorization":    true,
	"operatorOnlyRoutes":    true,
	"agentRouteAdminScopes": true,
	"agentSteeringRoutes":   true,
}

var grantPredicateFuncs = map[string]bool{
	"CanonicalGrant":             true,
	"CanonicalGrantKey":          true,
	"ParseAPIGrant":              true,
	"APIGrantsOf":                true,
	"SandboxAPIGrantsOf":         true,
	"IsDangerousGrant":           true,
	"IsDangerousAPIGrant":        true,
	"IsPersonOnlyGrant":          true,
	"GrantsSubsetOf":             true,
	"EscalatedCapabilities":      true,
	"AgentEscalatedCapabilities": true,
	"UncoveredCapabilityRefs":    true,
	"SplitRequestGrants":         true,
}

var grantPredicateMethods = map[string]bool{
	"Permits":              true,
	"PermitsTaskRepoGrant": true,
	"ValidateGrants":       true,
}

var secretPkgFuncs = map[string]bool{
	"GenerateKey":       true,
	"ParseKey":          true,
	"ReadKeyFile":       true,
	"WriteKeyFile":      true,
	"Encrypt":           true,
	"Decrypt":           true,
	"Open":              true,
	"OpenShared":        true,
	"OpenSecretManager": true,
	"New":               true,
	"DefaultKMSClient":  true,
}

// secretStoreMethodArity maps *secrets.Store methods that mint, read or write
// secret material to their argument counts, distinguishing Store.Set(s, k, v)
// from http.Header.Set(k, v) or ForbiddenSet.Set(repos).
var secretStoreMethodArity = map[string]int{
	"Resolve":     2,
	"KnownValues": 1,
	"Set":         3,
	"Ensure":      2,
	"Rotate":      2,
	"ImportKey":   1,
}

var scriptReleasePublishRE = regexp.MustCompile(
	`(?m)` +
		`(?:\bdocker\s+(?:push|login|manifest\s+push|buildx\s+build[^\n]*--push)\b)` +
		`|(?:\bcrane\s+(?:push|copy|tag|mutate)\b)` +
		`|(?:\bskopeo\s+copy\b)` +
		`|(?:\bcosign\s+(?:sign|attest)\b)` +
		`|(?:\bopenssl\s+(?:pkeyutl\s+-sign|dgst\b[^\n]*-hmac)\b)` +
		`|(?:\bgcloud\s+(?:artifacts|container|kms)[^\n]*\b(?:push|sign|asymmetric-sign)\b)` +
		`|(?:\bgit\s+(?:-C\s+\S+\s+)?push\b)` +
		`|(?:/git/refs/tags/)` +
		`|(?:\bgh\s+release\s+(?:create|upload|edit)\b)` +
		`|(?:uploads\.github\.com)` +
		`|(?:/releases/[^\s"']+/assets)` +
		`|(?:\bgrain-agentd-sign\b)` +
		`|(?:\bAGENTD_RELEASE_SIGNING_KEY\b)` +
		`|(?:\bDOCKER_PAT\b)` +
		`|(?:pkg/version\.Release=)`)

func scanGoSource(fset *token.FileSet, filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	imports := map[string]string{}
	hasNetHTTP := false
	hasSecrets := false
	hasSecretsRepo := false
	for _, imp := range f.Imports {
		ip := strings.Trim(imp.Path.Value, `"`)
		switch ip {
		case "net/http":
			hasNetHTTP = true
		case secretsImportPath:
			hasSecrets = true
		case secretsRepoImportPath:
			hasSecretsRepo = true
		}
		local := filepath.Base(ip)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		imports[local] = ip
	}

	seen := map[string]bool{}
	add := func(reason string) { seen[reason] = true }

	if hasSecretsRepo {
		add("imports pkg/secretsrepo")
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if routeScopeIdents[x.Name] {
				add("references route table " + x.Name)
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				if imports[id.Name] == modelImportPath && x.Sel.Name == "Grant" {
					add("uses model.Grant")
				}
			}
		case *ast.FuncDecl:
			name := x.Name.Name
			if hasNetHTTP && name == "route" {
				add("declares HTTP route helper")
			}
			if grantPredicateFuncs[name] {
				add("declares grant predicate " + name)
			}
			if name == "Allows" && x.Type.Params != nil && len(x.Type.Params.List) > 0 {
				add("declares Allows(...)")
			}
			if name == "RequiresHuman" || name == "OwnerApproval" {
				add("declares " + name)
			}
		case *ast.CallExpr:
			switch fn := x.Fun.(type) {
			case *ast.Ident:
				if hasNetHTTP && fn.Name == "route" {
					add("registers HTTP route via route(...)")
				}
				if grantPredicateFuncs[fn.Name] {
					add("calls " + fn.Name + "(...)")
				}
				if fn.Name == "Allows" && len(x.Args) > 0 {
					add("calls Allows(...)")
				}
				if fn.Name == "RequiresHuman" || fn.Name == "OwnerApproval" {
					add("calls " + fn.Name + "(...)")
				}
			case *ast.SelectorExpr:
				sel := fn.Sel.Name
				if hasNetHTTP && (sel == "route" || sel == "HandleFunc" || sel == "Handle") {
					add("registers HTTP route via ." + sel + "(...)")
				}
				if sel == "Allows" && len(x.Args) > 0 {
					add("calls .Allows(...)")
				}
				if sel == "RequiresHuman" || sel == "OwnerApproval" {
					add("calls ." + sel + "(...)")
				}
				if grantPredicateMethods[sel] {
					add("calls ." + sel + "(...)")
				}
				if id, ok := fn.X.(*ast.Ident); ok {
					switch imports[id.Name] {
					case modelImportPath:
						if grantPredicateFuncs[sel] {
							add("calls model." + sel + "(...)")
						}
					case tbImportPath:
						if sel == "RequiresHuman" || sel == "OwnerApproval" {
							add("calls trustboundary." + sel + "(...)")
						}
					case secretsImportPath:
						if secretPkgFuncs[sel] {
							add("calls secrets." + sel + "(...)")
						}
					case "crypto/ed25519":
						if sel == "GenerateKey" || sel == "Sign" {
							add("mints/signs key via ed25519." + sel + "(...)")
						}
					case "crypto/ecdsa":
						if sel == "GenerateKey" || sel == "SignASN1" || sel == "Sign" {
							add("mints/signs key via ecdsa." + sel + "(...)")
						}
					case "crypto/rsa":
						if sel == "GenerateKey" || sel == "SignPKCS1v15" || sel == "SignPSS" {
							add("mints/signs key via rsa." + sel + "(...)")
						}
					case "crypto/x509":
						if sel == "CreateCertificate" {
							add("mints certificate via x509.CreateCertificate(...)")
						}
					case "crypto/hmac":
						if sel == "New" {
							add("signs/verifies MAC via hmac.New(...)")
						}
					}
				}
				if wantArgs, ok := secretStoreMethodArity[sel]; ok && hasSecrets && len(x.Args) == wantArgs {
					add("calls secret store ." + sel + "(...)")
				}
			}
		}
		return true
	})

	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out, nil
}

func scanScriptSource(src string) []string {
	var nonCommentLines []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		nonCommentLines = append(nonCommentLines, line)
	}
	matches := scriptReleasePublishRE.FindAllString(strings.Join(nonCommentLines, "\n"), -1)
	seen := map[string]bool{}
	for _, m := range matches {
		seen[strings.TrimSpace(m)] = true
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func matchAuthAllowlist(rel string) (string, bool) {
	for _, e := range unclassifiedAuthAllowlist {
		if strings.HasSuffix(e.path, "/") {
			if strings.HasPrefix(rel, e.path) {
				return e.path, true
			}
			continue
		}
		if rel == e.path {
			return e.path, true
		}
	}
	return "", false
}

func isTestdataPath(rel string) bool {
	return strings.Contains("/"+rel, "/testdata/")
}

// TestAuthorizationBearingFilesAreClassified walks every non-test Go file under
// pkg/ and cmd/ and every script under scripts/, failing CI if
// trustboundary.Classify leaves unclassified any file that registers HTTP
// routes or route scopes, defines or consumes grant/capability predicates,
// mints or reads secrets, or publishes/signs release artifacts or pushes tags
// or images (unless listed with a justification in unclassifiedAuthAllowlist).
func TestAuthorizationBearingFilesAreClassified(t *testing.T) {
	root := "../.."
	fset := token.NewFileSet()

	allowlistHits := map[string]int{}
	for _, e := range unclassifiedAuthAllowlist {
		if strings.TrimSpace(e.path) == "" || strings.TrimSpace(e.reason) == "" {
			t.Fatalf("allowlist entry %+v must have both path and reason", e)
		}
		allowlistHits[e.path] = 0
	}

	checkCandidate := func(rel string, signals []string) {
		if len(signals) == 0 {
			return
		}
		if len(Classify([]string{rel})) > 0 {
			if entry, ok := matchAuthAllowlist(rel); ok && entry == rel {
				t.Errorf("%s is now classified by trustboundary.Classify; remove it from unclassifiedAuthAllowlist", rel)
			}
			return
		}
		if entry, ok := matchAuthAllowlist(rel); ok {
			allowlistHits[entry]++
			return
		}
		t.Errorf("%s is left unclassified by trustboundary.Classify (%s): classify it in Categories or document a justified exception in unclassifiedAuthAllowlist",
			rel, strings.Join(signals, "; "))
	}

	for _, dir := range []string{"pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || isTestdataPath(rel) {
				return nil
			}
			signals, err := scanGoSource(fset, p, nil)
			if err != nil {
				t.Errorf("parse %s: %v", rel, err)
				return nil
			}
			checkCandidate(rel, signals)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	err := filepath.WalkDir(filepath.Join(root, "scripts"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, "_test.sh") || strings.HasSuffix(rel, ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("read %s: %v", rel, err)
			return nil
		}
		checkCandidate(rel, scanScriptSource(string(data)))
		return nil
	})
	if err != nil {
		t.Fatalf("walk scripts: %v", err)
	}

	for _, e := range unclassifiedAuthAllowlist {
		if allowlistHits[e.path] == 0 {
			t.Errorf("unclassifiedAuthAllowlist entry %q matched no unclassified authorization-bearing file; remove stale entry", e.path)
		}
	}
}

func TestAuthorizationScannerDetectsSyntheticInputs(t *testing.T) {
	fset := token.NewFileSet()

	goCases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "server route registration",
			src: `package ui
import "net/http"
func register(s *Server, h http.HandlerFunc) { s.route("GET /api/x", h) }`,
			want: "registers HTTP route via .route(...)",
		},
		{
			name: "routeScopes and humanRouteScopes",
			src: `package ui
func check(p string) { _ = routeScopes[p]; _ = humanRouteScopes[p]; _ = routeAuthorization[p] }`,
			want: "references route table routeScopes",
		},
		{
			name: "model.Grant and CanonicalGrant",
			src: `package foo
import "github.com/ai-on-gke/grain/pkg/model"
func norm(g model.Grant) model.Grant { return model.CanonicalGrant(g.Capability) }`,
			want: "calls model.CanonicalGrant(...)",
		},
		{
			name: "Allows predicate with arguments",
			src: `package foo
func gate(set grantSet, repo string) bool { return set.Allows("tasks:read", repo) }`,
			want: "calls .Allows(...)",
		},
		{
			name: "RequiresHuman predicate",
			src: `package foo
import "github.com/ai-on-gke/grain/pkg/trustboundary"
func held(ms []trustboundary.Match) bool { return trustboundary.RequiresHuman(ms) }`,
			want: "calls trustboundary.RequiresHuman(...)",
		},
		{
			name: "secrets store Resolve and Set",
			src: `package foo
import (
	"context"
	"github.com/ai-on-gke/grain/pkg/secrets"
)
func rw(ctx context.Context, s *secrets.Store) {
	_, _ = s.Resolve(ctx, "tok")
	_ = s.Set("tok", "key", []byte("val"))
}`,
			want: "calls secret store .Resolve(...)",
		},
		{
			name: "benign zero-arg OverlapPolicy.Allows and http.Header.Set",
			src: `package foo
import (
	"net/http"
	"github.com/ai-on-gke/grain/pkg/secrets"
)
type overlap struct{}
func (overlap) Allows() bool { return true }
func plain(h http.Header, o overlap, _ *secrets.Store) bool {
	h.Set("X-Test", "1")
	return o.Allows()
}`,
			want: "",
		},
	}

	for _, tc := range goCases {
		got, err := scanGoSource(fset, tc.name+".go", tc.src)
		if err != nil {
			t.Fatalf("%s: unexpected parse error: %v", tc.name, err)
		}
		joined := strings.Join(got, "; ")
		if tc.want == "" && len(got) != 0 {
			t.Errorf("%s: got signals %v, want none", tc.name, got)
		}
		if tc.want != "" && !strings.Contains(joined, tc.want) {
			t.Errorf("%s: got signals %q, want substring %q", tc.name, joined, tc.want)
		}
	}

	scriptCases := []struct {
		name string
		src  string
		want bool
	}{
		{name: "docker push", src: "#!/usr/bin/env bash\ndocker push grainai/grain:v1.0.0\n", want: true},
		{name: "cosign sign", src: "#!/usr/bin/env bash\ncosign sign --yes \"$img\"\n", want: true},
		{name: "git push tag", src: "#!/usr/bin/env bash\ngit push -q \"$remote\" \"refs/tags/$version\"\n", want: true},
		{name: "release binary stamp", src: "#!/usr/bin/env bash\ngo build -ldflags \"-X github.com/ai-on-gke/grain/pkg/version.Release=$v\" ./cmd/grain\n", want: true},
		{name: "agentd signing", src: "#!/usr/bin/env bash\n\"$SIGN_TOOL\" manifest -signing-key \"$AGENTD_RELEASE_SIGNING_KEY\"\n", want: true},
		{name: "comment only", src: "#!/usr/bin/env bash\n# docker push example\n# git push --tags\necho ok\n", want: false},
	}

	for _, tc := range scriptCases {
		got := scanScriptSource(tc.src)
		if tc.want && len(got) == 0 {
			t.Errorf("script %s: got no signals, want match (%s)", tc.name, fmt.Sprintf("%q", tc.src))
		}
		if !tc.want && len(got) > 0 {
			t.Errorf("script %s: got %v, want no signals", tc.name, got)
		}
	}
}
