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

// What grain's own shipped prompts say about where this deployment's
// record actually is.
//
// The state repository stopped holding it: staterepo.ExportConfig writes
// the settings tables and the two memory ones and deletes every other
// file under tables/, so tasks, runs, comments and observations exist
// only in the daemon's database and are read with the self-debug task
// tools. Both prompts here said otherwise for a while, and the cost was
// paid by the runs that believed them -- this deployment's first memory
// run cloned its state repository, went looking for tables/task.json,
// found nothing of the sort, and had to work out for itself where the
// record had gone. These tests are what stops that guidance coming back
// the next time somebody edits either prompt.
//
// In pkg/model because the text is this package's own, and because both
// prompts are grain's rather than a deployment's (SystemTemplate and
// DirectorSessionBody's own doc comments on why): a deployment cannot
// put a correct paragraph back by configuring anything.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// operationalTableFiles are the files a prompt used to send a run to and
// no export has written since. A prompt may still name one -- saying it
// is not there is the whole point -- but only like that.
var operationalTableFiles = []string{
	"tables/task.json",
	"tables/task_run.json",
	"tables/task_comment.json",
	"tables/task_observation.json",
}

// namedOnlyAsAbsent fails for every sentence of body that names one of
// those files without saying, in the same sentence, that it is not
// there. Sentences rather than the whole body, because the whole body is
// exactly the sort of check that passes on a prompt saying "there is no
// tables/task.json" in one paragraph and "read tables/task.json" in the
// next.
func namedOnlyAsAbsent(t *testing.T, what, body string) {
	t.Helper()
	for _, sentence := range strings.Split(strings.ReplaceAll(body, "\n", " "), ". ") {
		for _, file := range operationalTableFiles {
			if !strings.Contains(sentence, file) {
				continue
			}
			if !strings.Contains(sentence, "no ") && !strings.Contains(sentence, "not ") {
				t.Errorf("the %s prompt still names %s as something a run can read:\n\n%s",
					what, file, strings.TrimSpace(sentence))
			}
		}
	}
}

// The nightly memory run's evidence is the task record, and the task
// record is behind the self-debug tools -- so the prompt has to name
// them, the way it names every tool the memory grant hands it.
func TestTheMemoryAgentIsSentToTheTaskToolsForTheRecord(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(model.MemoryAgentTemplateID,
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no memory agent template")
	}
	// A nil reader returns the four tools anyway, refusing every call:
	// this wants their names, not a route to the daemon.
	for _, tool := range []struct{ Name string }{{"grain task list"}, {"grain task read"}, {"grain task prompt"}, {"grain task transcript"}} { // the self-debug task tools, as `grain` commands
		if !strings.Contains(tmpl.Body, tool.Name) {
			t.Errorf("the memory agent's prompt never mentions %s, the tool that reads the "+
				"record it is scheduled to write a memory from", tool.Name)
		}
	}
	namedOnlyAsAbsent(t, "memory agent's", tmpl.Body)
	// And the state repository is still named for what it does hold,
	// since the memory this run replaces and the settings it must not
	// repeat are both in it.
	if !strings.Contains(tmpl.Body, "tables/memory.json") {
		t.Error("the prompt no longer says where the memory this run is replacing is exported")
	}
}

// A run whose task tools are not there must say so, not reason its way
// to a memory from the settings -- which is the one failure mode a
// nightly job with nobody watching it can repeat forever.
func TestTheMemoryAgentIsToldToReportAnUnreadableRecord(t *testing.T) {
	t.Parallel()
	for _, stateRepo := range []model.RepoRef{
		{Owner: "bwsalmon", Name: "grain-home"},
		{},
	} {
		tmpl := model.SystemTemplate(model.MemoryAgentTemplateID, stateRepo)
		if tmpl == nil {
			t.Fatal("this build ships no memory agent template")
		}
		if !strings.Contains(tmpl.Body, "not available to you") {
			t.Errorf("state repo %q: the prompt does not tell the run what to say when the "+
				"record cannot be read:\n%s", stateRepo, tmpl.Body)
		}
		if !strings.Contains(tmpl.Body, "comment_on_issue") {
			t.Errorf("state repo %q: the prompt does not say where to report it", stateRepo)
		}
		namedOnlyAsAbsent(t, "memory agent's", tmpl.Body)
	}
}

// The director is asked "why did that run fail" by a person sitting in
// front of it, which is the fastest way to a confident answer built out
// of the wrong files. It gets the same split: configuration and memory
// from the state repository, the record through self-debug, and a plain
// "I cannot see that" when the capability is not there.
func TestTheDirectorIsToldWhereTheRecordAndTheSettingsEachAre(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "grain-home"}
	for _, body := range []string{
		model.DirectorSessionBody(repo, model.ConsoleSession{}),
		model.DirectorSessionBody(model.RepoRef{}, model.ConsoleSession{TasksApproved: true}),
	} {
		for _, tool := range []struct{ Name string }{{"grain task list"}, {"grain task read"}, {"grain task prompt"}, {"grain task transcript"}} { // the self-debug task tools, as `grain` commands
			if !strings.Contains(body, tool.Name) {
				t.Errorf("the director's prompt never mentions %s, the tool that answers "+
					"what this deployment has run", tool.Name)
			}
		}
		if !strings.Contains(body, "self-debug") {
			t.Error("the director is not told which capability reading the record needs, so a " +
				"session without it cannot say what is missing")
		}
		namedOnlyAsAbsent(t, "director's", body)
	}
	if !strings.Contains(model.DirectorSessionBody(repo, model.ConsoleSession{}), "tables/grain_config.json") {
		t.Error("the director is no longer told where this deployment's own configuration is " +
			"exported, which is what the state checkout is for")
	}
}
