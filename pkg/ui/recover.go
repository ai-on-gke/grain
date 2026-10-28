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
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"

	"github.com/ai-on-gke/grain/pkg/apierror"
)

// serveRecovered runs next and turns a panic in it into the same
// sanitized 500 every other server-side failure gets (apierror.Body):
// "internal server error (ref ...)" for the caller, the panic value and
// its stack in the daemon's log under that ref.
//
// Without it a handler panic fell through to net/http's own recovery,
// which logs the stack but answers the caller by dropping the connection
// -- the UI was left showing a network error with nothing to quote back
// to an operator. Nothing about the panic itself (its message, the
// goroutine stack, file paths) is ever written to the response.
//
// http.ErrAbortHandler is re-raised untouched: it is how a handler asks
// net/http to abort the response on purpose, and it is not a fault.
func serveRecovered(w http.ResponseWriter, r *http.Request, next http.Handler) {
	rw := &recoverWriter{ResponseWriter: w}
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			panic(p)
		}
		err := fmt.Errorf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, p, debug.Stack())
		if rw.wroteHeader {
			// Too late to say anything: part of a response is already on
			// the wire. Log it under a ref the same way and abort, so the
			// client sees a truncated response rather than a 200 that
			// silently ends early.
			_ = apierror.Body(http.StatusInternalServerError, err)
			panic(http.ErrAbortHandler)
		}
		writeError(w, http.StatusInternalServerError, err)
	}()
	next.ServeHTTP(rw, r)
}

// recoverWriter records whether a response has been started, which is
// what decides whether serveRecovered can still send its 500. It passes
// Flush and Hijack through, because the SSE stream (team_events.go) and
// the executor upgrade (executors.go) type-assert for them.
type recoverWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *recoverWriter) WriteHeader(status int) {
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *recoverWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

func (w *recoverWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		w.wroteHeader = true
		f.Flush()
	}
}

func (w *recoverWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.wroteHeader = true
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *recoverWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
