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

package daemon

import (
	"context"
	"testing"
	"time"
)

// Run defers the state sync's stop and can return with its own context
// still live (a startup error). The stop must end the loop itself, with
// its final export, rather than wait for a cancellation that never
// comes (docs/test-runs/2026-10-15-k8s-run-hosts-sandboxes.md, F3).
func TestStateSyncStopsWithoutItsParentContextEnding(t *testing.T) {
	finals := make(chan struct{}, 1)
	sync := func(context.Context) (bool, error) { return false, nil }
	syncAll := func(context.Context) (bool, error) { finals <- struct{}{}; return false, nil }
	stop := startStateSync(context.Background(), sync, syncAll, make(chan struct{}))

	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stopping the state sync waited for a parent context that was never cancelled")
	}
	select {
	case <-finals:
	default:
		t.Fatal("the state sync stopped without its final export")
	}
}
