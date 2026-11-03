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

package daemon

// docs/test-plans/2026-11-03.md §7 item 4 over two simulated REST hosts
// rather than a forge CLI (forgeroute_graphql_test.go has that one): the
// node-ID mutations name no repository in their variables, so each must
// reach the host its RepoHeader names, and nothing about the second
// host's repository may reach the default one.

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

const secondTestHost = "ghes.example.com"

// graphqlLog records every request a sim answers and how many were
// GraphQL documents.
type graphqlLog struct {
	sim     *githubsim.Sim
	mu      sync.Mutex
	graphql int
	repos   []string
}

func (l *graphqlLog) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	l.mu.Lock()
	if path == "/graphql" {
		l.graphql++
	}
	l.repos = append(l.repos, method+" "+path+" repo="+headers[github.RepoHeader])
	l.mu.Unlock()
	return l.sim.Request(method, path, headers, body)
}

func newSecondHostSim(t *testing.T, owner, repo string) *githubsim.Sim {
	t.Helper()
	bare := filepath.Join(t.TempDir(), owner, repo+".git")
	gitT(t, "", "init", "-q", "--bare", "-b", "main", bare)
	sim := githubsim.New(owner, repo, bare, "main")
	sim.PullRequests = []githubsim.PullRequest{{Number: 7, Head: "grain/t7", Base: "main", State: "open"}}
	sim.ReviewComments[7] = []github.ReviewComment{{ID: 71, User: "review-bot[bot]", Body: "nit", Path: "a.go"}}
	return sim
}

func TestNodeMutationsOnASecondRESTHostReachThatHostOnly(t *testing.T) {
	def := &graphqlLog{sim: newSecondHostSim(t, "acme", "widgets")}
	second := &graphqlLog{sim: newSecondHostSim(t, "acme", "widgets")}
	r := &forgeRouter{def: def, defHost: "github.com",
		transportFor: func(host, owner, repo string) (github.Transport, error) {
			if host != secondTestHost {
				t.Errorf("transportFor(%q, %q, %q), want only %s", host, owner, repo, secondTestHost)
			}
			return second, nil
		},
		hostOf: func(owner, repo string) string { return "" }}
	client := github.NewClient(r, nil)
	qualified := secondTestHost + "/acme"

	if err := client.ResolveReviewThread(qualified, "widgets", "thread-71"); err != nil {
		t.Fatalf("ResolveReviewThread on %s: %v", secondTestHost, err)
	}
	if !second.sim.ResolvedThreads["thread-71"] || def.sim.ResolvedThreads["thread-71"] {
		t.Fatalf("resolved: second=%v default=%v, want only the second host",
			second.sim.ResolvedThreads, def.sim.ResolvedThreads)
	}
	if _, err := client.EnqueuePullRequest(qualified, "widgets", 7, ""); err != nil {
		t.Fatalf("EnqueuePullRequest on %s: %v", secondTestHost, err)
	}
	if second.sim.Enqueues != 1 || def.sim.Enqueues != 0 {
		t.Fatalf("enqueues: second=%d default=%d, want 1 and 0", second.sim.Enqueues, def.sim.Enqueues)
	}
	if def.graphql != 0 || len(def.repos) != 0 {
		t.Fatalf("the default host was asked: %q", def.repos)
	}
	if second.graphql < 2 {
		t.Fatalf("second host GraphQL documents = %d, want at least the two mutations: %q", second.graphql, second.repos)
	}

	// The same thread on the default host's same-named repository is
	// that host's, and goes there.
	if err := client.ResolveReviewThread("acme", "widgets", "thread-71"); err != nil {
		t.Fatalf("ResolveReviewThread on the default host: %v", err)
	}
	if !def.sim.ResolvedThreads["thread-71"] || def.graphql != 1 {
		t.Fatalf("default host resolved %v over %d GraphQL documents, want thread-71 over 1",
			def.sim.ResolvedThreads, def.graphql)
	}
}
