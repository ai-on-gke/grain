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

package mergelogins

import (
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

type fakeForge struct {
	vars  map[string]string
	types map[string]string
	asked []string
}

func (f *fakeForge) RepoVariable(owner, repo, name string) (string, bool, error) {
	v, ok := f.vars[owner+"/"+repo]
	if v == "denied" {
		return "", false, errors.New("403")
	}
	return v, ok, nil
}

func (f *fakeForge) AccountType(owner, repo, login string) (string, error) {
	f.asked = append(f.asked, login)
	t, ok := f.types[login]
	if !ok {
		return "", errors.New("404")
	}
	return t, nil
}

func TestCheckWarnsOnlyForUserAccounts(t *testing.T) {
	f := &fakeForge{
		vars:  map[string]string{"acme/widgets": "octocat, grain-merge[bot]", "acme/gears": "denied"},
		types: map[string]string{"octocat": "User", "ghost-org": "Organization"},
	}
	got := Check(f, []Setting{
		{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Name: "vars." + Variable},
		{Repo: model.RepoRef{Owner: "acme", Name: "gears"}, Name: "vars." + Variable},
		{Repo: model.RepoRef{Owner: "acme", Name: "none"}, Name: "vars." + Variable},
		{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Name: "-upgrade-reviewed-merge-logins",
			Logins: []string{"octocat", "ghost-org", "unknown"}},
	})
	if len(got) != 2 {
		t.Fatalf("warnings = %+v, want octocat twice", got)
	}
	for _, w := range got {
		if w.Login != "octocat" || w.Repo != "acme/widgets" || !strings.Contains(w.Message, "[bot]") {
			t.Fatalf("warning = %+v", w)
		}
	}
	if got[0].Setting != "vars.GRAIN_MERGE_LOGINS" || got[1].Setting != "-upgrade-reviewed-merge-logins" {
		t.Fatalf("settings = %q, %q", got[0].Setting, got[1].Setting)
	}
	// An App's bot login is never asked about, and a login is asked once.
	for _, login := range f.asked {
		if strings.HasSuffix(login, "[bot]") {
			t.Fatalf("asked about %s", login)
		}
	}
	if n := strings.Count(strings.Join(f.asked, ","), "octocat"); n != 1 {
		t.Fatalf("asked about octocat %d times", n)
	}
}

func TestSplitLogins(t *testing.T) {
	if got := SplitLogins(" a,b  c,,\n"); strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("SplitLogins = %q", got)
	}
}
