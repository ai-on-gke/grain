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

package daemon

import (
	"path/filepath"
	"testing"
)

// TestAbsDataDir is the regression for the 2026-10-29 walkthrough's F6: a
// relative -data-dir broke every embedded CI run, whose workflow clone
// runs git from another working directory.
func TestAbsDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if got, want := absDataDir("d2"), filepath.Join(dir, "d2"); got != want {
		t.Errorf("absDataDir(%q) = %q, want %q", "d2", got, want)
	}
	if got := absDataDir("/var/lib/grain"); got != "/var/lib/grain" {
		t.Errorf("absDataDir kept an absolute path as %q", got)
	}
	if got := absDataDir(""); got != "" {
		t.Errorf("absDataDir(\"\") = %q, want empty (the flag's own check refuses it)", got)
	}
}
