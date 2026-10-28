package ui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReadJSONBoundsBody pins #2423: readJSON stops reading at its limit
// and answers 413 instead of buffering whatever a client sends, while a
// body under the limit decodes as before.
func TestReadJSONBoundsBody(t *testing.T) {
	var v struct {
		Body string `json:"body"`
	}

	big := `{"body":"` + strings.Repeat("x", maxJSONBody) + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	if readJSON(w, r, &v) {
		t.Fatal("readJSON accepted a body over maxJSONBody")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}

	// The same body fits the attachment routes' larger bound.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	if !readJSONLimit(w, r, &v, maxAttachmentJSONBody) {
		t.Fatalf("readJSONLimit rejected a body under maxAttachmentJSONBody: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"body":`)))
	if readJSON(w, r, &v) || w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	if readOptionalJSON(w, r, &v) || w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("readOptionalJSON oversized body: status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

// TestAttachmentBodyLimitFitsMaxAttachments checks the attachment routes'
// bound still admits the largest request decodeAttachments would accept.
func TestAttachmentBodyLimitFitsMaxAttachments(t *testing.T) {
	encoded := (MaxAttachmentSize + 2) / 3 * 4
	if need := MaxAttachmentsPerRequest * encoded; maxAttachmentJSONBody <= need {
		t.Fatalf("maxAttachmentJSONBody = %d, need more than %d", maxAttachmentJSONBody, need)
	}
}
