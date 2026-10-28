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

package deploy

// The agreement between a go command that names a package by its import
// path and the go.mod that has to resolve it.
//
// Most builds here name a directory -- `go build ./cmd/grain`, `go build
// ./...` -- and those cannot go wrong quietly: the directory is either in
// the checkout or it is not. A handful name an import path instead,
// because the package is in another module. Dockerfile builds
// github.com/bwsalmon/granule/cmd/granule into the runtime image,
// scripts/granule-guest-probe.sh builds the same binary to look inside a
// guest with, and pkg/orchestrator's real-Docker tests build and run it.
// Those resolve through the module graph, and nothing else in this
// repository notices when they stop resolving.
//
// They did stop, for a day. e811fd4f1 ("Clean up module dependencies and
// deploy tests for granule", #1725) dropped github.com/bwsalmon/granule
// from go.mod and go.sum while every one of those call sites still named
// it, and each of them began failing with "no required module provides
// package ..." before it reached the network. `go build ./...` stayed
// green throughout, because `./...` never walks to a package outside the
// module -- which is exactly how it got through. GOFLAGS is
// `-mod=readonly` in every workflow, so nothing added the requirement
// back on the way past either.
//
// So: find every go command in the tree that names an import path, and
// assert the module it belongs to is one go.mod requires. This is a
// reading of the files, in the spirit of the rest of this package -- no
// build is run, and no network is touched.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// mainModule is this repository's own module path. A target under it is
// resolved from the checkout and needs no requirement at all.
const mainModule = "github.com/ai-on-gke/grain"

// packageResolvingSubcommands are the go subcommands whose arguments can
// be import paths, and which therefore fail on a module go.mod does not
// require. `run` is here as well as `build` because
// pkg/orchestrator/granule_docker_real_test.go uses it against granule
// and resolves the module exactly the same way.
var packageResolvingSubcommands = map[string]bool{
	"build":   true,
	"install": true,
	"run":     true,
	"test":    true,
	"vet":     true,
}

// importPathToken matches a whole word that is a package import path: a
// dotted host, then at least one path element.
//
// The anchors matter more than the alphabet does. `./...`, `./cmd/grain`
// and `/tmp/grain-connect/main.go` have no dotted host at their head and
// do not match, which is the point -- those are directory and file
// targets, and they are not what can go missing from the module graph.
// Neither does the `github.com/ai-on-gke/grain/pkg/version.Commit=abc123`
// inside an -ldflags -X, because `=` is not in the alphabet and the match
// has to cover the whole word.
// A `/...` wildcard is trimmed before the match rather than spelled as
// an optional group here, because `.` is in the path alphabet and the
// group would swallow it.
var importPathToken = regexp.MustCompile(`^(?:[a-z0-9-]+\.)+[a-z]{2,}(?:/[A-Za-z0-9_.~-]+)+$`)

// buildTarget is one import path a go command in this tree names, with
// where it says it -- so a failure names the line to fix rather than just
// the module that failed to resolve.
type buildTarget struct {
	file string
	line int
	path string
}

// goWords splits a line into the words a go command is assembled from,
// in both of the notations this tree writes one in.
//
// A shell invocation (`go build -o /out/granule <path>`) and a Go one
// (`exec.Command("go", "build", "-o", bin, "<path>")`) are the same
// command wearing different punctuation, so quotes, commas and brackets
// separate words here exactly as whitespace does. That is what lets one
// scanner read Dockerfile, a shell script and a _test.go file without
// three patterns that each go stale on their own schedule.
func goWords(line string) []string {
	return strings.FieldsFunc(line, func(r rune) bool {
		switch r {
		case ' ', '\t', '"', '\'', '`', ',', '(', ')', '[', ']', '{', '}':
			return true
		}
		return false
	})
}

// isGoCommand reports whether a word invokes the go tool: `go`, or a path
// ending in it -- Granulefile runs /usr/local/go/bin/go.
func isGoCommand(word string) bool {
	return word == "go" || strings.HasSuffix(word, "/go")
}

// goBuildTargetsIn extracts every import path the go commands in one
// file's text name.
//
// Line-oriented, and deliberately generous about what surrounds the
// target: these call sites are `RUN vcs=... && CGO_ENABLED=0 go build
// -buildvcs="$vcs" -o /out/granule <path>` and `exec.Command("go",
// "build", "-o", bin, "<path>")`, and a pattern tight enough to describe
// those two is a pattern the third call site escapes. Every word after
// the subcommand is offered to importPathToken instead, and the
// anchoring there is what rejects the flags.
//
// Whole-line comments are skipped, because this file is surrounded by
// prose about `go build` and an assertion must not be answered by an
// explanation.
func goBuildTargetsIn(name, text string) []buildTarget {
	var found []buildTarget
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimLeft(raw, " \t")
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		words := goWords(line)
		at := -1
		for i := 0; i+1 < len(words); i++ {
			if isGoCommand(words[i]) && packageResolvingSubcommands[words[i+1]] {
				at = i + 2
				break
			}
		}
		if at < 0 {
			continue
		}
		// From the first go command on the line to the end of it: a
		// second one further along would only re-read the same tail.
		for _, word := range words[at:] {
			word = strings.TrimSuffix(word, "/...")
			if importPathToken.MatchString(word) {
				found = append(found, buildTarget{file: name, line: n + 1, path: word})
			}
		}
	}
	return found
}

// buildableFile reports whether a file in the checkout is one a go
// command can live in. Everything else -- Markdown, JSON, the JS suite --
// only ever talks about builds.
func buildableFile(name string) bool {
	switch filepath.Ext(name) {
	case ".sh", ".go", ".yml", ".yaml":
		return true
	}
	return name == "Makefile" ||
		strings.HasPrefix(name, "Dockerfile") ||
		strings.HasPrefix(name, "Granulefile")
}

// thisFile is the path of this source file in the checkout it was
// compiled from, so that the walk below can leave it alone.
func thisFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file, so cannot exclude it from the walk")
	}
	return file
}

// goBuildTargets walks the checkout for every import path a go command
// names.
//
// third_party is skipped: a module vendored there carries its own go.mod,
// and a command run inside it resolves against that one rather than
// against ours. .git and node_modules are skipped because nothing in them
// is ours to fix.
//
// This file is skipped too, and that exclusion is the whole reason
// thisFile exists. Its fixtures are lines written to look exactly like
// the call sites -- that is what makes them fixtures -- and two of them
// name modules invented for the occasion. Read as commands they are a
// standing failure of the assertion; read by the fixture test, which is
// where a line of text is meant to be a line of text, they are what
// stops the assertion passing on a pattern that has quietly gone blind.
func goBuildTargets(t *testing.T) []buildTarget {
	t.Helper()
	root := repoRoot(t)
	skip := thisFile(t)
	var found []buildTarget
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := info.Name()
		if info.IsDir() {
			if path != root && (name == ".git" || name == "node_modules" || name == "third_party") {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() || !buildableFile(name) || path == skip {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found = append(found, goBuildTargetsIn(rel, string(b))...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the checkout for go commands: %v", err)
	}
	return found
}

// goModRequires is every module path go.mod requires, in both of the
// forms it writes them: the indented entries of a `require (` block and a
// single-line `require path version`.
func goModRequires(t *testing.T) []string {
	t.Helper()
	var required []string
	inBlock := false
	for _, raw := range strings.Split(read(t, "go.mod"), "\n") {
		line := strings.TrimSpace(raw)
		if at := strings.Index(line, "//"); at >= 0 {
			line = strings.TrimSpace(line[:at])
		}
		if line == "require (" {
			inBlock = true
			continue
		}
		if inBlock {
			if line == ")" {
				inBlock = false
				continue
			}
			if fields := strings.Fields(line); len(fields) >= 2 {
				required = append(required, fields[0])
			}
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 3 && fields[0] == "require" {
			required = append(required, fields[1])
		}
	}
	return required
}

// goModLocalReplacements is every `replace` in go.mod whose right-hand
// side is a directory of this checkout rather than another module
// version, keyed by the module path it stands in for.
func goModLocalReplacements(t *testing.T) map[string]string {
	t.Helper()
	local := map[string]string{}
	for _, raw := range strings.Split(read(t, "go.mod"), "\n") {
		line := strings.TrimSpace(raw)
		if at := strings.Index(line, "//"); at >= 0 {
			line = strings.TrimSpace(line[:at])
		}
		fields := strings.Fields(strings.TrimPrefix(line, "replace "))
		arrow := -1
		for i, f := range fields {
			if f == "=>" {
				arrow = i
				break
			}
		}
		if arrow <= 0 || arrow+1 >= len(fields) {
			continue
		}
		target := fields[arrow+1]
		if !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") && !strings.HasPrefix(target, "/") {
			continue
		}
		local[fields[0]] = target
	}
	return local
}

// providedBy returns the required module that provides an import path, or
// "" if none does -- the longest requirement that is a prefix of it at a
// path boundary, which is how the go command resolves the same question.
func providedBy(path string, required []string) string {
	best := ""
	for _, module := range required {
		if path != module && !strings.HasPrefix(path, module+"/") {
			continue
		}
		if len(module) > len(best) {
			best = module
		}
	}
	return best
}

// The assertion this file exists for.
func TestEveryGoBuildTargetOutsideThisModuleIsRequiredByGoMod(t *testing.T) {
	required := goModRequires(t)
	for _, target := range goBuildTargets(t) {
		if providedBy(target.path, []string{mainModule}) != "" {
			continue
		}
		if providedBy(target.path, required) != "" {
			continue
		}
		t.Errorf("%s:%d names %s, which no module in go.mod provides -- that command fails with "+
			"\"no required module provides package\" before it reaches the network, and `go build ./...` "+
			"will not notice, because ./... never walks outside this module",
			target.file, target.line, target.path)
	}
}

// A module go.mod requires and replaces with a directory of this checkout
// has to have that directory, holding a module of that name.
//
// This is the same failure wearing the other hat. go.mod requires
// github.com/bwsalmon/granule and points it at third_party/granule, so
// the requirement can be present and correct and every one of those call
// sites still fail -- on a checkout where that directory was removed, or
// on one where it is there but holds no module.
func TestEveryLocallyReplacedModuleIsPresentInTheCheckout(t *testing.T) {
	for module, target := range goModLocalReplacements(t) {
		dir := target
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(repoRoot(t), target)
		}
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			t.Errorf("go.mod replaces %s with %s, but %s/go.mod cannot be read: %v",
				module, target, target, err)
			continue
		}
		want := "module " + module
		if !strings.Contains(string(b), want+"\n") {
			t.Errorf("go.mod replaces %s with %s, but %s/go.mod does not declare %q",
				module, target, target, want)
		}
	}
}

// The scanner, against every form this tree writes a go command in -- so
// that the assertion above can never pass by finding nothing.
//
// A cross-file check that greps for something is only worth what its grep
// is worth: a pattern that quietly stops matching turns into a test that
// passes on any tree at all, and it passes most loudly on the tree that
// broke it. These cases are lifted from the real call sites, plus the
// near misses that must not be read as import paths.
func TestTheGoCommandScannerFindsATargetInEveryFormTheTreeUsesOne(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		want []string
	}{{
		name: "Dockerfile's RUN, behind a line continuation and an environment variable",
		text: "RUN vcs=\"$(./scripts/buildvcs.sh ${BUILDVCS})\" && mkdir -p /out \\\n" +
			"\t&& CGO_ENABLED=0 go build -buildvcs=\"$vcs\" -o /out/granule github.com/bwsalmon/granule/cmd/granule",
		want: []string{"github.com/bwsalmon/granule/cmd/granule"},
	}, {
		name: "a shell script's subshell",
		text: `  (cd "$repo_root" && go build -buildvcs="$vcs" -o "$workdir/granule" github.com/bwsalmon/granule/cmd/granule)`,
		want: []string{"github.com/bwsalmon/granule/cmd/granule"},
	}, {
		name: "a Go test shelling out, where the words are string literals",
		text: `	cmd := exec.Command("go", "build", "-o", bin, "github.com/bwsalmon/granule/cmd/granule")`,
		want: []string{"github.com/bwsalmon/granule/cmd/granule"},
	}, {
		name: "go run resolves a module the same way go build does",
		text: `	cmd := exec.Command("go", "run", "github.com/bwsalmon/granule/cmd/granule", "granulefile", "build", "-f", "Granulefile")`,
		want: []string{"github.com/bwsalmon/granule/cmd/granule"},
	}, {
		name: "a go tool reached by path",
		text: `RUN CGO_ENABLED=0 /usr/local/go/bin/go build -o /out/x github.com/bwsalmon/granule/cmd/granule`,
		want: []string{"github.com/bwsalmon/granule/cmd/granule"},
	}, {
		name: "a trailing /...",
		text: `go build github.com/bwsalmon/granule/cmd/...`,
		want: []string{"github.com/bwsalmon/granule/cmd"},
	}, {
		name: "the whole-module form",
		text: `go build -o bin github.com/bwsalmon/granule`,
		want: []string{"github.com/bwsalmon/granule"},
	}, {
		name: "two targets on one line",
		text: `go build github.com/a.io/x/cmd/one github.com/b.io/y/cmd/two`,
		want: []string{"github.com/a.io/x/cmd/one", "github.com/b.io/y/cmd/two"},
	}, {
		name: "a directory target is not an import path",
		text: `	CGO_ENABLED=0 go build -buildvcs=auto -ldflags "$(LDFLAGS)" -o $(BIN)/$@ ./cmd/$@`,
		want: nil,
	}, {
		name: "./... is not an import path",
		text: `        run: go build ./...`,
		want: nil,
	}, {
		name: "a file target is not an import path",
		text: `RUN CGO_ENABLED=0 /usr/local/go/bin/go build -o /usr/local/bin/grain-connect /tmp/grain-connect/main.go`,
		want: nil,
	}, {
		name: "an -ldflags -X names a package but does not build it",
		text: `go build -ldflags "-X github.com/ai-on-gke/grain/pkg/version.Commit=abc123" ./cmd/grain`,
		want: nil,
	}, {
		name: "a comment about a build is not a build",
		text: `# the line above runs go build github.com/bwsalmon/granule/cmd/granule`,
		want: nil,
	}, {
		name: "a Go comment about a build is not a build either",
		text: `// pkg/granule shells out to go build github.com/bwsalmon/granule/cmd/granule`,
		want: nil,
	}, {
		name: "a subcommand that resolves no package",
		text: `go mod download github.com/bwsalmon/granule`,
		want: nil,
	}} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, target := range goBuildTargetsIn("fixture", c.text) {
				got = append(got, target.path)
			}
			if strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Errorf("scanned %v, want %v", got, c.want)
			}
		})
	}
}

// go.mod parsing, against the form it writes a requirement in and the
// neighbours that are not requirements.
func TestGoModRequirementsAreReadWithoutTheLinesThatAreNotOne(t *testing.T) {
	required := goModRequires(t)
	if got := providedBy("github.com/bwsalmon/granule/cmd/granule", required); got != "github.com/bwsalmon/granule" {
		t.Errorf("go.mod's requirements yield %q for granule's cmd, want github.com/bwsalmon/granule; read %v", got, required)
	}
	for _, module := range required {
		switch module {
		case mainModule, "go", "1.26.6", "toolchain", "go1.26.10", "(", ")", "replace", "module":
			t.Errorf("%q was read as a requirement of go.mod, and it is not one", module)
		}
	}
}
