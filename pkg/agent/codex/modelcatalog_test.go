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

package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripApplyPatchNullsEveryModelAndKeepsTheRest(t *testing.T) {
	dump := `{"models":[{"slug":"a","apply_patch_tool_type":"freeform","shell_type":"unified_exec","future_field":{"x":1}},{"slug":"b"}],"etag":"e1"}`
	out, err := stripApplyPatch([]byte(dump))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Models []map[string]any `json:"models"`
		ETag   string           `json:"etag"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 2 || got.ETag != "e1" {
		t.Fatalf("catalog reshaped: %s", out)
	}
	for _, model := range got.Models {
		value, present := model["apply_patch_tool_type"]
		if !present || value != nil {
			t.Errorf("model %v: apply_patch_tool_type = %v (present %v), want null", model["slug"], value, present)
		}
	}
	if got.Models[0]["shell_type"] != "unified_exec" || got.Models[0]["future_field"] == nil {
		t.Errorf("other fields not carried through: %s", out)
	}
}

func TestStripApplyPatchRefusesWhatCodexWouldRefuse(t *testing.T) {
	for _, dump := range []string{``, `{}`, `{"models":[]}`, `{"models":"x"}`} {
		if _, err := stripApplyPatch([]byte(dump)); err == nil {
			t.Errorf("stripApplyPatch(%q) succeeded, want an error", dump)
		}
	}
}

// writeModelCatalog runs the configured codex binary's `debug models` in
// the run's home and points config.toml at the stripped result.
func TestWriteConfigNamesAStrippedModelCatalog(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	script := "#!/bin/sh\n" +
		"[ \"$1 $2\" = \"debug models\" ] || exit 3\n" +
		"[ -f \"$CODEX_HOME/config.toml\" ] || exit 4\n" +
		"[ \"$OPENAI_API_KEY\" = sk-test ] || exit 5\n" +
		"echo '{\"models\":[{\"slug\":\"gpt-x\",\"apply_patch_tool_type\":\"freeform\"}]}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f := New(fake, "grain", WithAPIKey("sk-test"))
	f.startMCP = newTestFramework(&recordingRunner{}, "grain").startMCP
	home, _, cleanup, err := f.writeConfig(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	config, err := os.ReadFile(filepath.Join(home, configFileName))
	if err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(home, modelCatalogFileName)
	quoted, _ := tomlString(catalogPath)
	lines := strings.Split(string(config), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "#") || lines[1] != "model_catalog_json = "+quoted {
		t.Fatalf("config does not name the catalog as its first key:\n%s", config)
	}
	catalog, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(catalog), "freeform") || !strings.Contains(string(catalog), `"apply_patch_tool_type":null`) {
		t.Errorf("catalog still offers apply_patch: %s", catalog)
	}
}

func TestWriteConfigFailsClosedWhenTheCatalogCannotBeRead(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '{\"models\":[]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := New(fake, "grain")
	f.startMCP = newTestFramework(&recordingRunner{}, "grain").startMCP
	if _, _, cleanup, err := f.writeConfig(context.Background(), nil, ""); err == nil {
		cleanup()
		t.Fatal("writeConfig succeeded with no usable model catalog, want an error")
	}
}
