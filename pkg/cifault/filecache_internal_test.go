package cifault

import (
	"testing"
	"time"
)

type countingLister struct{}

func (countingLister) ListPullRequestFiles(_, _ string, _ int) ([]string, error) {
	return []string{"go.mod"}, nil
}

// An answer that has expired is let go of, not only ignored: a daemon
// that lives for months asks about every pull request it ever had open.
func TestFileCacheForgetsExpiredAnswers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	cache := NewFileCache(countingLister{})
	cache.Now = func() time.Time { return now }

	for n := 1; n <= 10; n++ {
		cache.ListPullRequestFiles("acme", "widgets", n)
	}
	now = now.Add(FileTTL)
	cache.ListPullRequestFiles("acme", "widgets", 11)

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 1 {
		t.Fatalf("the cache holds %d answers after every other one expired, want 1", len(cache.entries))
	}
}
