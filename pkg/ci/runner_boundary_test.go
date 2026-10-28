package ci

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ciBuilders are the files outside this package allowed to name
// *ci.Service: the code that builds one. Everything else takes ci.Runner
// (docs/design/grit-split.md#GS-1), which is what lets the service move
// out of the process (grit.Client) without its callers changing.
var ciBuilders = map[string]string{
	// The daemon's wiring: BuildCIService, the grant admission it hangs
	// off the engine, and LiveCI's handle on the in-process service.
	"pkg/daemon/ci.go":                 "builds the daemon's CI service",
	"pkg/daemon/ci_capability_pass.go": "wires the built service's grant admission",
	// The demo's observability scene runs a real engine against a fake
	// sandbox backend, so it builds the service the daemon would.
	"pkg/demo/demo_observability.go": "builds the observability demo's CI service",
}

// TestOnlyBuildersNameTheCIService: no non-test file outside pkg/ci names
// ci.Service, apart from the builders above. A caller that needs
// something of CI asks ci.Runner; a type assertion on *ci.Service is how
// the tree used to reach past that, and is what this test exists to keep
// out.
func TestOnlyBuildersNameTheCIService(t *testing.T) {
	uses := namedOutside(t, "github.com/bwsalmon/grain/pkg/ci", "Service", "pkg/ci/")
	var offending []string
	for _, use := range uses {
		if _, ok := ciBuilders[use.file]; !ok {
			offending = append(offending, use.String())
		}
	}
	if len(offending) > 0 {
		t.Errorf("these name *ci.Service; take ci.Runner instead (or, for code that builds the service, list the file in ciBuilders):\n  %s",
			strings.Join(offending, "\n  "))
	}
	// A builder that no longer builds anything should come off the list,
	// or the exemption outlives its reason.
	seen := map[string]bool{}
	for _, use := range uses {
		seen[use.file] = true
	}
	for file, why := range ciBuilders {
		if !seen[file] {
			t.Errorf("%s is exempt (%s) but no longer names ci.Service: take it off ciBuilders", file, why)
		}
	}
}

// TestOnlyTheDaemonNamesThePackageCache: outside pkg/pkgcache, only the
// daemon's wiring names pkgcache.Cache (or opens one). The package
// cache's server takes a pkgcache.Store and the daemon administers it
// through a pkgcache.Admin, so the store can move into the cluster
// without anything else changing.
func TestOnlyTheDaemonNamesThePackageCache(t *testing.T) {
	var offending []string
	for _, name := range []string{"Cache", "NewCache", "Writer"} {
		for _, use := range namedOutside(t, "github.com/bwsalmon/grain/pkg/pkgcache", name, "pkg/pkgcache/") {
			if use.file != "pkg/daemon/packagecache.go" {
				offending = append(offending, use.String())
			}
		}
	}
	if len(offending) > 0 {
		t.Errorf("these name pkgcache's on-disk cache; take pkgcache.Store or pkgcache.Admin instead:\n  %s",
			strings.Join(offending, "\n  "))
	}
}

// namedUse is one place a non-test file names pkg.name.
type namedUse struct {
	file string
	line int
	expr string
}

func (u namedUse) String() string { return u.file + ":" + strconv.Itoa(u.line) + ": " + u.expr }

// namedOutside is every place a non-test Go file in this repository,
// outside the directory prefix own, names the identifier name exported
// by the package at importPath -- under whatever name the file imports
// it as.
func namedOutside(t *testing.T, importPath, name, own string) []namedUse {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var uses []namedUse
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, own) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		alias := ""
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == importPath {
				alias = filepath.Base(importPath)
				if imp.Name != nil {
					alias = imp.Name.Name
				}
			}
		}
		if alias == "" || alias == "_" {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != name {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == alias {
				uses = append(uses, namedUse{file: rel, line: fset.Position(sel.Pos()).Line, expr: alias + "." + name})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(uses) == 0 && name == "Service" {
		// The daemon always builds one, so finding none means this walk
		// is looking in the wrong place rather than that the tree is clean.
		t.Fatalf("found no use of %s.%s at all under %s: is the walk rooted at the repository?", importPath, name, root)
	}
	sort.Slice(uses, func(i, j int) bool { return uses[i].String() < uses[j].String() })
	return uses
}
