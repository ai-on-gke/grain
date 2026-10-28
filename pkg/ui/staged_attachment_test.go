package ui_test

import (
	"bytes"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// POST /api/tasks/{id}/staged-attachments: the hop a run's mcpserver
// makes to hold a file the run attached by path until its finish relays
// it -- over HTTP, through the route, into the store against the live run.
// A body past the 8 MiB an ordinary route takes is the case that matters:
// a full contact sheet is ~150 KB, and the limit on one file is 10 MB.
func TestHTTPClientStageAttachmentHoldsTheFileAgainstTheLiveRun(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)
	remote := accessCodeClient(client, srv.URL)

	sheet := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 9<<20/4)
	upload := ui.AttachmentUpload{Filename: "contact-sheet.png", ContentType: "image/png",
		Content: base64.StdEncoding.EncodeToString(sheet)}

	// With no run in flight there is no finish left to relay it.
	staged, err := remote.StageAttachment(ctx, task.ID, "screenshots/contact-sheet.png", upload)
	if err != nil || staged.Live {
		t.Fatalf("StageAttachment with no live run = %+v, %v; want not live", staged, err)
	}

	running(t, store, ctx, task.ID)
	staged, err = remote.StageAttachment(ctx, task.ID, "screenshots/contact-sheet.png", upload)
	if err != nil || !staged.Live {
		t.Fatalf("StageAttachment = %+v, %v; want it held against the live run", staged, err)
	}
	held, err := store.StagedAttachments(ctx, "run-"+task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := held["screenshots/contact-sheet.png"]
	if !ok || got.Filename != "contact-sheet.png" || got.ContentType != "image/png" || !bytes.Equal(got.Content, sheet) {
		t.Fatalf("staged = %d rows, want the contact sheet under the path the run named", len(held))
	}

	// A refusal survives the wire with its message intact.
	_, err = remote.StageAttachment(ctx, task.ID, "x.png", ui.AttachmentUpload{Filename: "x.png", Content: "not base64!"})
	if err == nil || !strings.Contains(err.Error(), "not valid base64") {
		t.Fatalf("err = %v, want the daemon's own refusal", err)
	}
}
