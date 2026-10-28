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

package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// grain/task-258: the merge queue's vocabulary is spelled out in three
// places, and this is what stops them drifting apart.
//
// orchestrator's queuestatus.go owns the states themselves
// (mergeStateWaitingCI and the rest, written onto the task every cycle).
// ui/src/state.js's MERGE_QUEUE_PHRASES turns each of them into the words
// on the row somebody is looking at. pkg/mcp's own mergeQueuePhrases
// mirrors that map so read_grain_task and list_grain_tasks describe a row
// in the same words as the screen -- which is the whole of why it exists,
// and is a claim that quietly stops being true the moment either side is
// edited alone.
//
// Nothing breaks loudly when they disagree, by design: an unknown state
// renders as no activity at all in a browser (so an old build never shows
// anybody "waiting_thing") and prints as its raw name here (so an agent
// explaining a row says something true rather than nothing). Both are the
// right behaviour for a deployment mid-upgrade and both are indefinite,
// so a state added to the daemon and forgotten in the other two files
// degrades for as long as nobody notices. This test is the noticing.
//
// # Why it reads the other two files off disk
//
// Because the alternatives cost more than the check is worth. A generated
// map would have to be generated into state.js, which is hand-written
// prose-and-code that explains itself paragraph by paragraph, or into a
// third file both sides import -- a build-time dependency on the frontend
// bundler for the browser and an embed for Go, to keep twelve strings in
// step. Reading the source and comparing is the cheaper half of that
// trade, and it is not new here: pkg/cli/taskattributes_test.go has
// read the frontend's own filter vocabulary off disk since grain/task-65,
// for the same reason and with the same regexes.
//
// It compares the vocabulary, not the rendering: that both sides know
// "waiting_review" and call it "Waiting for its review", not that either
// puts it on a screen correctly. state.test.js is what covers the second
// on that side and task_tools_test.go on this one.
//
// The patterns below read the two files as they are written today -- a
// flat table of state to phrase, closed at the first column. A rewrite
// that defeats them fails here loudly, with a message saying so, rather
// than passing on having found nothing to compare.

// frontendStateSource is ui/src/state.js, or a skip.
//
// A checkout without the frontend in it is not something this
// deployment's CI produces, but it is also not a reason to fail a Go
// suite -- the same call taskattributes_test.go makes.
func frontendStateSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "ui", "src", "state.js")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("skipping: cannot read %s: %v", path, err)
	}
	return string(body)
}

// jsObjectBody is the text between a declaration and the "};" that closes
// it in the first column, which is how both tables this reads are
// written.
func jsObjectBody(t *testing.T, source, opening, closing string) string {
	t.Helper()
	start := strings.Index(source, opening)
	if start < 0 {
		t.Fatalf("cannot find %q in ui/src/state.js: this test's patterns need updating", opening)
	}
	rest := source[start+len(opening):]
	end := strings.Index(rest, "\n"+closing)
	if end < 0 {
		t.Fatalf("cannot find the end of %q in ui/src/state.js: this test's patterns need updating", opening)
	}
	return rest[:end]
}

var (
	jsPhraseEntry = regexp.MustCompile(`(?m)^\s*(\w+):\s*"([^"\\]*)",\s*$`)
	jsSetMember   = regexp.MustCompile(`(?m)^\s*"([^"\\]*)",\s*$`)
)

// frontendMergeQueuePhrases is ui/src/state.js's MERGE_QUEUE_PHRASES:
// every merge-queue state the browser has words for, mapped to those
// words.
func frontendMergeQueuePhrases(t *testing.T) map[string]string {
	t.Helper()
	body := jsObjectBody(t, frontendStateSource(t), "const MERGE_QUEUE_PHRASES = {", "};")
	found := jsPhraseEntry.FindAllStringSubmatch(body, -1)
	if len(found) == 0 {
		t.Fatal("read no phrases at all out of MERGE_QUEUE_PHRASES: this test's patterns need updating")
	}
	phrases := make(map[string]string, len(found))
	for _, m := range found {
		phrases[m[1]] = m[2]
	}
	// Every line of that table is an entry, so a line the pattern did not
	// match is a line this test has silently skipped -- which is exactly
	// the drift it is here to catch.
	if got, want := len(phrases), countEntryLines(body); got != want {
		t.Fatalf("read %d phrases out of %d entry lines in MERGE_QUEUE_PHRASES: "+
			"this test's patterns need updating", got, want)
	}
	return phrases
}

// frontendMergeQueueLive is MERGE_QUEUE_LIVE, the subset of the states in
// which the row gets its live mark.
func frontendMergeQueueLive(t *testing.T) []string {
	t.Helper()
	body := jsObjectBody(t, frontendStateSource(t), "const MERGE_QUEUE_LIVE = new Set([", "]);")
	found := jsSetMember.FindAllStringSubmatch(body, -1)
	if len(found) == 0 {
		t.Fatal("read no states at all out of MERGE_QUEUE_LIVE: this test's patterns need updating")
	}
	states := make([]string, 0, len(found))
	for _, m := range found {
		states = append(states, m[1])
	}
	return states
}

// countEntryLines is how many lines of a table body are an entry rather
// than a comment or a blank -- what the patterns above ought to have
// matched.
func countEntryLines(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		n++
	}
	return n
}

// mergeQueueStates is orchestrator's own vocabulary, read out of
// queuestatus.go: the value of every mergeState* constant, by the name it
// is declared under.
//
// Off the parsed syntax rather than a regex because this side is Go and
// go/parser is in the standard library -- a constant that grows a type or
// moves to a block of its own goes on being read here, where a pattern
// would quietly stop finding it.
func mergeQueueStates(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join("..", "orchestrator", "queuestatus.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	states := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				if !strings.HasPrefix(name.Name, "mergeState") || i >= len(value.Values) {
					continue
				}
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s is not declared as a string literal: this test needs updating", name.Name)
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s's value %s: %v", name.Name, lit.Value, err)
				}
				states[name.Name] = unquoted
			}
		}
	}
	if len(states) == 0 {
		t.Fatalf("found no mergeState* constants in %s: this test needs updating", path)
	}
	return states
}

// TestMergeQueuePhrasesMatchTheFrontend is the map this package keeps
// against the one the browser draws from, entry for entry.
//
// Both halves matter. A state the frontend has words for and this package
// has not is an agent answering "a merge queue state this build has no
// words for" about a row that reads perfectly well on screen; a state
// only this package knows is the opposite, and means the words here were
// never the words anybody saw. A phrase that differs is the subtler one:
// both sides answer, in two different sentences, about the same row.
func TestMergeQueuePhrasesMatchTheFrontend(t *testing.T) {
	frontend := frontendMergeQueuePhrases(t)
	for _, state := range sortedKeys(frontend) {
		phrase, ok := mergeQueuePhrases[state]
		if !ok {
			t.Errorf("ui/src/state.js calls %q %q and pkg/mcp has no phrase for it: "+
				"add it to mergeQueuePhrases", state, frontend[state])
			continue
		}
		if phrase != frontend[state] {
			t.Errorf("%q is %q in ui/src/state.js and %q in pkg/mcp: "+
				"an operator and an agent would describe the same row differently",
				state, frontend[state], phrase)
		}
	}
	for _, state := range sortedKeys(mergeQueuePhrases) {
		if _, ok := frontend[state]; !ok {
			t.Errorf("pkg/mcp calls %q %q and ui/src/state.js has no phrase for it: "+
				"add it to MERGE_QUEUE_PHRASES, or drop it here if the state is gone",
				state, mergeQueuePhrases[state])
		}
	}
}

// TestEveryMergeQueueStateHasAPhrase is the other direction: the daemon's
// own vocabulary against the two maps that describe it.
//
// A state grain writes onto a task and neither map knows is a row that
// says nothing in a browser and prints raw to an agent -- the graceful
// degradation both sides implement on purpose, for the upgrade window,
// and which is meant to last an upgrade rather than a release.
func TestEveryMergeQueueStateHasAPhrase(t *testing.T) {
	states := mergeQueueStates(t)
	frontend := frontendMergeQueuePhrases(t)
	for _, name := range sortedKeys(states) {
		state := states[name]
		if _, ok := mergeQueuePhrases[state]; !ok {
			t.Errorf("orchestrator's %s (%q) has no phrase in pkg/mcp's mergeQueuePhrases", name, state)
		}
		if _, ok := frontend[state]; !ok {
			t.Errorf("orchestrator's %s (%q) has no phrase in ui/src/state.js's MERGE_QUEUE_PHRASES",
				name, state)
		}
	}
	// And no phrase for a state the daemon no longer writes: a row's
	// words outliving the state they were for is how a map accumulates
	// answers nothing can ever ask for.
	declared := map[string]bool{}
	for _, state := range states {
		declared[state] = true
	}
	for _, state := range sortedKeys(mergeQueuePhrases) {
		if !declared[state] {
			t.Errorf("both maps have a phrase for %q and orchestrator's queuestatus.go declares no such state",
				state)
		}
	}
}

// TestFrontendLiveMergeQueueStatesAreRealStates checks the one other
// place state.js spells these out: MERGE_QUEUE_LIVE, the subset that gets
// the live mark on the row.
//
// It is a subset on purpose -- merge_failed and unreadable are
// deliberately left out of it, because dressing a stuck change up as
// progress is the thing that vocabulary was invented to stop -- so this
// asks only that every member is a state the daemon can actually write. A
// typo there costs a row its live mark and nothing else, silently, which
// is the same failure mode as the maps above.
func TestFrontendLiveMergeQueueStatesAreRealStates(t *testing.T) {
	declared := map[string]bool{}
	for _, state := range mergeQueueStates(t) {
		declared[state] = true
	}
	for _, state := range frontendMergeQueueLive(t) {
		if !declared[state] {
			t.Errorf("ui/src/state.js's MERGE_QUEUE_LIVE holds %q, which is not a state "+
				"orchestrator's queuestatus.go declares: that row will never get its live mark", state)
		}
	}
}

// sortedKeys keeps these failures in a stable order, so that a suite that
// has broken in several places reads the same way twice.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
