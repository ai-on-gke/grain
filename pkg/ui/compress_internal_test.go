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

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCompressionLeavesStreamsAndCompressedBytesAlone: what withCompression
// must never touch -- an event stream, which a buffered gzip writer would
// hold back, a CI artifact that is already a gzipped tar, a websocket
// upgrade -- goes out exactly as its handler wrote it.
func TestCompressionLeavesStreamsAndCompressedBytesAlone(t *testing.T) {
	big := strings.Repeat("data: tick\n\n", 500)
	for _, tc := range []struct {
		name, contentType string
		header            map[string]string
		wantGzip          bool
	}{
		{name: "json", contentType: "application/json", wantGzip: true},
		{name: "html", contentType: "text/html; charset=utf-8", wantGzip: true},
		{name: "event stream", contentType: "text/event-stream"},
		{name: "gzipped artifact", contentType: "application/gzip"},
		{name: "octet stream", contentType: "application/octet-stream"},
		{name: "websocket", contentType: "application/json", header: map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}},
		{name: "range", contentType: "text/plain", header: map[string]string{"Range": "bytes=0-10"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			withCompression(rec, req, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(big))
				w.(http.Flusher).Flush()
			})
			gzipped := rec.Header().Get("Content-Encoding") == "gzip"
			if gzipped != tc.wantGzip {
				t.Fatalf("gzipped = %v, want %v", gzipped, tc.wantGzip)
			}
			if !tc.wantGzip && rec.Body.String() != big {
				t.Errorf("the response was altered on its way out")
			}
		})
	}
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"":                  false,
		"gzip":              true,
		"deflate, gzip;q=1": true,
		"br, GZIP":          true,
		"gzip;q=0":          false,
		"gzip; q=0.000":     false,
		"identity":          false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", header)
		if got := acceptsGzip(req); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}
