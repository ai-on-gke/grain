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
	"net/http"
	"testing"
)

// /api/config reports the remote executor listener the daemon really
// has, so the Teams pane's bootstrap command stops guessing port 8422
// (grain/task-2396). A wildcard bind is reported as ":port", leaving the
// host to the browser that knows which name reaches this machine.
func TestConfigReportsTheExecutorListener(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		bound, want string
	}{
		{"10.0.0.5:9422", "10.0.0.5:9422"},
		{"[::]:8422", ":8422"},
		{"0.0.0.0:7000", ":7000"},
		{":8422", ":8422"},
		{"[fd00::1]:8422", "[fd00::1]:8422"},
	} {
		srv, client := testServer(t)
		client.Config.ExecutorAddr = tc.bound
		client.Config.ExecutorTLS = true

		got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
		if got["executorAddr"] != tc.want {
			t.Errorf("bound %s: executorAddr = %v, want %s", tc.bound, got["executorAddr"], tc.want)
		}
		if got["executorTLS"] != true {
			t.Errorf("bound %s: executorTLS = %v, want true", tc.bound, got["executorTLS"])
		}
	}
}

// No -executor-addr (and `grain demo`, which runs no listener): nothing
// is reported, which the pane reads as remote execution being off.
func TestConfigOmitsTheExecutorListenerWhenThereIsNone(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if v, ok := got["executorAddr"]; ok {
		t.Fatalf("executorAddr = %v, want it absent with no executor listener", v)
	}
	if v, ok := got["executorTLS"]; ok {
		t.Fatalf("executorTLS = %v, want it absent with no executor listener", v)
	}
}
