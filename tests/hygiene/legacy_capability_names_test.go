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

package hygiene

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Retired capability names (docs/capabilities.md, model.CanonicalLegacyGrant)
// are still read for one release, so a declaration or a sentence that names
// one keeps working -- and keeps teaching whoever reads it to ask for a grant
// that no longer exists under that name (grain/task-4366: agents asking for
// grain-ci long after it became https://grain.dev/capabilities/grain#ci).
// TestNoLegacyCapabilityNames refuses a retired name in the places agents
// and people read as current:
//
//   - everything under .grain/ (YAML comments excepted: they may explain
//     history), the declarations and prompts grain reads from this
//     repository, and the bootstrap playbooks (pkg/playbooks) agents read;
//   - every string literal of grain's non-test Go code under pkg/ and cmd/:
//     agent prompts (pkg/orchestrator's sections, pkg/model's templates),
//     tool and CLI help, and the errors and labels people see.
//
// A sentence that names a retired spelling to explain where a grant came
// from says so ("formerly grain-ci", "the retired gcp-key:<name>"), and
// historyMarker lets it through. Migration and alias code has to spell the
// names it rewrites, and legacyNameExempt lists it.
//
// TestLegacyCapabilityNameInventory is the inventory this test was built
// from: run with GRAIN_LEGACY_INVENTORY=1 it prints every mention in the
// repository -- docs and tests included -- with how it is classified.

// legacyUnambiguous are retired names nothing current is called: any
// mention as a word is a retired spelling.
var legacyUnambiguous = regexp.MustCompile(
	`(?:^|[^\w#/.\-:@$])(` +
		`grain-ci|claude-proxy|openai-proxy|gemini-proxy|bootstrap-playbooks|` +
		`github-sandbox|grain-log|deployment-health|db-read|db-write|self-repair|` +
		`corp-research|git-push-any-ref|priority-dispatch|` +
		`gcp-key:[\w<>-]+|github-credential:[\w<>-]+|local-scripts:[\w<>-]+|` +
		`repo-read:[\w<>./-]*|repo-write:[\w<>./-]*|` +
		`api:[a-z<][\w<>:./-]*(?:@[\w<>./-]+)?` +
		`)(?:$|[^\w\-.\[(=/:])`)

// legacyMethodNames are retired bare names that are also the name of the
// method or preset they became (gcp#gcp-key, release#package-publish,
// api#self-debug, ...). "the gcp-key method" is current; a bare one is
// refused where it is granted or asked for as a capability.
// local-scripts is here too: the declaration an operator writes is still
// called that, and only the capability (local-scripts:<name>, now
// host-script grants) is retired.
const legacyMethodNames = `gcp-key|gemini-key|image-signing|registry-push|package-publish|read-issues|self-debug|release-gate-read|local-scripts`

var (
	// legacyMethodAsCapability is one of legacyMethodNames used as a
	// capability in prose: "the self-debug capability", "grant gcp-key",
	// "holds package-publish".
	legacyMethodAsCapability = regexp.MustCompile(
		`(?i)(?:\b(?:grant(?:s|ed|ing)?|hold(?:s|ing)?|need(?:s|ed)?|require(?:s|d)?|attach(?:es|ed)?|declare(?:s|d)?|ask(?:s|ed)? for)\s+(?:the\s+|a\s+|an\s+)?` + "`?" + `(` + legacyMethodNames + `)` + "`?" + `(?:$|[^\w\-#/.(]))|` +
			`(?:(?:^|[^\w#/.\-:@])` + "`?" + `(` + legacyMethodNames + `)` + "`?" + `\s+(?:CI\s+)?(?:capability|capabilities|grant)\b)`)
	// legacyMethodBare is one of legacyMethodNames as a word, which in a
	// .grain/ declaration value is the retired bare grant.
	legacyMethodBare = regexp.MustCompile(`(?:^|[^\w#/.\-:@$])(` + legacyMethodNames + `)(?:$|[^\w\-.\[(=/:])`)

	// historyMarker marks text that names a retired spelling as history.
	historyMarker = regexp.MustCompile(`(?i)formerly|retired|legacy|alias|renamed|migrat|deprecated|historical|older spelling|old name|until it became|became`)

	// legacyOtherMeaning is text where a retired name means something
	// else: grain-ci is also the name of a Kubernetes PriorityClass for CI
	// sandboxes and of the git identity CI commits as.
	legacyOtherMeaning = regexp.MustCompile(`PriorityClass|(?i)priority class|user\.name|\[bot\]`)

	// declarationKey is a .grain/ YAML line whose value is a capability.
	declarationKey = regexp.MustCompile(`^\s*(?:-\s*)?(?:capabilities|optional-capabilities|capability|grants)\s*:`)
)

// legacyNameExempt are the tracked paths (a trailing / for a directory)
// whose job is to spell retired names: the migrations that rewrite them,
// the alias tables that still read them, and the providers that convert
// a legacy declaration.
var legacyNameExempt = []string{
	"pkg/model/migrations.go",
	"pkg/model/schema.go",
	"pkg/model/legacy_grant_migration.go",
	"pkg/model/canonical_grant_migration.go",
	"pkg/model/gcp_key_migration.go",
	"pkg/model/gemini_key_migration.go",
	"pkg/model/release_migration.go",
	"pkg/model/model_proxy_migration.go",
	"pkg/model/self_debug_migration.go",
	"pkg/model/read_issues_migration.go",
	"pkg/model/bootstrap_playbooks_migration.go",
	"pkg/model/priority_dispatch.go",
	"pkg/capability/localscripts/",
	"pkg/daemon/localscripts_migrate.go",
	// LegacyOwnerDescription reproduces, byte for byte, the description
	// older gcp#gcp-key service accounts were created with.
	"pkg/capability/gcpsets/naming.go",
	"tests/hygiene/legacy_capability_names_test.go",
}

// legacyNameExactRefused are the tracked files where legacyNameExact
// does not apply, so a literal that is exactly a retired name is refused
// too. These files hold the grants grain ships rather than reads: the
// Director's defaults, the starter local policy init writes, the Settings
// capability rows, and the release-server providers whose CI publish
// grant the sandbox guest cache waits on. They spell the canonical
// reference and nothing older (grain/task-4482). The bare method names
// those providers keep as their registry key (image-signing, ...) are
// not retired spellings in Go literals, and stay.
var legacyNameExactRefused = []string{
	"pkg/cli/init_workstation.go",
	"pkg/model/console_session.go",
	"pkg/model/sandbox_guest_cache.go",
	"pkg/ui/capability_status.go",
	"pkg/capability/packagepublish/packagepublish.go",
	"pkg/capability/imagesigning/imagesigning.go",
	"pkg/capability/registrypush/registrypush.go",
}

// legacyNameExact reports a literal that is exactly a retired bare name
// (or a bare prefix such as "repo-read:"): the keys of alias tables
// (model.CanonicalLegacyGrant's) and the constants that name them, which
// say nothing to anyone by themselves. A whole legacy grant such as
// "api:tasks:read" is not one of these: a literal like that is what a
// refusal names, so it is checked.
func legacyNameExact(lit, name string) bool {
	if strings.TrimSpace(lit) != name {
		return false
	}
	before, after, colon := strings.Cut(name, ":")
	return !colon || after == "" || before == "gcp-key" || before == "github-credential" || before == "local-scripts"
}

func legacyExempt(rel string) bool {
	for _, p := range legacyNameExempt {
		if rel == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p)) {
			return true
		}
	}
	return false
}

type legacyHit struct {
	rel   string
	line  int
	name  string
	text  string
	class string
}

// legacyNamesIn is every retired name text mentions. bare also reports
// legacyMethodNames used as a bare word (a .grain/ declaration value),
// not only as a capability in prose.
func legacyNamesIn(text string, bare bool) []string {
	var out []string
	for _, m := range legacyUnambiguous.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	if bare {
		for _, m := range legacyMethodBare.FindAllStringSubmatch(text, -1) {
			out = append(out, m[1])
		}
		return out
	}
	for _, m := range legacyMethodAsCapability.FindAllStringSubmatch(text, -1) {
		if m[1] != "" {
			out = append(out, m[1])
		} else {
			out = append(out, m[2])
		}
	}
	return out
}

// goStringHits is every retired name in the string literals of a Go file.
func goStringHits(rel string, src []byte) []legacyHit {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil
	}
	var hits []legacyHit
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			// A struct tag names a declaration key (`yaml:"grain-ci"`,
			// a schedule's grain-ci: true), not a capability: walk
			// the field's type and skip its tag.
			if n.Type != nil {
				ast.Inspect(n.Type, visit)
			}
			return false
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			for _, name := range legacyNamesIn(s, false) {
				if !legacyNameExact(s, name) || slices.Contains(legacyNameExactRefused, rel) {
					hits = append(hits, legacyHit{rel: rel, line: fset.Position(n.Pos()).Line, name: name, text: s})
				}
			}
		}
		return true
	}
	ast.Inspect(f, visit)
	return hits
}

// grainFileHits is every retired name in a .grain/ file: YAML comments are
// skipped, a declaration line is checked for bare method names too, and a
// hit's text carries the line before it, so a sentence wrapped across the
// two that says it is history counts as history.
func grainFileHits(rel string, data []byte) []legacyHit {
	var hits []legacyHit
	yaml := strings.HasSuffix(rel, ".yml") || strings.HasSuffix(rel, ".yaml")
	prev := ""
	for i, line := range strings.Split(string(data), "\n") {
		if yaml {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if j := strings.Index(line, " #"); j >= 0 && !strings.ContainsAny(line[:j], `"'`) {
				line = line[:j]
			}
		}
		for _, name := range legacyNamesIn(line, yaml && declarationKey.MatchString(line)) {
			hits = append(hits, legacyHit{rel: rel, line: i + 1, name: name, text: prev + "\n" + line})
		}
		prev = line
	}
	return hits
}

// grainDeclarationListHits catches a bare method name as an item of a
// block-style capabilities list:
//
//	capabilities:
//	  - package-publish
func grainDeclarationListHits(rel string, data []byte) []legacyHit {
	var hits []legacyHit
	inList := false
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case declarationKey.MatchString(line) && strings.HasSuffix(trimmed, ":"):
			inList = true
			continue
		case inList && strings.HasPrefix(trimmed, "- "):
			item := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), `"'`)
			for _, m := range legacyMethodBare.FindAllStringSubmatch(item, -1) {
				hits = append(hits, legacyHit{rel: rel, line: i + 1, name: m[1], text: line})
			}
			continue
		case inList && (trimmed == "" || strings.HasPrefix(trimmed, "#")):
			continue
		}
		inList = false
	}
	return hits
}

func TestNoLegacyCapabilityNames(t *testing.T) {
	root := repoRoot(t)
	var bad []legacyHit
	for _, rel := range trackedFiles(t, root) {
		if legacyExempt(rel) {
			continue
		}
		// pkg/playbooks is the bootstrap config map an agent reads
		// (config-map#config-map {config_maps: [bootstrap]}).
		grain := strings.HasPrefix(rel, ".grain/") || strings.HasPrefix(rel, "pkg/playbooks/") && strings.HasSuffix(rel, ".md")
		goSrc := strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") &&
			(strings.HasPrefix(rel, "pkg/") || strings.HasPrefix(rel, "cmd/"))
		if !grain && !goSrc {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		var hits []legacyHit
		if goSrc {
			hits = goStringHits(rel, data)
		} else {
			hits = append(grainFileHits(rel, data), grainDeclarationListHits(rel, data)...)
		}
		for _, h := range hits {
			if historyMarker.MatchString(h.text) || legacyOtherMeaning.MatchString(h.text) {
				continue
			}
			bad = append(bad, h)
		}
	}
	for _, h := range bad {
		t.Errorf("%s:%d names the retired capability %q: use its canonical reference (docs/capabilities.md), "+
			"or say it is history (\"formerly %s\")", h.rel, h.line, h.name, h.name)
	}
}

// legacyHistoricalRecords are trees that record what was true when each
// file was written -- test runs and plans, release notes, security audits,
// design records -- so a retired name in them is history by definition.
var legacyHistoricalRecords = []string{
	"docs/test-runs/", "docs/test-plans/", "docs/releases/", "docs/security/audits/", "design/",
}

func legacyHistoricalRecord(rel string) bool {
	for _, p := range legacyHistoricalRecords {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// classifyLegacyHit is the inventory's class for a mention.
func classifyLegacyHit(h legacyHit) string {
	historical := historyMarker.MatchString(h.text)
	switch {
	case legacyExempt(h.rel):
		return "migration or alias code (OK)"
	case strings.HasSuffix(h.rel, "_test.go") || strings.Contains(h.rel, "/testdata/") || strings.HasPrefix(h.rel, "ui") && strings.Contains(h.rel, "test"):
		return "test fixture"
	case strings.HasPrefix(h.rel, ".grain/") && declarationKey.MatchString(h.text):
		return "live declaration"
	case strings.HasPrefix(h.rel, ".grain/templates/") || strings.HasPrefix(h.rel, "pkg/orchestrator/") ||
		strings.HasPrefix(h.rel, "pkg/model/") && strings.Contains(h.rel, "template"):
		if historical {
			return "agent prompt, history (OK)"
		}
		return "agent prompt or tool text"
	case legacyHistoricalRecord(h.rel):
		return "doc history (OK)"
	case strings.HasPrefix(h.rel, "docs/") || strings.HasPrefix(h.rel, "design/") || strings.HasSuffix(h.rel, ".md"):
		if historical {
			return "doc history (OK)"
		}
		return "doc presented as current"
	case strings.HasSuffix(h.rel, ".go"):
		if historical {
			return "user-facing string, history (OK)"
		}
		return "user-facing UI, API or CLI string"
	default:
		if historical {
			return "other, history (OK)"
		}
		return "other"
	}
}

// TestLegacyCapabilityNameInventory prints, with GRAIN_LEGACY_INVENTORY=1,
// every retired capability name in the repository and its class. It is the
// script behind TestNoLegacyCapabilityNames and checks nothing itself.
func TestLegacyCapabilityNameInventory(t *testing.T) {
	if os.Getenv("GRAIN_LEGACY_INVENTORY") == "" {
		t.Skip("set GRAIN_LEGACY_INVENTORY=1 to print the inventory")
	}
	root := repoRoot(t)
	var hits []legacyHit
	for _, rel := range trackedFiles(t, root) {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		if strings.HasSuffix(rel, ".go") {
			hits = append(hits, goStringHits(rel, data)...)
			continue
		}
		for n, line := range strings.Split(string(data), "\n") {
			for _, name := range legacyNamesIn(line, strings.HasPrefix(rel, ".grain/") && declarationKey.MatchString(line)) {
				hits = append(hits, legacyHit{rel: rel, line: n + 1, name: name, text: line})
			}
		}
	}
	counts := map[string]int{}
	for i := range hits {
		hits[i].class = classifyLegacyHit(hits[i])
		counts[hits[i].class]++
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].class < hits[j].class })
	for _, h := range hits {
		text := strings.TrimSpace(h.text)
		if len(text) > 160 {
			text = text[:160] + "…"
		}
		fmt.Printf("%s\t%s:%d\t%s\t%s\n", h.class, h.rel, h.line, h.name, text)
	}
	classes := make([]string, 0, len(counts))
	for c := range counts {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	for _, c := range classes {
		fmt.Printf("# %5d  %s\n", counts[c], c)
	}
}

// TestLegacyNameMatcherCatchesWhatItShould pins what the matcher behind
// TestNoLegacyCapabilityNames reports, so a loosened pattern cannot let
// the retired spellings back in silently.
func TestLegacyNameMatcherCatchesWhatItShould(t *testing.T) {
	for _, tc := range []struct {
		text string
		bare bool
		want string
	}{
		{text: "capabilities: [grain-ci]", bare: true, want: "grain-ci"},
		{text: "capabilities: [package-publish]", bare: true, want: "package-publish"},
		{text: `capabilities: ["repo-write:acme/widgets"]`, bare: true, want: "repo-write:acme/widgets"},
		{text: "grant api:ci-read@acme/widgets to read CI", want: "api:ci-read@acme/widgets"},
		{text: "you hold the self-debug capability", want: "self-debug"},
		{text: "stop granting gcp-key here", want: "gcp-key"},
		{text: "holds no local-scripts capability", want: "local-scripts"},
		{text: "a gcp-key:prod key", want: "gcp-key:prod"},
		{text: "needs grain-log", want: "grain-log"},
		{text: `capabilities: ["https://grain.dev/capabilities/release#package-publish"]`, bare: true},
		{text: "https://grain.dev/capabilities/gcp#gcp-key", bare: true},
		{text: "the gcp server's gcp-key method"},
		{text: "grain-ci: true"},
		{text: "/home/debian/.package-publish-token", bare: true},
		{text: "docs/image-signing.md"},
		{text: "a local-scripts declaration"},
		{text: "release#package-publish publishes the images"},
		{text: "-disable-self-upgrade"},
	} {
		got := legacyNamesIn(tc.text, tc.bare)
		switch {
		case tc.want == "" && len(got) > 0:
			t.Errorf("legacyNamesIn(%q) = %q, want nothing", tc.text, got)
		case tc.want != "" && (len(got) == 0 || got[0] != tc.want):
			t.Errorf("legacyNamesIn(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
	// An exact retired name is an alias table's key anywhere else, and
	// refused in the files that ship grants.
	src := []byte("package p\n\nconst c = \"github-sandbox\"\n")
	if hits := goStringHits("pkg/other/other.go", src); len(hits) != 0 {
		t.Errorf("goStringHits refused an exact retired name outside legacyNameExactRefused: %+v", hits)
	}
	for _, rel := range legacyNameExactRefused {
		if hits := goStringHits(rel, src); len(hits) != 1 {
			t.Errorf("goStringHits(%s) = %+v, want the exact retired name refused", rel, hits)
		}
	}
	if !historyMarker.MatchString("grain#ci (formerly grain-ci)") {
		t.Error(`historyMarker does not let "formerly grain-ci" through`)
	}
}
