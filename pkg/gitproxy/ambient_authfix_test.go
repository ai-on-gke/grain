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

package gitproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/authfix/authfixtest"
)

// A command-backed token source on a host whose credentials have lapsed:
// the tool cannot run at all. That is said as the fix the registered
// classifier names, not as a login the tool's own login command would
// repair.
func TestCommandTokenSaysExpiredHostCredentialsAsTheirFix(t *testing.T) {
	authfixtest.Register(t)
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not installed")
	}
	bin := t.TempDir()
	script := "#!" + sh + "\necho \"$0: host credential lapsed\" >&2\nexit 126\n"
	if err := os.WriteFile(filepath.Join(bin, "mint"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GRAIN_IN_CONTAINER", "0")

	_, err = commandToken(AmbientCommandSource{
		Command: "mint", Args: []string{"auth", "token"}, LoginFix: "mint auth login",
	}, "forge.example.test")
	expired, ok := authfix.As(err)
	if !ok {
		t.Fatalf("commandToken err = %v, want an authfix.Error", err)
	}
	if !strings.Contains(err.Error(), authfixtest.Action) || strings.Contains(err.Error(), "mint auth login") {
		t.Errorf("err = %q, want the registered fix and not the tool's own login", err)
	}
	if !strings.HasSuffix(expired.Path, "/mint") {
		t.Errorf("Path = %q, want the tool that could not run", expired.Path)
	}
}
