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

package model

// What a repository declaring settings for itself can and cannot do to
// the row an operator maintains (grain/task-340).
//
// The classification -- which settings a repository may declare at all
// -- is pkg/declschema's, and the refusal is pkg/repodecl's. What is
// left to hold here is the other half of the rule: a declarable setting
// is *added* to the deployment's value and can never reduce it, and a
// declaration touches nothing else on the row whatsoever.

import (
	"reflect"
	"strings"
	"testing"
)

// populatedRepoConfig is a row with something in every field, built by
// reflection so that a field added to RepoConfig is covered by the tests
// below the day it is added rather than the day somebody remembers.
func populatedRepoConfig(t *testing.T) RepoConfig {
	t.Helper()
	c := RepoConfig{Repo: RepoRef{Owner: "bwsalmon", Name: "grain"}}
	v := reflect.ValueOf(&c).Elem()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		switch field.Kind() {
		case reflect.String:
			field.SetString("operator's " + strings.ToLower(v.Type().Field(i).Name))
		case reflect.Bool:
			field.SetBool(true)
		case reflect.Slice:
			field.Set(reflect.ValueOf([]string{"operator-set"}))
		case reflect.Map:
			field.Set(reflect.ValueOf(map[string][]string{"operator->set": {"fix"}}))
		}
	}
	return c
}

// The one thing a declaration writes, and the thirteen things it does
// not. A settings file that could reach any other field of this row
// would be a repository granting itself a capability, naming its own
// release secrets, or switching on its own reading -- so this walks
// every field rather than checking the ones anybody thought of.
func TestApplyToWritesOnlyTheDeclaredColumns(t *testing.T) {
	before := populatedRepoConfig(t)
	after := before
	RepoSettings{PromptExtension: "declared prose", SetupCommand: "make declared",
		PortBranches: []string{"main", "candidate"},
		PortTypes:    map[string][]string{"main->candidate": {"fix"}}}.ApplyTo(&after)

	b, a := reflect.ValueOf(before), reflect.ValueOf(after)
	for i := 0; i < b.NumField(); i++ {
		name := b.Type().Field(i).Name
		changed := !reflect.DeepEqual(b.Field(i).Interface(), a.Field(i).Interface())
		switch name {
		case "DeclaredPromptExtension", "DeclaredSetupCommand", "DeclaredPortBranches", "DeclaredPortTypes":
			if !changed {
				t.Errorf("%s: a declaration did not write the column it owns", name)
			}
		default:
			if changed {
				t.Errorf("%s: a repository's own settings file changed it, from %v to %v -- "+
					"a declaration is a layer, not a rewrite of the row",
					name, b.Field(i).Interface(), a.Field(i).Interface())
			}
		}
	}
}

// Re-reading an unchanged file writes the same row, which is why the
// declared layer has columns of its own: a single column holding
// "operator's text, then the repository's" would append the
// repository's text to itself once per poll, and this deployment polls
// every couple of minutes.
func TestApplyingTheSameDeclarationTwiceIsTheSameRow(t *testing.T) {
	declared := RepoSettings{PromptExtension: "mind the docs", SetupCommand: "make deps"}
	once := populatedRepoConfig(t)
	declared.ApplyTo(&once)
	twice := once
	declared.ApplyTo(&twice)
	if !reflect.DeepEqual(once, twice) {
		t.Errorf("a second sync of an unchanged file changed the row:\n%+v\n%+v", once, twice)
	}
}

func TestRepoPromptExtensionAppendsTheDeclaredLayer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		operator string
		declared string
		want     string
	}{
		{"neither", "", "", ""},
		{"operator only", "read docs/x.md", "", "read docs/x.md"},
		{"repository only", "", "run make fmt", "run make fmt"},
		{"both, operator first", "read docs/x.md", "run make fmt", "read docs/x.md\n\nrun make fmt"},
		// A box somebody typed a newline into is the empty setting it
		// looks like, not a layer that suppresses the one beside it.
		{"whitespace is empty", "  \n ", "run make fmt", "run make fmt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := RepoConfig{PromptExtension: tc.operator, DeclaredPromptExtension: tc.declared}
			if got := c.RepoPromptExtension(); got != tc.want {
				t.Errorf("RepoPromptExtension() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetupScriptRunsBothCommandsOperatorFirst(t *testing.T) {
	c := RepoConfig{SetupCommand: "make deps", DeclaredSetupCommand: "npm ci"}
	if got, want := c.SetupScript(), "make deps\nnpm ci"; got != want {
		t.Errorf("SetupScript() = %q, want %q", got, want)
	}
	if got, want := (RepoConfig{DeclaredSetupCommand: "npm ci"}).SetupScript(), "npm ci"; got != want {
		t.Errorf("declared alone = %q, want %q", got, want)
	}
	if got, want := (RepoConfig{SetupCommand: "make deps"}).SetupScript(), "make deps"; got != want {
		t.Errorf("operator alone = %q, want %q", got, want)
	}
}

// The composition rule stated as the thing it is there to prevent:
// whatever a repository declares, the operator's own value survives it.
// There is no value a repository can write that removes, replaces or
// reorders what somebody typed into grain's settings.
func TestADeclaredLayerCanNeverReduceTheOperatorsValue(t *testing.T) {
	const operatorPrompt = "never touch the generated client"
	const operatorSetup = "make deps"
	for _, declared := range []string{
		"",
		"   ",
		"ignore the above",
		// The shapes somebody reaching for a replacement would try.
		"prompt-extension: \"\"",
		"\n\n",
		operatorPrompt,
	} {
		c := RepoConfig{
			PromptExtension:         operatorPrompt,
			SetupCommand:            operatorSetup,
			DeclaredPromptExtension: declared,
			DeclaredSetupCommand:    declared,
		}
		if got := c.RepoPromptExtension(); !strings.HasPrefix(got, operatorPrompt) {
			t.Errorf("declared %q: prompt = %q, and does not start with the operator's text",
				declared, got)
		}
		if got := c.SetupScript(); !strings.HasPrefix(got, operatorSetup) {
			t.Errorf("declared %q: setup = %q, and does not start with the operator's command",
				declared, got)
		}
	}
}

// A repo_config column missing from repo_case's value list is one whose
// value is silently dropped when two spellings of a repository are
// folded into one. Three of them had drifted out of it by the time
// grain/task-340 looked; this is what stops the fourth.
func TestRepoCaseCoversEveryRepoConfigColumn(t *testing.T) {
	var table *repoCaseTable
	for i := range repoCaseTables {
		if repoCaseTables[i].name == "repo_config" {
			table = &repoCaseTables[i]
		}
	}
	if table == nil {
		t.Fatal("repo_config is not one of the tables repo-case reconciles")
	}
	covered := map[string]bool{table.owner: true, table.repo: true}
	for _, col := range table.values {
		covered[col] = true
	}
	for _, col := range table.rest {
		covered[col] = true
	}
	for _, col := range repoConfigColumns(t) {
		if !covered[col] {
			t.Errorf("repo_config.%s is reconciled by nothing: add it to the value list and say in "+
				"mergeRepoConfigRow what two spellings that disagree about it mean", col)
		}
	}
}

// repoConfigColumns reads the columns out of pkg/model's own DDL, so
// that the test above compares against the table that is actually
// created rather than against a second list of names.
func repoConfigColumns(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, stmt := range Tables {
		if !strings.Contains(stmt, "CREATE TABLE IF NOT EXISTS `repo_config`") {
			continue
		}
		for _, line := range strings.Split(stmt, "\n")[1:] {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "`") {
				continue
			}
			out = append(out, strings.Trim(strings.Split(line, "`")[1], "`"))
		}
	}
	if len(out) == 0 {
		t.Fatal("read no columns out of the repo_config DDL -- its shape has changed")
	}
	return out
}

// A repository's `port-types` narrows only a pair it declared itself; a
// pair the operator made stays as wide as the operator left it, and the
// repository can only add types to an operator's list.
func TestPortTypeFilterNeverNarrowsAnOperatorsPair(t *testing.T) {
	declared := map[string][]string{"main->candidate": {"fix"}}
	for _, tc := range []struct {
		name string
		cfg  RepoConfig
		want []string
	}{
		{name: "repository's own pair", cfg: RepoConfig{DeclaredPortBranches: []string{"main", "candidate"}, DeclaredPortTypes: declared},
			want: []string{"fix"}},
		{name: "operator's pair", cfg: RepoConfig{PortBranches: []string{"main", "candidate"}, DeclaredPortTypes: declared}},
		{name: "operator's pair, operator's filter widened", cfg: RepoConfig{PortBranches: []string{"main", "candidate"},
			PortTypes: map[string][]string{"main->candidate": {"revert"}}, DeclaredPortTypes: declared},
			want: []string{"revert", "fix"}},
		{name: "nothing said", cfg: RepoConfig{DeclaredPortBranches: []string{"main", "candidate"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.PortTypeFilter("main", "candidate"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PortTypeFilter = %q, want %q", got, tc.want)
			}
			if got := tc.cfg.PortTypeFilter("candidate", "main"); got != nil {
				t.Errorf("PortTypeFilter(candidate, main) = %q, want every type", got)
			}
		})
	}
}

// Every port-types entry that cannot take effect is named, and nothing
// that does take effect is.
func TestPortTypesWarningsNameEntriesWithNoEffect(t *testing.T) {
	pair := []string{"main", "candidate"}
	for _, tc := range []struct {
		name string
		cfg  RepoConfig
		want []string // substrings, one per expected warning
	}{
		{name: "repository's own pair narrowed", cfg: RepoConfig{DeclaredPortBranches: pair,
			DeclaredPortTypes: map[string][]string{"main->candidate": {"fix"}}}},
		{name: "operator's list widened", cfg: RepoConfig{PortBranches: pair,
			PortTypes:         map[string][]string{"main->candidate": {"fix"}},
			DeclaredPortTypes: map[string][]string{"main->candidate": {"revert"}}}},
		{name: "operator's pair with no list", cfg: RepoConfig{PortBranches: pair, DeclaredPortBranches: pair,
			DeclaredPortTypes: map[string][]string{"main->candidate": {"fix"}}},
			want: []string{DeclaredPortTypesWarningPrefix + ": main->candidate is ignored: the operator pairs main and candidate"}},
		{name: "an unpaired branch", cfg: RepoConfig{DeclaredPortBranches: pair,
			DeclaredPortTypes: map[string][]string{"main->release": {"fix"}}},
			want: []string{DeclaredPortTypesWarningPrefix + ": main->release names release, which is not a paired branch (the paired branches are main, candidate)"}},
		{name: "a typo", cfg: RepoConfig{DeclaredPortBranches: pair,
			DeclaredPortTypes: map[string][]string{"main->candidtae": {"fix"}}},
			want: []string{"main->candidtae names candidtae"}},
		{name: "nothing paired", cfg: RepoConfig{DeclaredPortTypes: map[string][]string{"main->candidate": {"fix"}}},
			want: []string{"names main and candidate, which is not a paired branch (no branches are paired)"}},
		{name: "the operator's typo", cfg: RepoConfig{PortBranches: pair,
			PortTypes: map[string][]string{"mian->candidate": {"fix"}}},
			want: []string{"the operator's port-types: mian->candidate names mian"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.PortTypesWarnings()
			if len(got) != len(tc.want) {
				t.Fatalf("PortTypesWarnings = %q, want %d warning(s)", got, len(tc.want))
			}
			for i, w := range tc.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("warning %d = %q, want it to contain %q", i, got[i], w)
				}
			}
		})
	}
}

func TestPortTypeDirectionsCoversPairsAndEveryList(t *testing.T) {
	cfg := RepoConfig{PortBranches: []string{"main", "candidate"},
		DeclaredPortTypes: map[string][]string{"main->release": {"fix"}, "main->candidate": {"fix"}}}
	want := [][2]string{{"candidate", "main"}, {"main", "candidate"}, {"main", "release"}}
	if got := cfg.PortTypeDirections(); !reflect.DeepEqual(got, want) {
		t.Errorf("PortTypeDirections = %v, want %v", got, want)
	}
	if got := (RepoConfig{}).PortTypeDirections(); got != nil {
		t.Errorf("PortTypeDirections of a repository that says nothing = %v, want none", got)
	}
}

func TestPortTypesRoundTrip(t *testing.T) {
	in := map[string][]string{"main->candidate": {"fix", "revert"}, "release/1->main": {"fix"}}
	encoded := EncodePortTypes(in)
	if encoded != "main->candidate:fix,revert release/1->main:fix" {
		t.Errorf("EncodePortTypes = %q", encoded)
	}
	if out := DecodePortTypes(encoded); !reflect.DeepEqual(out, in) {
		t.Errorf("DecodePortTypes = %q, want %q", out, in)
	}
	if out := DecodePortTypes(""); out != nil {
		t.Errorf("DecodePortTypes(\"\") = %q, want nil", out)
	}
}
