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

package hostd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A hostd refused for its token is logged on the daemon's side too, at
// most once a minute however often it retries, with how many went
// unlogged in between.
func TestRemoteLogsRefusedTokensRateLimited(t *testing.T) {
	var lines []string
	clock := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	r := &Remote{
		Token: "right",
		Logf:  func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
		now:   func() time.Time { return clock },
	}
	refuse := func(token string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, RemotePath, nil)
		req.RemoteAddr = "10.0.0.7:5555"
		req.Header.Set(HostHeader, "workstation")
		if token != "" {
			req.Header.Set(TokenHeader, token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("ServeHTTP with token %q: got %d, want 401", token, w.Code)
		}
	}

	refuse("wrong")
	if len(lines) != 1 {
		t.Fatalf("after one refusal, logged %q; want one line", lines)
	}
	want := "grain daemon: refused a grain hostd on workstation connecting from 10.0.0.7:5555: it presented a token that is not this daemon's hostd token"
	if !strings.HasPrefix(lines[0], want) {
		t.Errorf("logged %q; want it to start %q", lines[0], want)
	}

	for range 5 {
		clock = clock.Add(5 * time.Second)
		refuse("wrong")
	}
	if len(lines) != 1 {
		t.Fatalf("retries inside a minute logged %q; want only the first line", lines)
	}

	clock = clock.Add(time.Minute)
	refuse("")
	if len(lines) != 2 {
		t.Fatalf("a refusal a minute on logged %q; want a second line", lines)
	}
	if want := "it presented no hostd token (5 more refused since the last line like this)"; !strings.HasSuffix(lines[1], want) {
		t.Errorf("logged %q; want it to end %q", lines[1], want)
	}
}
