package model_test

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// The three layers, and the two different rules they compose by: a repo
// adds to the deployment, and a task replaces both. Table-driven because
// what is worth pinning here is the whole grid of which layers are set
// rather than any single case in it -- a rule that only holds when every
// layer happens to be filled in is not the rule.
func TestPromptExtensionFor(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		deployment, repo, task string
		want                   string
	}{
		{name: "nothing set", want: ""},
		{name: "deployment alone", deployment: "house style", want: "house style"},
		{name: "repo alone", repo: "widgets style", want: "widgets style"},
		{
			name:       "a repo is appended to the deployment, never replacing it",
			deployment: "house style",
			repo:       "widgets style",
			want:       "house style\n\nwidgets style",
		},
		{
			name:       "a task replaces both",
			deployment: "house style",
			repo:       "widgets style",
			task:       "just this once",
			want:       "just this once",
		},
		{
			name: "a task with no other layer is used as it stands",
			task: "just this once",
			want: "just this once",
		},
		{
			// Every layer is trimmed on the way in (ui.UpdateSettings and
			// friends), so whitespace only reaches here from a row
			// written by hand or by an older build -- and it must read as
			// the empty setting it looks like rather than, in the task's
			// case, as an override that silences the two layers under it.
			name:       "whitespace is not a layer",
			deployment: "  house style\n",
			repo:       "\n",
			task:       "   ",
			want:       "house style",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := model.PromptExtensionFor(tc.deployment, tc.repo, tc.task); got != tc.want {
				t.Errorf("PromptExtensionFor(%q, %q, %q) = %q, want %q",
					tc.deployment, tc.repo, tc.task, got, tc.want)
			}
		})
	}
}
