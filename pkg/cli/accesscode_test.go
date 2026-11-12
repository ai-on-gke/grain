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

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestAccessCodeShowsAndRotatesTheCode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv(dataDirEnvVar, dir)
	code, err := ui.EnsureLocalAPIToken(ui.LocalAPITokenPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := runAccessCode(nil, &out, &errOut); err != nil {
		t.Fatalf("grain access-code: %v (%s)", err, errOut.String())
	}
	if strings.TrimSpace(out.String()) != code {
		t.Errorf("grain access-code printed %q, want %q", out.String(), code)
	}

	out.Reset()
	if err := runAccessCode([]string{"rotate"}, &out, &errOut); err != nil {
		t.Fatalf("grain access-code rotate: %v (%s)", err, errOut.String())
	}
	rotated := strings.TrimSpace(out.String())
	if rotated == "" || rotated == code {
		t.Fatalf("grain access-code rotate printed %q (old %q)", rotated, code)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ui.LocalAPITokenFileName))
	if strings.TrimSpace(string(b)) != rotated {
		t.Errorf("the file holds %q after rotate, want %q", b, rotated)
	}
	if fi, _ := os.Stat(filepath.Join(dir, ui.LocalAPITokenFileName)); fi.Mode().Perm() != 0o600 {
		t.Errorf("rotate left the file mode %o, want 0600", fi.Mode().Perm())
	}
}

func TestAccessCodeRotateNeedsAnExistingInstallation(t *testing.T) {
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv(dataDirEnvVar, t.TempDir())
	var out, errOut bytes.Buffer
	if err := runAccessCode([]string{"rotate"}, &out, &errOut); err == nil {
		t.Errorf("rotate with no code file succeeded: %q", out.String())
	}
}

// `grain access-code mode recovery|off|on` sets the mode and never prints
// the code: the two-argument form fell through to "show", so the mode
// file was never written and the code went to stdout instead
// (docs/test-runs/2026-11-11-caller-identity.md, F3).
func TestAccessCodeModeSetsTheModeWithoutPrintingTheCode(t *testing.T) {
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	dir := t.TempDir()
	path := ui.LocalAPITokenPath(dir)
	const code = "ABCD-EFGH-IJKL-MNOP"
	if err := os.WriteFile(path, []byte(code+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []ui.AccessCodeMode{ui.AccessCodeRecovery, ui.AccessCodeOff, ui.AccessCodeOn} {
		var out, errOut bytes.Buffer
		if err := runAccessCode([]string{"-data-dir", dir, "mode", string(mode)}, &out, &errOut); err != nil {
			t.Fatalf("grain access-code mode %s: %v (%s)", mode, err, errOut.String())
		}
		if strings.Contains(out.String()+errOut.String(), code) {
			t.Fatalf("grain access-code mode %s printed the code: %q", mode, out.String())
		}
		if got := strings.TrimSpace(out.String()); got != string(mode) {
			t.Errorf("grain access-code mode %s printed %q, want the mode", mode, got)
		}
		if got := ui.ReadAccessCodeMode(path); got != mode {
			t.Errorf("after grain access-code mode %s the mode file says %q", mode, got)
		}
	}
}
