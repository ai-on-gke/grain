package grit

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const module = "github.com/bwsalmon/grain/"

// gritAllowed is every package of this module that grit -- cmd/grit and
// pkg/grit/... -- may link (docs/design/grit-split.md, "The import
// lint"): grit itself, the neutral packages, and leaf utilities holding
// no grain state. It is an allowlist rather than a denylist so that a
// new grain package is off-limits to grit by default. Adding to it is a
// reviewed change to this file: the package must import nothing of
// grain's own, which TestNeutralPackagesStayNeutral checks for the ones
// marked neutral.
var gritAllowed = []string{
	"cmd/grit",
	"pkg/grit",
	"pkg/grit/...",

	// Neutral packages, shared by grain and grit.
	"pkg/attest",
	"pkg/gritapi",
	"pkg/granule",
	"pkg/granule/...",
	"pkg/treeid",
	"pkg/workflows",
	"pkg/workflows/...",

	// Leaf utilities.
	"pkg/actionscache",
	"pkg/jwks",
	"pkg/pkgcache",
	"pkg/pkgcache/...",
	"pkg/redact",
	"pkg/tunnel",
	"pkg/tunnel/...",
	"pkg/version",
}

// neutral are the packages both sides import. On top of grit's
// allowlist, none of them may import grain's core.
var neutral = []string{"pkg/attest", "pkg/gritapi", "pkg/granule", "pkg/treeid", "pkg/workflows"}

// grainCore is what a neutral package must never import, even through
// a dependency the allowlist would otherwise let it have.
var grainCore = []string{"pkg/model", "pkg/orchestrator", "pkg/daemon", "pkg/ci", "pkg/ui", "pkg/mcp", "pkg/capability"}

func matches(pkg string, patterns []string) bool {
	for _, p := range patterns {
		if base, ok := strings.CutSuffix(p, "/..."); ok {
			if pkg == base || strings.HasPrefix(pkg, base+"/") {
				return true
			}
		} else if pkg == p {
			return true
		}
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// deps is `go list -deps` over patterns, for this module's packages
// only, keyed by package and naming the root that pulled each one in.
func deps(t *testing.T, patterns ...string) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	out := map[string][]string{}
	for _, p := range patterns {
		// A neutral package the allowlist names before it has been
		// written (pkg/gritapi, pkg/workflows) has nothing to check yet.
		if _, err := os.Stat(filepath.Join(root, strings.TrimSuffix(p, "/..."))); errors.Is(err, os.ErrNotExist) {
			continue
		}
		cmd := exec.Command("go", "list", "-deps", "-test=false", "./"+p)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		b, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps ./%s: %v\n%s", p, err, stderr.String())
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if rel, ok := strings.CutPrefix(sc.Text(), module); ok {
				out[rel] = append(out[rel], p)
			}
		}
	}
	return out
}

// gritViolations are the dependencies grit may not have.
func gritViolations(deps map[string][]string) []string {
	var bad []string
	for pkg, from := range deps {
		if !matches(pkg, gritAllowed) {
			bad = append(bad, pkg+" (via "+strings.Join(from, ", ")+")")
		}
	}
	sort.Strings(bad)
	return bad
}

// neutralViolations are the dependencies a neutral package may not have.
func neutralViolations(deps map[string][]string) []string {
	var bad []string
	for pkg, from := range deps {
		if matches(pkg, grainCore) || !matches(pkg, gritAllowed) {
			bad = append(bad, pkg+" (via "+strings.Join(from, ", ")+")")
		}
	}
	sort.Strings(bad)
	return bad
}

func TestGritImportsOnlyWhatItsAllowlistNames(t *testing.T) {
	for _, b := range gritViolations(deps(t, "cmd/grit", "pkg/grit/...")) {
		t.Errorf("grit links %s, which is not on gritAllowed: grit may not depend on grain-only code (docs/design/grit-split.md, \"The import lint\")", b)
	}
}

func TestNeutralPackagesStayNeutral(t *testing.T) {
	var roots []string
	for _, n := range neutral {
		roots = append(roots, n+"/...")
	}
	for _, b := range neutralViolations(deps(t, roots...)) {
		t.Errorf("a neutral package links %s, which grit may not", b)
	}
}

// The lint has to fail when it should: these are the dependency lists
// `go list -deps` would give for a grit package and a neutral one that
// reached into grain.
func TestTheLintFailsOnAGrainOnlyImport(t *testing.T) {
	grit := map[string][]string{"pkg/grit/actions": {"pkg/grit/..."}, "pkg/ci": {"pkg/grit/..."}, "pkg/redact": {"cmd/grit"}}
	if got := gritViolations(grit); len(got) != 1 || !strings.HasPrefix(got[0], "pkg/ci ") {
		t.Errorf("gritViolations = %v, want pkg/ci alone", got)
	}
	neutral := map[string][]string{"pkg/treeid": {"pkg/treeid/..."}, "pkg/model": {"pkg/workflows/..."}, "pkg/orchestrator": {"pkg/attest/..."}}
	if got := neutralViolations(neutral); len(got) != 2 {
		t.Errorf("neutralViolations = %v, want pkg/model and pkg/orchestrator", got)
	}
}

// The lint is only worth something if it fails: a grain-only package
// must not match, and the allowlist must not swallow its neighbours.
func TestTheAllowlistIsNotAPrefixMatch(t *testing.T) {
	for _, pkg := range []string{"pkg/ci", "pkg/ci/cli", "pkg/model", "pkg/daemon", "pkg/gritapix", "pkg/tunnelx", "pkg/pkgcachefoo", "cmd/grain"} {
		if matches(pkg, gritAllowed) {
			t.Errorf("%s matches gritAllowed", pkg)
		}
	}
	for _, pkg := range []string{"pkg/grit/actions", "pkg/tunnel/mux", "pkg/treeid", "cmd/grit"} {
		if !matches(pkg, gritAllowed) {
			t.Errorf("%s does not match gritAllowed", pkg)
		}
	}
	if !matches("pkg/ci", grainCore) {
		t.Error("pkg/ci is not grain core")
	}
}
