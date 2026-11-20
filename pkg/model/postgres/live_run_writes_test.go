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

package postgres_test

import (
	"testing"
	"time"
)

// Not t.Parallel(): txid_current_snapshot() is cluster-wide, so sibling tests
// committing in other schemas on the shared test cluster legitimately advance
// the snapshot while this test checks that our own live-run flush does not.
func TestLiveRunWritesStayOffChangeTokenOnPostgres(t *testing.T) {
	store, _, ctx := openStore(t)
	t.Cleanup(func() { _ = store.Close() })
	store.SetLiveRunFlushIntervalForTest(20 * time.Millisecond)

	startedTask(t, store, ctx, "a1b2")
	before, err := store.ChangeToken(ctx)
	if err != nil {
		t.Fatalf("ChangeToken: %v", err)
	}
	changedCh := store.Changed()
	actCh := store.ActivityChanged()

	if live, err := store.SetTaskActivity(ctx, "a1b2", "running tests", now.Add(time.Second)); err != nil || !live {
		t.Fatalf("SetTaskActivity = (%v, %v), want (true, nil)", live, err)
	}
	select {
	case <-actCh:
	default:
		t.Fatal("ActivityChanged did not close on SetTaskActivity")
	}
	if err := store.SetRunTranscript(ctx, "run-a1b2", "postgres buffered transcript"); err != nil {
		t.Fatalf("SetRunTranscript: %v", err)
	}
	if err := store.FlushLiveRunWrites(ctx); err != nil {
		t.Fatalf("FlushLiveRunWrites: %v", err)
	}
	select {
	case <-changedCh:
		t.Fatal("Changed() fired on live-run flush")
	default:
	}

	afterFlush, err := store.ChangeToken(ctx)
	if err != nil {
		t.Fatalf("ChangeToken after flush: %v", err)
	}
	if afterFlush != before {
		t.Fatalf("ChangeToken moved across live-run flush on Postgres: %s -> %s", before, afterFlush)
	}

	// A real commit through the store must still move ChangeToken even if a
	// live-run flush commits immediately after it before the next read.
	if err := store.PutTask(ctx, task("c3d4", true)); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	if _, err := store.SetTaskActivity(ctx, "a1b2", "still testing", now.Add(2*time.Second)); err != nil {
		t.Fatalf("SetTaskActivity 2: %v", err)
	}
	if err := store.FlushLiveRunWrites(ctx); err != nil {
		t.Fatalf("FlushLiveRunWrites 2: %v", err)
	}
	afterPut, err := store.ChangeToken(ctx)
	if err != nil {
		t.Fatalf("ChangeToken after PutTask: %v", err)
	}
	if afterPut == before {
		t.Fatalf("ChangeToken stayed at %s after PutTask + live-run flush", afterPut)
	}
}
