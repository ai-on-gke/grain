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
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// screenshotScript is the Playwright program grain screenshot runs with
// node: screenshots of the app a run is working on, and a contact sheet
// of them to attach to its closing note.
//
//go:embed screenshot.mjs
var screenshotScript []byte

// defaultPlaywrightVersion is the playwright-core grain screenshot
// installs when neither the app nor npm's global root has Playwright. It
// matches the browsers the sandbox images pre-install
// (Granulefile's PLAYWRIGHT_VERSION into /var/cache/playwright); a
// sandbox with other browsers sets $GRAIN_PLAYWRIGHT_VERSION.
const defaultPlaywrightVersion = "1.62.1"

// sandboxPlaywrightBrowsers is where the sandbox images pre-install
// Playwright's browsers (Granulefile, Dockerfile.k8s-sandbox).
var sandboxPlaywrightBrowsers = "/var/cache/playwright"

// screenshotMissingPlaywright is screenshot.mjs's exit status when it
// found no Playwright module to import.
const screenshotMissingPlaywright = 3

// screenshotRun runs one process to completion and returns its exit
// status; tests replace it.
var screenshotRun = func(cmd *exec.Cmd) (int, error) {
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// screenshotCommand runs grain screenshot with args, everything after
// "screenshot". The flags are screenshot.mjs's own, checked there.
func (c *sandboxCLI) screenshotCommand(args []string) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		return c.help("screenshot")
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if isHelpFlag(a) {
			return c.help("screenshot")
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return fmt.Errorf("grain screenshot needs node on PATH (grain's sandbox images carry it at /usr/local/bin/node): %w", err)
	}
	dir, err := os.MkdirTemp("", "grain-screenshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "screenshot.mjs")
	if err := os.WriteFile(script, screenshotScript, 0o600); err != nil {
		return err
	}
	cache := c.screenshotCacheDir()
	env := append(os.Environ(), "GRAIN_SCREENSHOT_MODULE_PATHS="+strings.Join(c.screenshotModulePaths(cache), string(os.PathListSeparator)))
	// The images set PLAYWRIGHT_BROWSERS_PATH for their own shells, but an
	// agent's command runner need not pass it on: point Playwright at the
	// pre-installed browsers whenever they are there.
	if c.envOr("PLAYWRIGHT_BROWSERS_PATH", "") == "" {
		if fi, err := os.Stat(sandboxPlaywrightBrowsers); err == nil && fi.IsDir() {
			env = append(env, "PLAYWRIGHT_BROWSERS_PATH="+sandboxPlaywrightBrowsers)
		}
	}

	run := func() (int, error) {
		cmd := exec.Command(node, append([]string{script}, args...)...)
		cmd.Env = env
		cmd.Stdin = c.in
		cmd.Stdout = c.out
		cmd.Stderr = c.errOut
		return screenshotRun(cmd)
	}
	code, err := run()
	if err != nil {
		return err
	}
	if code == screenshotMissingPlaywright {
		version := c.envOr("GRAIN_PLAYWRIGHT_VERSION", defaultPlaywrightVersion)
		fmt.Fprintf(c.errOut, "grain screenshot: installing playwright-core@%s into %s (once per sandbox)\n", version, cache)
		if err := os.MkdirAll(cache, 0o755); err != nil {
			return err
		}
		install := exec.Command("npm", "install", "--prefix", cache, "--no-audit", "--no-fund", "--loglevel=error", "playwright-core@"+version)
		// The browsers are the image's own (PLAYWRIGHT_BROWSERS_PATH):
		// the module is all this needs.
		install.Env = append(os.Environ(), "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1")
		install.Stdout = c.errOut
		install.Stderr = c.errOut
		icode, err := screenshotRun(install)
		if err != nil {
			return fmt.Errorf("installing playwright-core: %w", err)
		}
		if icode != 0 {
			return fmt.Errorf("installing playwright-core@%s failed (status %d): add Playwright to the app instead (npm install -D playwright-core) and run grain screenshot from its directory", version, icode)
		}
		if code, err = run(); err != nil {
			return err
		}
	}
	if code != 0 {
		// screenshot.mjs has already said why on stderr.
		return errSandboxReported
	}
	return nil
}

// screenshotCacheDir is where grain screenshot installs playwright-core
// when it has to: $XDG_CACHE_HOME/grain/screenshot, else
// ~/.cache/grain/screenshot.
func (c *sandboxCLI) screenshotCacheDir() string {
	if x := c.envOr("XDG_CACHE_HOME", ""); x != "" {
		return filepath.Join(x, "grain", "screenshot")
	}
	home := c.envOr("HOME", "")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".cache", "grain", "screenshot")
}

// screenshotModulePaths are the directories screenshot.mjs looks for
// Playwright in after the working directory: grain's cache, then npm's
// global root.
func (c *sandboxCLI) screenshotModulePaths(cache string) []string {
	paths := []string{filepath.Join(cache, "node_modules")}
	if out, err := exec.Command("npm", "root", "-g").Output(); err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			paths = append(paths, root)
		}
	}
	return paths
}

func (c *sandboxCLI) envOr(name, fallback string) string {
	getenv := c.env
	if getenv == nil {
		getenv = os.Getenv
	}
	if v := getenv(name); v != "" {
		return v
	}
	return fallback
}
