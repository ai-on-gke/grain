package cifault_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/cifault"
)

var cacheStart = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// The pane polls every five seconds and asks about every candidate
// change each time; without this the answer would be a GitHub call per
// change per poll.
func TestFileCacheAnswersASecondAskFromMemory(t *testing.T) {
	t.Parallel()
	files := &fakeFiles{files: map[int][]string{26: {"scripts/granule/guest-inputs.sh"}}}
	now := cacheStart
	cache := cifault.NewFileCache(files)
	cache.Now = func() time.Time { return now }

	first, err := cache.ListPullRequestFiles("acme", "widgets", 26)
	if err != nil || len(first) != 1 {
		t.Fatalf("files = %v, err = %v", first, err)
	}
	now = now.Add(cifault.FileTTL - time.Second)
	again, _ := cache.ListPullRequestFiles("acme", "widgets", 26)
	if files.calls != 1 {
		t.Fatalf("a second ask cost %d GitHub calls", files.calls)
	}
	if len(again) != 1 || again[0] != first[0] {
		t.Fatalf("remembered the wrong answer: %v", again)
	}

	// Another pull request is another question.
	cache.ListPullRequestFiles("acme", "widgets", 31)
	if files.calls != 2 {
		t.Fatalf("calls = %d, want the second pull request asked about", files.calls)
	}

	// And a pushed-to pull request is a changed answer, so the memory
	// expires rather than standing for the life of the process.
	now = now.Add(2 * time.Second)
	cache.ListPullRequestFiles("acme", "widgets", 26)
	if files.calls != 3 {
		t.Fatalf("calls = %d, want the expired entry read again", files.calls)
	}
}

// A GitHub outage answers every candidate with an error, and the moment
// it does is the worst one to retry per candidate per poll.
func TestFileCacheRemembersAFailure(t *testing.T) {
	t.Parallel()
	files := &fakeFiles{err: errors.New("502 Bad Gateway")}
	cache := cifault.NewFileCache(files)
	cache.Now = func() time.Time { return cacheStart }

	if _, err := cache.ListPullRequestFiles("acme", "widgets", 26); err == nil {
		t.Fatal("want the error through")
	}
	if _, err := cache.ListPullRequestFiles("acme", "widgets", 26); err == nil {
		t.Fatal("want the remembered error through")
	}
	if files.calls != 1 {
		t.Fatalf("a failing GitHub was asked %d times", files.calls)
	}
}

// A deployment with no GitHub credential has no cache, and asking one
// anyway is how a nil lister reaches this: it answers nothing rather
// than panicking.
func TestNilFileCacheAnswersNothing(t *testing.T) {
	t.Parallel()
	cache := cifault.NewFileCache(nil)
	if cache != nil {
		t.Fatalf("a nil lister gave a cache: %+v", cache)
	}
	files, err := cache.ListPullRequestFiles("acme", "widgets", 26)
	if files != nil || err != nil {
		t.Fatalf("files = %v, err = %v", files, err)
	}
}
