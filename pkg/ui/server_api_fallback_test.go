package ui_test

// What an /api/ request no route matched is answered with. Before
// apiFallback every one of these fell through to the SPA handler and
// came back 200 with a page of HTML -- found by hand against a real
// deployment (task 244), where `DELETE /api/tasks/1` and a typo'd path
// were both indistinguishable from a call that worked.

import (
	"net/http"
	"strings"
	"testing"
)

func TestUnknownAPIPathIs404JSON(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/no-such-thing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want JSON", ct)
	}
	body := decode[map[string]string](t, rec)
	if !strings.Contains(body["error"], "/api/no-such-thing") {
		t.Fatalf("error = %q, want it to name the path asked for", body["error"])
	}
}

func TestWrongMethodOnARealAPIPathIs405(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodDelete, "/api/tasks/1", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 -- the path exists, the method does not", rec.Code)
	}
	body := decode[map[string]string](t, rec)
	if !strings.Contains(body["error"], http.MethodDelete) {
		t.Fatalf("error = %q, want it to name the method", body["error"])
	}
}

// A path with a wildcard segment is recognised through that wildcard,
// not by string equality: /api/repos/{owner}/{name}/branches is a real
// path however it is filled in.
func TestWrongMethodIsRecognisedThroughAWildcard(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/branches", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// The SPA still answers everything that is not the API: this is the
// fallback that lets a bookmarked /tasks/42 load at all.
func TestNonAPIPathsStillReachTheSPAHandler(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/tasks/42", "")
	// pkg/ui/static as checked in has no index.html, so the SPA handler
	// 404s here rather than serving a page; what matters is that this
	// was not answered with the API's own JSON error.
	if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q: a UI path was answered by the API fallback", ct)
	}
}
