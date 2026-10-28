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

package model_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestStagedAttachmentIsHeldAgainstTheLiveRunUntilCleared(t *testing.T) {
	store, ctx := open(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{
		ID: "a1b2", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}); err != nil {
		t.Fatal(err)
	}
	shot := model.StagedAttachment{Path: "screenshots/contact-sheet.png", Filename: "contact-sheet.png",
		ContentType: "image/png", Content: []byte("\x89PNG one"), CreatedAt: now}

	// No run in flight: nothing would ever relay it, so nothing is kept.
	if live, err := store.StageAttachment(ctx, "a1b2", shot); err != nil || live {
		t.Fatalf("StageAttachment with no live run = %v, %v; want not live", live, err)
	}

	if err := store.StartRun(ctx, model.Run{ID: "a1b2-1", TaskID: "a1b2", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if live, err := store.StageAttachment(ctx, "a1b2", shot); err != nil || !live {
		t.Fatalf("StageAttachment = %v, %v; want live", live, err)
	}
	// Naming the same path again replaces what was held under it.
	shot.Content = []byte("\x89PNG two")
	if _, err := store.StageAttachment(ctx, "a1b2", shot); err != nil {
		t.Fatal(err)
	}
	got, err := store.StagedAttachments(ctx, "a1b2-1")
	if err != nil {
		t.Fatal(err)
	}
	held, ok := got["screenshots/contact-sheet.png"]
	if len(got) != 1 || !ok || !bytes.Equal(held.Content, shot.Content) ||
		held.Filename != "contact-sheet.png" || held.ContentType != "image/png" {
		t.Fatalf("StagedAttachments = %+v, want the second copy alone", got)
	}

	if err := store.ClearStagedAttachments(ctx, "a1b2-1"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.StagedAttachments(ctx, "a1b2-1"); err != nil || len(got) != 0 {
		t.Fatalf("StagedAttachments after clearing = %v, %v; want none", got, err)
	}
}
