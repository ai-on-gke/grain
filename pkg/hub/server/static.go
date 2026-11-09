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
// bundle from `static/` (or `static/dist/` when built there), falling back to
// `static/placeholder.html` on unbuilt checkouts.
func NewStaticHandler() http.Handler {
	var bundleFS fs.FS
	for _, candidate := range []string{"static", "static/dist"} {
		sub, err := fs.Sub(embeddedStaticFS, candidate)
		if err != nil {
			continue
		}
		if f, openErr := sub.Open("index.html"); openErr == nil {
			_ = f.Close()
			bundleFS = sub
			break
		}
	}
	placeholderBytes, _ := embeddedStaticFS.ReadFile("static/placeholder.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if bundleFS != nil {
			clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
			if clean != "" && clean != "." && clean != "index.html" && clean != "placeholder.html" {
				if f, err := bundleFS.Open(clean); err == nil {
					info, statErr := f.Stat()
					_ = f.Close()
					if statErr == nil && !info.IsDir() {
						http.FileServer(http.FS(bundleFS)).ServeHTTP(w, r)
						return
					}
				}
			}
			if idx, err := fs.ReadFile(bundleFS, "index.html"); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(idx)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(placeholderBytes)
	})
}
