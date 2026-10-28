package security

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A ratchet rule is one the tree does not yet keep everywhere. Its
// current violations are counted per file in a committed baseline,
// .grain/baselines/<rule>.txt, and the test holds the tree to it in both
// directions:
//
//   - a file with more violations than its baseline line -- or one with
//     none -- fails, naming the new sites: new code keeps the rule;
//   - a file with fewer fails too, asking for the baseline to come down,
//     so a fix is recorded and cannot quietly be undone by the next change.
//
// The count only goes down. A baseline line is "<path> <count>", with #
// comments; a failure prints the baseline the tree actually has, so
// lowering it is a copy. Raising it is a reviewed diff to a file whose
// only purpose is to say "this is allowed", which is the point.
type ratchetRule struct {
	name string
	why  string
	// scan returns every violation in the tree, as "path:line: detail"
	// keyed by the slash-separated path relative to the repository root.
	scan func(t *testing.T, root string) map[string][]string
}

var ratchetRules = []ratchetRule{
	{
		name: "exec-env-implicit",
		why: "a child process started with exec.Command inherits the daemon's whole environment -- " +
			"its API keys, tokens and cloud credentials included -- unless cmd.Env is set. Set Env " +
			"explicitly in the function that builds the command (an explicit append(os.Environ(), ...) " +
			"is a decision; leaving it nil is not).",
		scan: scanExecEnv,
	},
	{
		name: "credential-client-defaults",
		why: "the packages that forward grain's credentials (gitproxy, modelproxy, pkgcache, github, " +
			"tunnel) must not use http.DefaultClient or the package-level http.Get/Post/Head: " +
			"that client follows redirects to any host with the request's headers and never times " +
			"out. Build an *http.Client with CheckRedirect and Timeout set.",
		scan: scanCredentialClients,
	},
	{
		name: "build-tls-disabled",
		why: "an image build or script that turns TLS verification off (curl -k/--insecure, wget " +
			"--no-check-certificate, apt Verify-Peer \"false\") installs whatever the network hands " +
			"it. Verify the peer, and pin a checksum for anything downloaded.",
		scan: scanBuildTLS,
	},
	{
		name: "git-argv-separator",
		why: "a variable handed to git ahead of \"--\" is read as an option if it starts with a dash: a " +
			"branch named --upload-pack=... or a path named --output=... runs or writes what it says " +
			"(#2420). Put \"--end-of-options\" before revisions and \"--\" before paths, or start the " +
			"argument with a fixed prefix (\"refs/heads/\"+b) that cannot be an option.",
		scan: scanGitArgv,
	},
	{
		name: "path-containment",
		why: "strings.HasPrefix(filepath.Clean(p), root) is a lexical check: a symlink under root that " +
			"points out of it passes, and without a trailing separator so does root-other (#2410, " +
			"#2393). Ask safepath.Within(root, p), which resolves symlinks, and use the path it returns.",
		scan: scanPathContainment,
	},
}

// TestRatchetRules is Layer 1's ratchet: see ratchetRule.
func TestRatchetRules(t *testing.T) {
	root := repoRoot(t)
	for _, rule := range ratchetRules {
		t.Run(rule.name, func(t *testing.T) {
			baselinePath := filepath.Join(root, ".grain", "baselines", rule.name+".txt")
			baseline, err := readBaseline(baselinePath)
			if err != nil {
				t.Fatalf("reading the baseline: %v", err)
			}
			found := rule.scan(t, root)
			checkRatchet(t, rule, baseline, found)
		})
	}
}

// reporter is the part of *testing.T checkRatchet uses, so the ratchet
// can be tested against a recorder.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
}

func checkRatchet(t reporter, rule ratchetRule, baseline map[string]int, found map[string][]string) {
	t.Helper()
	paths := map[string]bool{}
	for p := range baseline {
		paths[p] = true
	}
	for p := range found {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	failed := false
	for _, p := range sorted {
		want, got := baseline[p], len(found[p])
		switch {
		case got > want:
			if !failed {
				t.Errorf("%s: %s", rule.name, rule.why)
			}
			failed = true
			t.Errorf("%s: %d violation(s), baseline allows %d:", p, got, want)
			for _, site := range found[p] {
				t.Errorf("\t%s", site)
			}
		case got < want:
			failed = true
			t.Errorf("%s: %d violation(s), baseline says %d -- the count went down; lower the "+
				"baseline so it cannot go back up", p, got, want)
		}
	}
	if failed {
		var b strings.Builder
		for _, p := range sorted {
			if n := len(found[p]); n > 0 {
				fmt.Fprintf(&b, "%s %d\n", p, n)
			}
		}
		t.Logf("the tree's actual baseline for .grain/baselines/%s.txt:\n%s", rule.name, b.String())
	}
}

// readBaseline parses "<path> <count>" lines. A missing file is an empty
// baseline: a rule the tree already keeps everywhere needs none.
func readBaseline(path string) (map[string]int, error) {
	out := map[string]int{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: want \"<path> <count>\", got %q", path, n, line)
		}
		count, err := strconv.Atoi(fields[1])
		if err != nil || count <= 0 {
			return nil, fmt.Errorf("%s:%d: count %q is not a positive integer", path, n, fields[1])
		}
		if _, dup := out[fields[0]]; dup {
			return nil, fmt.Errorf("%s:%d: %s is listed twice", path, n, fields[0])
		}
		out[fields[0]] = count
	}
	return out, sc.Err()
}

// parseGo parses one file for the AST rules, with the path it is keyed by.
func parseGo(t *testing.T, root, path string) (*ast.File, *token.FileSet, string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	rel, _ := filepath.Rel(root, path)
	return f, fset, filepath.ToSlash(rel)
}

// importName is what file f calls the package at importPath, or "" when
// it does not import it. A dot or blank import is reported as "".
func importName(f *ast.File, importPath string) string {
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != importPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return ""
			}
			return imp.Name.Name
		}
		return importPath[strings.LastIndexByte(importPath, '/')+1:]
	}
	return ""
}

// isPkgSel reports whether e is pkg.<one of names>.
func isPkgSel(e ast.Expr, pkg string, names ...string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != pkg {
		return false
	}
	for _, n := range names {
		if sel.Sel.Name == n {
			return true
		}
	}
	return false
}

// scanExecEnv reports every exec.Command or exec.CommandContext call in
// a top-level function that never sets an Env field -- neither
// `x.Env = ...` nor `exec.Cmd{Env: ...}`. That is deliberately the
// function rather than the variable: a helper that builds a command and
// sets its Env a few lines later keeps the rule, and one that hands an
// unset command to its caller is in the baseline until it doesn't.
func scanExecEnv(t *testing.T, root string) map[string][]string {
	out := map[string][]string{}
	for _, path := range productionGoFiles(t, root) {
		f, fset, rel := parseGo(t, root, path)
		execPkg := importName(f, "os/exec")
		if execPkg == "" {
			continue
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var calls []*ast.CallExpr
			setsEnv := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if isPkgSel(n.Fun, execPkg, "Command", "CommandContext") {
						calls = append(calls, n)
					}
				case *ast.AssignStmt:
					for _, lhs := range n.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Env" {
							setsEnv = true
						}
					}
				case *ast.KeyValueExpr:
					if id, ok := n.Key.(*ast.Ident); ok && id.Name == "Env" {
						setsEnv = true
					}
				}
				return true
			})
			if setsEnv {
				continue
			}
			for _, c := range calls {
				out[rel] = append(out[rel], fmt.Sprintf("%s:%d: %s never sets Env",
					rel, fset.Position(c.Pos()).Line, fn.Name.Name))
			}
		}
	}
	return out
}

// credentialPackages are the directories whose HTTP clients carry
// grain's own credentials to somebody else's server.
var credentialPackages = []string{
	"pkg/gitproxy/", "pkg/modelproxy/", "pkg/pkgcache/", "pkg/github/", "pkg/tunnel/",
}

func scanCredentialClients(t *testing.T, root string) map[string][]string {
	out := map[string][]string{}
	for _, path := range productionGoFiles(t, root) {
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		inScope := false
		for _, dir := range credentialPackages {
			if strings.HasPrefix(rel, dir) {
				inScope = true
			}
		}
		if !inScope {
			continue
		}
		f, fset, _ := parseGo(t, root, path)
		httpPkg := importName(f, "net/http")
		if httpPkg == "" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if isPkgSel(n, httpPkg, "DefaultClient") {
					out[rel] = append(out[rel], fmt.Sprintf("%s:%d: http.DefaultClient", rel, fset.Position(n.Pos()).Line))
				}
			case *ast.CallExpr:
				if isPkgSel(n.Fun, httpPkg, "Get", "Post", "Head", "PostForm") {
					out[rel] = append(out[rel], fmt.Sprintf("%s:%d: package-level http.%s",
						rel, fset.Position(n.Pos()).Line, n.Fun.(*ast.SelectorExpr).Sel.Name))
				}
			}
			return true
		})
	}
	return out
}

// buildTLSDisabled matches a line that turns TLS verification off in a
// shell command or apt configuration.
var buildTLSDisabled = regexp.MustCompile(
	`\bcurl\b.*\s(-[a-zA-Z0-9]*k[a-zA-Z0-9]*|--insecure)(\s|$)` +
		`|\bwget\b.*--no-check-certificate` +
		`|Verify-(Peer|Host)\s*"?false`)

// buildFiles are the Dockerfiles and Granulefiles at the top of the tree
// and the shell under scripts/ and deploy/ -- what builds images and
// hosts. docs/ keeps hand-run transcripts, which are records rather than
// things anything runs.
func buildFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	top, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range top {
		if !e.IsDir() && (strings.HasPrefix(e.Name(), "Dockerfile") || strings.HasPrefix(e.Name(), "Granulefile")) {
			files = append(files, filepath.Join(root, e.Name()))
		}
	}
	for _, dir := range []string{"scripts", "deploy"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == ".terraform" {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, ".sh") || strings.HasPrefix(name, "Dockerfile") || strings.HasPrefix(name, "Granulefile") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
	return files
}

func scanBuildTLS(t *testing.T, root string) map[string][]string {
	out := map[string][]string{}
	for _, path := range buildFiles(t, root) {
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if buildTLSDisabled.MatchString(line) {
				out[rel] = append(out[rel], fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
		}
	}
	return out
}

// TestRatchetScannersCatchWhatTheyClaim runs each scanner over a small
// tree of known violations and non-violations, so a scanner that stopped
// seeing anything cannot pass every real tree forever.
func TestRatchetScannersCatchWhatTheyClaim(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example\n")
	write("cmd/x/x.go", `package x

import (
	"context"
	"os"
	osexec "os/exec"
)

func leaks(ctx context.Context) error {
	return osexec.CommandContext(ctx, "git", "status").Run()
}

func setsLater() error {
	cmd := osexec.Command("git", "status")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run()
}

func literal() *osexec.Cmd {
	return &osexec.Cmd{Path: "/bin/true", Env: []string{}}
}
`)
	write("pkg/github/client.go", `package github

import "net/http"

var bad = http.DefaultClient

func fetch(u string) { http.Get(u) }

var good = &http.Client{Timeout: 1}
`)
	write("pkg/other/client.go", `package other

import "net/http"

var outOfScope = http.DefaultClient
`)
	write("Dockerfile", `FROM debian
# curl -k in a comment is not a violation
RUN curl -4 -k -fsSL https://example.com/x -o /x
RUN curl -fsSL https://example.com/y -o /y && sha256sum -c y.sha256
RUN printf 'Acquire::https::Verify-Peer "false";\n' > /etc/apt/apt.conf.d/99
`)
	write("scripts/fetch.sh", "wget --no-check-certificate https://example.com\ncurl --insecure https://example.com\ncurl -fsSLo out https://example.com\n")
	write("deploy/files/ok.sh", "curl -fsSL --max-time 60 https://example.com -o k.tgz\n")
	write("docs/run.sh", "curl -k https://example.com\n")
	write("pkg/gitwrap/git.go", `package gitwrap

import (
	"context"
	"os/exec"
)

const remoteName = "origin"

type repo struct{ dir string }

// run and git are the package's own helpers: git forwards to run, which
// execs "git" with its arguments spread, and is checked at their callers.
func (r *repo) run(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	out, err := cmd.Output()
	return string(out), err
}

func (r *repo) git(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, nil, args...)
}

func newCommand(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, []string{}
	return cmd
}

func (r *repo) sync(ctx context.Context, branch, file string) {
	r.git(ctx, "fetch", remoteName, branch)
	r.git(ctx, "fetch", remoteName, "--end-of-options", branch)
	r.git(ctx, "fetch", remoteName, "refs/heads/"+branch)
	r.run(ctx, nil, "log", "-1", "--format=%H", branch)
	r.git(ctx, "commit", "-m", branch)
	r.git(ctx, "status")
	newCommand(ctx, "", "git", "-C", r.dir, "fetch", branch)
	bad := exec.Command("git", "-C", r.dir, "checkout", branch, "--", file)
	good := exec.Command("git", "-C", r.dir, "log", "--", file)
	bad.Env, good.Env = []string{}, []string{}
}
`)
	write("pkg/paths/paths.go", `package paths

import (
	"os"
	"path/filepath"
	"strings"
)

const tablesDir = "tables/"

func lexical(root, p string) bool {
	return strings.HasPrefix(filepath.Clean(p), root)
}

func withSeparator(root, p string) bool {
	full := filepath.Join(root, p)
	return full == root || strings.HasPrefix(full, root+string(os.PathSeparator))
}

func throughAVariable(root, p string) bool {
	clean := filepath.Clean(root)
	return strings.HasPrefix(p, clean+"/")
}

// Checks on a name's shape, against prefixes the source fixes, and a
// test name's subtests: none of them a filesystem containment check.
func shape(rel, name, pattern string) bool {
	return strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		strings.HasPrefix(rel, tablesDir) ||
		strings.HasPrefix(name, pattern+"/")
}
`)
	write("pkg/safepath/safepath.go", `package safepath

import (
	"path/filepath"
	"strings"
)

// The helper itself is where the question is allowed to be asked.
func within(root, p string) bool { return strings.HasPrefix(filepath.Clean(p), root) }
`)

	want := map[string]map[string]int{
		"exec-env-implicit":          {"cmd/x/x.go": 1},
		"credential-client-defaults": {"pkg/github/client.go": 2},
		"build-tls-disabled":         {"Dockerfile": 2, "scripts/fetch.sh": 2},
		"git-argv-separator":         {"pkg/gitwrap/git.go": 4},
		"path-containment":           {"pkg/paths/paths.go": 3},
	}
	for _, rule := range ratchetRules {
		w, ok := want[rule.name]
		if !ok {
			t.Errorf("rule %s has no example here; add one", rule.name)
			continue
		}
		got := rule.scan(t, root)
		for p, n := range w {
			if len(got[p]) != n {
				t.Errorf("%s: %s: got %d violation(s) %q, want %d", rule.name, p, len(got[p]), got[p], n)
			}
		}
		for p, sites := range got {
			if _, ok := w[p]; !ok {
				t.Errorf("%s: unexpected violation(s) in %s: %q", rule.name, p, sites)
			}
		}
	}
}

// TestRatchetGoesBothWays checks the ratchet itself: over the baseline
// and under it both fail, and equal passes.
func TestRatchetGoesBothWays(t *testing.T) {
	rule := ratchetRule{name: "example", why: "example"}
	sites := map[string][]string{"a.go": {"a.go:1", "a.go:2"}}
	for _, tc := range []struct {
		name     string
		baseline map[string]int
		fail     bool
	}{
		{"equal", map[string]int{"a.go": 2}, false},
		{"over", map[string]int{"a.go": 1}, true},
		{"unlisted", map[string]int{}, true},
		{"under", map[string]int{"a.go": 3}, true},
		{"stale file", map[string]int{"a.go": 2, "gone.go": 1}, true},
	} {
		rec := &recorder{}
		checkRatchet(rec, rule, tc.baseline, sites)
		if rec.failed != tc.fail {
			t.Errorf("%s: failed=%v, want %v", tc.name, rec.failed, tc.fail)
		}
	}
}

type recorder struct{ failed bool }

func (*recorder) Helper()                 {}
func (r *recorder) Errorf(string, ...any) { r.failed = true }
func (*recorder) Logf(string, ...any)     {}
