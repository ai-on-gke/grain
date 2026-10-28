package cifault

import (
	"strconv"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// FileTTL is how long the files an open pull request touches are
// remembered. They change when somebody pushes to it, which is rarely,
// and each miss is a GitHub API call made while somebody waits.
const FileTTL = 10 * time.Minute

// FileCache is a FileLister with FileTTL of memory in front of it, and
// is itself a FileLister, so that whatever asks for a fault's fixes
// neither knows nor cares whether the answer came from GitHub.
//
// It exists because the readers ask far more often than the answer
// changes: the CI pane polls every five seconds, and a deployment that
// has gone red on several branches at once is exactly the one with
// several candidate changes to ask about per poll. A failed call is
// remembered too -- a GitHub outage that answered every candidate with
// an error would otherwise be retried per candidate per poll, which is
// the worst moment to do that.
type FileCache struct {
	lister FileLister
	// Now is the clock, injectable so tests can age an entry out
	// without sleeping. nil means time.Now().UTC().
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]*fileEntry
}

type fileEntry struct {
	files     []string
	err       error
	expiresAt time.Time
}

// NewFileCache wraps a lister. A nil lister -- a deployment with no
// GitHub credential -- gives a nil cache, and a nil cache answers
// nothing rather than panicking: a deployment that cannot ask names no
// fix, which is the answer it gave before there was anything to ask.
func NewFileCache(lister FileLister) *FileCache {
	if lister == nil {
		return nil
	}
	return &FileCache{lister: lister, entries: map[string]*fileEntry{}}
}

// ListPullRequestFiles answers from memory where it can and from the
// wrapped lister where it cannot.
func (c *FileCache) ListPullRequestFiles(owner, repo string, number int) ([]string, error) {
	if c == nil || c.lister == nil {
		return nil, nil
	}
	if model.IsConfigRepo(model.RepoRef{Owner: owner, Name: repo}) {
		// The controller-local config repository's pull requests are
		// grain's own, not GitHub's: asking GitHub about one is a 404
		// about a repository it does not have.
		return nil, nil
	}
	key := owner + "/" + repo + "#" + strconv.Itoa(number)
	now := c.now()
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.files, entry.err
	}
	c.mu.Unlock()

	// Outside the lock on purpose: this is a network call, and holding
	// the lock across it would serialize every other candidate change
	// behind whichever one GitHub is slowest about. Two callers racing
	// the same pull request make two calls and store the same answer.
	files, err := c.lister.ListPullRequestFiles(owner, repo, number)
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*fileEntry{}
	}
	// Expired answers go as new ones arrive. Nothing else ever removes
	// one, and a long-lived daemon asks about every pull request it has
	// ever had open, so without this the map only grows.
	for k, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = &fileEntry{files: files, err: err, expiresAt: now.Add(FileTTL)}
	c.mu.Unlock()
	return files, err
}

func (c *FileCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}
