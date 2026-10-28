package ui

// spaHandler itself, in isolation from a real embedded build -- the
// external ui_test package's own TestStaticFrontendIsServed only ever
// sees pkg/ui/static as checked in (placeholder.html, no index.html),
// so it can't exercise the fallback-to-index.html branch a real `npm
// run build` output takes. An in-memory fstest.MapFS stands in for that
// real build here instead.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSpaHandlerServesRealFilesDirectly(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<html>root</html>")},
		"assets/app.js": {Data: []byte("console.log('hi')")},
	}
	h := spaHandler(fsys)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "console.log('hi')" {
		t.Fatalf("body = %q, want the real asset content", rec.Body.String())
	}
}

func TestSpaHandlerFallsBackToIndexForAnUnknownPath(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html>root</html>")},
	}
	h := spaHandler(fsys)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/repos", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "<html>root</html>" {
		t.Fatalf("body = %q, want index.html's content", rec.Body.String())
	}
}

func TestSpaHandlerWithNoIndexFallsThroughTo404(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"placeholder.html": {Data: []byte("placeholder")},
	}
	h := spaHandler(fsys)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/repos", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 -- no index.html to fall back to", rec.Code)
	}
}
