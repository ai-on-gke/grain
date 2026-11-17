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
	"testing"
)

func TestCheckExportedSurfaceDelta(t *testing.T) {
	ws := &InMemoryWorkspace{
		BaseFiles: map[string]string{
			"foo.go": `package foo
func OldFunc() {}
type OldStruct struct { OldField int }
`,
			"bar.ts": `export function oldTS() {}`,
		},
		HeadFiles: map[string]string{
			"foo.go": `package foo
func OldFunc() {}
func NewFunc() {}
type OldStruct struct { OldField int }
type NewStruct struct { NewField int }
`,
			"bar.ts": `export function oldTS() {}
export const newTS = 42;`,
		},
	}

	pd := ParsedDiff{
		Files: []DiffFile{
			{
				OldPath: "foo.go",
				NewPath: "foo.go",
			},
			{
				OldPath: "bar.ts",
				NewPath: "bar.ts",
			},
		},
	}

	params := map[string]any{"max-exports": 2}
	opts := RunOptions{}

	findings, err := CheckExportedSurfaceDelta(context.Background(), ws, pd, opts, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// newTS, NewFunc, NewStruct, NewStruct.NewField = 4 symbols. Limit is 2.
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (limit exceeded), got %d", len(findings))
	}
	f := findings[0]
	if f.RuleID != "golden:exported-surface-delta" {
		t.Errorf("wrong rule ID: %s", f.RuleID)
	}
}
