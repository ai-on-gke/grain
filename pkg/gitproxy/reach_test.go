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

package gitproxy_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

func reachFixture(t *testing.T) (*gitproxy.Reach, *gitproxy.CredentialSet, *githubsim.Sim, *time.Time) {
	t.Helper()
	creds, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.SetToken("bouncer", "tok-1"); err != nil {
		t.Fatal(err)
	}
	sim := githubsim.New("acme", "widgets", "", "main")
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"tok-1": {{FullName: "gke-labs/bouncer", Push: true}, {FullName: "acme/docs", Push: false}},
		"tok-2": {{FullName: "gke-labs/other", Push: true}},
	}
	now := time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)
	return &gitproxy.Reach{Credentials: creds, API: sim, Now: func() time.Time { return now }}, creds, sim, &now
}

func TestReachChecksPushAndReadAgainstGitHubsListing(t *testing.T) {
	t.Parallel()
	reach, _, _, _ := reachFixture(t)
	for _, c := range []struct {
		owner, repo string
		push        bool
		want        gitproxy.Reachability
	}{
		{"gke-labs", "bouncer", true, gitproxy.Reachable},
		{"GKE-Labs", "Bouncer", true, gitproxy.Reachable},
		{"acme", "docs", false, gitproxy.Reachable},
		{"acme", "docs", true, gitproxy.Unreachable},
		{"bwsalmon", "grain", false, gitproxy.Unreachable},
	} {
		got, err := reach.Check("bouncer", c.owner, c.repo, c.push)
		if err != nil || got != c.want {
			t.Errorf("Check(%s/%s, push=%v) = %v, %v; want %v", c.owner, c.repo, c.push, got, err, c.want)
		}
	}
}

// A listing is kept for a short while, and a credential whose token
// changed is listed again at once rather than answered from the old one.
func TestReachCachesPerTokenAndRefreshesWhenTheTokenChanges(t *testing.T) {
	t.Parallel()
	reach, creds, sim, now := reachFixture(t)
	if _, ok, err := reach.Repos("bouncer"); !ok || err != nil {
		t.Fatalf("Repos: ok=%v err=%v", ok, err)
	}
	calls := len(sim.Calls)
	if _, _, err := reach.Repos("bouncer"); err != nil || len(sim.Calls) != calls {
		t.Fatalf("a second listing within the TTL asked GitHub again (%d calls, was %d)", len(sim.Calls), calls)
	}
	if err := creds.SetToken("bouncer", "tok-2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := reach.Check("bouncer", "gke-labs", "other", true); got != gitproxy.Reachable {
		t.Errorf("after the token changed, Check = %v; want the new token's listing", got)
	}
	*now = now.Add(gitproxy.DefaultReachTTL + time.Second)
	before := len(sim.Calls)
	if _, _, err := reach.Repos("bouncer"); err != nil || len(sim.Calls) == before {
		t.Errorf("a stale listing was not refreshed")
	}
}

// Nothing to ask, or GitHub refusing the listing, is "unknown": a caller
// refuses nothing on it.
func TestReachIsUnknownWithoutAListing(t *testing.T) {
	t.Parallel()
	reach, creds, _, _ := reachFixture(t)
	if err := creds.SetToken("stale", "tok-revoked"); err != nil {
		t.Fatal(err)
	}
	if got, err := reach.Check("stale", "gke-labs", "bouncer", true); got != gitproxy.ReachUnknown || err == nil {
		t.Errorf("Check with a token GitHub refuses = %v, %v; want unknown and an error", got, err)
	}
	var none *gitproxy.Reach
	if got, err := none.Check("bouncer", "gke-labs", "bouncer", true); got != gitproxy.ReachUnknown || err != nil {
		t.Errorf("nil Reach Check = %v, %v; want unknown", got, err)
	}
}

func TestReachKeepsNonDefaultHostPrefixEvenWhenDefaultHostFieldIsEmpty(t *testing.T) {
	t.Parallel()
	reach, creds, _, _ := reachFixture(t)
	if err := creds.SetToken("ghe", "tok-ghe"); err != nil {
		t.Fatal(err)
	}
	if err := creds.SetHost("ghe", gitproxy.CredentialHost{
		Host: "ghe.example.com",
		Kind: model.GitHostGitHub,
	}); err != nil {
		t.Fatal(err)
	}
	gheSim := githubsim.New("acme", "internal", "", "main")
	gheSim.Reachable = map[string][]githubsim.ReachableRepo{
		"tok-ghe": {{FullName: "acme/internal", Push: true}},
	}
	reach.Transports = map[string]github.Transport{
		"ghe.example.com": gheSim,
	}

	repos, ok, err := reach.ReposForHost("ghe", "ghe.example.com")
	if err != nil || !ok || len(repos) != 1 {
		t.Fatalf("ReposForHost = %v, %v, %v; want 1 repo", repos, ok, err)
	}
	if repos[0].Repo != "ghe.example.com/acme/internal" {
		t.Errorf("Repo = %q, want ghe.example.com/acme/internal", repos[0].Repo)
	}
}

func TestReachCoalescesConcurrentListings(t *testing.T) {
	t.Parallel()
	reach, _, sim, _ := reachFixture(t)
	var wg sync.WaitGroup
	const workers = 8
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repos, ok, err := reach.Repos("bouncer")
			if err != nil || !ok || len(repos) != 2 {
				errs <- err
				return
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Repos error: %v", err)
		}
	}
	// Cache is empty on start; with singleflight coalescing, concurrent callers
	// may trigger 1 listing (not 8). Note sim.Calls counts paginated GET calls;
	// 1 listing of 2 repos takes 1 GET call.
	if len(sim.Calls) != 1 {
		t.Errorf("concurrent Repos made %d GitHub API calls, want 1", len(sim.Calls))
	}
}

type panickingReachAPI struct {
	github.Transport
	panics int
}

func (p *panickingReachAPI) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	if p.panics == 0 {
		p.panics++
		panic("boom during reachable repo list")
	}
	return p.Transport.Request(method, path, headers, body)
}

func TestReachPanicClearsInflightFlight(t *testing.T) {
	t.Parallel()
	reach, _, sim, _ := reachFixture(t)
	reach.API = &panickingReachAPI{Transport: sim}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _, _ = reach.Repos("bouncer")
	}()
	if recovered == nil {
		t.Fatal("expected initial Repos call to panic")
	}

	// The aborted flight must have been closed and removed so a retry succeeds
	// instead of blocking forever on flight.done.
	repos, ok, err := reach.Repos("bouncer")
	if err != nil || !ok || len(repos) != 2 {
		t.Fatalf("retry Repos after panic = (%d repos, ok=%v, err=%v), want 2 repos, ok=true, nil", len(repos), ok, err)
	}
}
