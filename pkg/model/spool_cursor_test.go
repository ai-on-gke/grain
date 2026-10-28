package model_test

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
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
