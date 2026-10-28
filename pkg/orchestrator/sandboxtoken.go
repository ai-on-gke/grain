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

package orchestrator

import (
	"context"
	"log"
	"time"
)

// DefaultSandboxTokenRefreshInterval is how often a live run's git-proxy
// token is refreshed (Deps.RefreshSandboxToken). It only has to be well
// inside gitproxy.DefaultSandboxTokenTTL, a day: a refresh that fails, or
// a daemon too busy to make one for a while, still leaves the token most
// of a day to run before the next attempt matters.
const DefaultSandboxTokenRefreshInterval = time.Hour

// keepSandboxTokenFresh refreshes sandbox's git-proxy token every interval
// until ctx is done or the returned stop is called, and returns stop.
//
// A token is minted with a TTL (gitproxy.DefaultSandboxTokenTTL) as a
// backstop for one whose Revoke never comes, and nothing else ties it to
// its run's lifetime -- so without this a run that lived past the TTL
// lost its git access mid-run. Refreshing from the goroutine that owns
// the run, rather than from a sweep over every live run, makes "this run
// is still live" true by construction: the refresher exists exactly as
// long as the sandbox is held, and a daemon that dies takes it with it,
// leaving the TTL in charge.
//
// stop waits for any refresh in flight, so a caller that stops before
// revoking can be sure no refresh lands after the revoke. A nil refresh
// does nothing and its stop is a no-op. A failed refresh is logged and
// tried again at the next tick: the token it would have extended still
// has the rest of its TTL.
func keepSandboxTokenFresh(ctx context.Context, refresh func(sandbox string) (string, error), sandbox string, interval time.Duration) (stop func()) {
	if refresh == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = DefaultSandboxTokenRefreshInterval
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := refresh(sandbox); err != nil {
					log.Printf("orchestrator: refreshing sandbox %s's git-proxy token: %v", sandbox, err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
