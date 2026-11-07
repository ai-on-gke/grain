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

package sandboxcli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeNodeOnPath puts an executable "node" (never run: screenshotRun is
// replaced) alone on PATH, so exec.LookPath finds it and `npm root -g`
// finds nothing.
func fakeNodeOnPath(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	node := filepath.Join(bin, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return node
}

func TestGrainScreenshotInstallsPlaywrightOnceAndPassesFlagsThrough(t *testing.T) {
	node := fakeNodeOnPath(t)
	cache := t.TempDir()
	type call struct {
		args   []string
		env    []string
		script []byte
	}
	var calls []call
	answers := []int{screenshotMissingPlaywright, 0, 0}
	prev := screenshotRun
	t.Cleanup(func() { screenshotRun = prev })
	screenshotRun = func(cmd *exec.Cmd) (int, error) {
		c := call{args: cmd.Args, env: cmd.Env}
		if len(cmd.Args) > 1 && strings.HasSuffix(cmd.Args[1], "screenshot.mjs") {
			c.script, _ = os.ReadFile(cmd.Args[1])
		}
		calls = append(calls, c)
		code := answers[0]
		answers = answers[1:]
		return code, nil
	}
	env := fakeSandboxEnv(map[string]string{
		"GRAIN_SANDBOX_PROXY": "http://proxy.invalid", "GRAIN_SANDBOX_TOKEN": "tok",
		"XDG_CACHE_HOME": cache, "GRAIN_PLAYWRIGHT_VERSION": "1.2.3",
	}, "")
	_, stderr, code := runIn(t, env, "screenshot", "--url", "http://127.0.0.1:5173", "--pages", "/,/cart", "--click", "#menu")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(calls) != 3 {
		t.Fatalf("ran %d processes, want the script, npm install, the script: %+v", len(calls), calls)
	}
	// 1. The embedded script, with the flags as they were given.
	if calls[0].args[0] != node || !slices.Equal(calls[0].args[2:], []string{"--url", "http://127.0.0.1:5173", "--pages", "/,/cart", "--click", "#menu"}) {
		t.Errorf("first run %q", calls[0].args)
	}
	if string(calls[0].script) != string(screenshotScript) {
		t.Errorf("the script run is not the embedded screenshot.mjs")
	}
	wantModules := "GRAIN_SCREENSHOT_MODULE_PATHS=" + filepath.Join(cache, "grain", "screenshot", "node_modules")
	if !slices.Contains(calls[0].env, wantModules) {
		t.Errorf("env lacks %q", wantModules)
	}
	// 2. Playwright was missing: playwright-core at the asked-for
	// version, into the cache, without downloading browsers.
	install := strings.Join(calls[1].args, " ")
	if !strings.Contains(install, "npm install --prefix "+filepath.Join(cache, "grain", "screenshot")) || !strings.HasSuffix(install, "playwright-core@1.2.3") {
		t.Errorf("install ran %q", install)
	}
	if !slices.Contains(calls[1].env, "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1") {
		t.Errorf("install env lacks PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1")
	}
	if !strings.Contains(stderr, "installing playwright-core@1.2.3") {
		t.Errorf("stderr %q does not say it is installing", stderr)
	}
	// 3. And the script again.
	if !slices.Equal(calls[2].args[2:], calls[0].args[2:]) {
		t.Errorf("second run %q", calls[2].args)
	}
}

func TestGrainScreenshotReportsTheScriptsFailure(t *testing.T) {
	fakeNodeOnPath(t)
	prev := screenshotRun
	t.Cleanup(func() { screenshotRun = prev })
	runs := 0
	screenshotRun = func(*exec.Cmd) (int, error) { runs++; return 1, nil }
	env := fakeSandboxEnv(map[string]string{"GRAIN_SANDBOX_PROXY": "http://proxy.invalid", "GRAIN_SANDBOX_TOKEN": "tok", "XDG_CACHE_HOME": t.TempDir()}, "")
	_, stderr, code := runIn(t, env, "screenshot", "--url", "http://127.0.0.1:1")
	if code != 1 || runs != 1 {
		t.Fatalf("exit %d after %d runs, want 1 after 1", code, runs)
	}
	// The script printed its own reason; the CLI adds nothing.
	if stderr != "" {
		t.Errorf("stderr %q, want the script's own words only", stderr)
	}
}

func TestGrainScreenshotHelp(t *testing.T) {
	prev := screenshotRun
	t.Cleanup(func() { screenshotRun = prev })
	screenshotRun = func(*exec.Cmd) (int, error) { t.Fatal("help ran a process"); return 0, nil }
	env := fakeSandboxEnv(map[string]string{"GRAIN_SANDBOX_PROXY": "http://proxy.invalid", "GRAIN_SANDBOX_TOKEN": "tok"}, "")
	for _, args := range [][]string{{"screenshot"}, {"screenshot", "--help"}, {"screenshot", "--url", "http://x", "-h"}, {"help", "screenshot"}} {
		stdout, _, code := runIn(t, env, args...)
		if code != 0 || !strings.HasPrefix(stdout, "grain screenshot --url") || !strings.Contains(stdout, "--reference FILE") {
			t.Errorf("%q: exit %d, stdout %q", args, code, stdout)
		}
	}
	top, _, _ := runIn(t, env, "help")
	if !strings.Contains(top, "grain screenshot --url") {
		t.Errorf("grain help does not list grain screenshot")
	}
}
