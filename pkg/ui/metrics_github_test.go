package ui

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

type fakeCallStats struct{ stats github.Stats }

func (f fakeCallStats) Stats() github.Stats { return f.stats }

// A UI that was not handed a transport reports the section unavailable
// rather than reporting zero -- which would read as a deployment that
// has made no calls, and is the opposite of what this exists to show.
func TestGitHubMetricsAreUnavailableWithNoTransport(t *testing.T) {
	t.Parallel()
	got := githubMetrics(nil)
	if got.Enabled {
		t.Error("a UI with no transport reported the section as enabled")
	}
	if got.Sent != 0 || len(got.Endpoints) != 0 {
		t.Errorf("got %+v, want the zero section", got)
	}
}

// Spent is the number that matters, and it is Sent minus the 304s: a
// call shape that revalidates a thousand times costs nothing.
func TestGitHubMetricsReportsWhatWasActuallySpent(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 9, 8, 18, 0, 0, 0, time.UTC)
	got := githubMetrics(fakeCallStats{github.Stats{
		Sent: 100, Revalidated: 90, Declined: 4, Failed: 2, Retried: 1,
		Endpoints: map[string]github.EndpointStats{
			"/repos/{owner}/{repo}/pulls/{n}":                {Sent: 60, Revalidated: 58},
			"/repos/{owner}/{repo}/commits/{sha}/check-runs": {Sent: 30, Revalidated: 22, Failed: 2},
			"/repos/{owner}/{repo}/branches/{branch}":        {Sent: 10, Revalidated: 10},
		},
		Limit: github.RateLimitSnapshot{
			Limit: 5000, Remaining: 4231, Resets: reset,
			Seen: time.Date(2026, 9, 8, 17, 30, 0, 0, time.UTC),
		},
	}})

	if !got.Enabled {
		t.Fatal("the section reported itself unavailable")
	}
	if got.Spent != 10 {
		t.Errorf("Spent = %d, want 100 sent less 90 revalidated", got.Spent)
	}
	if got.Declined != 4 || got.Failed != 2 {
		t.Errorf("Declined = %d Failed = %d", got.Declined, got.Failed)
	}
	// Retried rides through beside them: it is the one number that says
	// how much of GitHub's bad minute the transport absorbed by itself,
	// and a section that dropped it would report the retry as free.
	if got.Retried != 1 {
		t.Errorf("Retried = %d, want the transport's own count", got.Retried)
	}
	if got.Limit != 5000 || got.Remaining != 4231 || !got.LimitResetsAt.Equal(reset) {
		t.Errorf("budget = %+v", got)
	}
	if !got.LimitSeen {
		t.Error("LimitSeen is false despite headers having been seen")
	}
}

// The first row has to be the one worth reading, and that is the one
// that spent the most -- not the one called the most, which on a healthy
// deployment is whatever revalidates best.
func TestGitHubMetricsPutsTheExpensiveEndpointFirst(t *testing.T) {
	t.Parallel()
	got := githubMetrics(fakeCallStats{github.Stats{
		Sent: 100, Revalidated: 88,
		Endpoints: map[string]github.EndpointStats{
			// Called most, costs nothing.
			"/repos/{owner}/{repo}/pulls/{n}": {Sent: 80, Revalidated: 79},
			// Called less, costs most.
			"/repos/{owner}/{repo}/commits/{sha}/check-runs": {Sent: 20, Revalidated: 9},
		},
	}})
	if len(got.Endpoints) != 2 {
		t.Fatalf("got %d rows", len(got.Endpoints))
	}
	if got.Endpoints[0].Endpoint != "/repos/{owner}/{repo}/commits/{sha}/check-runs" {
		t.Errorf("first row is %q, want the one that spent the most", got.Endpoints[0].Endpoint)
	}
	if got.Endpoints[0].Spent != 11 || got.Endpoints[1].Spent != 1 {
		t.Errorf("spends = %d and %d", got.Endpoints[0].Spent, got.Endpoints[1].Spent)
	}
}

// A report read twice has to read the same way, so ties break by name
// rather than by map order.
func TestGitHubMetricsOrdersTiesStably(t *testing.T) {
	t.Parallel()
	in := fakeCallStats{github.Stats{
		Endpoints: map[string]github.EndpointStats{
			"/b": {Sent: 5}, "/a": {Sent: 5}, "/c": {Sent: 5},
		},
	}}
	for i := 0; i < 20; i++ {
		got := githubMetrics(in)
		if got.Endpoints[0].Endpoint != "/a" || got.Endpoints[2].Endpoint != "/c" {
			t.Fatalf("order moved between reads: %v", got.Endpoints)
		}
	}
}

// A deployment that has made calls but seen no rate-limit headers -- a
// mock GitHub, an enterprise host that omits them -- must not report a
// budget of zero, which reads as exhausted.
func TestGitHubMetricsSaysWhenNoBudgetHasBeenSeen(t *testing.T) {
	t.Parallel()
	got := githubMetrics(fakeCallStats{github.Stats{Sent: 5}})
	if got.LimitSeen {
		t.Error("LimitSeen is true though no response carried a budget")
	}
	if !got.LimitResetsAt.IsZero() {
		t.Errorf("LimitResetsAt = %v, want zero", got.LimitResetsAt)
	}
}

// The per-caller rows are ordered by what they spent, kinds within each
// the same way, and the budget's projection and the cache come through.
func TestGitHubMetricsBreakTheSpendDownByCaller(t *testing.T) {
	t.Parallel()
	exhausted := time.Date(2026, 10, 26, 4, 50, 0, 0, time.UTC)
	got := githubMetrics(fakeCallStats{github.Stats{
		Callers: map[string]github.CallerStats{
			"review-threads": {Sent: 40, Revalidated: 38, Kinds: map[string]github.KindStats{
				"review comments": {Sent: 40, Revalidated: 38}}},
			"sync": {Sent: 300, Revalidated: 100, Kinds: map[string]github.KindStats{
				"PR read": {Sent: 100, Revalidated: 80}, "head read": {Sent: 200, Revalidated: 20}}},
		},
		Budget: github.BudgetSnapshot{RateLimitSnapshot: github.RateLimitSnapshot{Limit: 5000, Remaining: 600},
			SpendPerHour: 900, ExhaustedAt: exhausted, Low: true, Credentials: 1},
		Cache: github.CacheStats{Entries: 300, MaxEntries: 2048, Evicted: 7},
	}})
	if len(got.Callers) != 2 || got.Callers[0].Caller != "sync" || got.Callers[0].Spent != 200 {
		t.Fatalf("callers = %+v, want sync first with 200 spent", got.Callers)
	}
	if k := got.Callers[0].Kinds; k[0].Kind != "head read" || k[0].Spent != 180 || k[1].Spent != 20 {
		t.Errorf("sync's kinds = %+v, want head read (180) before PR read (20)", k)
	}
	b := got.Budget
	if !b.Low || b.Remaining != 600 || !b.ExhaustedAt.Equal(exhausted) || b.LowFraction != github.LowBudgetFraction {
		t.Errorf("budget = %+v", b)
	}
	if got.Cache.Evicted != 7 || got.Cache.MaxEntries != 2048 {
		t.Errorf("cache = %+v", got.Cache)
	}
}
