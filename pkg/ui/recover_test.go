package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/apierror"
)

// A handler panic answers with apierror's sanitized 500, and the panic
// value and stack go to the log under the same ref -- never to the body.
func TestServeRecoveredSanitizesPanic(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	serveRecovered(rec, req, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret token sk-123 at /home/grain/pkg/ui/tasks.go")
	}))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"sk-123", "tasks.go", "goroutine", "panic"} {
		if strings.Contains(body, leak) {
			t.Errorf("response body leaks %q: %s", leak, body)
		}
	}
	var got apierror.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding body %q: %v", body, err)
	}
	if got.Code != apierror.CodeInternal || got.Ref == "" {
		t.Fatalf("body = %+v, want code %q with a ref", got, apierror.CodeInternal)
	}
	if l := logs.String(); !strings.Contains(l, got.Ref) || !strings.Contains(l, "sk-123") || !strings.Contains(l, "goroutine") {
		t.Fatalf("log does not carry the panic and stack under ref %s:\n%s", got.Ref, l)
	}
}

// A panic after the response has started cannot be turned into a 500;
// it aborts the response instead of appending an error body to it.
func TestServeRecoveredAbortsStartedResponse(t *testing.T) {
	prev := log.Writer()
	log.SetOutput(&bytes.Buffer{})
	defer log.SetOutput(prev)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	defer func() {
		p := recover()
		err, ok := p.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", p)
		}
		if strings.Contains(rec.Body.String(), "internal server error") {
			t.Fatalf("error body appended to a started response: %q", rec.Body.String())
		}
	}()
	serveRecovered(rec, req, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic("boom")
	}))
}

// The SSE stream and the executor upgrade type-assert for Flusher and
// Hijacker; wrapping the writer must not hide either.
func TestServeRecoveredKeepsWriterInterfaces(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	serveRecovered(rec, req, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("writer is not an http.Flusher")
		}
		if _, ok := w.(http.Hijacker); !ok {
			t.Error("writer is not an http.Hijacker")
		}
	}))
}
