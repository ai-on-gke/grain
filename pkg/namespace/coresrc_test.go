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
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
