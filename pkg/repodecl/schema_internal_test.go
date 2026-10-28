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

package repodecl

// The half of the shared definition that cannot be checked from outside
// this package: the raw structs above are unexported, and they are the
// only place the keys a repository *may* write are written down.
//
// The settings file is in it too, and there it is the point rather than
// a nicety: `.grain/settings.yml` accepts two keys out of a settings row
// with fourteen fields, and a field quietly added to the struct would be
// a repository writing a setting nobody classified (grain/task-340).
//
// pkg/declschema holds the refusals and this package reads them at parse
// time, so those cannot drift. The accepted keys are the other
// direction, and this is what pins them: a key added to a raw struct and
// not to the schema is a field of the format nothing else knows about,
// and a key added to the schema and not to a raw struct is a field a
// repository is promised and then told "field not found".

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/declschema"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestTheRawStructsAreTheSchemasRepoKeys(t *testing.T) {
	for _, tc := range []struct {
		kind model.DeclarationKind
		raw  any
	}{
		{model.DeclaredTemplate, rawTemplate{}},
		{model.DeclaredSuite, rawSuite{}},
		{model.DeclaredSchedule, rawSchedule{}},
		{model.DeclaredSettings, rawRepoSettings{}},
	} {
		s, ok := declschema.Of(tc.kind)
		if !ok {
			t.Fatalf("no schema for %q", tc.kind)
		}
		want := sorted(s.Keys(declschema.Repo))
		got := sorted(yamlKeys(reflect.TypeOf(tc.raw)))
		if strings.Join(want, " ") != strings.Join(got, " ") {
			t.Errorf("%s: the file accepts %v and the schema says %v -- one definition, so a key "+
				"belongs in both or in neither", tc.kind, got, want)
		}
	}
}

// And the other side of the same coin: a raw struct must not carry a
// field for a key the schema refuses. It would decode before the refusal
// could be reported, which is the silent drop this whole arrangement
// exists to prevent.
func TestNoRawStructAcceptsARefusedKey(t *testing.T) {
	for _, tc := range []struct {
		kind model.DeclarationKind
		raw  any
	}{
		{model.DeclaredTemplate, rawTemplate{}},
		{model.DeclaredSuite, rawSuite{}},
		{model.DeclaredSchedule, rawSchedule{}},
		{model.DeclaredSettings, rawRepoSettings{}},
	} {
		refused := declschema.RepoRefusals(tc.kind)
		for _, key := range yamlKeys(reflect.TypeOf(tc.raw)) {
			if why, no := refused[key]; no {
				t.Errorf("%s: the file has a field for %q, which the schema refuses (%q)",
					tc.kind, key, why)
			}
		}
	}
}

func yamlKeys(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	return out
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
