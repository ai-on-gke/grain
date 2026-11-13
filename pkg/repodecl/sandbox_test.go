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

package repodecl_test

// A template's `sandbox:` block (grain/task-4591): the sandbox shape
// every task it files asks for, under a workflow job's own keys.

import (
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

func TestDeclaredTemplateSandboxIsParsed(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/publish.yml": "title: Publish images\nsandbox:\n  disk-gb: 100\n",
		".grain/templates/big.yml":     "title: Big build\nsandbox: {cpus: 8, memory-mb: 32768, disk-gb: 200}\n",
		".grain/templates/plain.yml":   "title: Plain\n",
	})
	byName := map[string]model.Template{}
	for _, f := range declared.Templates {
		byName[f.Template.Name] = f.Template
	}
	if got, want := byName["publish"].Sandbox, (model.SandboxShape{DiskGB: 100}); got != want {
		t.Errorf("publish Sandbox = %+v, want %+v", got, want)
	}
	if got, want := byName["big"].Sandbox, (model.SandboxShape{CPUs: 8, MemoryMB: 32768, DiskGB: 200}); got != want {
		t.Errorf("big Sandbox = %+v, want %+v", got, want)
	}
	if !byName["plain"].Sandbox.IsZero() {
		t.Errorf("plain Sandbox = %+v, want none", byName["plain"].Sandbox)
	}
}

// Each value is a positive integer no larger than the ceiling a task's
// own shape is held to, and a value out of range refuses the set -- it is
// a mistake whatever version reads it, not a file too new for this one.
func TestDeclaredTemplateSandboxIsValidated(t *testing.T) {
	for _, tc := range []struct {
		block string
		want  string
	}{
		{"sandbox:\n  disk-gb: 0\n", "disk-gb must be a positive integer, got 0"},
		{"sandbox:\n  cpus: -2\n", "cpus must be a positive integer, got -2"},
		{"sandbox:\n  cpus: 33\n", "cpus is 33, and may be at most 32"},
		{"sandbox:\n  memory-mb: 64\n", "memory-mb is 64, and must be at least 128"},
		{"sandbox:\n  memory-mb: 200000\n", "memory-mb is 200000, and may be at most 131072"},
		{"sandbox:\n  disk-gb: 1025\n", "disk-gb is 1025, and may be at most 1024"},
	} {
		files := map[string]string{".grain/templates/bad.yml": "title: Bad\n" + tc.block}
		msg := loadErr(t, files)
		if !strings.Contains(msg, tc.want) || !strings.Contains(msg, ".grain/templates/bad.yml") {
			t.Errorf("%q: error = %q, want the file and %q", tc.block, msg, tc.want)
		}
		_, err := repodecl.Load(tree(files), repo)
		var schema *repodecl.SchemaError
		if errors.As(err, &schema) {
			t.Errorf("%q: a value out of range is a SchemaError (%v); want a refusal of the whole set", tc.block, err)
		}
	}
}

// A key the block does not know is refused by name -- and, being one a
// newer grain may have added, as a SchemaError, so that a daemon older
// than the file holds that file back rather than refusing everything.
func TestDeclaredTemplateSandboxRefusesAnUnknownKeyAsTooNew(t *testing.T) {
	files := map[string]string{
		".grain/templates/gpu.yml":   "title: GPU\nsandbox:\n  disk-gb: 50\n  gpus: 1\n",
		".grain/templates/plain.yml": "title: Plain\n",
	}
	_, err := repodecl.Load(tree(files), repo)
	var schema *repodecl.SchemaError
	if err == nil || !errors.As(err, &schema) {
		t.Fatalf("Load = %v, want a SchemaError", err)
	}
	if !strings.Contains(err.Error(), "gpus") || !strings.Contains(err.Error(), "cpus, memory-mb, disk-gb") {
		t.Errorf("error = %q, want the key and what the block accepts", err)
	}
	declared, err := repodecl.LoadForwardCompatible(tree(files), repo)
	if err != nil {
		t.Fatalf("LoadForwardCompatible: %v", err)
	}
	if len(declared.Templates) != 1 || len(declared.Refused) != 1 || declared.Refused[0].Path != ".grain/templates/gpu.yml" {
		t.Errorf("templates = %d, refused = %+v; want plain loaded and gpu.yml held back", len(declared.Templates), declared.Refused)
	}
}

// What an older grain sees: a template whose `sandbox:` it has never
// heard of is a SchemaError naming the key, the shape a daemon from
// before this change meets. It is held back and reported (forward_test.go),
// never loaded without its shape and left to file undersized tasks
// silently.
func TestASandboxBlockIsNotSilentlyDroppedByAParserThatLacksIt(t *testing.T) {
	files := map[string]string{
		".grain/templates/publish.yml": "title: Publish\nsandbox: {disk-gb: 100}\nfrom-a-newer-grain: true\n",
	}
	declared, err := repodecl.LoadForwardCompatible(tree(files), repo)
	if err != nil {
		t.Fatalf("LoadForwardCompatible: %v", err)
	}
	if len(declared.Templates) != 0 || len(declared.Refused) != 1 {
		t.Fatalf("templates = %+v, refused = %+v; want the file held back and reported", declared.Templates, declared.Refused)
	}
	if !strings.Contains(declared.Refused[0].Message, "from-a-newer-grain") {
		t.Errorf("refusal = %q, want the unknown key named", declared.Refused[0].Message)
	}
}
