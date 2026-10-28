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

package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A host-directory sandbox's run_command executes as the daemon's own
// account, so a path that account can read is one an agent can read:
// on a single-tenant daemon that includes <data-dir>/local-api-token,
// the access code, which together with the UI's URL is full owner
// access (docs/access-code.md, grain/task-4366). The working directory
// and sandboxEnv's allowlist confine nothing on the filesystem.
//
// hide is the deny-list that closes that. Given one, run_command runs
// its command under bubblewrap (bwrap) rather than as a plain child:
//
//   - a mount namespace in which every hidden directory is an empty
//     tmpfs and every hidden file is /dev/null, so the paths still exist
//     but hold nothing. The sandbox root is bound back on top when it
//     sits under a hidden directory, so the run keeps its own files.
//     bwrap drops every capability before it runs the command, and the
//     kernel locks mounts a namespace inherits, so neither an umount
//     nor a nested `unshare` uncovers what is underneath.
//   - a PID namespace with its own /proc, so the command cannot reach
//     the hidden files through /proc/<daemon>/root, nor read the token
//     out of the memory or environment of the daemon or of the `grain
//     mcpserver` that started it, both of which run as the same account.
//
// Everything else is the host's as before: the same filesystem, network,
// devices and tools. One thing does change: bwrap sets no_new_privs, so
// sudo and other setuid programs do not work in a confined command --
// which is the point, since root reads every file there is.
//
// It fails closed. With a non-empty hide and no working bwrap, the
// command is refused rather than run unconfined.

// confineBinary is the bubblewrap executable run_command looks up on
// PATH for a confined command.
const confineBinary = "bwrap"

// commandArgv is the argv run_command executes command with in root:
// plain `bash -c command` with nothing to hide, else that wrapped in
// bwrap (confinedArgv).
func commandArgv(root string, hide []string, command string) ([]string, error) {
	shell := []string{"bash", "-c", command}
	if len(hide) == 0 {
		return shell, nil
	}
	bwrap, err := exec.LookPath(confineBinary)
	if err != nil {
		return nil, fmt.Errorf("run_command refused: this sandbox must not reach %s, which needs bubblewrap "+
			"to hide them, and %q is not installed on this host (%v) -- install the bubblewrap package, "+
			"or dispatch into a container or granule sandbox instead", strings.Join(hide, ", "), confineBinary, err)
	}
	return confinedArgv(bwrap, root, hide, shell)
}

// confinedArgv wraps argv in bwrap, hiding hide from it and starting
// it in root. A hidden path that does not exist is skipped: there is
// nothing under it to read, and bwrap cannot mount over it.
func confinedArgv(bwrap, root string, hide, argv []string) ([]string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("run_command: resolving sandbox root %q: %w", root, err)
	}
	args := []string{bwrap,
		"--die-with-parent",
		"--unshare-pid",
		"--dev-bind", "/", "/",
		"--proc", "/proc",
	}
	rebindRoot := false
	for _, p := range hide {
		// bwrap mounts over the path it resolves to, and so must the
		// root check below: a data directory reached through a symlink
		// is hidden at its real location, where every other name for
		// it also leads.
		real, err := filepath.EvalSymlinks(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("run_command: resolving hidden path %q: %w", p, err)
		}
		info, err := os.Stat(real)
		if err != nil {
			return nil, fmt.Errorf("run_command: hidden path %q: %w", p, err)
		}
		if real == realRoot {
			return nil, fmt.Errorf("run_command: sandbox root %q is itself hidden path %q", root, p)
		}
		if info.IsDir() {
			args = append(args, "--tmpfs", real)
			if within(realRoot, real) {
				rebindRoot = true
			}
		} else {
			args = append(args, "--ro-bind", "/dev/null", real)
		}
	}
	if rebindRoot {
		args = append(args, "--bind", realRoot, realRoot)
	}
	args = append(args, "--chdir", realRoot, "--")
	return append(args, argv...), nil
}

// within reports whether p is dir or a path under it; both are clean
// and absolute.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CheckHostConfinement reports whether run_command can hide paths on
// this host: it confines a command from a scratch directory holding a
// file, and fails unless the command runs and the file reads empty.
// A daemon whose host sandboxes must hide its data directory calls it
// at start-up, so that a host without bubblewrap, or one whose kernel
// refuses it unprivileged user namespaces, is refused up front rather
// than failing every run_command.
func CheckHostConfinement(ctx context.Context) error {
	scratch, err := os.MkdirTemp("", "grain-confine-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	hidden := filepath.Join(scratch, "hidden")
	root := filepath.Join(scratch, "root")
	for _, dir := range []string{hidden, root} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}
	canary := filepath.Join(hidden, "canary")
	if err := os.WriteFile(canary, []byte("visible"), 0o600); err != nil {
		return err
	}
	argv, err := commandArgv(root, []string{hidden}, "cat "+canary+" 2>/dev/null; true")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = sandboxEnv(root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s cannot confine a command on this host: %v: %s", confineBinary, err, strings.TrimSpace(string(out)))
	}
	if strings.Contains(string(out), "visible") {
		return fmt.Errorf("%s ran, but a command it confined could still read a file it was told to hide", confineBinary)
	}
	return nil
}
