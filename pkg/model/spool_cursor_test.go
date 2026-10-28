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
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSpoolCursorAdvancesAndGoesWithTheRun(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	if err := store.SetSpoolCursor(ctx, "run-1", 10, at); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSpoolCursor(ctx, "run-1", 25, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := store.SpoolCursors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var c model.SpoolCursor = got["run-1"]
	if c.Offset != 25 || !c.UpdatedAt.Equal(at.Add(time.Second)) {
		t.Fatalf("cursor = %+v", c)
	}
}
