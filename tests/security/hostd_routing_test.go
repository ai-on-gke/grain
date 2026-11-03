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

package security

import (
	"context"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A task's hostd pin moves its agent CLI onto another machine, and onto
// whatever CLI login lives there. An agent with tasks:write may not route
// work it files or edits to a hostd its own task does not run on: not on
// a filing, and not through PUT /api/tasks/{id}/hostd. The default hostd,
// and the acting task's own selector, stay allowed.
func TestAgentCannotRouteWorkToAnotherHostd(t *testing.T) {
	agent := map[string]string{ui.ActingTaskHeader: "901"}
	srv, store := networkWideningFixture(t, model.NetworkNone)
	ctx := context.Background()
	siblingHostd := func() string {
		t.Helper()
		sel, err := store.TaskHostd(ctx, "902")
		if err != nil {
			t.Fatal(err)
		}
		return sel
	}

	for _, body := range []string{`{"hostd":"someone-laptop"}`, `{"Hostd":"gpu=a100"}`} {
		if code, resp := serveNetworkWidening(t, srv, http.MethodPut, "/api/tasks/902/hostd", body, agent); code != http.StatusForbidden {
			t.Errorf("PUT hostd %s: %d %s, want 403", body, code, resp)
		}
	}
	if got := siblingHostd(); got != "" {
		t.Errorf("sibling hostd = %q after refused pins, want unchanged", got)
	}
	if code, resp := serveNetworkWidening(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"elsewhere","repo":"acme/widgets","hostd":"someone-laptop"}`, agent); code != http.StatusForbidden {
		t.Errorf("filing with a hostd: %d %s, want 403", code, resp)
	}

	// The acting task's own selector, in any label order, and the
	// default hostd are what it already has.
	if err := store.SetTaskHostd(ctx, "901", "gpu=a100,zone=b"); err != nil {
		t.Fatal(err)
	}
	if code, resp := serveNetworkWidening(t, srv, http.MethodPut, "/api/tasks/902/hostd", `{"hostd":"zone=b,gpu=a100"}`, agent); code != http.StatusOK {
		t.Fatalf("PUT the acting task's own hostd: %d %s, want 200", code, resp)
	}
	if got := siblingHostd(); got != "gpu=a100,zone=b" {
		t.Errorf("sibling hostd = %q, want gpu=a100,zone=b", got)
	}
	if code, resp := serveNetworkWidening(t, srv, http.MethodPut, "/api/tasks/902/hostd", `{"hostd":""}`, agent); code != http.StatusOK {
		t.Fatalf("PUT the default hostd: %d %s, want 200", code, resp)
	}
	if got := siblingHostd(); got != "" {
		t.Errorf("sibling hostd = %q after clearing, want the default", got)
	}
}
