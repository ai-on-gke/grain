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
	"testing"
)

// Memory is text this deployment repeats into every prompt it ever
// issues, so the two things worth pinning are which layers reach that
// prompt and what an empty layer does to the ones beside it -- the same
// two questions prompt_extension_test.go asks of the settings box next
// door, with the one difference that matters here: there is no per-task
// layer to override either of these, because a memory is not an
// instruction to be exempt from.

func TestMemoryForComposesLayers(t *testing.T) {
	got := MemoryFor("the nightly suite is flaky on retry", "run `make frontend` first")
	if !strings.Contains(got, "the nightly suite is flaky on retry") {
		t.Errorf("MemoryFor() = %q, want the deployment's own memory in it", got)
	}
	if !strings.Contains(got, "run `make frontend` first") {
		t.Errorf("MemoryFor() = %q, want the repo's own memory in it", got)
	}
	if !strings.Contains(got, "What grain has learned across this deployment:") {
		t.Errorf("MemoryFor() = %q, want deployment layer named as such", got)
	}
	if !strings.Contains(got, "About this repository:") {
		t.Errorf("MemoryFor() = %q, want repo layer named as such", got)
	}
	if strings.Contains(got, "team") {
		t.Errorf("MemoryFor() = %q, want no team layer: memory has none", got)
	}
}

func TestMemoryForDropsEmptyLayers(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		deployment, repo, want string
	}{
		{"nothing at all", "", "", ""},
		{"the deployment alone", "learned this", "", "What grain has learned across this deployment:\n\nlearned this"},
		{"whitespace is nothing", "  \n ", "", ""},
		{"a repo whose deployment has said nothing still gets a heading",
			"", "learned that", "About this repository:\n\nlearned that"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MemoryFor(tc.deployment, tc.repo); got != tc.want {
				t.Errorf("MemoryFor(%q, %q) = %q, want %q", tc.deployment, tc.repo, got, tc.want)
			}
		})
	}
}

// A memory a person wrote goes into a prompt saying so, because the
// sentence every prompt already carries about memory -- earlier runs'
// observations, to be replaced once they stop being true -- is the wrong
// thing to say about a correction somebody made on the Settings pane.
func TestMemoryHandNoteSpeaksOnlyForAPerson(t *testing.T) {
	note := MemoryHandNote(Principal{Kind: PrincipalHuman, ID: "bwsalmon"})
	if note == "" {
		t.Fatal("a memory a person wrote reaches a prompt saying nothing about who wrote it")
	}
	if !strings.Contains(note, "person") {
		t.Errorf("MemoryHandNote = %q, want it to say a person wrote what follows", note)
	}

	// And says nothing about anyone else. An agent's memory is what the
	// prompt already describes, and a row from before the author was
	// recorded is unknown -- which is neither, and a line of prompt on
	// every dispatch guessing at it might simply be false.
	for _, by := range []Principal{
		{Kind: PrincipalAgent, ID: "a1b2"},
		{Kind: PrincipalAutomation, ID: "grain"},
		{},
	} {
		if got := MemoryHandNote(by); got != "" {
			t.Errorf("MemoryHandNote(%+v) = %q, want nothing said", by, got)
		}
	}
}
