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

package model_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func boolPtr(v bool) *bool           { return &v }
func slicePtr(v ...string) *[]string { s := append([]string{}, v...); return &s }

func TestCleanSkillFolderRejectsEscapes(t *testing.T) {
	valid := map[string]string{
		".claude/skills":   ".claude/skills",
		"./.gemini/skills": ".gemini/skills",
		"skills/":          "skills",
		"tools/agent_1":    "tools/agent_1",
	}
	for in, want := range valid {
		got, err := model.CleanSkillFolder(in)
		if err != nil || got != want {
			t.Errorf("CleanSkillFolder(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		".",
		"./",
		"/",
		"/etc/passwd",
		"..",
		"../outside",
		".claude/../../etc",
		"skills/../other",
		"skills\\sub",
		"bad space/skills",
		"skills;rm -rf",
	}
	for _, in := range invalid {
		if got, err := model.CleanSkillFolder(in); err == nil {
			t.Errorf("CleanSkillFolder(%q) = %q, expected error", in, got)
		}
	}
}

func TestEncodeDecodeSkillsConfigPreservesEmptyFolders(t *testing.T) {
	emptyFolders := model.NewSkillsConfig(nil, slicePtr())
	enc := model.EncodeSkillsConfig(emptyFolders)
	if enc != `{"folders":[]}` {
		t.Fatalf("EncodeSkillsConfig(folders: []) = %q, want {\"folders\":[]}", enc)
	}
	dec := model.DecodeSkillsConfig(enc)
	if dec == nil || dec.Folders == nil || len(*dec.Folders) != 0 {
		t.Fatalf("DecodeSkillsConfig(%q) = %+v, want explicit empty folders slice", enc, dec)
	}

	disabled := model.NewSkillsConfig(boolPtr(false), nil)
	if got := model.DecodeSkillsConfig(model.EncodeSkillsConfig(disabled)); !disabled.Equal(got) {
		t.Fatalf("round-trip disabled = %+v, want %+v", got, disabled)
	}
}

func TestResolveSkillFoldersLayerPrecedence(t *testing.T) {
	// Default when all layers are unset.
	if got := model.ResolveSkillFolders(nil, nil, nil); !slices.Equal(got, model.DefaultSkillFolders) {
		t.Fatalf("default folders = %v, want %v", got, model.DefaultSkillFolders)
	}

	// Deployment narrows folders; repo and task inherit.
	dep := model.NewSkillsConfig(nil, slicePtr(".claude/skills", ".codex/skills"))
	if got := model.ResolveSkillFolders(dep, nil, nil); !slices.Equal(got, []string{".claude/skills", ".codex/skills"}) {
		t.Fatalf("deployment folders = %v", got)
	}

	// Repo overrides deployment; task overrides repo.
	repo := model.NewSkillsConfig(boolPtr(true), slicePtr(".claude/skills", ".gemini/skills"))
	if got := model.ResolveSkillFolders(dep, repo, nil); !slices.Equal(got, []string{".claude/skills", ".gemini/skills"}) {
		t.Fatalf("repo folders = %v", got)
	}
	task := model.NewSkillsConfig(nil, slicePtr(".gemini/skills"))
	if got := model.ResolveSkillFolders(dep, repo, task); !slices.Equal(got, []string{".gemini/skills"}) {
		t.Fatalf("task override folders = %v", got)
	}

	// Disabled via enabled: false or folders: [] at any layer.
	if got := model.ResolveSkillFolders(dep, model.NewSkillsConfig(boolPtr(false), nil), nil); len(got) != 0 {
		t.Fatalf("repo enabled:false = %v, want empty", got)
	}
	if got := model.ResolveSkillFolders(dep, model.NewSkillsConfig(nil, slicePtr()), nil); len(got) != 0 {
		t.Fatalf("repo folders:[] = %v, want empty", got)
	}
	if got := model.ResolveSkillFolders(dep, repo, model.NewSkillsConfig(boolPtr(false), nil)); len(got) != 0 {
		t.Fatalf("task enabled:false = %v, want empty", got)
	}
	if got := model.ResolveSkillFolders(dep, repo, model.NewSkillsConfig(nil, slicePtr())); len(got) != 0 {
		t.Fatalf("task folders:[] = %v, want empty", got)
	}

	// Operator repo_config wins over declared .grain/settings.yml when operator disables.
	declaredOn := model.NewSkillsConfig(boolPtr(true), slicePtr(".claude/skills"))
	operatorOff := model.NewSkillsConfig(boolPtr(false), nil)
	composed := model.ComposeRepoSkills(operatorOff, declaredOn)
	if got := model.ResolveSkillFolders(nil, composed, nil); len(got) != 0 {
		t.Fatalf("operator disabled over declared = %v, want empty", got)
	}

	// Upper layer re-enabling with non-empty folders is not defeated by lower enabled:false.
	declaredOff := model.NewSkillsConfig(boolPtr(false), nil)
	operatorFolders := model.NewSkillsConfig(nil, slicePtr(".claude/skills"))
	composedFolders := model.ComposeRepoSkills(operatorFolders, declaredOff)
	if got := model.ResolveSkillFolders(nil, composedFolders, nil); !slices.Equal(got, []string{".claude/skills"}) {
		t.Fatalf("operator folders over declared enabled:false = %v, want [.claude/skills]", got)
	}

	// Upper layer re-enabling with enabled:true is not defeated by lower folders:[].
	declaredEmpty := model.NewSkillsConfig(nil, slicePtr())
	operatorEnabled := model.NewSkillsConfig(boolPtr(true), nil)
	composedEnabled := model.ComposeRepoSkills(operatorEnabled, declaredEmpty)
	if got := model.ResolveSkillFolders(dep, composedEnabled, nil); !slices.Equal(got, []string{".claude/skills", ".codex/skills"}) {
		t.Fatalf("operator enabled:true over declared folders:[] = %v, want deployment folders", got)
	}
}
