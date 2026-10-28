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

package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// A daemon that is down or restarting answers none of the calls `grain
// list` makes. The first one to run out of its retry budget is the
// answer: the list must not go on to spend a whole budget each on the
// settings and the listing, which made `grain list` take about six
// minutes to fail against a stopped daemon where every other command
// takes two (docs/test-runs/2026-10-12-upgrade-interruption-restart.md,
// finding 4).
func TestListStopsAtTheFirstCallTheDaemonDidNotAnswer(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]int{}
	// A plain-text 503 is a gateway's, which the client retries for any
	// method until its budget is spent -- a restart behind a proxy.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		http.Error(w, "upstream connect error", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	c := ui.NewHTTPClient(srv.URL)
	c.RetryPolicy = &ui.RetryPolicy{Budget: 200 * time.Millisecond, Initial: 10 * time.Millisecond, Max: 50 * time.Millisecond}

	err := cmdList(context.Background(), c, &printer{}, nil)
	var down *ui.DaemonUnavailableError
	if !errors.As(err, &down) {
		t.Fatalf("cmdList err = %v, want a DaemonUnavailableError", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Errorf("cmdList asked %d endpoints of a daemon that answered none (%v), want it to stop after the first", len(paths), paths)
	}
}
