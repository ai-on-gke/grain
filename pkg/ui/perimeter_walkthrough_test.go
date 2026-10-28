package ui_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

// The routes that carry attachments may take a body far past the 8 MiB
// every other route is held to, but only for the attachments' own
// content. A 9 MiB description with no attachment at all was answered
// 201 in the 2026-10-01 perimeter walkthrough; it is a 413 now, while
// the same-sized file posted as an attachment still goes through.
func TestAttachmentRoutesHoldNonAttachmentBytesToMaxJSONBody(t *testing.T) {
	srv, _ := testServer(t)
	big := strings.Repeat("x", 9<<20)

	if rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"t","repo":"acme/widgets","description":"`+big+`"}`); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST /api/tasks with a 9 MiB description: status = %d, want 413: %.200s", rec.Code, rec.Body)
	}

	file := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("y", 9<<20)))
	body, _ := json.Marshal(map[string]any{
		"title": "t", "repo": "acme/widgets",
		"attachments": []ui.AttachmentUpload{{Filename: "big.bin", ContentType: "application/octet-stream", Content: file}},
	})
	rec := do(t, srv, http.MethodPost, "/api/tasks", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/tasks with a 9 MiB attachment: status = %d, want 201: %.200s", rec.Code, rec.Body)
	}
	id := decode[ui.Task](t, rec).ID

	for _, path := range []string{"/api/tasks/" + id + "/comments", "/api/tasks/" + id + "/messages"} {
		if rec := do(t, srv, http.MethodPost, path, `{"body":"`+big+`"}`); rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("POST %s with a 9 MiB body: status = %d, want 413: %.200s", path, rec.Code, rec.Body)
		}
	}
}

// img-src admits http: as well as https: -- a Markdown image a task's
// author linked over plain http was blocked by the policy (and logged a
// securitypolicyviolation) on every task pane that showed it. Images
// cannot run script, and https: already admits any origin, so this
// opens no channel that was not open.
func TestContentSecurityPolicyAdmitsMarkdownImagesOverHTTP(t *testing.T) {
	srv, _ := testServer(t)
	csp := do(t, srv, http.MethodGet, "/", "").Header().Get("Content-Security-Policy")
	var imgSrc string
	for _, d := range strings.Split(csp, ";") {
		if f := strings.Fields(d); len(f) > 0 && f[0] == "img-src" {
			imgSrc = " " + strings.Join(f[1:], " ") + " "
		}
	}
	for _, want := range []string{" 'self' ", " data: ", " blob: ", " http: ", " https: "} {
		if !strings.Contains(imgSrc, want) {
			t.Errorf("img-src = %q, missing %q", imgSrc, strings.TrimSpace(want))
		}
	}
}
