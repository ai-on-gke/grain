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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/console"
)

func TestConsoleHeldTail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window string
		at     int
		fresh  bool
		want   int
	}{
		{"quiet console serves everything", "leak: ghp_0123456789", 0, false, 0},
		{"unfinished long token is held", "leak: ghp_0123456789", 0, true, len("ghp_0123456789")},
		{"finished line is served", "leak: ghp_0123456789\r\n", 0, true, 0},
		{"short prompt is served", "Continue? [y/N]", 0, true, 0},
		{"shell prompt is served", "debian@sandbox:~$ ", 0, true, 0},
		{"token with no whitespace at all is held", "ghp_0123456789", 0, true, len("ghp_0123456789")},
		{"never more than what the poll asked for", "leak: ghp_0123456789", 12, true, len("leak: ghp_0123456789") - 12},
		{"an unbroken stream past the cap is served", strings.Repeat("x", consoleHoldMax+1), 0, true, 0},
		{"empty window", "", 0, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := consoleHeldTail([]byte(tc.window), tc.at, tc.fresh); got != tc.want {
				t.Errorf("consoleHeldTail(%q, %d, %v) = %d, want %d", tc.window, tc.at, tc.fresh, got, tc.want)
			}
		})
	}
}

// TestConsoleOutputHoldsBackAnUnfinishedCredential is CE-1's residual
// from the credential-exposure audit: a poll that lands while a run is
// still printing a credential must not serve the part it has so far,
// which redaction cannot recognise until the rest arrives.
func TestConsoleOutputHoldsBackAnUnfinishedCredential(t *testing.T) {
	const half = "PLANTEDstoreSECRETfirstHALF"
	consoles := console.NewManager()
	sess, err := consoles.Start(console.Spec{TaskID: "7", StartedAt: time.Now()}, console.Command{
		Path: "/bin/sh",
		Args: []string{"-c", `printf 'leak: %s' "$1"; sleep 30`, "sh", half},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = consoles.Close("7")
		<-sess.Done()
	})
	srv := NewServer(Config{Consoles: consoles}, nil)

	poll := func(offset int64) ConsoleOutputResponse {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/console/7/output?offset=%d", offset), nil)
		r.SetPathValue("id", "7")
		w := httptest.NewRecorder()
		srv.handleConsoleOutput(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/console/7/output answered %d: %s", w.Code, w.Body.String())
		}
		var out ConsoleOutputResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	decode := func(out ConsoleOutputResponse) string {
		t.Helper()
		data, err := base64.StdEncoding.DecodeString(out.Output)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, _, _ := sess.Output(0)
		if strings.Contains(string(raw), half) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the console never printed: %q", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Still printing, as far as the route can tell: the unfinished token
	// is left for the next poll, and the offset handed back says so.
	defer func(prev time.Duration) { consoleHoldQuiet = prev }(consoleHoldQuiet)
	consoleHoldQuiet = time.Hour
	held := poll(0)
	if got := decode(held); strings.Contains(got, "PLANTED") || !strings.Contains(got, "leak: ") {
		t.Errorf("a fresh console's poll served %q, want everything before the unfinished token", got)
	}
	if again := poll(held.Offset); decode(again) != "" || again.Offset != held.Offset {
		t.Errorf("polling from the held offset served %q (offset %d), want nothing new (offset %d)", decode(again), again.Offset, held.Offset)
	}

	// Quiet: the run has stopped printing, so what it has is served from
	// where the last poll left off.
	consoleHoldQuiet = 0
	rest := poll(held.Offset)
	if got := decode(rest); got != half {
		t.Errorf("a quiet console's poll from offset %d served %q, want %q", held.Offset, got, half)
	}
}
