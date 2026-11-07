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

package demo

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestDemoActionsCacheFlushRemovesFlushedRepos(t *testing.T) {
	c := &demoActionsCache{
		maxBytes: 20 * (1 << 30),
		repos: []ui.ActionsCacheRepoStats{
			{Repo: "acme/gadgets", Bytes: 1 * (1 << 30)},
			{Repo: "acme/widgets", Bytes: 2 * (1 << 30)},
			{Repo: "acme/empty", Bytes: 0},
		},
	}

	entries, bytes, maxBytes, repos := c.ActionsCacheStats()
	if entries != 20 || bytes != 3*(1<<30) || maxBytes != 20*(1<<30) {
		t.Fatalf("initial stats = (%d, %d, %d), want (20, %d, %d)", entries, bytes, maxBytes, 3*(1<<30), 20*(1<<30))
	}
	if len(repos) != 2 || repos[0].Repo != "acme/widgets" || repos[1].Repo != "acme/gadgets" {
		t.Fatalf("initial repos = %+v, want [acme/widgets acme/gadgets] sorted largest-first", repos)
	}

	freedEntries, freedBytes := c.FlushActionsCache("acme/gadgets")
	if freedEntries != 10 || freedBytes != 1*(1<<30) {
		t.Fatalf("FlushActionsCache(acme/gadgets) = (%d, %d), want (10, %d)", freedEntries, freedBytes, 1*(1<<30))
	}
	entries, bytes, _, repos = c.ActionsCacheStats()
	if entries != 10 || bytes != 2*(1<<30) || len(repos) != 1 || repos[0].Repo != "acme/widgets" {
		t.Fatalf("after single-repo flush: entries=%d bytes=%d repos=%+v", entries, bytes, repos)
	}

	freedEntries, freedBytes = c.FlushActionsCache("")
	if freedEntries != 10 || freedBytes != 2*(1<<30) {
		t.Fatalf("FlushActionsCache(\"\") = (%d, %d), want (10, %d)", freedEntries, freedBytes, 2*(1<<30))
	}
	entries, bytes, _, repos = c.ActionsCacheStats()
	if entries != 0 || bytes != 0 || len(repos) != 0 || c.ActionsCacheEntryCount() != 0 {
		t.Fatalf("after full flush: entries=%d bytes=%d entryCount=%d repos=%+v, want all empty", entries, bytes, c.ActionsCacheEntryCount(), repos)
	}
}
