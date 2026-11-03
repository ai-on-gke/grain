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

package namespace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/redact"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const layerGoMod = "module example.com/layer\n\ngo 1.26\n\nrequire github.com/ai-on-gke/grain v0.0.0-20261030003551-7e1761c2dc31\n"

func readNode(t *testing.T, n *ResolvedNode) string {
	t.Helper()
	rc, err := n.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func entryNames(n *ResolvedNode) []string {
	var names []string
	for _, e := range n.Entries {
		names = append(names, e.Name)
	}
	return names
}

// A daemon built from a module on top of core serves core's source: the
// layer's own paths first, core's beneath them, and core alone under
// src://core/ -- so the src://pkg/... every example names reads.
func TestSrcServesCoreBeneathALayer(t *testing.T) {
	layer, core := t.TempDir(), t.TempDir()
	writeTree(t, layer, map[string]string{
		"go.mod":            layerGoMod,
		"cmd/grain/main.go": "layer main",
		"google/google.go":  "layer only",
	})
	writeTree(t, core, map[string]string{
		"go.mod":              "module github.com/ai-on-gke/grain\n",
		"cmd/grain/main.go":   "core main",
		"cmd/grit/main.go":    "core grit",
		"pkg/cli/sandbox.go":  "core sandbox",
		"pkg/cli/sandbox2.go": "core sandbox2",
	})
	s := &Server{SrcDir: layer, CoreSrcDir: core, CoreModule: CoreModulePath + "@v0.0.0-20261030003551-7e1761c2dc31"}
	scope := Scope{MayReadSrc: true}
	ctx := context.Background()

	n, err := s.Resolve(ctx, scope, "src", "pkg/cli/sandbox.go")
	if err != nil {
		t.Fatalf("src://pkg/cli/sandbox.go: %v", err)
	}
	if got := readNode(t, n); got != "core sandbox" {
		t.Errorf("src://pkg/cli/sandbox.go = %q, want core's", got)
	}
	n, err = s.Resolve(ctx, scope, "src", "cmd/grain/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := readNode(t, n); got != "layer main" {
		t.Errorf("src://cmd/grain/main.go = %q, want the layer's to win", got)
	}
	n, err = s.Resolve(ctx, scope, "src", "core/cmd/grain/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := readNode(t, n); got != "core main" {
		t.Errorf("src://core/cmd/grain/main.go = %q, want core's", got)
	}

	n, err = s.Resolve(ctx, scope, "src", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(entryNames(n), ","); got != "cmd,core,go.mod,google,pkg" {
		t.Errorf("src:// lists %s", got)
	}
	n, err = s.Resolve(ctx, scope, "src", "cmd")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(entryNames(n), ","); got != "grain,grit" {
		t.Errorf("src://cmd lists %s, want both trees' entries", got)
	}

	matches, err := s.Glob(ctx, scope, "src", "pkg", "**/*.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(matches, ","); got != "cli/sandbox.go,cli/sandbox2.go" {
		t.Errorf("glob src://pkg **/*.go = %s", got)
	}
	res, err := s.Grep(ctx, scope, "src", "pkg", GrepOptions{Pattern: "sandbox2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "core sandbox2") {
		t.Errorf("grep src://pkg missed core: %q", res.Output)
	}

	if _, err := s.Resolve(ctx, scope, "src", "nope/x.go"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a path in neither tree: %v, want not found", err)
	}
	if _, err := s.Resolve(ctx, scope, "src", "core/.git/config"); !errors.Is(err, ErrForbidden) {
		t.Errorf("src://core/.git: %v, want forbidden", err)
	}
}

// A layer with a top-level core/ of its own keeps it.
func TestSrcLayerKeepsItsOwnCoreDir(t *testing.T) {
	layer, core := t.TempDir(), t.TempDir()
	writeTree(t, layer, map[string]string{"go.mod": layerGoMod, "core/x.go": "layer core"})
	writeTree(t, core, map[string]string{"x.go": "real core"})
	s := &Server{SrcDir: layer, CoreSrcDir: core}
	n, err := s.Resolve(context.Background(), Scope{MayReadSrc: true}, "src", "core/x.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := readNode(t, n); got != "layer core" {
		t.Errorf("src://core/x.go = %q, want the layer's own", got)
	}
}

// Without core's source shipped, a miss says where core is to be had
// rather than a bare "not found".
func TestSrcMissPointsAtCoreWhenItDidNotShip(t *testing.T) {
	layer := t.TempDir()
	writeTree(t, layer, map[string]string{"go.mod": layerGoMod})
	mod := CoreModulePath + "@v0.0.0-20261030003551-7e1761c2dc31"
	s := &Server{SrcDir: layer, CoreModule: mod}
	_, err := s.Resolve(context.Background(), Scope{MayReadSrc: true}, "src", "pkg/cli/sandboxcli.go")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	for _, want := range []string{"src://pkg/cli/sandboxcli.go", "go mod download -json " + mod, "ai-on-gke/grain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// Core's own daemon (no CoreModule) keeps the plain not-found.
	s = &Server{SrcDir: layer}
	_, err = s.Resolve(context.Background(), Scope{MayReadSrc: true}, "src", "pkg")
	if err == nil || strings.Contains(err.Error(), "go mod download") {
		t.Errorf("core daemon's miss = %v", err)
	}
}

func TestCoreSource(t *testing.T) {
	const ver = "v0.0.0-20261030003551-7e1761c2dc31"
	mod := CoreModulePath + "@" + ver

	t.Run("core itself", func(t *testing.T) {
		src := t.TempDir()
		writeTree(t, src, map[string]string{"go.mod": "module github.com/ai-on-gke/grain\n"})
		if dir, m := CoreSource(src, t.TempDir()); dir != "" || m != "" {
			t.Errorf("CoreSource(core) = %q, %q", dir, m)
		}
	})
	t.Run("no go.mod", func(t *testing.T) {
		if dir, m := CoreSource(t.TempDir()); dir != "" || m != "" {
			t.Errorf("CoreSource = %q, %q", dir, m)
		}
	})
	t.Run("shipped candidate wins", func(t *testing.T) {
		src, shipped := t.TempDir(), t.TempDir()
		writeTree(t, src, map[string]string{"go.mod": layerGoMod})
		dir, m := CoreSource(src, filepath.Join(t.TempDir(), "missing"), shipped)
		if dir != shipped || m != mod {
			t.Errorf("CoreSource = %q, %q", dir, m)
		}
	})
	t.Run("local replace", func(t *testing.T) {
		src := t.TempDir()
		writeTree(t, src, map[string]string{
			"go.mod":                  layerGoMod + "\nreplace github.com/ai-on-gke/grain => ./third_party/core\n",
			"third_party/core/go.mod": "module github.com/ai-on-gke/grain\n",
		})
		dir, m := CoreSource(src)
		if dir != filepath.Join(src, "third_party", "core") || m != mod {
			t.Errorf("CoreSource = %q, %q", dir, m)
		}
	})
	t.Run("module cache", func(t *testing.T) {
		src, cache := t.TempDir(), t.TempDir()
		writeTree(t, src, map[string]string{"go.mod": layerGoMod})
		want := filepath.Join(cache, "github.com", "ai-on-gke", "grain@"+ver)
		writeTree(t, want, map[string]string{"go.mod": "module github.com/ai-on-gke/grain\n"})
		t.Setenv("GOMODCACHE", cache)
		if dir, m := CoreSource(src); dir != want || m != mod {
			t.Errorf("CoreSource = %q, %q; want %q", dir, m, want)
		}
	})
	t.Run("not shipped", func(t *testing.T) {
		src := t.TempDir()
		writeTree(t, src, map[string]string{"go.mod": layerGoMod})
		t.Setenv("GOMODCACHE", t.TempDir())
		if dir, m := CoreSource(src); dir != "" || m != mod {
			t.Errorf("CoreSource = %q, %q", dir, m)
		}
	})
}

// src://pkg is bigger than maxWalkFiles -- core's pkg/ alone is over 3000
// files -- and a grep of it has to reach the packages that sort last,
// core's beneath a layer's included, or an agent reads a symbol that is
// there as one that is not. node_modules/ is passed over unless named.
func TestGrepOfSrcReachesPastTheDefaultWalkCap(t *testing.T) {
	layer, core := t.TempDir(), t.TempDir()
	layerFiles := map[string]string{"go.mod": layerGoMod}
	for i := 0; i < maxWalkFiles+500; i++ {
		layerFiles[fmt.Sprintf("pkg/aaa/f%04d.go", i)] = "package aaa\n"
	}
	writeTree(t, layer, layerFiles)
	writeTree(t, core, map[string]string{
		"go.mod":                       "module github.com/ai-on-gke/grain\n",
		"pkg/namespace/coresrc.go":     "package namespace\n\nfunc CoreSource() {}\n",
		"pkg/ui/node_modules/x/x.js":   "func CoreSource() {}\n",
		"pkg/ui/node_modules/y/y.go":   "func CoreSource() {}\n",
		"pkg/zzz/node_modules_note.go": "// not a node_modules/ dir\n",
	})
	s := &Server{SrcDir: layer, CoreSrcDir: core, CoreModule: CoreModulePath + "@v0.0.0-20261030003551-7e1761c2dc31"}
	scope := Scope{MayReadSrc: true}
	ctx := context.Background()

	for _, root := range []string{"pkg", "", "core/pkg"} {
		res, err := s.Grep(ctx, scope, "src", root, GrepOptions{Pattern: "func CoreSource"})
		if err != nil {
			t.Fatalf("grep src://%s: %v", root, err)
		}
		// src:// itself holds core twice: beneath the layer, and as core/.
		if res.WalkTruncated || res.Truncated || res.Matches == 0 || strings.Contains(res.Output, "node_modules") ||
			!strings.Contains(res.Output, "namespace/coresrc.go:3:func CoreSource") {
			t.Errorf("grep src://%s = %+v, want core's coresrc.go, untruncated", root, res)
		}
	}
	res, err := s.Grep(ctx, scope, "src", "pkg", GrepOptions{Pattern: "func CoreSource", GlobFilter: "**/node_modules/**"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Matches != 2 {
		t.Errorf("grep src://pkg --glob '**/node_modules/**' = %q, want both node_modules files", res.Output)
	}
	matches, err := s.Glob(ctx, scope, "src", "pkg", "**/*.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(matches, ","); !strings.Contains(got, "namespace/coresrc.go") || strings.Contains(got, "node_modules/") || len(matches) != maxWalkFiles+502 {
		t.Errorf("glob src://pkg **/*.go = %d matches, want %d with core's and no node_modules/", len(matches), maxWalkFiles+502)
	}
}

// The higher cap is src://'s alone: every other scheme still stops at
// maxWalkFiles.
func TestWalkCapIsHigherForSrcOnly(t *testing.T) {
	if walkCap("src") != maxSrcWalkFiles || maxSrcWalkFiles <= maxWalkFiles {
		t.Errorf("walkCap(src) = %d, want %d (> %d)", walkCap("src"), maxSrcWalkFiles, maxWalkFiles)
	}
	for _, scheme := range []string{"ci", "task", "config", "memory", "deploy"} {
		if walkCap(scheme) != maxWalkFiles {
			t.Errorf("walkCap(%s) = %d, want %d", scheme, walkCap(scheme), maxWalkFiles)
		}
	}
}

// A grep redacts only the lines it prints, through one redactor per
// request, yet still never finds a secret by its value nor prints one in
// a context line: redacting every line of every file first made a grep
// of core's src://pkg take 15s (docs/test-runs/2026-11-03-sandbox-cli-edges.md).
func TestGrepRedactsOnlyWhatItPrintsAndStillHidesSecrets(t *testing.T) {
	const secret = "custom-secret-value-424242"
	const ghToken = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ123456"
	root := t.TempDir()
	files := map[string]string{
		"pkg/a/a.go": "package a\n// token " + secret + " here\nfunc Before() {}\n" + ghToken + "\nfunc Wanted() {}\nvar after = \"" + secret + "\"\n",
	}
	for i := 0; i < 40; i++ {
		files[fmt.Sprintf("pkg/b/f%02d.go", i)] = "package b\n"
	}
	writeTree(t, root, files)
	builds := 0
	s := &Server{SrcDir: root, NewRedactor: func() *redact.Redactor {
		builds++
		return redact.New([]redact.KnownValue{{Label: "secret custom", Value: secret}})
	}}
	scope := Scope{MayReadSrc: true}
	grep := func(pattern string, ctxLines int) GrepResult {
		t.Helper()
		res, err := s.Grep(context.Background(), scope, "src", "pkg", GrepOptions{Pattern: pattern, ContextLines: ctxLines})
		if err != nil {
			t.Fatalf("grep %q: %v", pattern, err)
		}
		return res
	}
	for _, probe := range []string{secret, "custom-secret", ghToken, "ghp_[A-Z]+"} {
		if res := grep(probe, 0); res.Matches != 0 || strings.Contains(res.Output, secret) || strings.Contains(res.Output, ghToken) {
			t.Errorf("grep %q found a secret by its value: %d %q", probe, res.Matches, res.Output)
		}
	}
	builds = 0
	res := grep(`^func Wanted\(`, 2)
	if res.Matches != 1 || !strings.Contains(res.Output, "a/a.go:5:func Wanted() {}") {
		t.Errorf("grep for Wanted = %d %q", res.Matches, res.Output)
	}
	if strings.Contains(res.Output, secret) || strings.Contains(res.Output, ghToken) || !strings.Contains(res.Output, "[redacted: secret custom]") {
		t.Errorf("a context line printed a secret: %q", res.Output)
	}
	if builds != 1 {
		t.Errorf("one grep over %d files built %d redactors, want 1", len(files), builds)
	}
	if res := grep("token", 0); res.Matches != 1 || !strings.Contains(res.Output, "// token [redacted: secret custom] here") {
		t.Errorf("a line that matches beside a secret = %d %q", res.Matches, res.Output)
	}
}
