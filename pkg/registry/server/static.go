package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:static
var embeddedStaticFS embed.FS

// NewStaticHandler returns an http.Handler serving the embedded registry SPA
// bundle from `static/dist` (when built) or `static/placeholder.html`.
func NewStaticHandler() http.Handler {
	distFS, err := fs.Sub(embeddedStaticFS, "static/dist")
	hasDistIndex := false
	if err == nil {
		if f, openErr := distFS.Open("index.html"); openErr == nil {
			_ = f.Close()
			hasDistIndex = true
		}
	}
	placeholderBytes, _ := embeddedStaticFS.ReadFile("static/placeholder.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if hasDistIndex {
			clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
			if clean == "" || clean == "." {
				clean = "index.html"
			}
			if f, err := distFS.Open(clean); err == nil {
				_ = f.Close()
				http.FileServer(http.FS(distFS)).ServeHTTP(w, r)
				return
			}
			if idx, err := fs.ReadFile(distFS, "index.html"); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(idx)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(placeholderBytes)
	})
}
