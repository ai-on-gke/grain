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

package ui_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/dispatchcondition"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A dispatch condition reaches GET /api/config and GET /api/sandboxes
// as the daemon recorded it -- its name, one line of error, the remedy
// -- and never with more than that one line: a reconciler that died on
// a panic recorded the panic's stack in the journal, and what these two
// routes serve to a browser and to grain health is the sentence.
func TestConfigAndHealthCarryADispatchConditionAsOneLine(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	set := &dispatchcondition.Set{Logf: func(string, ...any) {}}
	set.Block("reconciler", fmt.Errorf("panic: runtime error: nil map write\ngoroutine 7 [running]:\n\t/home/builder/grain/pkg/daemon/run.go:1140 +0x3c"),
		"fix what the error names and restart the daemon")
	client.Config.ReconcilerDown = func() bool { return true }
	client.Config.DispatchConditions = set
	srv := ui.NewServerWithClient(client)

	type condition struct {
		Name, Error, Remedy, Check, Held string
	}
	for _, path := range []string{"/api/config", "/api/sandboxes"} {
		rec := do(t, srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body)
		}
		got := decode[struct {
			DispatchBlocked []condition `json:"dispatchBlocked"`
		}](t, rec)
		if len(got.DispatchBlocked) != 1 {
			t.Fatalf("GET %s dispatchBlocked = %+v, want the one condition", path, got.DispatchBlocked)
		}
		c := got.DispatchBlocked[0]
		if c.Name != "reconciler" || !strings.HasPrefix(c.Error, "panic: runtime error: nil map write") ||
			c.Remedy == "" || !strings.HasPrefix(c.Check, "dispatch blocked -- reconciler: ") || c.Held == "" {
			t.Errorf("GET %s dispatchBlocked[0] = %+v, want the reconciler condition with its error, check, remedy and how long held", path, c)
		}
		for _, field := range []string{c.Error, c.Check} {
			if strings.Contains(field, "\n") || strings.Contains(field, "goroutine") || strings.Contains(field, "/home/builder") {
				t.Errorf("GET %s serves the panic's stack: %q", path, field)
			}
		}
	}

	set.Clear("reconciler")
	got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if got["dispatchBlocked"] != nil {
		t.Errorf("dispatchBlocked after the clear = %v, want omitted", got["dispatchBlocked"])
	}
}
