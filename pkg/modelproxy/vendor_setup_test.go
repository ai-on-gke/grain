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

package modelproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The codex recipe is shell a run pastes, so it is run here rather than
// only read: every line up to the login, with CODEX_HOME moved into a
// temporary directory, has to leave a config.toml whose openai_base_url
// is exactly the address placed in the sandbox -- the /v1 included,
// since codex appends /responses to it. The login itself needs the codex
// binary, which this suite does not carry; it was run by hand against
// codex-cli 0.153.0 and a stub vendor behind this proxy.
func TestCodexSetupWritesTheProxyAddressIntoItsOwnConfig(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	urlPath := filepath.Join(dir, "openai-proxy-url")
	tokenPath := filepath.Join(dir, "openai-proxy-token")
	const address = "http://172.17.0.1:41234/v1"
	if err := os.WriteFile(urlPath, []byte(address+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "codex-home")

	recipe := OpenAI.Setup(urlPath, tokenPath)
	var script []string
	for _, line := range strings.Split(recipe, "\n") {
		if !strings.HasPrefix(line, "    ") {
			continue
		}
		line = strings.TrimPrefix(line, "    ")
		if strings.HasPrefix(line, "codex login --with-api-key < "+tokenPath) {
			continue
		}
		script = append(script, strings.ReplaceAll(line, CodexHome, home))
	}
	if len(script) != 3 {
		t.Fatalf("recipe has %d shell lines before the login, want 3:\n%s", len(script), recipe)
	}
	out, err := exec.Command("sh", "-ec", strings.Join(script, "\n")).CombinedOutput()
	if err != nil {
		t.Fatalf("running the recipe: %v\n%s", err, out)
	}
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `openai_base_url = "` + address + `"` + "\n"; string(config) != want {
		t.Errorf("config.toml = %q, want %q", config, want)
	}
}
