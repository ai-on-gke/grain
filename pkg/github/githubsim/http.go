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

package githubsim

import (
	"fmt"
	"io"
	"net/http"
)

// Handler serves the sim's REST API over HTTP at the server's root, as
// api.github.com serves GitHub's, for a client that is not Go -- curl in
// scripts/release-from-trunk.sh, candidate-gate.sh or
// release-train-actions.sh, pointed at it with RELEASE_GITHUB_API_URL.
// A request the sim has no answer for is a 501 naming it, rather than the
// panic Request raises for a Go caller.
func (s *Sim) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				http.Error(w, fmt.Sprintf(`{"message":"githubsim: no answer for %s %s: %v"}`, r.Method, r.URL.RequestURI(), v), http.StatusNotImplemented)
			}
		}()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		headers := map[string]string{}
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		resp, err := s.Request(r.Method, r.URL.RequestURI(), headers, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		if w.Header().Get("Content-Type") == "" && len(resp.Body) > 0 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		status := resp.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(resp.Body)
	})
}
