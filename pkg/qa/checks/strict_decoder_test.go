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

package checks

import (
	"context"
	"strings"
	"testing"
)

func newFileDiff(path string) string {
	return "diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1,10 @@\n+package x\n"
}

// TestStrictConfigDecoderIsTypeAware pairs every structural decode (which must never be flagged)
// with the struct decode it is most easily confused with. The negative cases are the shapes that
// produced all 25 night-run findings (3 enforced treatment blocks, 22 control shadow findings).
func TestStrictConfigDecoderIsTypeAware(t *testing.T) {
	const header = "package config\nimport (\n\t\"bytes\"\n\t\"encoding/json\"\n\t\"io\"\n\t\"gopkg.in/yaml.v3\"\n)\nvar _ = io.EOF\nvar _ = json.Valid\nvar _ = bytes.NewReader\n"
	cases := []struct {
		name string
		body string
		want int
	}{
		// Night-run shape: validateSingleYAMLMapping / inspectYAMLPolicyRoot.
		{"yaml.Node document check", `func validateSingleYAMLMapping(data []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	var extra yaml.Node
	return dec.Decode(&extra)
}`, 0},
		{"struct decode beside yaml.Node", `type Policy struct{ Name string }
func load(data []byte) (Policy, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var p Policy
	err := dec.Decode(&p)
	return p, err
}`, 1},
		// Night-run shape: checkJSONUniqueKeys / inspectJSONPolicyObject.
		{"json token streaming", `func checkJSONUniqueKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}`, 0},
		{"json streaming array of structs", `type Item struct{ ID int }
func items(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if _, err := dec.Token(); err != nil {
		return err
	}
	for dec.More() {
		var it Item
		if err := dec.Decode(&it); err != nil {
			return err
		}
	}
	return nil
}`, 1},
		// Night-run shape: ParseConfigYAML (node pre-scan, then a strict struct decoder).
		{"node pre-scan then strict struct decoder", `type Config struct{ CPUs []int }
func ParseConfigYAML(data []byte) (Config, error) {
	nodeDec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := nodeDec.Decode(&doc); err != nil {
		return Config{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	err := dec.Decode(&c)
	return c, err
}`, 0},
		{"node pre-scan then lax struct decoder", `type Config struct{ CPUs []int }
func ParseConfigYAML(data []byte) (Config, error) {
	nodeDec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := nodeDec.Decode(&doc); err != nil {
		return Config{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var c Config
	err := dec.Decode(&c)
	return c, err
}`, 1},
		{"map[string]any", `func m(data []byte) error {
	var out map[string]any
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(&out)
}`, 0},
		{"map of structs", `type Profile struct{ Level int }
func m(data []byte) error {
	var out map[string]Profile
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(&out)
}`, 1},
		{"json.RawMessage", `func raw(data []byte) error {
	var r json.RawMessage
	return json.NewDecoder(bytes.NewReader(data)).Decode(&r)
}`, 0},
		{"pointer parameter to struct", `type Spec struct{ A string }
func into(data []byte, dst *Spec) error {
	return json.NewDecoder(bytes.NewReader(data)).Decode(dst)
}`, 1},
		{"generic any destination is not proven", `func decodeStrictYAML(r io.Reader, dst any) error {
	return yaml.NewDecoder(r).Decode(dst)
}`, 0},
		{"interface{} variable", `func i(data []byte) error {
	var v interface{}
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(&v)
}`, 0},
		{"named slice of structs via new", `type Rule struct{ Match string }
type Rules []Rule
func r(data []byte) error {
	rs := new(Rules)
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(rs)
}`, 1},
		{"named map of any", `type Bag map[string]any
func b(data []byte) error {
	var bag Bag
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(&bag)
}`, 0},
		{"alias to struct", `type inner struct{ X int }
type Outer = inner
func a(data []byte) error {
	o := &Outer{}
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(o)
}`, 1},
		{"empty struct", `func e(data []byte) error {
	var v struct{}
	return json.NewDecoder(bytes.NewReader(data)).Decode(&v)
}`, 0},
		{"struct field target", `type Section struct{ K string }
type File struct{ S Section }
func f(data []byte) error {
	var file File
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(&file.S)
}`, 1},
		{"yaml.Unmarshal into struct", `type U struct{ K string }
func u(data []byte) error {
	var v U
	return yaml.Unmarshal(data, &v)
}`, 1},
		{"yaml.Unmarshal into yaml.Node", `func u(data []byte) error {
	var n yaml.Node
	return yaml.Unmarshal(data, &n)
}`, 0},
		{"decoder returned without Decode", `func open(r io.Reader) *yaml.Decoder {
	return yaml.NewDecoder(r)
}`, 0},
		{"scalar", `func s(data []byte) error {
	var n int
	return json.NewDecoder(bytes.NewReader(data)).Decode(&n)
}`, 0},
		{"strict json struct decoder", `type J struct{ K string }
func j(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var v J
	return dec.Decode(&v)
}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "pkg/config/load.go"
			diff := newFileDiff(path)
			ws := &InMemoryWorkspace{Diff: diff, HeadFiles: map[string]string{path: header + tc.body + "\n"}}
			got, err := CheckStrictConfigDecoder(context.Background(), ws, ParseUnifiedDiff(diff), nil)
			if err != nil {
				t.Fatalf("CheckStrictConfigDecoder: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d findings, want %d: %+v", len(got), tc.want, got)
			}
		})
	}
}

// TestStrictConfigDecoderResolvesTypesAcrossFiles covers a decode target declared in another file
// of the same package (not in the diff) and in another package of the same module.
func TestStrictConfigDecoderResolvesTypesAcrossFiles(t *testing.T) {
	const loader = `package config
import (
	"bytes"
	"gopkg.in/yaml.v3"
	"example.com/m/pkg/model"
)
func LoadLocal(data []byte) error {
	var c Local
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(&c)
}
func LoadNode(data []byte) error {
	var n NodeAlias
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(&n)
}
func LoadModel(data []byte) error {
	var m model.Settings
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(&m)
}
func LoadModelBag(data []byte) error {
	var m model.Bag
	return yaml.NewDecoder(bytes.NewReader(data)).Decode(&m)
}
`
	head := map[string]string{
		"go.mod":                   "module example.com/m\n\ngo 1.24\n",
		"pkg/config/load.go":       loader,
		"pkg/config/types.go":      "package config\nimport \"gopkg.in/yaml.v3\"\ntype Local struct{ Name string }\ntype NodeAlias = yaml.Node\n",
		"pkg/model/settings.go":    "package model\ntype (\n\tSettings struct{ Level int }\n\tBag map[string]string\n)\n",
		"pkg/model/unrelated.go":   "package model\nfunc Local() {}\n",
		"pkg/other/decoy_types.go": "package other\ntype Settings map[string]any\n",
	}
	diff := newFileDiff("pkg/config/load.go")
	ws := &InMemoryWorkspace{Diff: diff, HeadFiles: head}
	got, err := CheckStrictConfigDecoder(context.Background(), ws, ParseUnifiedDiff(diff), nil)
	if err != nil {
		t.Fatalf("CheckStrictConfigDecoder: %v", err)
	}
	var fns []string
	for _, f := range got {
		fns = append(fns, f.Summary)
	}
	joined := strings.Join(fns, "\n")
	if len(got) != 2 || !strings.Contains(joined, "LoadLocal decodes into Local") || !strings.Contains(joined, "LoadModel decodes into model.Settings") {
		t.Fatalf("want findings for LoadLocal and LoadModel only, got:\n%s", joined)
	}
}

// TestStrictConfigDecoderBoundsLookups keeps cross-file resolution inside the S3 budget: at most
// maxStrictDecoderLookups greps however many unresolved type names the diff contains.
func TestStrictConfigDecoderBoundsLookups(t *testing.T) {
	var b strings.Builder
	b.WriteString("package config\nimport (\n\t\"bytes\"\n\t\"gopkg.in/yaml.v3\"\n)\n")
	for i := 0; i < 3*maxStrictDecoderLookups; i++ {
		name := "T" + strings.Repeat("x", i+1)
		b.WriteString("func L" + name + "(d []byte) error {\n\tvar v " + name + "\n\treturn yaml.NewDecoder(bytes.NewReader(d)).Decode(&v)\n}\n")
	}
	diff := newFileDiff("pkg/config/load.go")
	ws := &countingWorkspace{InMemoryWorkspace: InMemoryWorkspace{Diff: diff, HeadFiles: map[string]string{"pkg/config/load.go": b.String()}}}
	got, err := CheckStrictConfigDecoder(context.Background(), ws, ParseUnifiedDiff(diff), nil)
	if err != nil {
		t.Fatalf("CheckStrictConfigDecoder: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("undeclared types must not be flagged, got %+v", got)
	}
	if ws.greps > maxStrictDecoderLookups {
		t.Fatalf("greps = %d, want <= %d", ws.greps, maxStrictDecoderLookups)
	}
}

type countingWorkspace struct {
	InMemoryWorkspace
	greps int
}

func (w *countingWorkspace) GrepSymbolSites(ctx context.Context, symbol string) ([]CallSite, error) {
	w.greps++
	return w.InMemoryWorkspace.GrepSymbolSites(ctx, symbol)
}
