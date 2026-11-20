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

package cifault

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
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
	// flights are the reads on their way to GitHub right now, so that
	// every caller asking about the same pull request meanwhile waits on
	// that one call rather than making its own. The pane, every agent's
	// wait_for_checks and the orchestrator all ask at once when a cached
	// fault list expires, and before this each of them paid for the
	// same cold pull request.
	flights map[string]*fileFlight
}

type fileFlight struct {
	done  chan struct{}
	files []string
	err   error
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
	if flight, ok := c.flights[key]; ok {
		c.mu.Unlock()
		<-flight.done
		return flight.files, flight.err
	}
	flight := &fileFlight{done: make(chan struct{})}
	if c.flights == nil {
		c.flights = map[string]*fileFlight{}
	}
	c.flights[key] = flight
	c.mu.Unlock()

	// Outside the lock on purpose: this is a network call, and holding
	// the lock across it would serialize every other candidate change
	// behind whichever one GitHub is slowest about.
	settled := false
	defer func() {
		// A lister that panicked still lets its waiters go.
		if !settled {
			c.mu.Lock()
			delete(c.flights, key)
			c.mu.Unlock()
			flight.err = errors.New("listing pull request files did not finish")
			close(flight.done)
		}
	}()
	files, err := c.lister.ListPullRequestFiles(owner, repo, number)
	flight.files, flight.err = files, err
	settled = true
	c.mu.Lock()
	delete(c.flights, key)
	close(flight.done)
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
	// A deadline is the asker's, not GitHub's answer about this pull
	// request: remembered, it would answer every later pass with the same
	// error for FileTTL without asking again.
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		c.entries[key] = &fileEntry{files: files, err: err, expiresAt: now.Add(FileTTL)}
	}
	c.mu.Unlock()
	return files, err
}

func (c *FileCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}
