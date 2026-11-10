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

import (
	"slices"
	"testing"
	"testing/fstest"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

func TestRepoSettingsAndTemplateParseSkillsDeclaration(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	tree := fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{
			Data: []byte("skills:\n  folders: [\".claude/skills\", \".gemini/skills\"]\n  enabled: true\n"),
		},
		".grain/templates/gemini-only.yml": &fstest.MapFile{
			Data: []byte("name: Gemini task\ntitle: Run with gemini skills\nprompt: Do the work.\nskills:\n  folders: [\".gemini/skills\"]\n"),
		},
		".grain/templates/no-skills.yml": &fstest.MapFile{
			Data: []byte("name: No skills\ntitle: Run without skills\nprompt: Do the work.\nskills:\n  folders: []\n"),
		},
	}

	settingsFile, err := repodecl.LoadSettings(tree, repo)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if settingsFile == nil || settingsFile.Settings.Skills == nil || settingsFile.Settings.Skills.Folders == nil {
		t.Fatalf("settings.Skills = %+v", settingsFile)
	}
	if !slices.Equal(*settingsFile.Settings.Skills.Folders, []string{".claude/skills", ".gemini/skills"}) {
		t.Errorf("settings folders = %v", *settingsFile.Settings.Skills.Folders)
	}

	loaded, err := repodecl.Load(tree, repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Templates) != 2 {
		t.Fatalf("templates = %d, want 2", len(loaded.Templates))
	}
	byName := map[string]model.Template{}
	for _, dt := range loaded.Templates {
		byName[dt.Template.Name] = dt.Template
	}
	gemini := byName["Gemini task"]
	if gemini.Skills == nil || gemini.Skills.Folders == nil || !slices.Equal(*gemini.Skills.Folders, []string{".gemini/skills"}) {
		t.Errorf("Gemini task skills = %+v", gemini.Skills)
	}
	none := byName["No skills"]
	if none.Skills == nil || none.Skills.Folders == nil || len(*none.Skills.Folders) != 0 {
		t.Errorf("No skills template = %+v, want empty folders slice", none.Skills)
	}

	badTree := fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{
			Data: []byte("skills:\n  folders: [\"../escape\"]\n"),
		},
	}
	if _, err := repodecl.LoadSettings(badTree, repo); err == nil {
		t.Error("expected escaping folder in .grain/settings.yml to be refused")
	}
}
