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

package staterepo

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// A template's change target crosses settings/templates.yml as the bare
// kind, or {kind, spec} with the spec as a mapping, and comes back as
// the two columns it was.
func TestChangeTargetColumnsRoundTripThroughTheSettingsFile(t *testing.T) {
	f := declFieldShape{key: "change-target", columns: []string{"change_target_kind", "change_target_spec"}, shape: shapeChangeTarget}
	for _, c := range []struct {
		kind, spec, yaml string
	}{
		{"", "", "change-target: \"\"\n"},
		{"workspace", "", "change-target: workspace\n"},
		{"workspace", `{"start_cl":12,"workspace":"mine"}`, "change-target:\n    kind: workspace\n    spec: {\"start_cl\": 12, \"workspace\": \"mine\"}\n"},
	} {
		name, node, err := columnNode(f, map[string]any{"change_target_kind": c.kind, "change_target_spec": c.spec})
		if err != nil {
			t.Fatal(err)
		}
		doc := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{key(name), node}}
		out, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != c.yaml {
			t.Errorf("%s %s written as %q, want %q", c.kind, c.spec, out, c.yaml)
		}
		var back yaml.Node
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		if err := decodeIntoRow("settings/templates.yml", "tpl-1", f, back.Content[0].Content[1], row); err != nil {
			t.Fatal(err)
		}
		if row["change_target_kind"] != c.kind || row["change_target_spec"] != c.spec {
			t.Errorf("%s %s read back as %v", c.kind, c.spec, row)
		}
	}
}
