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

// PlaceFileOverSSH is the placement half of what ConfigureGitCredentials-
// OverSSH does for git: a controller-side write into a sandbox it has no
// filesystem route to. A capability's model.Placement (gcpkey's minted
// service-account key, geminikey's API key, githubsandbox's token) names
// an absolute path inside the sandbox, and for a local directory the
// executor just calls os.MkdirAll/os.WriteFile under that directory's
// root. A kontur VM has no such root -- SSH, over runner, is the only
// path to its filesystem at all -- so the same three steps run remotely
// instead, the same way ssh_tools.go's own handlers run `cat`/`dd`/
// `mkdir` rather than calling os.* directly.

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

// FilePlacement is one file in a batched placement (PlaceFilesOverSSH).
type FilePlacement struct {
	Path    string
	Content []byte
	Mode    string
	Owner   string
}

type batchFilePlacer interface {
	PutFiles(ctx context.Context, files []sandbox.FileUpload) error
}

func parsePlacementMode(mode string) fs.FileMode {
	if mode == "" {
		return 0o600
	}
	v, err := strconv.ParseUint(mode, 8, 32)
	if err != nil || v == 0 {
		return 0o600
	}
	return fs.FileMode(v)
}

// PlaceFilesOverSSH writes a batch of files to runner's remote host. When
// runner supports granule's batched PutFiles protocol, all files are uploaded
// in a single round-trip; otherwise each file is placed with PlaceFileOverSSH.
func PlaceFilesOverSSH(ctx context.Context, runner remoteRunner, files []FilePlacement) error {
	if len(files) == 0 {
		return nil
	}
	if bp, ok := runner.(batchFilePlacer); ok {
		uploads := make([]sandbox.FileUpload, 0, len(files))
		allAbs := true
		for _, f := range files {
			if !strings.HasPrefix(f.Path, "/") {
				allAbs = false
				break
			}
			uploads = append(uploads, sandbox.FileUpload{
				Path:    f.Path,
				Content: f.Content,
				Mode:    parsePlacementMode(f.Mode),
				Owner:   f.Owner,
			})
		}
		if allAbs {
			return bp.PutFiles(ctx, uploads)
		}
	}
	for _, f := range files {
		if err := PlaceFileOverSSH(ctx, runner, f.Path, string(f.Content), f.Mode); err != nil {
			return err
		}
	}
	return nil
}

// PlaceFileOverSSH writes content to filePath on runner's remote host
// with mode (an octal string, model.Placement.EffectiveMode's own shape),
// creating filePath's parent directory first. filePath is used as given
// rather than resolved against any workspace, because a placement's path
// is already the absolute path it means inside the sandbox -- unlike
// read_file/write_file's own file_path, which is an agent's argument and
// is deliberately left relative to wherever a session starts.
//
// The mode is applied to an empty file BEFORE the content goes in, not
// chmod'd afterwards as ConfigureGitCredentialsOverSSH does: everything
// placed this way is credential material, and a `dd` that creates the
// file under the login user's umask leaves it world-readable for as long
// as it takes the next command to run. `install -m` collapses "create,
// empty, with the final mode" into the one step that has no such window,
// and truncates an existing file to the same end state -- so a re-place
// over a leftover file cannot inherit a wider mode either.
func PlaceFileOverSSH(ctx context.Context, runner remoteRunner, filePath, content, mode string) error {
	if bp, ok := runner.(batchFilePlacer); ok && strings.HasPrefix(filePath, "/") {
		return bp.PutFiles(ctx, []sandbox.FileUpload{{
			Path:    filePath,
			Content: []byte(content),
			Mode:    parsePlacementMode(mode),
		}})
	}
	// Each of the three steps is idempotent -- mkdir -p, an install over
	// whatever is there, a dd that truncates -- so one the transport cut
	// off is run again rather than failing the sandbox's setup.
	run := func(argv []string, stdin string) (string, string, int) {
		return kubectlgate.RetryIdempotent(ctx, func() (string, string, int) { return runner.Run(ctx, argv, stdin) })
	}
	parent := path.Dir(filePath)
	if _, stderr, exitCode := run([]string{"mkdir", "-p", "--", parent}, ""); exitCode != 0 {
		return fmt.Errorf("creating remote %s: %s", parent, strings.TrimSpace(stderr))
	}
	if _, stderr, exitCode := run([]string{"install", "-m", mode, "--", "/dev/null", filePath}, ""); exitCode != 0 {
		return fmt.Errorf("creating remote %s with mode %s: %s", filePath, mode, strings.TrimSpace(stderr))
	}
	if _, stderr, exitCode := run([]string{"dd", "of=" + filePath, "status=none"}, content); exitCode != 0 {
		return fmt.Errorf("writing remote %s: Error writing %s: %s", filePath, filePath, strings.TrimSpace(stderr))
	}
	return nil
}
