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
	"os"
	"path/filepath"
	"testing"
)

type grainPlacer struct {
	sha256sum string
	placed    map[string]string
	modes     map[string]string
}

func (p *grainPlacer) PlaceFile(_ context.Context, path, content, mode string) error {
	if p.placed == nil {
		p.placed, p.modes = map[string]string{}, map[string]string{}
	}
	p.placed[path], p.modes[path] = content, mode
	return nil
}

func (p *grainPlacer) RunInSandbox(context.Context, []string, string) (string, string, int) {
	return p.sha256sum, "", 0
}

func writeSandboxGrain(t *testing.T, content string) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "grain-sandbox")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(content))
	return path, hex.EncodeToString(digest[:])
}

// A sandbox whose image predates the sandbox CLI gets this daemon's own
// grain-sandbox on run_command's PATH; one that already carries those
// bytes, in its image or from an earlier attempt, is not sent them again.
func TestPlaceSandboxGrain(t *testing.T) {
	ctx := context.Background()
	binary, sum := writeSandboxGrain(t, "#!grain-sandbox v2\n")

	stale := &grainPlacer{sha256sum: "0123  /usr/local/bin/grain\n"}
	if err := placeSandboxGrain(ctx, binary, "", stale); err != nil {
		t.Fatal(err)
	}
	if stale.placed[SandboxGrainPath] != "#!grain-sandbox v2\n" || stale.modes[SandboxGrainPath] != "0755" {
		t.Errorf("placed %v (modes %v), want %s with mode 0755", stale.placed, stale.modes, SandboxGrainPath)
	}

	for _, where := range []string{"/usr/local/bin/grain", SandboxGrainPath} {
		current := &grainPlacer{sha256sum: sum + "  " + where + "\n"}
		if err := placeSandboxGrain(ctx, binary, "", current); err != nil {
			t.Fatal(err)
		}
		if len(current.placed) != 0 {
			t.Errorf("a sandbox already carrying this build at %s was sent it again: %v", where, current.placed)
		}
	}

	// A local sandbox directory gets the same file under its root.
	root := t.TempDir()
	if err := placeSandboxGrain(ctx, binary, root, nil); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(root, SandboxGrainPath)
	if raw, err := os.ReadFile(full); err != nil || string(raw) != "#!grain-sandbox v2\n" {
		t.Errorf("reading %s: %q, %v", full, raw, err)
	} else if info, _ := os.Stat(full); info.Mode().Perm() != 0o755 {
		t.Errorf("%s mode = %v, want 0755", full, info.Mode().Perm())
	}

	// No binary configured places nothing.
	none := &grainPlacer{}
	if err := placeSandboxGrain(ctx, "", "", none); err != nil || len(none.placed) != 0 {
		t.Errorf("with no binary: err %v, placed %v", err, none.placed)
	}
}

func TestSandboxGrainBinaryBeside(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "grain")
	if got := SandboxGrainBinaryBeside(exe); got != "" {
		t.Errorf("with no grain-sandbox beside it: %q, want \"\"", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "grain-sandbox"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := SandboxGrainBinaryBeside(exe); got != filepath.Join(dir, "grain-sandbox") {
		t.Errorf("SandboxGrainBinaryBeside = %q", got)
	}
}
