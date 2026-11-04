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
	"bufio"
	"compress/gzip"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Responses are gzipped for a caller that accepts it (grain task 2983).
//
// Nothing here was compressed before, and GET /api/tasks -- which every
// open tab polls every three seconds -- was 14.5 MB of JSON on a
// deployment of three thousand tasks; gzip alone takes that to 2.9 MB,
// and JSON compresses about tenfold in general. Over a remote proxy that
// is most of what a page waits on.
//
// What is compressed is decided per response, when its headers are
// written, from what the handler said it is sending:
//
//   - **text only.** JSON, text/*, JavaScript, SVG and XML. Anything else
//     -- a CI artifact, which is already a gzipped tar, an image, an
//     octet-stream -- goes out as it is.
//   - **never a stream.** text/event-stream is left alone, and a
//     websocket upgrade (or anything else hijacking the connection) never
//     reaches a compressing writer at all: the request is not wrapped.
//   - **never twice, never a range.** A response that already names a
//     Content-Encoding, or a request asking for a byte range, is passed
//     through.
//
// Every response this wraps carries Vary: Accept-Encoding, so a cache in
// between -- a proxy, the browser's own -- never hands the gzipped bytes
// to a caller that did not ask for them. A compressed response's strong
// ETag is made weak: the bytes on the wire are no longer the bytes it was
// computed over. etagMatches compares weakly, so a poller's
// If-None-Match still earns its 304 either way.

// gzipMinLength is the smallest declared body worth compressing: below it
// gzip's own header and trailer are most of what would be sent.
const gzipMinLength = 1024

var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	return w
}}

// withCompression serves next, gzipping its response when r accepts gzip
// and the response is one worth compressing.
func withCompression(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request)) {
	if !acceptsGzip(r) || r.Method == http.MethodHead || r.Header.Get("Range") != "" ||
		r.Header.Get("Upgrade") != "" {
		next(w, r)
		return
	}
	w.Header().Add("Vary", "Accept-Encoding")
	gw := &gzipResponseWriter{ResponseWriter: w}
	defer gw.close()
	next(gw, r)
}

// acceptsGzip reports whether r's Accept-Encoding names gzip with a
// non-zero quality.
func acceptsGzip(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept-Encoding") {
		for _, part := range strings.Split(v, ",") {
			coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
				continue
			}
			if q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q="); ok {
				if f, err := strconv.ParseFloat(q, 64); err == nil && f == 0 {
					return false
				}
			}
			return true
		}
	}
	return false
}

// compressibleType reports whether a response of this Content-Type is
// worth gzipping.
func compressibleType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch {
	case mt == "text/event-stream":
		return false
	case strings.HasPrefix(mt, "text/"):
		return true
	case mt == "application/json", strings.HasSuffix(mt, "+json"),
		mt == "application/javascript", mt == "application/xml",
		strings.HasSuffix(mt, "+xml"), mt == "application/x-ndjson":
		return true
	}
	return false
}

// gzipResponseWriter compresses what its handler writes, once its headers
// say the response is one to compress (withCompression).
type gzipResponseWriter struct {
	http.ResponseWriter
	decided bool
	gz      *gzip.Writer
}

// decide settles whether this response is compressed, from the headers as
// they stand when it starts.
func (w *gzipResponseWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	if status < http.StatusOK || status == http.StatusNoContent || status == http.StatusNotModified ||
		status == http.StatusPartialContent || h.Get("Content-Encoding") != "" ||
		h.Get("Content-Range") != "" || !compressibleType(h.Get("Content-Type")) {
		return
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n, err := strconv.Atoi(cl); err == nil && n < gzipMinLength {
			return
		}
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		h.Set("ETag", "W/"+etag)
	}
	gz := gzipWriters.Get().(*gzip.Writer)
	gz.Reset(w.ResponseWriter)
	w.gz = gz
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	w.decide(status)
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.decided {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush sends what has been compressed so far, so a handler that streams
// a compressible response still streams.
func (w *gzipResponseWriter) Flush() {
	if !w.decided {
		w.decide(http.StatusOK)
	}
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok || w.gz != nil {
		return nil, nil, http.ErrNotSupported
	}
	w.decided = true
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *gzipResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipResponseWriter) close() {
	if w.gz == nil {
		return
	}
	_ = w.gz.Close()
	gzipWriters.Put(w.gz)
	w.gz = nil
}
