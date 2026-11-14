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

package demotest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// binDir holds the `grain` binary Main's package builds, once, the first
// time a test asks CLI for it: a package whose tests never run the CLI
// never pays for the build. It is empty outside Main.
var (
	binDir    string
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// Main runs m with a directory for the one `grain` binary CLI builds,
// removed when m is done, as ui/e2e/global-setup.js builds one for the
// whole classic suite. Call it from TestMain:
//
//	func TestMain(m *testing.M) { demotest.Main(m) }
func Main(m *testing.M) {
	dir, err := os.MkdirTemp("", "grain-demotest-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "demotest: %v\n", err)
		os.Exit(1)
	}
	binDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// moduleRoot is the repository root, found from this file's own path:
// the build runs there whatever directory the test binary runs in.
func moduleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot find demotest's source")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above " + file)
		}
		dir = parent
	}
}

// Binary is the path of the `grain` binary CLI runs, built on first use
// with the same tags and release stamp the classic suite's is.
func Binary(t testing.TB) string {
	t.Helper()
	if binDir == "" {
		t.Fatal("demotest.CLI needs the package's TestMain to call demotest.Main(m)")
	}
	buildOnce.Do(func() {
		root, err := moduleRoot()
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(binDir, "grain")
		cmd := exec.Command("go", "build",
			"-tags", "grain_e2e",
			"-ldflags", "-X github.com/ai-on-gke/grain/pkg/version.Release=v1.2.0",
			"-o", binPath, "./cmd/grain")
		cmd.Dir = root
		// The test's own environment, on purpose: the toolchain's GOPATH,
		// GOCACHE and GOFLAGS are what make this build the one `go test`
		// is running.
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/grain: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

// CLIResult is one `grain` run.
type CLIResult struct {
	Stdout, Stderr string
	// Err is the run's error: nil, or an *exec.ExitError for a non-zero
	// exit.
	Err error
}

// ExitCode is the run's exit status, 0 for success.
func (r CLIResult) ExitCode() int {
	var exit *exec.ExitError
	if errors.As(r.Err, &exit) {
		return exit.ExitCode()
	}
	if r.Err != nil {
		return -1
	}
	return 0
}

// RunCLI runs `grain args...` against srv, signed in with its access code
// the way an operator's shell is ($GRAIN_SERVER, $GRAIN_TOKEN), with a
// HOME of its own so no context or config of the machine's leaks in.
func RunCLI(t testing.TB, srv *Server, args ...string) CLIResult {
	t.Helper()
	bin := Binary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	home := t.TempDir()
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"GRAIN_SERVER=" + srv.URL,
		"GRAIN_TOKEN=" + srv.AccessCode,
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GRAIN_") || name == "HOME" || name == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return CLIResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
}

// CLI is RunCLI for a run that has to succeed: it fails t, showing both
// streams, unless `grain args...` exits 0, and returns its stdout.
func CLI(t testing.TB, srv *Server, args ...string) string {
	t.Helper()
	r := RunCLI(t, srv, args...)
	if r.Err != nil {
		t.Fatalf("grain %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.Err, r.Stdout, r.Stderr)
	}
	return r.Stdout
}
