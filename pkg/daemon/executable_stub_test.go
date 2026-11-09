// Copyright 2026 The Grain Authors
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

package daemon

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// systemInstallBin is resolved at package init time, before any test can
// override PATH with t.Setenv (including tests that strip PATH or install a
// fake "install" binary onto PATH).
var systemInstallBin, _ = exec.LookPath("install")

// writeExecutableStub writes an executable stub to path without opening path's
// inode for writing inside the Go test process, avoiding Linux ETXTBSY ("text
// file busy") races when sibling parallel tests fork child processes.
func writeExecutableStub(t testing.TB, path, script string) {
	t.Helper()
	if runtime.GOOS == "windows" || systemInstallBin == "" {
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatalf("writing executable stub %s: %v", path, err)
		}
		return
	}
	f, err := os.CreateTemp("", "grain-exec-stub-*.src")
	if err != nil {
		t.Fatalf("creating staging file for %s: %v", path, err)
	}
	src := f.Name()
	defer os.Remove(src)
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		t.Fatalf("writing staging file for %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing staging file for %s: %v", path, err)
	}
	if out, err := exec.Command(systemInstallBin, "-m", "0755", src, path).CombinedOutput(); err != nil {
		t.Fatalf("installing executable stub %s: %v: %s", path, err, out)
	}
}

func TestWriteExecutableStubAvoidsETXTBSYUnderParallelForks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell stubs do not run on Windows")
	}
	t.Parallel()
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("no true(1) on PATH: %v", err)
	}

	dir := t.TempDir()
	var stop atomic.Bool
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_ = exec.Command(trueBin).Run()
			}
		}()
	}
	defer func() {
		stop.Store(true)
		wg.Wait()
	}()

	for i := range 100 {
		bin := filepath.Join(dir, fmt.Sprintf("stub-%d", i))
		writeExecutableStub(t, bin, "#!/bin/sh\necho ok\n")
		out, err := exec.Command(bin).CombinedOutput()
		if err != nil {
			t.Fatalf("executing stub %d failed: %v (%s)", i, err, out)
		}
		if string(out) != "ok\n" {
			t.Fatalf("stub %d output = %q, want %q", i, out, "ok\n")
		}
	}
}

// TestDaemonTestsUseWriteExecutableStub prevents tests in pkg/daemon from
// writing executable files directly from the test process via os.WriteFile or
// os.OpenFile, which reintroduces the ETXTBSY race whenever parallel tests fork
// concurrently.
func TestDaemonTestsUseWriteExecutableStub(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", pkgDir, err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") || name == "executable_stub_test.go" {
			continue
		}
		full := filepath.Join(pkgDir, name)
		file, err := parser.ParseFile(fset, full, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 3 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "os" {
				return true
			}
			if sel.Sel.Name != "WriteFile" && sel.Sel.Name != "OpenFile" {
				return true
			}
			lit, ok := call.Args[2].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				return true
			}
			mode, err := strconv.ParseUint(lit.Value, 0, 32)
			if err != nil || mode&0o111 == 0 {
				return true
			}
			pos := fset.Position(call.Pos())
			t.Errorf("%s:%d calls os.%s with executable mode %s; use writeExecutableStub / daemon.WriteExecutableStub so parallel forks cannot fail with ETXTBSY",
				name, pos.Line, sel.Sel.Name, lit.Value)
			return true
		})
	}
}
