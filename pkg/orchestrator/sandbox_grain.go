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

package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// SandboxGrainPath is where sandbox setup places this daemon's own build
// of the sandbox's grain (cmd/grain-sandbox): protocol.SandboxBinDir,
// which every run_command puts first on PATH, so it answers `grain`
// ahead of whatever the image carries at /usr/local/bin/grain.
//
// It is placed because an image is not rebuilt when the daemon is. A
// granule guest or sandbox pod image built before the sandbox CLI
// (pkg/sandboxcli) reached cmd/grain-sandbox answers every `grain`
// command but host-script with "not a command this sandbox's grain has",
// which is what every run's first `grain` call met after the Stage 5
// upgrade made the CLI how a run reaches CI and the namespace -- and a
// run on a repository without grain in its module graph has no way to
// build one. An image that does carry it can still be older than the
// daemon whose proxy routes it talks to, which the same placement fixes.
// protocol.ReservedPath keeps every grant's placements off it.
var SandboxGrainPath = protocol.SandboxBinDir + "/" + protocol.SandboxGrainName

// sandboxGrainImagePath is the image's own copy, checked so a sandbox
// whose image was built from this very commit is not sent the same
// eight megabytes again.
const sandboxGrainImagePath = "/usr/local/bin/grain"

// SandboxGrainBinaryBeside is the grain-sandbox binary installed next to
// executable (the daemon image puts /usr/local/bin/grain-sandbox beside
// /usr/local/bin/grain), or "" when there is none.
func SandboxGrainBinaryBeside(executable string) string {
	if executable == "" {
		return ""
	}
	p := filepath.Join(filepath.Dir(executable), "grain-sandbox")
	if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return p
}

// sandboxGrainBinary is one binary read off the controller's disk, with
// its digest, cached per path: every dispatch places the same file.
type sandboxGrainBinary struct {
	content string
	sum     string
}

var sandboxGrainCache sync.Map // path -> sandboxGrainBinary

func readSandboxGrain(path string) (sandboxGrainBinary, error) {
	if v, ok := sandboxGrainCache.Load(path); ok {
		return v.(sandboxGrainBinary), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return sandboxGrainBinary{}, err
	}
	digest := sha256.Sum256(raw)
	b := sandboxGrainBinary{content: string(raw), sum: hex.EncodeToString(digest[:])}
	sandboxGrainCache.Store(path, b)
	return b, nil
}

// placeSandboxGrain puts binary at SandboxGrainPath in the sandbox unless
// the sandbox already carries those exact bytes there or at
// /usr/local/bin/grain. Like applyPlacements it writes through placer
// where the sandbox has one and under root where it is a local directory.
// Errors are the caller's to log rather than fail the run over: a run
// without it still has the image's grain and its MCP tools.
func placeSandboxGrain(ctx context.Context, binary, root string, placer SandboxPlacer) error {
	if binary == "" {
		return nil
	}
	b, err := readSandboxGrain(binary)
	if err != nil {
		return fmt.Errorf("reading %s: %w", binary, err)
	}
	if placer != nil {
		if commander, ok := placer.(SandboxCommander); ok {
			stdout, _, _ := commander.RunInSandbox(ctx, []string{"sh", "-c",
				"sha256sum " + SandboxGrainPath + " " + sandboxGrainImagePath + " 2>/dev/null; true"}, "")
			if carriesDigest(stdout, b.sum) {
				return nil
			}
		}
		if err := placer.PlaceFile(ctx, SandboxGrainPath, b.content, "0755"); err != nil {
			return fmt.Errorf("placing %s: %w", SandboxGrainPath, err)
		}
		return nil
	}
	if root == "" {
		return nil
	}
	full := filepath.Join(root, strings.TrimPrefix(SandboxGrainPath, "/"))
	if raw, err := os.ReadFile(full); err == nil {
		if digest := sha256.Sum256(raw); hex.EncodeToString(digest[:]) == b.sum {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(b.content), 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", full, err)
	}
	return os.Chmod(full, 0o755)
}

// carriesDigest reports whether sha256sum's output names sum for any file.
func carriesDigest(sha256sumOutput, sum string) bool {
	for _, line := range strings.Split(sha256sumOutput, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == sum {
			return true
		}
	}
	return false
}
