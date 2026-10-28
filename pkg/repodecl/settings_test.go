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

// What a repository may say about itself, and -- at far greater length,
// because it is the half that matters -- what it may not (grain/task-340).
//
// A silently ignored `capabilities:` key is the failure mode the whole
// design exists to avoid, so every key on the "never" list has a test
// here that it is refused *and* that the refusal names the key. The list
// itself is not restated: it is read out of pkg/declschema, so a field
// classified there is covered here the day it is classified.

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ai-on-gke/grain/pkg/declschema"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

func TestSettingsAreLoadedFromGrainSettings(t *testing.T) {
	tree := fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{Data: []byte(`
prompt-extension: "Follow instructions."
setup-command: "make init"
`)},
	}
	declared, err := repodecl.Load(tree, repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if declared.Settings == nil {
		t.Fatalf("Settings is nil, want declared settings")
	}
	if declared.Settings.Path != ".grain/settings.yml" {
		t.Errorf("path = %q, want .grain/settings.yml", declared.Settings.Path)
	}
	s := declared.Settings.Settings
	if s.PromptExtension != "Follow instructions." || s.SetupCommand != "make init" {
		t.Errorf("prompt/setup = %q / %q", s.PromptExtension, s.SetupCommand)
	}
}

func TestSettingsAlternativePaths(t *testing.T) {
	for _, path := range []string{".grain/settings.yaml", ".grain/config.yml", ".grain/config.yaml"} {
		tree := fstest.MapFS{
			path: &fstest.MapFile{Data: []byte("setup-command: make init\n")},
		}
		declared, err := repodecl.Load(tree, repo)
		if err != nil {
			t.Fatalf("Load(%s): %v", path, err)
		}
		if declared.Settings == nil || declared.Settings.Path != path ||
			declared.Settings.Settings.SetupCommand != "make init" {
			t.Errorf("failed for path %s: %+v", path, declared.Settings)
		}
	}
}

func TestMultipleSettingsFilesRefused(t *testing.T) {
	tree := fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{Data: []byte("setup-command: make init\n")},
		".grain/config.yml":   &fstest.MapFile{Data: []byte("setup-command: make init\n")},
	}
	_, err := repodecl.Load(tree, repo)
	if err == nil {
		t.Fatalf("Load with multiple settings files succeeded, want error")
	}
	if !strings.Contains(err.Error(), "multiple settings files found") {
		t.Fatalf("error = %q, want multiple settings files error", err.Error())
	}
}

// A settings file with no keys in it declares nothing and is not an
// error. With two keys accepted and twelve refused, a file holding
// nothing but a comment saying where the rest of them went is a
// reasonable thing for a repository to keep -- and the YAML decoder
// answers an empty document with io.EOF, which would otherwise fail
// every declaration in the tree over a file that says nothing.
func TestAnEmptySettingsFileDeclaresNothing(t *testing.T) {
	for _, text := range []string{"", "\n", "# where the rest of them went\n"} {
		tree := fstest.MapFS{".grain/settings.yml": &fstest.MapFile{Data: []byte(text)}}
		declared, err := repodecl.Load(tree, repo)
		if err != nil {
			t.Fatalf("Load(%q): %v", text, err)
		}
		if declared.Settings == nil {
			t.Fatalf("Load(%q): no settings file was read at all", text)
		}
		if !reflect.DeepEqual(declared.Settings.Settings, model.RepoSettings{}) {
			t.Errorf("Load(%q) declared %+v, want nothing", text, declared.Settings.Settings)
		}
	}
}

func TestSettingsRefusesUnknownFields(t *testing.T) {
	tree := fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{Data: []byte("bogus-field: 123\n")},
	}
	_, err := repodecl.Load(tree, repo)
	if err == nil {
		t.Fatalf("Load with unknown field succeeded, want error")
	}
	if !strings.Contains(err.Error(), "field bogus-field not found") && !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, want unknown field error", err.Error())
	}
}

// Every key a repository may not write, refused with the key named.
//
// Driven off the classification rather than off a list typed here: a
// setting added to model.RepoConfig and classified as one a repository
// may not declare is covered by this test without anybody remembering
// to extend it, and one classified as declarable drops out of it.
func TestEveryForbiddenSettingIsRefusedByName(t *testing.T) {
	refusals := declschema.RepoRefusals(model.DeclaredSettings)
	if len(refusals) == 0 {
		t.Fatal("the settings schema refuses nothing at all, which cannot be right")
	}
	for key, why := range refusals {
		t.Run(key, func(t *testing.T) {
			tree := fstest.MapFS{
				".grain/settings.yml": &fstest.MapFile{Data: []byte(forbiddenKeyFile(key))},
			}
			_, err := repodecl.Load(tree, repo)
			if err == nil {
				t.Fatalf("a repository declared %q and grain accepted it", key)
			}
			// The key itself, so that somebody reading the error knows
			// which line to delete -- a refusal that says only "this file
			// is wrong" is barely better than the silent drop.
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error = %q, and does not name %q", err, key)
			}
			// And the reason, in the words the classification gives it,
			// so that the answer to "why not?" is in the error rather
			// than in somebody's memory of a design document.
			if want := firstSentence(why.Reason()); !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to say %q", err, want)
			}
			if !strings.Contains(err.Error(), ".grain/settings.yml") {
				t.Errorf("error = %q, and does not say which file", err)
			}
		})
	}
}

// The one that would look like an oversight if it were missing, said on
// its own because it is the bootstrap: a repository cannot switch on the
// reading of its own word.
func TestSettingsRefusesDeclarationsSwitch(t *testing.T) {
	tree := fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{Data: []byte("declarations: true\n")},
	}
	_, err := repodecl.Load(tree, repo)
	if err == nil {
		t.Fatalf("Load with declarations key succeeded, want refusal")
	}
	if !strings.Contains(err.Error(), "declarations") ||
		!strings.Contains(err.Error(), "good enough to read at all") {
		t.Fatalf("error = %q, want the bootstrap refusal", err.Error())
	}
}

// The spelling somebody reaches for first, and the single key this whole
// design exists to refuse rather than drop. It is not a field of
// RepoConfig at all -- nothing would have decoded it -- which is exactly
// why it has to be refused by name instead of falling through to "field
// not found".
func TestSettingsRefusesABareCapabilitiesKey(t *testing.T) {
	tree := fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{Data: []byte("capabilities:\n  - gcp-key\n")},
	}
	_, err := repodecl.Load(tree, repo)
	if err == nil {
		t.Fatalf("a repository granted itself gcp-key and grain accepted it")
	}
	if !strings.Contains(err.Error(), "capabilities") ||
		!strings.Contains(err.Error(), "never what that work is allowed to do") {
		t.Fatalf("error = %q, want the authority refusal", err.Error())
	}
}

// A file that makes one mistake hears one sentence, and a file that
// makes two different kinds of mistake hears both -- pkg/declschema's
// Group, exercised through a real settings file.
func TestSettingsSaysOneThingPerReason(t *testing.T) {
	tree := fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{Data: []byte(
			"default-capabilities: [gcp-key]\nci-capabilities: [gcp-key]\nignore-github-ci: true\n")},
	}
	_, err := repodecl.Load(tree, repo)
	if err == nil {
		t.Fatal("Load succeeded, want refusals")
	}
	got := err.Error()
	if n := strings.Count(got, "never what that work is allowed to do"); n != 1 {
		t.Errorf("the authority rule is explained %d times, want once:\n%s", n, got)
	}
	if !strings.Contains(got, "ci-capabilities, default-capabilities") {
		t.Errorf("the two capability keys are not reported together:\n%s", got)
	}
	if !strings.Contains(got, "grading its own homework") {
		t.Errorf("ignore-github-ci is not refused for its own reason:\n%s", got)
	}
}

// Nothing is declared while any of it is refused. A settings file that
// asks for a capability does not land its prompt extension either --
// the same all-or-nothing Load already applies to a bad template, and
// the reason a refusal cannot be worked around by putting the forbidden
// key in the same file as a legitimate one.
func TestARefusedSettingDeclaresNothingAtAll(t *testing.T) {
	tree := fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{Data: []byte(
			"prompt-extension: mind the docs\nrelease-secrets: [gcp-key]\n")},
	}
	declared, err := repodecl.Load(tree, repo)
	if err == nil {
		t.Fatal("Load succeeded, want a refusal")
	}
	if !declared.Empty() {
		t.Errorf("declared %+v alongside the refusal, want nothing", declared)
	}
}

// forbiddenKeyFile is a one-key settings file that sets key to something
// of the right shape for it, since a refusal has to happen before the
// value is ever looked at.
func forbiddenKeyFile(key string) string {
	switch key {
	case "default-capabilities", "capabilities", "ci-capabilities", "release-secrets",
		"unfixable-checks", "base-branch-ci-jobs":
		return key + ":\n  - gcp-key\n"
	case "ignore-github-ci", "require-branch-up-to-date", "watch-default-branch",
		"merge-on-green", "base-repair-bypasses-human-gates", "release-workflows-disabled", "declarations":
		return key + ": true\n"
	case "merge-recheck":
		return key + ": enforce\n"
	default:
		return key + ": something\n"
	}
}

// firstSentence is as much of a refusal's reason as an error has to
// carry verbatim -- enough to pin that it is *this* reason and not
// another, without pinning the wording of a paragraph.
func firstSentence(reason string) string {
	if i := strings.Index(reason, ":"); i > 0 {
		return reason[:i]
	}
	if i := strings.Index(reason, "--"); i > 0 {
		return strings.TrimSpace(reason[:i])
	}
	return reason
}

// `port-branches` is the one list a repository declares: which of its
// branches each merge is ported between.
func TestSettingsDeclaresPortBranches(t *testing.T) {
	tree := fstest.MapFS{".grain/settings.yml": &fstest.MapFile{Data: []byte("port-branches: [main, ' candidate ', '']\n")}}
	declared, err := repodecl.Load(tree, repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := declared.Settings.Settings.PortBranches
	if !reflect.DeepEqual(got, []string{"main", "candidate"}) {
		t.Fatalf("PortBranches = %q, want [main candidate]", got)
	}
	var cfg model.RepoConfig
	declared.Settings.Settings.ApplyTo(&cfg)
	if targets := cfg.PortTargets("candidate"); !reflect.DeepEqual(targets, []string{"main"}) {
		t.Fatalf("PortTargets(candidate) = %q, want [main]", targets)
	}
	if targets := cfg.PortTargets("feature"); targets != nil {
		t.Fatalf("PortTargets(feature) = %q, want none", targets)
	}
}

// `port-types` narrows a direction to the conventional-commit types it
// ports, and a direction it cannot read is an error rather than a filter
// silently dropped.
func TestSettingsDeclaresPortTypes(t *testing.T) {
	tree := fstest.MapFS{".grain/settings.yml": &fstest.MapFile{Data: []byte(
		"port-branches: [main, candidate]\nport-types:\n  main->candidate: [Fix, ' revert', fix]\n")}}
	declared, err := repodecl.Load(tree, repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := declared.Settings.Settings.PortTypes
	if want := map[string][]string{"main->candidate": {"fix", "revert"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PortTypes = %q, want %q", got, want)
	}
	var cfg model.RepoConfig
	declared.Settings.Settings.ApplyTo(&cfg)
	if types := cfg.PortTypeFilter("main", "candidate"); !reflect.DeepEqual(types, []string{"fix", "revert"}) {
		t.Errorf("PortTypeFilter(main, candidate) = %q, want [fix revert]", types)
	}
	if types := cfg.PortTypeFilter("candidate", "main"); types != nil {
		t.Errorf("PortTypeFilter(candidate, main) = %q, want every type", types)
	}

	for _, bad := range []string{
		"port-types:\n  main: [fix]\n",
		"port-types:\n  main->main: [fix]\n",
		"port-types:\n  main->candidate: []\n",
		"port-types:\n  main->candidate: ['fix: x']\n",
	} {
		tree := fstest.MapFS{".grain/settings.yml": &fstest.MapFile{Data: []byte(bad)}}
		if _, err := repodecl.Load(tree, repo); err == nil {
			t.Errorf("Load accepted %q", bad)
		}
	}
}
