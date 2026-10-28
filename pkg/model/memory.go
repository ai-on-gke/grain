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

import (
	"strings"
	"time"
)

// Memory is what grain has learned about working here, written down so
// the next run starts where the last one left off (grain/task-237).
//
// It is prompt_extension.go's shape with the other author. A prompt
// extension is standing instructions a *person* writes -- a house style,
// a test command, "read docs/x.md before touching the migrations" -- and
// only a person ever edits one. Memory is the same kind of text (a block
// of prose appended to every dispatch's prompt) accumulated by the
// memory agent instead: a scheduled run that reads what this deployment
// has actually been doing -- the tasks, the conversations, the pull
// requests, all of them rows in the state repository -- and writes down
// the patterns worth telling the next run about. "The e2e suite is flaky
// on the third retry", "this repo's CI needs `make frontend` before `go
// vet`", "changes to pkg/model need a schema note".
//
// Two layers say it, and MemoryFor is the whole of how they compose:
//
//   - the deployment's own, on every dispatch (the row with no repo);
//   - a repo's own, appended after it for a task that targets that repo.
//
// Append, never replace, for the reason RepoConfig.PromptExtension
// appends to Config.PromptExtension: the two are written at two
// different moments about two different scopes, and a repo's notes
// silently discarding what was learned deployment-wide would be a
// setting that fails where nobody is looking.
//
// There is no per-task layer, and deliberately so. A prompt extension
// has one because a task sometimes has to be exempt from a standing
// instruction; memory is not an instruction to be exempt from, it is
// what grain believes about the ground the task stands on, and a task
// that wants to say something for itself has a body to say it in.
//
// Kept separate from the prompt extension rather than folded into it --
// the same text, in the same prompt, under a different heading -- for
// one reason: they have different authors, and a settings box a person
// maintains must not be silently rewritten by an agent. Somebody who
// opens the prompt extension expects to find what they last typed. So
// memory gets its own rows, its own API (ui.Client.PutMemory, the
// update_memory tool behind the "memory" capability) and its own
// heading in the prompt, and the two never touch.
type Memory struct {
	// Repo is which repo this memory is about, or nil for the
	// deployment-wide memory. Stored as the
	// (owner, name) key, with the deployment-wide row keyed by two empty
	// strings -- see schema.go's `memory` table.
	Repo *RepoRef
	// Text is the memory itself: free prose, written by whatever holds
	// the memory capability, replaced wholesale on every write. Empty is
	// how a memory is forgotten -- PutMemory deletes the row rather than
	// keeping an empty one, so "has a row" and "has something to say"
	// stay the same fact.
	Text string
	// UpdatedAt is when that text was last written, so a reader (the
	// Settings pane, a person reading the state repository's own
	// tables/memory.json) can tell a note from last night from one
	// nobody has revisited in a month.
	UpdatedAt time.Time
	// UpdatedBy is who wrote it, in the vocabulary every other authored
	// row in this store uses (Attribution's own Principal): a
	// PrincipalAgent for last night's memory agent, a PrincipalHuman for
	// somebody correcting a note on the Settings pane.
	//
	// It exists because the two take the same route. A person editing on
	// the pane and the memory agent mid-run send the same body to the
	// same handler on the same daemon (ui.Client.PutMemory), so without
	// this column nothing about a landed write says which happened --
	// and memory is replaced wholesale, so "an operator corrected this
	// and tonight's agent overwrote it" was a thing that could happen
	// with no trace of it anywhere. It reads the other way too: a note
	// somebody is deciding whether to trust reads identically whether an
	// agent inferred it from a week of transcripts or an operator typed
	// it.
	//
	// The zero Principal is "unknown", which is every row written before
	// this column existed. Unknown is neither of the two answers and
	// must not be rendered as either -- the pane draws no chip at all
	// for one (MemoryPanel.jsx), and the prompt says nothing about it
	// (MemoryHandNote).
	//
	// A claim rather than a proof. The API this arrives through is
	// unauthenticated (see pkg/ui's own doc comments), so what is
	// recorded is what the caller said it was, defaulted to
	// ui.Config.Actor for a request that names nobody. That is the same
	// footing every other attribution in this store stands on -- a
	// comment's author, a task's origin -- and it is worth knowing that
	// it is the footing rather than assuming a stronger one.
	UpdatedBy Principal
}

// MaxMemoryLength is the longest a single memory may be, in bytes --
// enforced on the way in (ui.Client.PutMemory) rather than at dispatch,
// so the run that wrote too much is told about it while it can still
// choose what to keep.
//
// Memory is prompt text on every single dispatch, which is what the
// limit is for: an agent asked to write down what it noticed will
// happily write down everything it noticed, and the cost of that lands
// on every run this deployment makes afterwards, forever. 8 KiB per
// layer is a couple of pages -- more than enough for the patterns worth
// carrying, and small enough that a deployment with a memory for its
// busiest repo still spends under a percent of a context window on it.
//
// A run that hits it is not being told to say less about the work; it is
// being told that memory is a summary, and that the tasks, the
// transcripts and the state repository are still where the detail lives.
const MaxMemoryLength = 8 * 1024

// MemoryFor joins the two layers into the one block of text a
// dispatch's prompt carries, or "" when neither has anything to say:
//   - the deployment's own layer ("What grain has learned across this deployment:")
//   - the repo's layer ("About this repository:")
//
// Every layer is trimmed before it is read, exactly as
// PromptExtensionFor trims its own, so a layer holding nothing but
// whitespace is omitted entirely rather than contributing a blank
// paragraph.
//
// There were four layers while grain had teams: a team's own and a
// team's notes about a repo sat between these two. They folded into
// these (single-tenant reset, stage 4d).
func MemoryFor(deployment, repo string) string {
	var parts []string
	if d := strings.TrimSpace(deployment); d != "" {
		parts = append(parts, "What grain has learned across this deployment:\n\n"+d)
	}
	if r := strings.TrimSpace(repo); r != "" {
		parts = append(parts, "About this repository:\n\n"+r)
	}
	return strings.Join(parts, "\n\n")
}

// MemoryHandNote is the line a memory a person wrote goes into a prompt
// with, and "" for one an agent wrote or one whose author is unknown.
//
// memorySection tells every run that memory is notes rather than
// instructions -- an earlier run's observation, to be weighed against
// the tree in front of it and replaced when it has stopped being true
// (orchestrator's memorySection, and Memory's own doc comment). That is
// the right thing to say about what the memory agent wrote and the wrong
// thing to say about what an operator typed on the Settings pane: a
// person correcting a note has made a decision, and "replace what is no
// longer true" quietly reads as "replace what the operator just fixed"
// when the run cannot tell the two apart. Since grain/task-333 the pane
// is a real write path, so this is a real case rather than a hypothesis.
//
// Only the human case says anything. An agent's memory is what the
// section already describes, and a row from before the author was
// recorded is unknown -- and a sentence guessing at either would be a
// line of prompt on every dispatch that might be false.
func MemoryHandNote(by Principal) string {
	if by.Kind != PrincipalHuman {
		return ""
	}
	return "(A person wrote the following by hand rather than an agent: it is somebody's " +
		"own correction, so keep it unless what it says has actually stopped being true.)"
}
