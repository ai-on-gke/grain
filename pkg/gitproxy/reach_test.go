package gitproxy_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
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
