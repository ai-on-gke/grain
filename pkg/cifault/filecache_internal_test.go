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
